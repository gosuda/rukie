package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/cli"
	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/githttp"
	"github.com/gosuda/rukie/internal/gitssh"
	"github.com/gosuda/rukie/internal/logging"
	"github.com/gosuda/rukie/internal/mcp"
	"github.com/gosuda/rukie/internal/mirror"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/repowatch"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/supervisor"
	"github.com/gosuda/rukie/internal/sysops"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func stdIO() cli.IO {
	return cli.IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}
}

func run(argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rukie <command> [flags]\ncommands: serve, bootstrap, dev, mcp, auth, user, admin, api")
		return 64
	}
	switch argv[0] {
	case "serve":
		return runServe(argv[1:])
	case "dev":
		return runDev(argv[1:])
	case "bootstrap":
		return runBootstrap(argv[1:])
	case "backup":
		return runBackup(argv[1:])
	case "restore":
		return runRestore(argv[1:])
	case "admin":
		if code, ok := tryHandWrittenAdmin(argv[1:]); ok {
			return code
		}
		return cli.Run(context.Background(), stdIO(), argv)
	case "mcp":
		return cli.RunMCP(context.Background(), stdIO(), argv[1:])
	default:
		if msg, ok := cli.StubFor(argv); ok {
			_, _ = fmt.Fprintln(os.Stdout, msg)
			return 1
		}
		return cli.Run(context.Background(), stdIO(), argv)
	}
}

func runBootstrap(argv []string) int {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	configFlag := fs.String("config", "", "config file path")
	username := fs.String("username", "", "admin username")
	secret := auth.RegisterSecretFlags(fs, "password")
	if err := fs.Parse(argv); err != nil {
		return 64
	}
	if *username == "" {
		fmt.Fprintln(os.Stderr, "bootstrap: --username is required")
		return 64
	}
	password, err := secret.Resolve(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap:", err.Error())
		return 64
	}
	cfg, err := config.Load(configPath(*configFlag))
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 78
	}
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	defer func() {
		_ = st.Close(ctx)
	}()
	svc := auth.New(st)
	u, err := svc.BootstrapAdmin(ctx, *username, password, "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap:", err.Error())
		return 1
	}
	if _, err := fmt.Fprintln(os.Stdout, "admin "+u.Name+" created"); err != nil {
		return 1
	}
	return 0
}

func configPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("RUKIE_CONFIG"); env != "" {
		return env
	}
	return "/etc/rukie/config.toml"
}

