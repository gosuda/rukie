package op_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
)

type splitSiteIn struct {
	Provider string `path:"provider"`
}

type splitSiteOut struct {
	Via      string `json:"via"`
	Provider string `json:"provider"`
}

type splitOwnerIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
}

type splitOwnerOut struct {
	Via   string `json:"via"`
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

type splitLoginOut struct {
	Via string `json:"via"`
}

type splitNameIn struct {
	Owner string `path:"owner"`
}

type splitGitIn struct {
	Owner string `path:"owner"`
	Repo  string `path:"repo"`
	Rest  string `path:"rest"`
}

type splitGitOut struct {
	Via string `json:"via"`
}

func stubIsSite(seg string) bool {
	return seg == "login" || seg == "-"
}

func mountSplitTable(t *testing.T, table *op.Table) *op.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv, err := op.Mount(mux, table, op.Config{
		Authenticator: stubAuth{granted: grantedPrincipal()},
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(_ context.Context, _ op.AuditRow) error { return nil }),
		CSRFKey:       testCSRFKey,
		Views:         op.NewViews(),
		Title:         "test",
		Version:       "0",
		IsSiteSegment: stubIsSite,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	return srv
}

func registerSplitTable(t *testing.T) *op.Server {
	t.Helper()
	table := op.NewTable()
	mustRegister(t, table, op.Op[splitSiteIn, splitSiteOut]{
		Name: "test.site.oidc", Kind: op.KindRead, Method: http.MethodGet, Path: "/login/oidc/{provider}",
		Scope: op.ScopePublicRead, Summary: "Site OIDC",
		Handler: func(_ context.Context, _ op.Principal, in splitSiteIn) (splitSiteOut, error) {
			return splitSiteOut{Via: "site", Provider: in.Provider}, nil
		},
	})
	mustRegister(t, table, op.Op[splitOwnerIn, splitOwnerOut]{
		Name: "test.owner.settings", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/settings",
		Scope: op.ScopePublicRead, Summary: "Owner settings",
		Handler: func(_ context.Context, _ op.Principal, in splitOwnerIn) (splitOwnerOut, error) {
			return splitOwnerOut{Via: "owner", Owner: in.Owner, Repo: in.Repo}, nil
		},
	})
	mustRegister(t, table, op.Op[splitLoginOut, splitLoginOut]{
		Name: "test.site.login", Kind: op.KindRead, Method: http.MethodGet, Path: "/login",
		Scope: op.ScopePublicRead, Summary: "Site login",
		Handler: func(_ context.Context, _ op.Principal, _ splitLoginOut) (splitLoginOut, error) {
			return splitLoginOut{Via: "site-login"}, nil
		},
	})
	mustRegister(t, table, op.Op[splitNameIn, splitOwnerOut]{
		Name: "test.owner.profile", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}",
		Scope: op.ScopePublicRead, Summary: "Owner profile",
		Handler: func(_ context.Context, _ op.Principal, in splitNameIn) (splitOwnerOut, error) {
			return splitOwnerOut{Via: "owner-profile", Owner: in.Owner}, nil
		},
	})
	mustRegister(t, table, op.Op[splitGitIn, splitGitOut]{
		Name: "test.owner.git", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}/{repo}/git/{rest...}",
		Scope: op.ScopePublicRead, Summary: "Owner git",
		Handler: func(_ context.Context, _ op.Principal, _ splitGitIn) (splitGitOut, error) {
			return splitGitOut{Via: "git"}, nil
		},
	})
	return mountSplitTable(t, table)
}

func getSplit(t *testing.T, srv *op.Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestSplitMuxSeparatesConflictingPatterns(t *testing.T) {
	srv := registerSplitTable(t)
	rec := getSplit(t, srv, "/login/oidc/settings")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"via":"site"`) {
		t.Fatalf("oidc route: code=%d body=%q", rec.Code, rec.Body.String())
	}
	rec = getSplit(t, srv, "/alice/hello/settings")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"via":"owner"`) {
		t.Fatalf("owner route: code=%d body=%q", rec.Code, rec.Body.String())
	}
	rec = getSplit(t, srv, "/bob.keys")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"via":"owner-profile"`) {
		t.Fatalf("dot-suffix owner shape: code=%d body=%q", rec.Code, rec.Body.String())
	}
	rec = getSplit(t, srv, "/alice/hello/git/info/refs")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"via":"git"`) {
		t.Fatalf("git shape: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSplitMuxReservedNeverReachesOwner(t *testing.T) {
	srv := registerSplitTable(t)
	rec := getSplit(t, srv, "/login")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"via":"site-login"`) {
		t.Fatalf("reserved first segment: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSplitMuxRejectsUnlistedLiteral(t *testing.T) {
	table := op.NewTable()
	mustRegister(t, table, op.Op[splitLoginOut, splitLoginOut]{
		Name: "test.bad.literal", Kind: op.KindRead, Method: http.MethodGet, Path: "/frobnicate/x",
		Scope: op.ScopePublicRead, Summary: "Unlisted literal",
		Handler: func(_ context.Context, _ op.Principal, _ splitLoginOut) (splitLoginOut, error) {
			return splitLoginOut{}, nil
		},
	})
	mux := http.NewServeMux()
	_, err := op.Mount(mux, table, op.Config{
		Authenticator: stubAuth{granted: grantedPrincipal()},
		Title:         "test",
		Version:       "0",
		IsSiteSegment: stubIsSite,
	})
	if err == nil || !strings.Contains(err.Error(), "frobnicate") {
		t.Fatalf("mount err = %v, want the frobnicate segment named", err)
	}
}

func TestSplitMuxNilClassifierRejectsOwnerPatterns(t *testing.T) {
	table := op.NewTable()
	mustRegister(t, table, op.Op[splitNameIn, splitOwnerOut]{
		Name: "test.owner.profile", Kind: op.KindRead, Method: http.MethodGet, Path: "/{owner}",
		Scope: op.ScopePublicRead, Summary: "Owner profile",
		Handler: func(_ context.Context, _ op.Principal, _ splitNameIn) (splitOwnerOut, error) {
			return splitOwnerOut{}, nil
		},
	})
	mux := http.NewServeMux()
	_, err := op.Mount(mux, table, op.Config{
		Authenticator: stubAuth{granted: grantedPrincipal()},
		Title:         "test",
		Version:       "0",
	})
	if err == nil || !strings.Contains(err.Error(), "IsSiteSegment") {
		t.Fatalf("mount err = %v, want the missing classifier named", err)
	}
}
