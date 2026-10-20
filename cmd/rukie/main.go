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
	"github.com/gosuda/rukie/internal/logging"
	"github.com/gosuda/rukie/internal/mcp"
	"github.com/gosuda/rukie/internal/op"
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
	svc, err := auth.Open(cfg.Store.SQLite.Path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	defer func() {
		_ = svc.Close()
	}()
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
	logger, lerr := logging.New(cfg.Log.Level, cfg.Log.Format, cfg.Log.Source)
	if lerr != nil {
		fmt.Fprintln(os.Stderr, lerr.Error())
		return 78
	}
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
		return 3
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
	halveCh := make(chan struct{})
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	halveCtx, cancelHalve := context.WithCancel(context.Background())
	defer cancelHalve()
	go func() {
		<-sigCh
		stop()
		select {
		case <-sigCh:
			close(halveCh)
			cancelHalve()
		case <-ctx.Done():
		}
	}()
	var supOnce sync.Once
	var supFinal error
	waitSupervisorOnce := func() error {
		supOnce.Do(func() {
			timer := time.NewTimer(supervisorWaitBackstop)
			defer timer.Stop()
			select {
			case supFinal = <-supDone:
			case <-halveCtx.Done():
				supFinal = fmt.Errorf("serve: supervisor wait halved: %w", halveCtx.Err())
			case <-timer.C:
				supFinal = fmt.Errorf("serve: supervisor did not exit within 30s")
			}
		})
		return supFinal
	}
	reloadLogLevelOnSIGHUP(cfg, logger, configPath(*configFlag))
	if rerr := waitSupervisorReady(ctx, sup, waitSupervisorOnce, cfg); rerr != nil {
		fmt.Fprintln(os.Stderr, rerr.Error())
		supStop()
		if serr := waitSupervisorOnce(); serr != nil {
			fmt.Fprintln(os.Stderr, "serve:", serr.Error())
		}
		return 1
	}
	app, cleanup, err := setupApp(ctx, cfg, logger, sup.DaemonProbe)
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
			case <-ctx.Done():
			}
			return nil
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
				return ctx.Err()
			}
		},
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
	code := serveHTTP(ctx, logger, app.handler, cfg.HTTP, halveCh, ownedDrainRunners, supervisorDeathStopsServing)
	supStop()
	finalErr := waitSupervisorOnce()
	shutdownHalved := halveCtx.Err() != nil
	if finalErr != nil && !shutdownHalved {
		logger.Error("supervisor exited", slog.Any("err", finalErr))
		return 1
	}
	return code
}

func startSupervisor(cfg config.Config) (*supervisor.Supervisor, error) {
	if externalDaemonSelected(cfg) {
		return supervisor.New(supervisor.Config{
			ExternalSocket: cfg.Ruk.Socket,
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
		Binary:       bin,
		DaemonConfig: daemonPath,
		SocketPath:   cfg.Ruk.SocketPath,
		Env:          supervisor.HermeticEnv(os.Environ()),
		Stdout:       os.Stderr,
		Stderr:       os.Stderr,
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

func reloadLogLevelOnSIGHUP(cfg config.Config, logger *slog.Logger, path string) {
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hupCh)
		for range hupCh {
			level, next, err := supervisor.ReloadLogLevel(cfg.Log.Format, cfg.Log.Source, path)
			if err != nil {
				logger.Error("sighup reload", slog.Any("err", err))
				continue
			}
			slog.SetDefault(next)
			logger = next
			next.Info("reloaded log level", slog.String("level", level))
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
}

type gitFallback struct {
	git      *githttp.Server
	fallback http.Handler
	daemon   func() (bool, string)
}

func (h gitFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := githttp.RouteGitPath(r.URL.Path); ok {
		if h.daemon != nil {
			if ready, _ := h.daemon(); !ready {
				supervisor.WriteUnavailable(w)
				return
			}
		}
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

func setupApp(ctx context.Context, cfg config.Config, logger *slog.Logger, daemonProbe func() (bool, string)) (*app, func(), error) {
	cleanup := func() {}
	st, err := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path, Logger: logger})
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
	authSvc, err := auth.Open(cfg.Store.SQLite.Path)
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	cleanup = func() {
		_ = authSvc.Close()
		_ = st.Close(context.Background())
	}
	limiter := access.NewLimiter()
	authDeps := api.Deps{Service: authSvc, Limiter: limiter, Reveals: auth.NewRevealCache()}
	if regErr := api.RegisterAuth(table, authDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterUser(table, authDeps); regErr != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", regErr)
	}
	if regErr := api.RegisterAdmin(table, authDeps); regErr != nil {
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
	api.RegisterAdminViews(views)
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
	server.Handle("GET /healthz", api.Healthz())
	server.Handle("GET /readyz", api.Readiness{Store: st, Daemon: daemonProbe}.Handler())
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
	})
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	daemon := daemonProbe
	if daemon == nil {
		daemon = api.NotReadyDaemon
	}
	return &app{server: server, handler: gitFallback{git: git, fallback: server, daemon: daemon}, store: st, auth: authSvc, docs: docs, spool: spool}, cleanup, nil
}

func drainWithoutServingSocket(logger *slog.Logger, steps []supervisor.Step) {
	drainCtx, cancel := context.WithTimeoutCause(context.Background(), supervisor.DefaultDrainTimeout, fmt.Errorf("offline drain timed out"))
	defer cancel()
	if err := supervisor.Drain(drainCtx, steps); err != nil {
		logger.Error("offline drain", slog.Any("err", err))
	}
}

const httpDrainBudgetFallback = 30 * time.Second

func serveHTTP(ctx context.Context, logger *slog.Logger, handler http.Handler, httpCfg config.HTTPConfig, halveCh <-chan struct{}, ownedDrainRunners map[string]func(context.Context) error, supervisorDeathStopsServing <-chan error) int {
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
		shutCtx, cancel := context.WithTimeoutCause(ctx, budget, fmt.Errorf("http drain timed out"))
		defer cancel()
		go func() {
			select {
			case <-halveCh:
				cancel()
			case <-shutCtx.Done():
			}
		}()
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
		drainCtx, cancel := context.WithTimeoutCause(context.Background(), supervisor.DefaultDrainTimeout, fmt.Errorf("drain timed out"))
		defer cancel()
		go func() {
			select {
			case <-halveCh:
				cancel()
			case <-drainCtx.Done():
			}
		}()
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