func superviseWorker(ctx context.Context, interval time.Duration, retry func(error) bool, run func(context.Context) error) {
	for {
		if err := run(ctx); err != nil && !retry(err) {
			return
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func runServe(argv []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configFlag := fs.String("config", "", "config file path")
	checkConfig := fs.Bool("check-config", false, "validate the config and exit")
	showEffective := fs.Bool("show-effective", false, "print the effective config with --check-config")
	if err := fs.Parse(argv); err != nil {
		return 64
	}
	cfg, cfgErr := config.Load(configPath(*configFlag))
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, cfgErr.Error())
		if *checkConfig {
			return 2
		}
		return 78
	}
	if *checkConfig {
		if *showEffective {
			fmt.Print(cfg.Effective())
		}
		return 0
	}
	lvl, lerr := logging.ParseLevel(cfg.Log.Level)
	if lerr != nil {
		fmt.Fprintln(os.Stderr, lerr.Error())
		return 78
	}
	levelVar := &slog.LevelVar{}
	levelVar.Set(lvl)
	logger := logging.NewWithVar(levelVar, cfg.Log.Format, cfg.Log.Source)
	slog.SetDefault(logger)
	if err := sysops.EnsureDataDir(cfg.DataDir); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	if derr := sysops.CheckDowngrade(cfg); derr != nil {
		fmt.Fprintln(os.Stderr, derr.Error())
		return sysops.ExitDowngradeRefused
	}
	if _, serr := sysops.LoadSecretKey(cfg.SecretKeyFile); serr != nil {
		fmt.Fprintln(os.Stderr, serr.Error())
		return 2
	}
	lock, lerr := sysops.AcquireDataDirLock(cfg.DataDir)
	if lerr != nil {
		fmt.Fprintln(os.Stderr, lerr.Error())
		return sysops.ExitLockHeld
	}
	defer func() { _ = lock.Release() }()
	sup, serr := startSupervisor(cfg)
	if serr != nil {
		fmt.Fprintln(os.Stderr, serr.Error())
		return 1
	}
	supCtx, supStop := context.WithCancel(context.Background())
	defer supStop()
	supDone := make(chan error, 1)
	go func() { supDone <- sup.Run(supCtx) }()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	halves := &halveRegistry{}
	serveDone := make(chan struct{})
	go watchShutdownSignals(sigCh, stop, halves, serveDone)
	var supOnce sync.Once
	var supFinal error
	waitSupervisorOnce := func() error {
		supOnce.Do(func() {
			timer := time.NewTimer(supervisorWaitBackstop)
			defer timer.Stop()
			select {
			case supFinal = <-supDone:
			case <-timer.C:
				supFinal = fmt.Errorf("serve: supervisor did not exit within 30s")
			}
		})
		return supFinal
	}
	reloadLogLevelOnSIGHUP(configPath(*configFlag), levelVar)
	if rerr := waitSupervisorReady(ctx, sup, waitSupervisorOnce, cfg); rerr != nil {
		fmt.Fprintln(os.Stderr, rerr.Error())
		supStop()
		if serr := waitSupervisorOnce(); serr != nil {
			fmt.Fprintln(os.Stderr, "serve:", serr.Error())
		}
		return 1
	}
	app, cleanup, err := setupApp(ctx, cfg, logger, sup)
	if err != nil {
		logger.Error("setup app", slog.Any("err", err))
		supStop()
		if serr := waitSupervisorOnce(); serr != nil {
			logger.Error("supervisor wait", slog.Any("err", serr))
		}
		return 1
	}
	if merr := leaveDowngradeMarkerWithoutDBOpen(cfg); merr != nil {
		logger.Error("schema marker", slog.Any("err", merr))
		supStop()
		if serr := waitSupervisorOnce(); serr != nil {
			logger.Error("supervisor wait", slog.Any("err", serr))
		}
		return 1
	}
	spoolCtx, stopSpool := context.WithCancel(context.Background())
	defer stopSpool()
	spoolDone := make(chan error, 1)
	go func() { spoolDone <- events.DrainLoop(spoolCtx, app.spool, app.store, events.EnqueuePush) }()
	ownedDrainRunners := map[string]func(context.Context) error{
		"store": func(ctx context.Context) error {
			done := make(chan struct{})
			go func() { cleanup(); close(done) }()
			select {
			case <-done:
				return nil
			case <-ctx.Done():
				logger.Error("store drain overrun", slog.Any("err", context.Cause(ctx)))
				return ctx.Err()
			}
		},
		"spool": func(ctx context.Context) error {
			stopSpool()
			select {
			case err := <-spoolDone:
				if err != nil && !errors.Is(err, context.Canceled) {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
			return replaySpoolFinalPass(ctx, app)
		},
		"child": func(ctx context.Context) error {
			supStop()
			done := make(chan error, 1)
			go func() { done <- waitSupervisorOnce() }()
			select {
			case err := <-done:
				return err
			case <-ctx.Done():
				logger.Error("child stop overrun", slog.Any("err", context.Cause(ctx)))
				if err := <-done; err != nil {
					return errors.Join(ctx.Err(), err)
				}
				return ctx.Err()
			}
		},
	}
	if serr := app.watcher.StartAll(ctx); serr != nil {
		logger.Error("repo watchers", slog.Any("err", serr))
		supStop()
		if werr := waitSupervisorOnce(); werr != nil {
			logger.Error("supervisor wait", slog.Any("err", werr))
		}
		return 1
	}
	workerCtx, workerStop := context.WithCancel(ctx)
	sweepCtx, sweepStop := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	sweepDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		superviseWorker(workerCtx, time.Minute, func(err error) bool {
			return errors.Is(err, store.ErrBackupInProgress)
		}, func(ctx context.Context) error {
			return app.jobs.RunKinds(ctx, repowatch.WorkerConfig("serve"), repowatch.PurgeKinds(), app.watcher.HandlePurge)
		})
	}()
	go func() {
		defer close(sweepDone)
		_ = app.watcher.RunSweeper(sweepCtx, time.Hour)
	}()
	mirrorDone := make(chan error, 1)
	go func() {
		// A drain error is transient (a store hiccup, a rejected run). Let the
		// scheduler restart instead of silently dropping every later mirror.
		superviseWorker(workerCtx, time.Minute, func(error) bool { return true }, func(ctx context.Context) error {
			return app.mirror.Scheduler(ctx, 10*time.Second, "serve-mirror")
		})
		mirrorDone <- workerCtx.Err()
	}()
	ownedDrainRunners["followers"] = app.watcher.StopAll
	ownedDrainRunners["queue"] = func(context.Context) error {
		workerStop()
		sweepStop()
		<-workerDone
		<-sweepDone
		mirrorErr := <-mirrorDone
		if mirrorErr != nil && !errors.Is(mirrorErr, context.Canceled) {
			return mirrorErr
		}
		return nil
	}
	if app.ssh != nil {
		if _, err := serveSSHListeners(ctx, app.ssh, cfg.SSH.Listen); err != nil {
			logger.Error("ssh listen", slog.Any("err", err))
			supStop()
			_ = waitSupervisorOnce()
			return 78
		}
		for _, addr := range cfg.SSH.Listen {
			logger.Info("listening ssh", slog.String("addr", addr))
		}
		ownedDrainRunners["ssh"] = func(context.Context) error {
			return app.ssh.Close()
		}
	}
	supervisorDeathStopsServing := make(chan error, 1)
	go func() {
		if err := waitSupervisorOnce(); err != nil {
			select {
			case supervisorDeathStopsServing <- err:
			default:
			}
		}
	}()
	code := serveHTTP(ctx, logger, app.handler, cfg.HTTP, halves, ownedDrainRunners, supervisorDeathStopsServing)
	close(serveDone)
	supStop()
	if finalErr := waitSupervisorOnce(); finalErr != nil {
		logger.Error("supervisor exited", slog.Any("err", finalErr))
		return 1
	}
	return code
}

func watchShutdownSignals(sigCh <-chan os.Signal, stop context.CancelFunc, halves *halveRegistry, serveDone <-chan struct{}) {
	<-sigCh
	stop()
	for {
		select {
		case <-sigCh:
			halves.halveAll()
		case <-serveDone:
			return
		}
	}
}

type halveRegistry struct {
	fns    []func()
	mu     sync.Mutex
	halved bool
}

func (r *halveRegistry) add(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.halved {
		fn()
		return
	}
	r.fns = append(r.fns, fn)
}

func (r *halveRegistry) halveAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.halved {
		return
	}
	r.halved = true
	for _, fn := range r.fns {
		fn()
	}
}

type halveDeadline struct {
	deadline time.Time
	timer    *time.Timer
	cancel   context.CancelCauseFunc
	mu       sync.Mutex
	halved   bool
}

func newHalveDeadline(parent context.Context, timeout time.Duration, cause error) (context.Context, *halveDeadline) {
	ctx, cancel := context.WithCancelCause(parent)
	hd := &halveDeadline{deadline: time.Now().Add(timeout), cancel: cancel}
	hd.timer = time.AfterFunc(timeout, func() { cancel(cause) })
	return ctx, hd
}

func (hd *halveDeadline) halve() {
	hd.mu.Lock()
	defer hd.mu.Unlock()
	if hd.halved {
		return
	}
	hd.halved = true
	remaining := time.Until(hd.deadline)
	if remaining < 0 {
		remaining = 0
	}
	half := remaining / 2
	hd.deadline = time.Now().Add(half)
	hd.timer.Reset(half)
}

func (hd *halveDeadline) stop() {
	hd.timer.Stop()
}

func startSupervisor(cfg config.Config) (*supervisor.Supervisor, error) {
	timeout, err := startupTimeout(cfg)
	if err != nil {
		return nil, err
	}
	if externalDaemonSelected(cfg) {
		return supervisor.New(supervisor.Config{
			ExternalSocket: cfg.Ruk.Socket,
			StartupTimeout: timeout,
		})
	}
	bin, err := rukproc.ResolveBinary(cfg.Ruk.BinaryPath)
	if err != nil {
		return nil, err
	}
	daemonPath, err := supervisor.WriteDaemonConfig(cfg)
	if err != nil {
		return nil, err
	}
	return supervisor.New(supervisor.Config{
		Binary:         bin,
		DaemonConfig:   daemonPath,
		SocketPath:     cfg.Ruk.SocketPath,
		StartupTimeout: timeout,
		Env:            supervisor.HermeticEnv(os.Environ()),
		Stdout:         os.Stderr,
		Stderr:         os.Stderr,
	})
}

const supervisorWaitBackstop = 30 * time.Second

const maxStartupTimeoutMsBeforeNanosOverflow = (1<<63 - 1) / 1_000_000

func externalDaemonSelected(cfg config.Config) bool {
	return cfg.Ruk.Socket != ""
}

func leaveDowngradeMarkerWithoutDBOpen(cfg config.Config) error {
	return sysops.RefreshMetaVersion(cfg)
}

func replaySpoolFinalPass(ctx context.Context, app *app) error {
	return app.spool.Replay(ctx, app.store, events.EnqueuePush)
}

func startupTimeout(cfg config.Config) (time.Duration, error) {
	ms := cfg.Ruk.StartupTimeoutMs
	if ms == 0 {
		return supervisor.StartupTimeout, nil
	}
	if ms > maxStartupTimeoutMsBeforeNanosOverflow {
		return 0, fmt.Errorf("serve: ruk.startup_timeout_ms %d overflows a duration", ms)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func waitSupervisorReady(ctx context.Context, sup *supervisor.Supervisor, waitSupervisorOnce func() error, cfg config.Config) error {
	timeout, err := startupTimeout(cfg)
	if err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeoutCause(ctx, timeout, fmt.Errorf("serve: daemon startup timed out"))
	defer cancel()
	readyErr := make(chan error, 1)
	go func() { readyErr <- sup.WaitReady(readyCtx) }()
	exited := make(chan error, 1)
	go func() { exited <- waitSupervisorOnce() }()
	select {
	case err := <-exited:
		cancel()
		if err != nil {
			return err
		}
		return fmt.Errorf("serve: supervisor exited during startup")
	case err := <-readyErr:
		return err
	case <-ctx.Done():
		cancel()
		return fmt.Errorf("serve: interrupted during startup: %w", ctx.Err())
	}
}

func reloadLogLevelOnSIGHUP(path string, levelVar *slog.LevelVar) {
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hupCh)
		for range hupCh {
			level, err := supervisor.ReloadLogLevel(levelVar, path)
			if err != nil {
				slog.Default().Error("sighup reload", slog.Any("err", err))
				continue
			}
			slog.Default().Info("reloaded log level", slog.String("level", level))
		}
	}()
}

type app struct {
	server  *op.Server
	handler http.Handler
	store   *store.Store
	auth    *auth.Service
	docs    *api.DocStore
	spool   *events.Spool
	watcher *repowatch.Watcher
	jobs    *queue.Queue
	ssh     *gitssh.Server
	mirror  mirror.Worker
}

type gitFallback struct {
	git      http.Handler
	fallback http.Handler
	keys     api.Deps
}

func (h gitFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if owner, ok := api.OwnerKeysPath(r.URL.Path); ok {
			api.ServeOwnerKeys(w, r, h.keys, owner)
			return
		}
	}
	if _, _, ok := githttp.RouteGitPath(r.URL.Path); ok {
		h.git.ServeHTTP(w, r)
		return
	}
	h.fallback.ServeHTTP(w, r)
}

