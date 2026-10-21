package main

import (
	"cmp"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/cli"
	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/logging"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/sysops"
)

var devSeedExtras []func(context.Context, string) error

type devFlags struct {
	dir    string
	rukBin string
	addr   string
}

type devDaemonConfig struct {
	SocketPath          string   `toml:"socket_path"`
	RepoRoots           []string `toml:"repo_roots"`
	AllowedUIDs         []int    `toml:"allowed_uids"`
	FrameTimeoutMs      int      `toml:"frame_timeout_ms"`
	RetentionIntervalMs int      `toml:"retention_interval_ms"`
}

type devRun struct {
	start time.Time
	std   cli.IO
	flags devFlags
	dir   string
	fresh bool
}

func runDev(argv []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runDevCtx(ctx, stdIO(), argv)
}

func runDevCtx(ctx context.Context, std cli.IO, argv []string) int {
	flags, rest, err := parseDevArgs(argv)
	if err != nil {
		emitDevErr(std, err.Error())
		return 64
	}
	if len(rest) > 0 && rest[0] == "help" {
		_, _ = fmt.Fprintln(std.Stdout, "usage: rukie dev [--dir <dir>] [--ruk-bin <bin>] [--addr <addr>] [reset|seed]")
		_, _ = fmt.Fprintln(std.Stdout, "reset and seed manage the named --dir environment")
		return 0
	}
	dir := flags.dir
	fresh := false
	if dir == "" {
		if len(rest) > 0 {
			emitDevErr(std, "dev "+rest[0]+" needs --dir <dir>")
			return 64
		}
		tmp, err := os.MkdirTemp("", "rukie-dev-*")
		if err != nil {
			emitDevErr(std, "dev: "+err.Error())
			return 1
		}
		dir, fresh = tmp, true
	}
	run := devRun{std: std, flags: flags, dir: dir, start: time.Now(), fresh: fresh}
	if len(rest) == 0 {
		return run.serve(ctx)
	}
	switch rest[0] {
	case "reset":
		return run.reset()
	case "seed":
		return run.seed(ctx)
	default:
		emitDevErr(std, "dev: unknown subcommand "+rest[0])
		return 64
	}
}

func parseDevArgs(argv []string) (devFlags, []string, error) {
	var flags devFlags
	var rest []string
	i := 0
	for i < len(argv) {
		name, value, isFlag := cutDevFlag(argv[i])
		if !isFlag {
			rest = append(rest, argv[i:]...)
			break
		}
		if name == "help" || name == "h" {
			return flags, []string{"help"}, nil
		}
		if value == "" && !strings.Contains(argv[i], "=") {
			if i+1 >= len(argv) || strings.HasPrefix(argv[i+1], "--") {
				return flags, nil, fmt.Errorf("dev: flag --%s needs an argument", name)
			}
			i++
			value = argv[i]
		}
		switch name {
		case "dir":
			flags.dir = value
		case "ruk-bin":
			flags.rukBin = value
		case "addr":
			flags.addr = value
		default:
			return flags, nil, fmt.Errorf("dev: unknown flag --%s", name)
		}
		i++
	}
	if len(rest) > 1 {
		return flags, nil, fmt.Errorf("dev: takes no arguments after the subcommand")
	}
	return flags, rest, nil
}

func cutDevFlag(arg string) (string, string, bool) {
	rest, found := strings.CutPrefix(arg, "--")
	if !found {
		return "", "", false
	}
	name, value, _ := strings.Cut(rest, "=")
	return name, value, true
}

func emitDevErr(std cli.IO, msg string) {
	_, _ = fmt.Fprintln(std.Stderr, msg)
}

func (d *devRun) reset() int {
	if _, err := os.Stat(d.dir); err != nil {
		if os.IsNotExist(err) {
			_, _ = fmt.Fprintln(d.std.Stdout, "dev reset: "+d.dir+" is absent")
			return 0
		}
		emitDevErr(d.std, "dev reset: "+err.Error())
		return 1
	}
	if err := os.RemoveAll(d.dir); err != nil {
		emitDevErr(d.std, "dev reset: "+err.Error())
		return 1
	}
	_, _ = fmt.Fprintln(d.std.Stdout, "dev reset: removed "+d.dir)
	return 0
}

