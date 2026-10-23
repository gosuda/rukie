package op_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
)

type escapedBranchIn struct {
	Owner  string `path:"owner"`
	Repo   string `path:"repo"`
	Branch string `path:"branch"`
}

type escapedBranchOut struct {
	Branch string `json:"branch"`
}

func TestEscapedSlashParamRoutes(t *testing.T) {
	table := op.NewTable()
	mustRegister(t, table, op.Op[escapedBranchIn, escapedBranchOut]{
		Name: "test.branch.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/{owner}/{repo}/branches/{branch}",
		Scope: op.ScopePublicRead, Summary: "Test branch",
		Handler: func(_ context.Context, _ op.Principal, in escapedBranchIn) (escapedBranchOut, error) {
			return escapedBranchOut{Branch: in.Branch}, nil
		},
	})
	mux := http.NewServeMux()
	srv, err := op.Mount(mux, table, op.Config{
		PublicOrigin:  "http://example.com",
		Authenticator: stubAuth{granted: grantedPrincipal()},
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		CSRFKey:       testCSRFKey,
		Views:         op.NewViews(),
		Title:         "test",
		Version:       "0",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	rec := doRequest(srv, "GET", "/test/ada/hello/branches/feat%2Fx", map[string]string{"Accept": "application/json"}, "")
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "feat/x") {
		t.Fatalf("body misses unescaped branch: %s", rec.Body.String())
	}
}
