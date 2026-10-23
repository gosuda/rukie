package mcp_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authsdk "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/mcp"
	"github.com/gosuda/rukie/internal/op"
)

func postMCP(t *testing.T, e *confEnv, target, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func TestMCPRouteMethods(t *testing.T) {
	e := buildConfEnv(t)
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(method, "/-/mcp", nil)
		rec := httptest.NewRecorder()
		e.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /-/mcp = %d, want 405", method, rec.Code)
		}
		if rec.Header().Get("Allow") != http.MethodPost {
			t.Fatalf("%s Allow = %q, want POST", method, rec.Header().Get("Allow"))
		}
	}
	rec := postMCP(t, e, "/-/mcp", `{}`, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST without credentials = %d, want 401 (POST must reach the MCP verifier, not a 405)", rec.Code)
	}
}

func TestMCPAuthFailureShape(t *testing.T) {
	e := buildConfEnv(t)
	ctx := t.Context()
	minted, err := e.svc.CreateToken(ctx, auth.CreateTokenInput{UserID: e.userID, Name: "doomed", Scopes: op.Scopes(0).Add(op.ScopeRepoRead)})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := e.svc.RevokeTokenByPrefix(ctx, e.userID, minted.Token.PublicPrefix, "127.0.0.1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	cases := []struct {
		name  string
		token string
	}{
		{"missing header", ""},
		{"garbage token", "rk_thisisnotavalidtoken000000000000000000"},
		{"revoked token", minted.Secret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postMCP(t, e, "/-/mcp", `{}`, tc.token)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if media := rec.Header().Get("Content-Type"); !strings.HasPrefix(media, "text/plain") {
				t.Fatalf("content-type = %q, want the SDK text/plain shape", media)
			}
			if header := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(header, "Bearer") {
				t.Fatalf("www-authenticate = %q, want a Bearer challenge", header)
			}
			if strings.Contains(rec.Body.String(), `"code"`) || strings.Contains(rec.Body.String(), `"type"`) {
				t.Fatalf("body must not be RFC 9457 JSON: %q", rec.Body.String())
			}
		})
	}
}

func TestMCPVerifier(t *testing.T) {
	e := buildConfEnv(t)
	ctx := t.Context()
	verify := mcp.VerifyToken(e.svc)
	info, err := verify(ctx, e.token, nil)
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	if info.Expiration.IsZero() {
		t.Fatalf("verifier must return a non-zero expiration")
	}
	found := false
	for _, scope := range info.Scopes {
		if scope == op.ScopeRepoRead.String() {
			found = true
		}
	}
	if !found {
		t.Fatalf("scopes = %v, want the minted repo:read", info.Scopes)
	}
	if info.UserID != "1" && info.UserID == "" {
		t.Fatalf("user id = %q", info.UserID)
	}
	for _, bad := range []string{"", "rk_garbage00000000000000000000000000000000"} {
		if _, err := verify(ctx, bad, nil); !errors.Is(err, authsdk.ErrInvalidToken) {
			t.Fatalf("token %q: err = %v, want ErrInvalidToken", bad, err)
		}
	}
}

func TestMCPNameCollisionRejected(t *testing.T) {
	doc := &op.Document{
		Operations: map[string]op.OperationSchema{"dup.one": {}, "dup_one": {}},
		Components: map[string]op.Schema{},
	}
	specs := []op.Spec{{Name: "dup.one"}, {Name: "dup_one"}}
	filter := op.ToolFilter{Principal: fullTestPrincipal(), Toolsets: []string{"dup"}}
	if _, err := mcp.NewServer(specs, doc, stubInvoker{}, filter, ""); err == nil {
		t.Fatalf("colliding tool names must be rejected")
	} else if !strings.Contains(err.Error(), "collides") {
		t.Fatalf("err = %v, want a collision report", err)
	}
	distinct := &op.Document{
		Operations: map[string]op.OperationSchema{"dup.one": {}, "dup.two": {}},
		Components: map[string]op.Schema{},
	}
	distinctSpecs := []op.Spec{{Name: "dup.one"}, {Name: "dup.two"}}
	if _, err := mcp.NewServer(distinctSpecs, distinct, stubInvoker{}, filter, ""); err != nil {
		t.Fatalf("distinct names must pass: %v", err)
	}
}

func TestMCPDomains(t *testing.T) {
	domains := map[string]bool{}
	for _, d := range mcp.Domains {
		domains[d] = true
	}
	for _, want := range []string{"auth", "user", "org", "team", "repo", "code", "branch", "tag", "search", "release", "webhook", "mirror", "star", "explore", "inbox", "admin", "change", "intent", "ask", "view", "land", "proof", "ci"} {
		if !domains[want] {
			t.Fatalf("domain %q missing", want)
		}
	}
	if domains["task"] {
		t.Fatalf("the retired task domain must not register")
	}
}

func TestMCPToolResultShapes(t *testing.T) {
	spec := op.Spec{Name: "mcpconf.get", Summary: "conf"}
	okResp := &op.Response{Status: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"id":"a"}`)}
	ok := mcp.ToolResult(&spec, okResp)
	if ok.IsError {
		t.Fatalf("2xx must not set IsError")
	}
	if len(ok.Content) != 1 {
		t.Fatalf("2xx must carry one text item")
	}
	errResp := &op.Response{Status: 409, Header: http.Header{"Content-Type": []string{"application/problem+json"}}, Body: []byte(`{"code":"conflict","status":409}`)}
	failed := mcp.ToolResult(&spec, errResp)
	if !failed.IsError {
		t.Fatalf("non-2xx must set IsError")
	}
	byteResp := &op.Response{Status: 200, Header: http.Header{"Content-Type": []string{"application/octet-stream"}}, Body: []byte{1, 2, 3}}
	linked := mcp.ToolResult(&spec, byteResp)
	if linked.IsError || len(linked.Content) != 1 {
		t.Fatalf("byte body must return one content item without IsError")
	}
	if mcp.ToolResult(&spec, nil).IsError != true {
		t.Fatalf("nil response must report IsError")
	}
}
