package api_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

type llmsEnv struct {
	server *op.Server
	base   string
}

func buildLLMSEnv(t *testing.T) *llmsEnv {
	t.Helper()
	path := t.TempDir() + "/llms.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(t.Context()) })
	svc, err := auth.Open(path)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache()}
	table := op.NewTable()
	docs := &api.DocStore{}
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, docs) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
	} {
		if serr := step(); serr != nil {
			t.Fatalf("register: %v", serr)
		}
	}
	base := "http://example.com"
	fill, err := api.RegisterLLMS(table, base)
	if err != nil {
		t.Fatalf("register llms: %v", err)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterLLMSViews(views)
	var key [32]byte
	if _, rerr := rand.Read(key[:]); rerr != nil {
		t.Fatalf("rand: %v", rerr)
	}
	mux := http.NewServeMux()
	server, err := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       key[:],
		Views:         views,
		Title:         "test",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	fill(server.Document())
	return &llmsEnv{server: server, base: base}
}

func getLLMS(t *testing.T, e *llmsEnv, method, target, accept string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func TestLLMSFreshness(t *testing.T) {
	e := buildLLMSEnv(t)
	specs := e.server.Specs()
	if len(specs) == 0 {
		t.Fatal("no specs registered")
	}
	index := api.LLMSIndex(specs, e.base)
	full := api.LLMSFull(specs, e.server.Document(), e.base)
	for _, s := range specs {
		if !strings.Contains(index, "`"+s.Name+"`") {
			t.Fatalf("index misses %q", s.Name)
		}
		if !strings.Contains(full, "## `"+s.Name+"`") {
			t.Fatalf("full misses section %q", s.Name)
		}
		if !strings.Contains(full, op.ToolName(s.Name)) {
			t.Fatalf("full misses MCP name for %q", s.Name)
		}
	}
	for _, want := range []string{e.base, "/-/mcp", "rukie auth login", "text/markdown", "/llms-full.txt", "/-/api/openapi.json"} {
		if !strings.Contains(index, want) {
			t.Fatalf("index misses start-here %q", want)
		}
	}
	grown := append(slices.Clone(specs), op.Spec{
		Name: "api.probe.get", Method: "GET", Path: "/probe",
		Scope: op.ScopePublicRead, Kind: op.KindRead, Summary: "Probe op",
	})
	if got := api.LLMSIndex(grown, e.base); got == index || !strings.Contains(got, "api.probe.get") {
		t.Fatal("index ignores the table it renders")
	}
	if got := api.LLMSFull(grown, e.server.Document(), e.base); got == full || !strings.Contains(got, "## `api.probe.get`") {
		t.Fatal("full ignores the table it renders")
	}
	if got := api.LLMSIndex(nil, e.base); !strings.Contains(got, "# rukie") || strings.Contains(got, "| `") {
		t.Fatalf("empty index = %q", got)
	}
	if got := api.LLMSFull(nil, nil, ""); !strings.Contains(got, "# rukie operation reference") || strings.Contains(got, "## `") {
		t.Fatalf("empty full = %q", got)
	}
	if got := api.LLMSIndex(specs, ""); !strings.Contains(got, "- Full per-op reference: /llms-full.txt") {
		t.Fatalf("empty base must yield origin-relative links: %q", got)
	}
}

func TestLLMSRoutes(t *testing.T) {
	e := buildLLMSEnv(t)
	rec := getLLMS(t, e, http.MethodGet, "/llms.txt", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("index json status %d: %s", rec.Code, rec.Body.String())
	}
	if media := rec.Header().Get("Content-Type"); !strings.HasPrefix(media, "application/json") {
		t.Fatalf("index json type %q", media)
	}
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode index: %v", err)
	}
	text, _ := decoded["text"].(string)
	if !strings.Contains(text, e.base) || !strings.Contains(text, "api.index") {
		t.Fatalf("index json misses base or ops: %q", text)
	}
	if etag := rec.Header().Get("ETag"); etag == "" || len(etag) < 3 || etag[0] != '"' {
		t.Fatalf("index misses a quoted ETag: %q", etag)
	}
	rec = getLLMS(t, e, http.MethodGet, "/llms.txt", "text/markdown")
	if rec.Code != http.StatusOK {
		t.Fatalf("index markdown status %d", rec.Code)
	}
	if media := rec.Header().Get("Content-Type"); !strings.HasPrefix(media, "text/markdown") {
		t.Fatalf("index markdown type %q", media)
	}
	if body := rec.Body.String(); !strings.HasPrefix(body, "# rukie") || strings.Contains(body, "- **text**:") {
		t.Fatalf("index markdown must carry the raw file: %q", body)
	}
	rec = getLLMS(t, e, http.MethodGet, "/llms.txt", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	if rec.Code != http.StatusOK {
		t.Fatalf("index html status %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "<pre>") || strings.Contains(body, "<script") {
		t.Fatalf("index html must wrap the file in pre without scripts")
	}
	rec = getLLMS(t, e, http.MethodGet, "/llms-full.txt", "text/markdown")
	if rec.Code != http.StatusOK {
		t.Fatalf("full markdown status %d", rec.Code)
	}
	body := rec.Body.String()
	for _, s := range e.server.Specs() {
		if !strings.Contains(body, "## `"+s.Name+"`") {
			t.Fatalf("served full misses %q", s.Name)
		}
	}
	for _, want := range []string{"## `auth.login`", "## `api.llms.get`", "## `api.llmsfull.get`", "### Inputs", "### Errors"} {
		if !strings.Contains(body, want) {
			t.Fatalf("served full misses %q", want)
		}
	}
	etag := getLLMS(t, e, http.MethodGet, "/llms.txt", "text/markdown").Header().Get("ETag")
	for _, accept := range []string{"application/json", "text/markdown", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"} {
		req := httptest.NewRequest(http.MethodGet, "/llms.txt", nil)
		req.Header.Set("Accept", accept)
		req.Header.Set("If-None-Match", etag)
		cond := httptest.NewRecorder()
		e.server.ServeHTTP(cond, req)
		if cond.Code != http.StatusNotModified {
			t.Fatalf("%s conditional status %d, want 304", accept, cond.Code)
		}
		if cond.Body.Len() != 0 {
			t.Fatalf("%s 304 must have no body", accept)
		}
	}
	rec = getLLMS(t, e, http.MethodGet, "/llms.txt", "application/xml")
	if rec.Code != http.StatusNotAcceptable {
		t.Fatalf("xml status %d, want 406", rec.Code)
	}
	rec = getLLMS(t, e, http.MethodPost, "/llms.txt", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post status %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Fatalf("allow %q misses GET", allow)
	}
}

func TestLLMSReserved(t *testing.T) {
	routes := map[string]bool{}
	for _, r := range api.Routes() {
		routes[r.Pattern] = true
	}
	for _, want := range []string{"GET /llms.txt", "GET /llms-full.txt"} {
		if !routes[want] {
			t.Fatalf("route table misses %q", want)
		}
	}
	reserved := map[string]bool{}
	for _, name := range api.Reserved() {
		reserved[name] = true
	}
	for _, want := range []string{"llms.txt", "llms-full.txt"} {
		if !reserved[want] {
			t.Fatalf("reserved registry misses %q", want)
		}
	}
}

func TestParityGateLLMS(t *testing.T) {
	e := buildLLMSEnv(t)
	specs := e.server.Specs()
	doc := e.server.Document()
	if doc == nil {
		t.Fatal("missing document")
	}
	principal := op.Principal{UserID: 1, Kind: op.PrincipalHuman, Grants: auth.ScopesAll()}
	tools, err := op.BuildTools(specs, doc, op.ToolFilter{Principal: principal})
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	toolNames := map[string]bool{}
	for _, tool := range tools {
		toolNames[tool.Name] = true
	}
	root, err := op.BuildCommands(specs, doc)
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	entries := op.ReferenceEntries(specs)
	if len(entries) != len(specs) {
		t.Fatalf("reference holds %d entries for %d ops", len(entries), len(specs))
	}
	var refPage strings.Builder
	if err := op.RenderReference(&refPage, op.ViewCtx{}, entries); err != nil {
		t.Fatalf("reference: %v", err)
	}
	index := api.LLMSIndex(specs, e.base)
	full := api.LLMSFull(specs, doc, e.base)
	for _, s := range specs {
		if s.Hidden {
			continue
		}
		if _, ok := doc.Operations[s.Name]; !ok {
			t.Fatalf("openapi misses %q", s.Name)
		}
		if !toolNames[op.ToolName(s.Name)] {
			t.Fatalf("mcp misses %q", s.Name)
		}
		node := root
		for segment := range strings.SplitSeq(s.Name, ".") {
			next, ok := node.Children[segment]
			if !ok {
				t.Fatalf("cli misses %q", s.Name)
			}
			node = next
		}
		if node.Spec == nil || node.Spec.Name != s.Name {
			t.Fatalf("cli leaf for %q resolves nowhere", s.Name)
		}
		if !strings.Contains(refPage.String(), s.Name) {
			t.Fatalf("reference page misses %q", s.Name)
		}
		if !strings.Contains(index, "`"+s.Name+"`") {
			t.Fatalf("llms.txt misses %q", s.Name)
		}
		if !strings.Contains(full, "## `"+s.Name+"`") {
			t.Fatalf("llms-full.txt misses %q", s.Name)
		}
	}
}
