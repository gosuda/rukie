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
	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/logging"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: rukie <command> [flags]\ncommands: serve")
		return 64
	}
	switch argv[0] {
	case "serve":
		return runServe(argv[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", argv[0])
		return 64
	}
}

type stubAuth struct{}

func (stubAuth) Bearer(_ context.Context, _ string) (op.Principal, error) {
	return op.Principal{}, op.ErrUnauthenticated
}

func (stubAuth) Session(_ context.Context, _ string) (op.Principal, error) {
	return op.AnonymousPrincipal(), nil
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
	views := op.NewViews()
	api.RegisterViews(views)
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
		Authenticator: stubAuth{},
		Idempotency:   idem,
		Audit:         auditor,
		Limiter:       access.NewLimiter(),
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
	server.Handle("POST /-/mcp", api.MCPStub())
	server.Handle("GET /-/mcp", api.MCPStub())
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
