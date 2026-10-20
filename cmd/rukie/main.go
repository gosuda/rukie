package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/cli"
	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/githttp"
	"github.com/gosuda/rukie/internal/logging"
	"github.com/gosuda/rukie/internal/mcp"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
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
	cfg, err := config.Load(configPath(*configFlag))
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	app, cleanup, err := setupApp(ctx, cfg, logger)
	defer cleanup()
	if err != nil {
		logger.Error("setup app", slog.Any("err", err))
		return 1
	}
	return serveHTTP(ctx, logger, app.handler, cfg.HTTP)
}

type app struct {
	server  *op.Server
	handler http.Handler
	store   *store.Store
	auth    *auth.Service
	docs    *api.DocStore
}

type gitFallback struct {
	git      *githttp.Server
	fallback http.Handler
}

func (h gitFallback) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

func setupApp(ctx context.Context, cfg config.Config, logger *slog.Logger) (*app, func(), error) {
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
	server.Handle("GET /readyz", api.Readiness{Store: st, Daemon: api.NotReadyDaemon}.Handler())
	mcpMount, err := mcp.NewMount(authSvc, server, cfg.HTTP.ExternalURL)
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	server.Handle("POST /-/mcp", mcpMount.Authenticated())
	server.Handle("GET /-/mcp", mcpMount.MethodNotAllowed())
	server.Handle("DELETE /-/mcp", mcpMount.MethodNotAllowed())
	git, err := githttp.New(githttp.Config{
		Auth:       authSvc,
		Limiter:    limiter,
		SocketPath: cfg.Ruk.SocketPath,
		RepoRoot:   cfg.Ruk.RepoRoot,
		Limits:     wire.DefaultGitLimits(),
	})
	if err != nil {
		cleanup()
		return nil, cleanup, fmt.Errorf("setupApp: %w", err)
	}
	return &app{server: server, handler: gitFallback{git: git, fallback: server}, store: st, auth: authSvc, docs: docs}, cleanup, nil
}

func serveHTTP(ctx context.Context, logger *slog.Logger, handler http.Handler, httpCfg config.HTTPConfig) int {
	httpServer := &http.Server{
		ReadTimeout:       time.Duration(httpCfg.ReadTimeoutMs) * time.Millisecond,
		ReadHeaderTimeout: time.Duration(httpCfg.ReadHeaderTimeoutMs) * time.Millisecond,
		WriteTimeout:      time.Duration(httpCfg.WriteTimeoutMs) * time.Millisecond,
		IdleTimeout:       time.Duration(httpCfg.IdleTimeoutMs) * time.Millisecond,
		Handler:           handler,
	}
	errCh := make(chan error, len(httpCfg.Listen))
	for _, addr := range httpCfg.Listen {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			logger.Error("listen", slog.String("addr", addr), slog.Any("err", err))
			return 78
		}
		logger.Info("listening", slog.String("addr", ln.Addr().String()))
		go func() {
			errCh <- httpServer.Serve(ln)
		}()
	}
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Second, fmt.Errorf("shutdown timed out"))
		defer cancel()
		_ = httpServer.Shutdown(shutCtx)
		return 0
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", slog.Any("err", err))
			return 1
		}
		return 0
	}
}