func (d *devRun) seed(ctx context.Context) int {
	_, password, token, err := seedDevData(ctx, d.dir)
	if err != nil {
		emitDevErr(d.std, err.Error())
		return 1
	}
	d.printCredentials("", password, token)
	return 0
}

func (d *devRun) printCredentials(base, password, token string) {
	out := d.std.Stdout
	if base != "" {
		_, _ = fmt.Fprintln(out, "rukie dev ready in "+time.Since(d.start).Truncate(time.Millisecond).String())
		_, _ = fmt.Fprintln(out, "instance: "+base)
	}
	_, _ = fmt.Fprintln(out, "user: dev")
	_, _ = fmt.Fprintln(out, "password: "+password)
	_, _ = fmt.Fprintln(out, "token: "+token)
	_, _ = fmt.Fprintln(out, "data: "+d.dir)
}

func genDevPassword() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 20)
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("genDevPassword: %w", err)
	}
	for i := range out {
		out[i] = alphabet[int(raw[i])%len(alphabet)]
	}
	return string(out), nil
}

func seedDevData(ctx context.Context, dir string) (int64, string, string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, "", "", fmt.Errorf("seedDevData: %w", err)
	}
	dbPath := filepath.Join(dir, "meta.sqlite")
	st, err := store.Open(ctx, store.Options{Path: dbPath})
	if err != nil {
		return 0, "", "", fmt.Errorf("seedDevData: %w", err)
	}
	svc, err := auth.Open(dbPath)
	if err != nil {
		_ = st.Close(context.Background())
		return 0, "", "", fmt.Errorf("seedDevData: %w", err)
	}
	userID, password, token, seedErr := seedDevUser(ctx, svc)
	if seedErr != nil {
		_ = svc.Close()
		_ = st.Close(context.Background())
		return 0, "", "", seedErr
	}
	skipped, herr := seedDevHello(ctx, dir, st, userID)
	if herr != nil {
		_ = svc.Close()
		_ = st.Close(context.Background())
		return 0, "", "", herr
	}
	if skipped {
		_, _ = fmt.Fprintln(os.Stderr, "dev: hello repo skipped: ruk binary is not available")
	}
	_ = svc.Close()
	_ = st.Close(context.Background())
	for _, extra := range devSeedExtras {
		if err := extra(ctx, dir); err != nil {
			return 0, "", "", fmt.Errorf("seedDevData: %w", err)
		}
	}
	return userID, password, token, nil
}

func seedDevHello(ctx context.Context, dir string, st *store.Store, userID int64) (bool, error) {
	bin, berr := rukproc.ResolveBinary("")
	if berr != nil {
		return true, nil
	}
	roots := filepath.Join(dir, "repos")
	if merr := os.MkdirAll(filepath.Join(roots, "dev"), 0o750); merr != nil {
		return false, fmt.Errorf("seedDevHello: %w", merr)
	}
	repoDir := filepath.Join(roots, "dev", "hello.git")
	if _, serr := os.Stat(filepath.Join(repoDir, ".ruk")); serr != nil {
		if out, rerr := rukproc.Run(ctx, bin, []string{"git", "init", repoDir}, devSeedEnv()); rerr != nil {
			return false, fmt.Errorf("seedDevHello: %w\n%s", rerr, out)
		}
	}
	var present bool
	rerr := st.Read(ctx, func(tx *store.ReadTx) error {
		_, ferr := tx.FindRepo(userID, 0, "hello")
		present = ferr == nil
		return nil
	})
	if rerr != nil {
		return false, fmt.Errorf("seedDevHello: %w", rerr)
	}
	if present {
		return false, nil
	}
	now := time.Now()
	serr := st.Write(ctx, func(tx *store.WriteTx) error {
		id, ierr := tx.InsertRepo(now, store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: userID, Valid: true},
			Name:          "hello",
			Visibility:    "private",
			Description:   "Seeded development repository",
			DefaultBranch: "main",
			RukPath:       "dev/hello.git",
		})
		if ierr != nil {
			return ierr
		}
		return tx.InsertDefaultRule(id, now, "main")
	})
	if serr != nil {
		return false, fmt.Errorf("seedDevHello: %w", serr)
	}
	return false, nil
}

func devSeedEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"RUK_COORDINATOR=off",
		"LANG=C",
	}
}

func seedDevUser(ctx context.Context, svc *auth.Service) (int64, string, string, error) {
	password, err := genDevPassword()
	if err != nil {
		return 0, "", "", err
	}
	user, err := svc.BootstrapAdmin(ctx, "dev", password, "127.0.0.1")
	if err != nil {
		msg := err.Error()
		if msg != "an admin already exists" && msg != "username is taken" {
			return 0, "", "", fmt.Errorf("seedDevUser: %w", err)
		}
		existing, gerr := svc.GetUser(ctx, "dev")
		if gerr != nil {
			return 0, "", "", fmt.Errorf("seedDevUser: %w", gerr)
		}
		if _, rerr := svc.ResetPassword(ctx, existing.ID, auth.ResetPasswordInput{Target: "dev", Password: password}, "127.0.0.1"); rerr != nil {
			return 0, "", "", fmt.Errorf("seedDevUser: %w", rerr)
		}
		user = existing
	}
	scopes := op.Scopes(0).
		Add(op.ScopeRepoCreate).
		Add(op.ScopeRepoRead).
		Add(op.ScopeRepoWrite).
		Add(op.ScopeIntentsRead).
		Add(op.ScopeIntentsWrite)
	minted, err := svc.CreateToken(ctx, auth.CreateTokenInput{Name: "dev", UserID: user.ID, Scopes: scopes, IP: "127.0.0.1"})
	if err != nil {
		return 0, "", "", fmt.Errorf("seedDevUser: %w", err)
	}
	return user.ID, password, minted.Secret, nil
}

func (d *devRun) writeDaemonConfig() (string, string, error) {
	if err := os.MkdirAll(d.dir, 0o750); err != nil {
		return "", "", fmt.Errorf("writeDaemonConfig: %w", err)
	}
	roots := filepath.Join(d.dir, "repos")
	if err := os.MkdirAll(roots, 0o750); err != nil {
		return "", "", fmt.Errorf("writeDaemonConfig: %w", err)
	}
	socket := filepath.Join(d.dir, "ruk.sock")
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return "", "", fmt.Errorf("writeDaemonConfig: %w", err)
	}
	cfg := devDaemonConfig{
		SocketPath:          socket,
		RepoRoots:           []string{roots},
		AllowedUIDs:         []int{os.Getuid()},
		FrameTimeoutMs:      5000,
		RetentionIntervalMs: 0,
	}
	var encoded strings.Builder
	if err := toml.NewEncoder(&encoded).Encode(cfg); err != nil {
		return "", "", fmt.Errorf("writeDaemonConfig: %w", err)
	}
	path := filepath.Join(d.dir, "daemon.toml")
	if err := os.WriteFile(path, []byte(encoded.String()), 0o600); err != nil {
		return "", "", fmt.Errorf("writeDaemonConfig: %w", err)
	}
	return path, socket, nil
}

