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
	"syscall"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/cli"
	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/logging"
	"github.com/gosuda/rukie/internal/mcp"
	"github.com/gosuda/rukie/internal/op"
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
		fmt.Fprintln(os.Stderr, "usage: rukie <command> [flags]\ncommands: serve, bootstrap, mcp, auth, user, admin, api")
		return 64
	}
	switch argv[0] {
	case "serve":
		return runServe(argv[1:])
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
	st, err := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path, Logger: logger})
	if err != nil {
		logger.Error("open store", slog.Any("err", err))
		return 1
	}
	defer func() {
		_ = st.Close(ctx)
	}()
	table := op.NewTable()
	docs := &api.DocStore{}
	if regErr := api.RegisterAll(table, docs); regErr != nil {
		logger.Error("register ops", slog.Any("err", regErr))
		return 1
	}
	authSvc, err := auth.Open(cfg.Store.SQLite.Path)
	if err != nil {
		logger.Error("open auth", slog.Any("err", err))
		return 1
	}
	defer func() {
		_ = authSvc.Close()
	}()
	limiter := access.NewLimiter()
	authDeps := api.Deps{Service: authSvc, Limiter: limiter, Reveals: auth.NewRevealCache()}
	if regErr := api.RegisterAuth(table, authDeps); regErr != nil {
		logger.Error("register auth ops", slog.Any("err", regErr))
		return 1
	}
	if regErr := api.RegisterUser(table, authDeps); regErr != nil {
		logger.Error("register user ops", slog.Any("err", regErr))
		return 1
	}
	if regErr := api.RegisterAdmin(table, authDeps); regErr != nil {
		logger.Error("register admin ops", slog.Any("err", regErr))
		return 1
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
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
		logger.Error("csrf key", slog.Any("err", rerr))
		return 1
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
		logger.Error("mount ops", "err", err)
		return 1
	}
	docs.Set(server.OpenAPI())
	server.Handle("GET /healthz", api.Healthz())
	server.Handle("GET /readyz", api.Readiness{Store: st, Daemon: api.NotReadyDaemon}.Handler())
	mcpMount, err := mcp.NewMount(authSvc, server, cfg.HTTP.ExternalURL)
	if err != nil {
		logger.Error("mount mcp", slog.Any("err", err))
		return 1
	}
	server.Handle("POST /-/mcp", mcpMount.Authenticated())
	server.Handle("GET /-/mcp", mcpMount.MethodNotAllowed())
	server.Handle("DELETE /-/mcp", mcpMount.MethodNotAllowed())
	httpServer := &http.Server{
		ReadTimeout:       time.Duration(cfg.HTTP.ReadTimeoutMs) * time.Millisecond,
		ReadHeaderTimeout: time.Duration(cfg.HTTP.ReadHeaderTimeoutMs) * time.Millisecond,
		WriteTimeout:      time.Duration(cfg.HTTP.WriteTimeoutMs) * time.Millisecond,
		IdleTimeout:       time.Duration(cfg.HTTP.IdleTimeoutMs) * time.Millisecond,
		Handler:           server,
	}
	errCh := make(chan error, len(cfg.HTTP.Listen))
	for _, addr := range cfg.HTTP.Listen {
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