func llmsBase(cfg config.Config) string {
	if cfg.HTTP.ExternalURL != "" {
		return strings.TrimSuffix(cfg.HTTP.ExternalURL, "/")
	}
	if len(cfg.HTTP.Listen) > 0 {
		return "http://" + cfg.HTTP.Listen[0]
	}
	return ""
}

func setupApp(ctx context.Context, cfg config.Config, logger *slog.Logger, sup *supervisor.Supervisor) (*app, func(), error) {
	cleanup := func() {}
	holdBarrier := func() (func(), bool) {
		return sysops.HoldSharedBarrier(cfg.DataDir)
	}
	st, err := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path, Logger: logger, Barrier: holdBarrier})
	if err != nil {
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	cleanup = func() {
		_ = st.Close(context.Background())
	}
	table := op.NewTable()
	docs := &api.DocStore{}
	if regErr := api.RegisterAll(table, docs); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	authSvc := auth.New(st)
	key, kerr := sysops.LoadSecretKey(cfg.SecretKeyFile)
	if kerr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", kerr)
	}
	authSvc.SetSealKey(key)
	var oidcCfgs []auth.OIDCProviderConfig
	for _, p := range cfg.OIDC {
		secretRaw, serr := os.ReadFile(filepath.Clean(p.ClientSecretFile))
		if serr != nil {
			cleanup()
			return nil, cleanup, fmt.Errorf("setupApp: oidc %s: %w", p.Name, serr)
		}
		oidcCfgs = append(oidcCfgs, auth.OIDCProviderConfig{
			ID: p.Name, Issuer: p.Issuer, ClientID: p.ClientID,
			ClientSecret: strings.TrimSpace(string(secretRaw)),
			Scopes:       p.Scopes, AllowSignup: p.AllowSignup, RedirectURL: p.RedirectURL,
		})
	}
	if len(oidcCfgs) > 0 {
		if serr := authSvc.SetOIDCProviders(ctx, oidcCfgs); serr != nil {
			cleanup()
			return nil, cleanup, fmt.Errorf("setupApp: %w", serr)
		}
	}
	cleanup = func() {
		_ = st.Close(context.Background())
	}
	limiter := access.NewLimiter()
	authDeps := api.Deps{Service: authSvc, Limiter: limiter, Reveals: auth.NewRevealCache(), Store: st}
	if regErr := api.RegisterAuth(table, authDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterUser(table, authDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterSSHKeys(table, authDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterAdmin(table, authDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterF12(table, authDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	repoClient := dialRepoClient(ctx, logger, cmp.Or(cfg.Ruk.Socket, cfg.Ruk.SocketPath))
	repoJobs := queue.Open(st, time.Now)
	watcher := repowatch.New(repowatch.Config{
		Store: st, Queue: repoJobs, Client: repoClient, RepoRoot: cfg.Ruk.RepoRoot,
		Barrier: holdBarrier, Logger: logger,
	})
	repoDeps := api.Deps{
		Service: authSvc, Limiter: limiter, Reveals: authDeps.Reveals,
		Store: st, Ruk: repoClient, Queue: repoJobs,
		RepoRoot: cfg.Ruk.RepoRoot, RukBin: cfg.Ruk.BinaryPath,
		SealKey:       key,
		OnRepoCreated: func(repoID int64) { watcher.Start(ctx, repoID) },
		OnRepoDeleted: watcher.Stop,
	}
	if regErr := api.RegisterRepos(table, repoDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterCode(table, repoDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterRefs(table, repoDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterReview(table, repoDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterMirror(table, repoDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterOrgs(table, repoDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	fillLLMS, regErr := api.RegisterLLMS(table, llmsBase(cfg))
	if regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterSSHKeysViews(views)
	api.RegisterAdminViews(views)
	api.RegisterF12Views(views)
	api.RegisterReposViews(views)
	api.RegisterCodeViews(views)
	api.RegisterRefsViews(views)
	api.RegisterReviewViews(views)
	api.RegisterMirrorViews(views)
	api.RegisterOrgsViews(views)
	api.RegisterLLMSViews(views)
	idem := op.NewMemoryIdempotency()
	idem.StartSweeper(ctx, time.Minute)
	auditor := op.FuncAuditor(func(_ context.Context, row op.AuditRow) error {
		logger.Info("request",
			slog.String("op", row.Op), slog.String("method", row.Method), slog.String("path", row.Path),
			slog.Int("status", row.Status), slog.String("decision", row.Decision), slog.String("req_id", row.RequestID))
		return nil
	})
	csrfKey := make([]byte, 32)
	if _, rerr := rand.Read(csrfKey); rerr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", rerr)
	}
	mux := http.NewServeMux()
	server, err := op.Mount(mux, table, op.Config{
		PublicOrigin:  cfg.HTTP.ExternalURL,
		IsSiteSegment: api.SiteSegment,
		Authenticator: auth.NewAuthenticator(authSvc),
		Idempotency:   idem,
		Audit:         auditor,
		Limiter:       limiter,
		CSRFKey:       csrfKey,
		MaxBody:       cfg.HTTP.MaxRequestBodyBytes,
		Views:         views,
		Title:         "rukie",
		Version:       "0.0.0",
	})
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	docs.Set(server.OpenAPI())
	fillLLMS(server.Document())
	daemon := api.NotReadyDaemon
	if sup != nil {
		daemon = sup.DaemonProbe
	}
	server.Handle("GET /healthz", api.Healthz())
	server.Handle("GET /readyz", api.Readiness{Store: st, Daemon: daemon}.Handler())
	mcpMount, err := mcp.NewMount(authSvc, server, cfg.HTTP.ExternalURL)
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	server.Handle("POST /-/mcp", mcpMount.Authenticated())
	server.Handle("GET /-/mcp", mcpMount.MethodNotAllowed())
	server.Handle("DELETE /-/mcp", mcpMount.MethodNotAllowed())
	spool, err := events.OpenSpool(cfg.DataDir)
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	git, err := githttp.New(githttp.Config{
		Auth:       authSvc,
		Limiter:    limiter,
		SocketPath: cmp.Or(cfg.Ruk.Socket, cfg.Ruk.SocketPath),
		RepoRoot:   cfg.Ruk.RepoRoot,
		Limits:     wire.DefaultGitLimits(),
		Feed:       events.OpenHookSink(st, spool, time.Now),
		Store:      st,
		Barrier:    holdBarrier,
	})
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	gitHandler := http.Handler(git)
	if sup != nil {
		gitHandler = sup.FailFast(git)
	}
	sshSrv, err := setupSSH(cfg, authSvc, limiter, st, spool, holdBarrier)
	if err != nil {
		cleanup()
		return nil, cleanup, err
	}
	return &app{server: server, handler: api.RawBytes{Deps: repoDeps}.Wrap(gitFallback{git: gitHandler, keys: authDeps, fallback: server}), store: st, auth: authSvc, docs: docs, spool: spool, watcher: watcher, jobs: repoJobs, ssh: sshSrv, mirror: mirror.Worker{Store: st, Queue: repoJobs, Ruk: repoClient, SealKey: key, Clock: time.Now, RepoRoot: cfg.Ruk.RepoRoot, WorkerCount: cfg.Mirrors.MaxConcurrentPushes}}, cleanup, nil
}

func dialRepoClient(ctx context.Context, logger *slog.Logger, socket string) *ruk.Client {
	if socket == "" {
		return nil
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	client, err := ruk.Dial(dialCtx, socket)
	if err != nil {
		logger.Warn("ruk dial deferred", slog.Any("err", err))
		return nil
	}
	return client
}

func drainWithoutServingSocket(logger *slog.Logger, steps []supervisor.Step) {
	drainCtx, cancel := context.WithTimeoutCause(context.Background(), supervisor.DefaultDrainTimeout, fmt.Errorf("offline drain timed out"))
	defer cancel()
	if err := supervisor.Drain(drainCtx, steps); err != nil {
		logger.Error("offline drain", slog.Any("err", err))
	}
}

const httpDrainBudgetFallback = 30 * time.Second

func serveHTTP(ctx context.Context, logger *slog.Logger, handler http.Handler, httpCfg config.HTTPConfig, halves *halveRegistry, ownedDrainRunners map[string]func(context.Context) error, supervisorDeathStopsServing <-chan error) int {
	httpServer := &http.Server{
		ReadTimeout:       time.Duration(httpCfg.ReadTimeoutMs) * time.Millisecond,
		ReadHeaderTimeout: time.Duration(httpCfg.ReadHeaderTimeoutMs) * time.Millisecond,
		WriteTimeout:      time.Duration(httpCfg.WriteTimeoutMs) * time.Millisecond,
		IdleTimeout:       time.Duration(httpCfg.IdleTimeoutMs) * time.Millisecond,
		Handler:           handler,
	}
	runners := map[string]func(context.Context) error{}
	maps.Copy(runners, ownedDrainRunners)
	runners["http"] = func(ctx context.Context) error {
		budget := time.Duration(httpCfg.ShutdownDrainMs) * time.Millisecond
		if budget <= 0 {
			budget = httpDrainBudgetFallback
		}
		shutCtx, hd := newHalveDeadline(ctx, budget, fmt.Errorf("http drain timed out"))
		defer hd.stop()
		halves.add(hd.halve)
		return httpServer.Shutdown(shutCtx)
	}
	steps, derr := supervisor.OrderedDrain(runners)
	if derr != nil {
		logger.Error("drain order", slog.Any("err", derr))
		return 1
	}
	errCh := make(chan error, len(httpCfg.Listen))
	for _, addr := range httpCfg.Listen {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			logger.Error("listen", slog.String("addr", addr), slog.Any("err", err))
			drainWithoutServingSocket(logger, steps)
			return 78
		}
		logger.Info("listening", slog.String("addr", ln.Addr().String()))
		go func() {
			errCh <- httpServer.Serve(ln)
		}()
	}
	drain := func() {
		drainCtx, hd := newHalveDeadline(context.Background(), supervisor.DefaultDrainTimeout, fmt.Errorf("drain timed out"))
		defer hd.stop()
		halves.add(hd.halve)
		if err := supervisor.Drain(drainCtx, steps); err != nil {
			logger.Error("drain", slog.Any("err", err))
		}
	}
	select {
	case <-ctx.Done():
		drain()
		return 0
	case supErr := <-supervisorDeathStopsServing:
		logger.Error("supervisor exited mid-serve", slog.Any("err", supErr))
		drain()
		return 1
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", slog.Any("err", err))
			drain()
			return 1
		}
		drain()
		return 0
	}
}
