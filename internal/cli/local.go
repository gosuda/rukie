package cli

import (
	"context"
	"net/url"
	"strings"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/sysops"
)

func dispatchLocal(ctx context.Context, std IO, inv *op.Invocation, configPath string, w wrapperOpts) int {
	if !strings.HasPrefix(inv.Leaf.Spec.Name, "admin.") {
		emit(std.Stderr, "rukie: only admin ops run with --config; use --server for the rest")
		return 64
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 78
	}
	st, err := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 1
	}
	defer func() { _ = st.Close(ctx) }()
	svc, err := auth.Open(cfg.Store.SQLite.Path)
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 1
	}
	defer func() { _ = svc.Close() }()
	sealKey, serr := sysops.LoadSecretKey(cfg.SecretKeyFile)
	if serr != nil {
		emit(std.Stderr, "rukie:", serr.Error())
		return 2
	}
	svc.SetSealKey(sealKey)
	principal, code := localPrincipal(ctx, std, svc, inv, w)
	if code != 0 {
		return code
	}
	local, err := assemble(api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache()}, auth.NewAuthenticator(svc))
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 1
	}
	if code := applyInput(std, inv); code != 0 {
		return code
	}
	spec := inv.Leaf.Spec
	if w.dryRun {
		if !emitf(std.Stdout, "%s %s\n", spec.Method, spec.Path) {
			return 1
		}
		raw, _ := jsonv2.Marshal(inv.Args, jsonv1.DefaultOptionsV1())
		if !emit(std.Stdout, string(raw)) {
			return 1
		}
		return 0
	}
	if spec.Kind == op.KindMutation && inv.Args["idempotency_key"] == nil {
		inv.Args["idempotency_key"] = op.GenerateIdempotencyKey()
	}
	req, err := inv.Args.ToRequest(spec, local.Document(), localBase(cfg.HTTP.ExternalURL))
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 1
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&op.LocalInvoker{Server: local, Principal: principal}).Invoke(ctx, req)
	if err != nil {
		emit(std.Stderr, "rukie:", err.Error())
		return 1
	}
	return render(std, inv, resp)
}

func localBase(raw string) *url.URL {
	if trimmed := strings.TrimSuffix(strings.TrimSpace(raw), "/"); trimmed != "" {
		if u, err := url.Parse(trimmed); err == nil && u.Host != "" {
			return u
		}
	}
	return &url.URL{Scheme: "http", Host: "internal"}
}

func localPrincipal(ctx context.Context, std IO, svc *auth.Service, inv *op.Invocation, w wrapperOpts) (op.Principal, int) {
	token, session, code := credentials(std, inv, w)
	if code != 0 {
		return op.Principal{}, code
	}
	auther := auth.NewAuthenticator(svc)
	if token != "" {
		p, err := auther.Bearer(ctx, token)
		if err != nil {
			emit(std.Stderr, "rukie: invalid token")
			return op.Principal{}, 3
		}
		return p, 0
	}
	if session != "" {
		secret := session
		if _, value, ok := strings.Cut(session, "="); ok {
			secret = value
		}
		p, err := auther.Session(ctx, strings.TrimSpace(secret))
		if err != nil {
			emit(std.Stderr, "rukie: invalid session")
			return op.Principal{}, 3
		}
		return p, 0
	}
	emit(std.Stderr, "rukie: --token-file or --session-file is required with --config")
	return op.Principal{}, 3
}