func waitDaemon(ctx context.Context, socket string) error {
	deadline := time.Now().Add(15 * time.Second)
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		client, err := ruk.Dial(dialCtx, socket)
		cancel()
		if err == nil {
			_ = client.Close()
			return nil
		}
		if ctx.Err() != nil {
			return fmt.Errorf("waitDaemon: %w", ctx.Err())
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waitDaemon: daemon did not answer at %s", socket)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waitDaemon: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func devHTTPServer(app *app, cfg config.Config) *http.Server {
	return &http.Server{
		ReadTimeout:       time.Duration(cfg.HTTP.ReadTimeoutMs) * time.Millisecond,
		ReadHeaderTimeout: time.Duration(cfg.HTTP.ReadHeaderTimeoutMs) * time.Millisecond,
		WriteTimeout:      time.Duration(cfg.HTTP.WriteTimeoutMs) * time.Millisecond,
		IdleTimeout:       time.Duration(cfg.HTTP.IdleTimeoutMs) * time.Millisecond,
		Handler:           app.handler,
	}
}

func (d *devRun) serve(ctx context.Context) int {
	if d.fresh {
		defer func() {
			_ = os.RemoveAll(d.dir)
		}()
	}
	daemonPath, socket, err := d.writeDaemonConfig()
	if err != nil {
		emitDevErr(d.std, err.Error())
		return 1
	}
	bin, err := rukproc.ResolveBinary(d.flags.rukBin)
	if err != nil {
		emitDevErr(d.std, err.Error())
		return 1
	}
	child, err := rukproc.StartServe(bin, daemonPath, os.Environ(), os.Stderr, os.Stderr)
	if err != nil {
		emitDevErr(d.std, err.Error())
		return 1
	}
	defer func() {
		_ = child.Kill()
	}()
	if derr := waitDaemon(ctx, socket); derr != nil {
		emitDevErr(d.std, derr.Error())
		return 1
	}
	ln, err := net.Listen("tcp", cmp.Or(d.flags.addr, "127.0.0.1:0"))
	if err != nil {
		emitDevErr(d.std, "dev: "+err.Error())
		return 78
	}
	defer func() {
		_ = ln.Close()
	}()
	base := "http://" + ln.Addr().String()
	cfg := config.Defaults()
	cfg.DataDir = d.dir
	cfg.Store.SQLite.Path = filepath.Join(d.dir, "meta.sqlite")
	cfg.SecretKeyFile = filepath.Join(d.dir, "dev-secret.key")
	cfg.HTTP.Listen = []string{ln.Addr().String()}
	cfg.HTTP.ExternalURL = base
	cfg.Ruk.SocketPath = socket
	cfg.Ruk.RepoRoot = filepath.Join(d.dir, "repos")
	cfg.Ruk.BinaryPath = bin
	if verr := cfg.Validate(); verr != nil {
		emitDevErr(d.std, "dev: "+verr.Error())
		return 78
	}
	_, password, token, serr := seedDevData(ctx, d.dir)
	if serr != nil {
		emitDevErr(d.std, serr.Error())
		return 1
	}
	logger, lerr := logging.New(cfg.Log.Level, cfg.Log.Format, cfg.Log.Source)
	if lerr != nil {
		emitDevErr(d.std, lerr.Error())
		return 78
	}
	slog.SetDefault(logger)
	if _, serr := os.Stat(cfg.SecretKeyFile); serr != nil {
		if !os.IsNotExist(serr) {
			emitDevErr(d.std, serr.Error())
			return 1
		}
		if _, gerr := sysops.GenerateSecretKey(cfg.SecretKeyFile); gerr != nil {
			emitDevErr(d.std, gerr.Error())
			return 1
		}
	}
	app, cleanup, err := setupApp(ctx, cfg, logger, nil)
	defer cleanup()
	if err != nil {
		emitDevErr(d.std, err.Error())
		return 1
	}
	httpServer := devHTTPServer(app, cfg)
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Serve(ln)
	}()
	d.printCredentials(base, password, token)
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Second, fmt.Errorf("dev: shutdown timed out"))
		defer cancel()
		_ = httpServer.Shutdown(shutCtx)
		return 0
	case serr := <-errCh:
		if serr != nil && !errors.Is(serr, http.ErrServerClosed) {
			emitDevErr(d.std, "dev: "+serr.Error())
			return 1
		}
		return 0
	}
}
