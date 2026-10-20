package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func buildSplitEnv(t *testing.T) *op.Server {
	t.Helper()
	path := t.TempDir() + "/split.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	svc, err := auth.Open(path)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(), Store: st}
	table := op.NewTable()
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, &api.DocStore{}) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
		func() error { return api.RegisterF12(table, deps) },
		func() error { return api.RegisterRepos(table, deps) },
		func() error { return api.RegisterCode(table, deps) },
		func() error { return api.RegisterRefs(table, deps) },
	} {
		if serr := step(); serr != nil {
			t.Fatalf("register: %v", serr)
		}
	}
	if verr := table.Validate(); verr != nil {
		t.Fatalf("validate: %v", verr)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterF12Views(views)
	api.RegisterReposViews(views)
	api.RegisterCodeViews(views)
	api.RegisterRefsViews(views)
	mux := http.NewServeMux()
	server, merr := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       make([]byte, 32),
		Views:         views,
		Title:         "test",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if merr != nil {
		t.Fatalf("mount: %v", merr)
	}
	return server
}

func getSplitJSON(t *testing.T, srv *op.Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestSplitMountsFullTable(t *testing.T) {
	_ = buildSplitEnv(t)
}

func TestSplitOIDCBeatsOwnerSettings(t *testing.T) {
	srv := buildSplitEnv(t)
	rec := getSplitJSON(t, srv, "/login/oidc/settings")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "unknown oidc provider") {
		t.Fatalf("oidc route: code=%d body=%q", rec.Code, rec.Body.String())
	}
	rec = getSplitJSON(t, srv, "/alice/hello/settings")
	if strings.Contains(rec.Body.String(), "unknown oidc provider") {
		t.Fatal("owner route reached the OIDC handler")
	}
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "repository was not found") {
		t.Fatalf("owner route: code=%d body=%q, want the owner repo lookup", rec.Code, rec.Body.String())
	}
}

func TestSplitReservedSegmentsCoverMountedLiterals(t *testing.T) {
	path := t.TempDir() + "/split.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	svc, err := auth.Open(path)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	defer func() { _ = svc.Close() }()
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(), Store: st}
	table := op.NewTable()
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, &api.DocStore{}) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
		func() error { return api.RegisterF12(table, deps) },
		func() error { return api.RegisterRepos(table, deps) },
		func() error { return api.RegisterCode(table, deps) },
		func() error { return api.RegisterRefs(table, deps) },
	} {
		if serr := step(); serr != nil {
			t.Fatalf("register: %v", serr)
		}
	}
	for _, spec := range table.Specs() {
		trimmed := strings.TrimPrefix(spec.Path, "/")
		first, _, _ := strings.Cut(trimmed, "/")
		if strings.HasPrefix(first, "{") {
			continue
		}
		if !api.SiteSegment(first) {
			t.Errorf("mounted literal %q of op %q is not a reserved site segment", first, spec.Name)
		}
	}
	for _, seg := range []string{"login", "logout", "settings", "admin", "new", "explore", "search", "inbox", "changes", "tasks", "ci", "-", "healthz", "readyz", "llms.txt", "llms-full.txt"} {
		if !api.SiteSegment(seg) {
			t.Errorf("reserved segment %q does not route site-side", seg)
		}
	}
}
