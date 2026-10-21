package op_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
)

type mcpPathIn struct {
	Rev  string `path:"rev"`
	Path string `path:"path"`
}

type mcpPathOut struct {
	OK bool `json:"ok"`
}

type mcpBranchIn struct {
	Branch string `path:"branch"`
}

func mountPathTable(t *testing.T) (*op.Server, *op.Document) {
	t.Helper()
	table := op.NewTable()
	if err := op.Register(table, op.Op[mcpPathIn, mcpPathOut]{
		Name: "test.path.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/blob/{rev}/{path...}",
		Scope: op.ScopePublicRead, Summary: "Path escaping probe",
		Handler: func(context.Context, op.Principal, mcpPathIn) (mcpPathOut, error) {
			return mcpPathOut{OK: true}, nil
		},
	}); err != nil {
		t.Fatalf("register path op: %v", err)
	}
	if err := op.Register(table, op.Op[mcpBranchIn, mcpPathOut]{
		Name: "test.branch.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/branch/{branch}",
		Scope: op.ScopePublicRead, Summary: "Branch escaping probe",
		Handler: func(context.Context, op.Principal, mcpBranchIn) (mcpPathOut, error) {
			return mcpPathOut{OK: true}, nil
		},
	}); err != nil {
		t.Fatalf("register branch op: %v", err)
	}
	server, err := op.Mount(http.NewServeMux(), table, op.Config{Title: "test", Version: "0"})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	doc := server.Document()
	if doc == nil {
		t.Fatalf("missing document")
	}
	return server, doc
}

func pathSpec(t *testing.T, server *op.Server, name string) op.Spec {
	t.Helper()
	for _, s := range server.Specs() {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("missing spec %q", name)
	return op.Spec{}
}

func TestToRequestCatchAllKeepsSlashes(t *testing.T) {
	server, doc := mountPathTable(t)
	spec := pathSpec(t, server, "test.path.get")
	base, err := url.Parse("http://127.0.0.1:3000")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	req, err := op.Args{"rev": "main", "path": "src/main.go"}.ToRequest(&spec, doc, base)
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}
	if got := req.URL.Path; got != "/test/blob/main/src/main.go" {
		t.Fatalf("Path = %q", got)
	}
	if got := req.URL.EscapedPath(); strings.Contains(got, "%25") {
		t.Fatalf("double escaped: %q", got)
	}
	if got := req.URL.EscapedPath(); got != "/test/blob/main/src/main.go" {
		t.Fatalf("EscapedPath = %q", got)
	}
}

func TestToRequestSingleSegmentEscapesOnce(t *testing.T) {
	server, doc := mountPathTable(t)
	spec := pathSpec(t, server, "test.branch.get")
	base, err := url.Parse("http://127.0.0.1:3000")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	req, err := op.Args{"branch": "feat/x"}.ToRequest(&spec, doc, base)
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}
	if got := req.URL.Path; got != "/test/branch/feat/x" {
		t.Fatalf("Path = %q", got)
	}
	if got := req.URL.EscapedPath(); got != "/test/branch/feat%2Fx" {
		t.Fatalf("EscapedPath = %q", got)
	}
}

func TestToRequestSpecialCharsEscapeOnce(t *testing.T) {
	server, doc := mountPathTable(t)
	spec := pathSpec(t, server, "test.branch.get")
	base, err := url.Parse("http://127.0.0.1:3000")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	req, err := op.Args{"branch": "a b?c"}.ToRequest(&spec, doc, base)
	if err != nil {
		t.Fatalf("ToRequest: %v", err)
	}
	if got := req.URL.Path; got != "/test/branch/a b?c" {
		t.Fatalf("Path = %q", got)
	}
	if got := req.URL.EscapedPath(); strings.Contains(got, "%25") {
		t.Fatalf("double escaped: %q", got)
	}
	if got, want := req.URL.EscapedPath(), "/test/branch/a%20b%3Fc"; got != want {
		t.Fatalf("EscapedPath = %q, want %q", got, want)
	}
}
