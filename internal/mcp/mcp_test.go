package mcp_test

import (
	"context"
	"crypto/rand"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "modernc.org/sqlite"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/mcp"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

type confGetIn struct {
	ID string `path:"id"`
}

type confGetOut struct {
	ID   string `json:"id"`
	Note string `json:"note"`
}

type confCreateBody struct {
	Note string `json:"note"`
}

type confCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           confCreateBody
}

type confCreateOut struct {
	Note string `json:"note"`
}

type confFailIn struct{}

type confFailOut struct{}

type confEnv struct {
	server *op.Server
	mount  *mcp.Mount
	svc    *auth.Service
	mux    *http.ServeMux
	token  string
	url    string
	userID int64
}

func buildConfEnv(t *testing.T) *confEnv {
	t.Helper()
	ctx := t.Context()
	path := t.TempDir() + "/k8.sqlite"
	st, err := store.Open(ctx, store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	svc, err := auth.Open(path)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	user, err := svc.BootstrapAdmin(ctx, "mcptester", "K8-summit-trail-42x", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	readScope, ok := op.ParseScope("repo:read")
	if !ok {
		t.Fatalf("parse scope")
	}
	minted, err := svc.CreateToken(ctx, auth.CreateTokenInput{UserID: user.ID, Name: "mcp", Scopes: op.Scopes(0).Add(readScope)})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	table := op.NewTable()
	mustReg(t, table, op.Op[confGetIn, confGetOut]{
		Name: "mcpconf.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/mcpconf/{id}",
		Scope: op.ScopePublicRead, Summary: "Read a conf value",
		Handler: func(_ context.Context, _ op.Principal, in confGetIn) (confGetOut, error) {
			return confGetOut{ID: in.ID, Note: "note-for-" + in.ID}, nil
		},
	})
	mustReg(t, table, op.Op[confCreateIn, confCreateOut]{
		Name: "mcpconf.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/mcpconf",
		Scope: op.ScopeRepoRead, Summary: "Store a conf value",
		Handler: func(_ context.Context, _ op.Principal, in confCreateIn) (confCreateOut, error) {
			return confCreateOut{Note: in.Body.Note}, nil
		},
	})
	mustReg(t, table, op.Op[confFailIn, confFailOut]{
		Name: "mcpconf.fail", Kind: op.KindRead, Method: http.MethodGet, Path: "/mcpconf-fail",
		Scope: op.ScopePublicRead, Summary: "Always conflicts",
		Problems: []op.Code{op.CodeConflict},
		Handler: func(_ context.Context, _ op.Principal, _ confFailIn) (confFailOut, error) {
			return confFailOut{}, op.Errorf(op.CodeConflict, "someone else landed first")
		},
	})
	mustReg(t, table, op.Op[confFailIn, confFailOut]{
		Name: "mcpconf.hidden", Kind: op.KindRead, Method: http.MethodGet, Path: "/mcpconf-hidden",
		Scope: op.ScopePublicRead, Summary: "Hidden", Hidden: true,
		Handler: func(_ context.Context, _ op.Principal, _ confFailIn) (confFailOut, error) {
			return confFailOut{}, nil
		},
	})
	var key [32]byte
	_, _ = rand.Read(key[:])
	mux := http.NewServeMux()
	server, err := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       access.NewLimiter(),
		CSRFKey:       key[:],
		Views:         op.NewViews(),
		Title:         "test",
		Version:       "0",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	live := httptest.NewServer(server)
	t.Cleanup(live.Close)
	mount, err := mcp.NewMount(svc, server, live.URL)
	if err != nil {
		t.Fatalf("mcp mount: %v", err)
	}
	server.Handle("POST /-/mcp", mount.Authenticated())
	server.Handle("GET /-/mcp", mount.MethodNotAllowed())
	server.Handle("DELETE /-/mcp", mount.MethodNotAllowed())
	return &confEnv{server: server, mount: mount, svc: svc, mux: mux, token: minted.Secret, url: live.URL, userID: user.ID}
}

func mustReg[In, Out any](t *testing.T, table *op.Table, o op.Op[In, Out]) {
	t.Helper()
	if err := op.Register(table, o); err != nil {
		t.Fatalf("register %q: %v", o.Name, err)
	}
}

type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (b *bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(clone)
}

func dialMCP(t *testing.T, e *confEnv, query string) (*msdk.ClientSession, func()) {
	t.Helper()
	httpClient := &http.Client{Transport: &bearerRoundTripper{token: e.token, base: http.DefaultTransport}}
	client := msdk.NewClient(&msdk.Implementation{Name: "k8test", Title: "k8test", Version: "0"}, nil)
	session, err := client.Connect(t.Context(), &msdk.StreamableClientTransport{
		Endpoint:             e.url + "/-/mcp" + query,
		HTTPClient:           httpClient,
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return session, func() { _ = session.Close() }
}

func httpJSON(t *testing.T, e *confEnv, method, target, body string) (int, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.url+target, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var decoded map[string]any
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("%s %s: decode %d: %v\n%s", method, target, resp.StatusCode, err, string(raw))
	}
	return resp.StatusCode, decoded
}

func TestMCPConformance(t *testing.T) {
	e := buildConfEnv(t)
	session, done := dialMCP(t, e, "?toolsets=mcpconf")
	defer done()
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]*msdk.Tool{}
	for _, tool := range listed.Tools {
		got[tool.Name] = tool
	}
	want := []string{"mcpconf_get", "mcpconf_create", "mcpconf_fail"}
	if len(got) != len(want) {
		t.Fatalf("tools = %v, want %v (hidden must be absent)", keys(got), want)
	}
	for _, name := range want {
		if got[name] == nil {
			t.Fatalf("missing tool %q", name)
		}
	}
	if !got["mcpconf_get"].Annotations.ReadOnlyHint {
		t.Fatalf("read tool must carry readOnlyHint")
	}
	if got["mcpconf_create"].Annotations.ReadOnlyHint {
		t.Fatalf("mutation tool must not carry readOnlyHint")
	}
	createProps, ok := got["mcpconf_create"].InputSchema.(map[string]any)["properties"].(map[string]any)
	if !ok || createProps["idempotency_key"] == nil || createProps["note"] == nil {
		t.Fatalf("mutation schema = %v, want note and idempotency_key", got["mcpconf_create"].InputSchema)
	}

	status, httpBody := httpJSON(t, e, "GET", "/mcpconf/abc", "")
	if status != 200 {
		t.Fatalf("http status %d", status)
	}
	res, err := session.CallTool(t.Context(), &msdk.CallToolParams{Name: "mcpconf_get", Arguments: map[string]any{"id": "abc"}})
	if err != nil {
		t.Fatalf("call read: %v", err)
	}
	if res.IsError {
		t.Fatalf("read IsError: %v", res.Content)
	}
	if !reflect.DeepEqual(res.StructuredContent, httpBody) {
		t.Fatalf("mcp != http:\n%v\n%v", res.StructuredContent, httpBody)
	}

	httpStatus, httpCreated := httpJSON(t, e, "POST", "/mcpconf", `{"note":"hello"}`)
	if httpStatus != 201 && httpStatus != 200 {
		t.Fatalf("http create status %d", httpStatus)
	}
	created, err := session.CallTool(t.Context(), &msdk.CallToolParams{
		Name:      "mcpconf_create",
		Arguments: map[string]any{"note": "hello", "idempotency_key": "k8test-key-1"},
	})
	if err != nil {
		t.Fatalf("call create: %v", err)
	}
	if created.IsError {
		t.Fatalf("create IsError: %+v struct=%v", created.Content, created.StructuredContent)
	}
	if !reflect.DeepEqual(created.StructuredContent, httpCreated) {
		t.Fatalf("mcp != http:\n%v\n%v", created.StructuredContent, httpCreated)
	}

	failStatus, failBody := httpJSON(t, e, "GET", "/mcpconf-fail", "")
	if failStatus != 409 {
		t.Fatalf("http fail status %d", failStatus)
	}
	failed, err := session.CallTool(t.Context(), &msdk.CallToolParams{Name: "mcpconf_fail", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call fail: %v", err)
	}
	if !failed.IsError {
		t.Fatalf("domain error must set IsError")
	}
	for _, key := range []string{"request_id", "trace_id"} {
		delete(failed.StructuredContent.(map[string]any), key)
		delete(failBody, key)
	}
	if !reflect.DeepEqual(failed.StructuredContent, failBody) {
		t.Fatalf("mcp != http:\n%v\n%v", failed.StructuredContent, failBody)
	}
}

func keys(got map[string]*msdk.Tool) []string {
	return slices.Sorted(maps.Keys(got))
}

func TestMCPDefaultToolBudget(t *testing.T) {
	if len(mcp.DefaultToolNames) != mcp.MaxDefaultTools {
		t.Fatalf("planned default list = %d, want %d", len(mcp.DefaultToolNames), mcp.MaxDefaultTools)
	}
	for _, name := range mcp.DefaultToolNames {
		if strings.Contains(name, "_") || strings.Contains(name, " ") {
			t.Fatalf("default tool %q is not dotted-lowercase", name)
		}
		if strings.HasPrefix(name, "task.") {
			t.Fatalf("default tool %q uses the retired task domain", name)
		}
	}
	if err := mcp.CheckPlannedList(); err != nil {
		t.Fatalf("planned list: %v", err)
	}
	if err := mcp.CheckBudget(numberedTools(35)); err != nil {
		t.Fatalf("35 tools must pass: %v", err)
	}
	if err := mcp.CheckBudget(numberedTools(36)); err != nil {
		t.Fatalf("36 tools must pass: %v", err)
	}
	if err := mcp.CheckBudget(numberedTools(37)); err == nil {
		t.Fatalf("37 tools must fail the budget")
	}
	if err := mcp.CheckBudget(append(numberedTools(35), "tool-x")); err == nil {
		t.Fatalf("a duplicate tool name must fail the budget")
	}
}

func numberedTools(n int) []string {
	out := make([]string, 0, n)
	for i := range n {
		out = append(out, "tool-"+strings.Repeat("x", i+1))
	}
	return out
}

func TestMCPDefaultGrowsWithLandedOps(t *testing.T) {
	table := op.NewTable()
	stub := func(_ context.Context, _ op.Principal, _ confFailIn) (confFailOut, error) {
		return confFailOut{}, nil
	}
	for _, name := range mcp.DefaultToolNames {
		mustReg(t, table, op.Op[confFailIn, confFailOut]{
			Name: name, Kind: op.KindRead, Method: http.MethodGet, Path: "/k8dflt/" + strings.ReplaceAll(name, ".", "/"),
			Scope: op.ScopePublicRead, Summary: "Default budget stub", Handler: stub,
		})
	}
	mustReg(t, table, op.Op[confFailIn, confFailOut]{
		Name: "mcpconf.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/mcpconf",
		Scope: op.ScopePublicRead, Summary: "Landed but outside the default set", Handler: stub,
	})
	var key [32]byte
	_, _ = rand.Read(key[:])
	server, err := op.Mount(http.NewServeMux(), table, op.Config{
		Authenticator: denyAuth{},
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       access.NewLimiter(),
		CSRFKey:       key[:],
		Views:         op.NewViews(),
		Title:         "test",
		Version:       "0",
	})
	if err != nil {
		t.Fatalf("mount 36: %v", err)
	}
	full := fullTestPrincipal()
	deflated, err := mcp.NewServer(mcp.DefaultSpecs(server.Specs()), server.Document(), stubInvoker{}, op.ToolFilter{Principal: full}, "")
	if err != nil {
		t.Fatalf("default server: %v", err)
	}
	_ = deflated
	tools, err := op.BuildTools(mcp.DefaultSpecs(server.Specs()), server.Document(), op.ToolFilter{Principal: full})
	if err != nil {
		t.Fatalf("default tools: %v", err)
	}
	if len(tools) != mcp.MaxDefaultTools {
		t.Fatalf("default tools = %d, want %d", len(tools), mcp.MaxDefaultTools)
	}
	present := map[string]bool{}
	for _, tool := range tools {
		present[tool.Name] = true
	}
	for _, name := range mcp.DefaultToolNames {
		if !present[op.ToolName(name)] {
			t.Fatalf("default set lost %q", name)
		}
	}
	if present["mcpconf_get"] {
		t.Fatalf("unplanned op leaked into the default set")
	}
	explicit, err := op.BuildTools(server.Specs(), server.Document(), op.ToolFilter{Principal: full, Toolsets: []string{"mcpconf"}})
	if err != nil {
		t.Fatalf("explicit toolset: %v", err)
	}
	if len(explicit) != 1 || explicit[0].Name != "mcpconf_get" {
		t.Fatalf("explicit toolset = %v, want [mcpconf_get]", explicit)
	}
}

type denyAuth struct{}

func (denyAuth) Bearer(_ context.Context, _ string) (op.Principal, error) {
	return op.Principal{}, op.ErrUnauthenticated
}

func (denyAuth) Session(_ context.Context, _ string) (op.Principal, error) {
	return op.AnonymousPrincipal(), nil
}

type stubInvoker struct{}

func (stubInvoker) Invoke(_ context.Context, _ *http.Request) (*op.Response, error) {
	return &op.Response{Status: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{}`)}, nil
}

func fullTestPrincipal() op.Principal {
	var grants op.Scopes
	for _, s := range op.AllScopes() {
		grants = grants.Add(s)
	}
	return op.Principal{Kind: op.PrincipalHuman, UserID: 1, Grants: grants}
}
