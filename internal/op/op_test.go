package op_test

import (
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/op"
)

type pageIn struct{}

type pageOut struct {
	Title string `json:"title"`
	Count int    `json:"count"`
}

type apiOnlyIn struct{}

type apiOnlyOut struct {
	Hello string `json:"hello"`
}

type formBody struct {
	Name string `json:"name" minLength:"1" maxLength:"64"`
}

type formIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           formBody
}

type formOut struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type jsonBody struct {
	Note string `json:"note,omitempty"`
}

type jsonCreateIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           jsonBody
}

type jsonCreateOut struct {
	Note string `json:"note"`
}

type pagedIn struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit"`
}

type pagedItem struct {
	Name string `json:"name"`
}

type pagedOut struct {
	Items []pagedItem `json:"items"`
}

type guardedBody struct {
	Value string `json:"value,omitempty"`
}

type guardedIn struct {
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           guardedBody
}

type guardedOut struct {
	Executions int `json:"executions"`
}

type updateBody struct {
	Title string `json:"title"`
}

type updateIn struct {
	ID             string `path:"id"`
	IfMatch        string `header:"If-Match"`
	IdempotencyKey string `header:"Idempotency-Key"`
	Body           updateBody
}

type updateOut struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type sunsetIn struct{}

type sunsetOut struct {
	Now string `json:"now"`
}

type counters struct {
	revs        map[string]string
	formExec    int
	guardedExec int
	mu          sync.Mutex
}

func (c *counters) guardedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.guardedExec
}

type stubAuth struct {
	granted op.Principal
}

func (s stubAuth) Bearer(_ context.Context, secret string) (op.Principal, error) {
	if secret == "good-token" {
		return s.granted, nil
	}
	return op.Principal{}, op.ErrUnauthenticated
}

func (s stubAuth) Session(_ context.Context, _ string) (op.Principal, error) {
	return op.AnonymousPrincipal(), nil
}

func grantedPrincipal() op.Principal {
	return op.Principal{
		UserID:  7,
		Kind:    op.PrincipalHuman,
		TokenID: 42,
		Grants: op.Scopes(0).
			Add(op.ScopePublicRead).
			Add(op.ScopeRepoRead).
			Add(op.ScopeRepoWrite),
	}
}

var testCSRFKey = []byte("0123456789abcdef0123456789abcdef")

func buildTestServer(t *testing.T) (*op.Server, *counters) {
	t.Helper()
	c := &counters{revs: map[string]string{}}
	table := op.NewTable()
	mustRegister(t, table, op.Op[pageIn, pageOut]{
		Name: "test.page.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/page",
		Scope: op.ScopePublicRead, Summary: "Test page",
		ETag: func(_ pageOut) string { return "page-1" },
		Handler: func(_ context.Context, _ op.Principal, _ pageIn) (pageOut, error) {
			return pageOut{Title: "hello", Count: 3}, nil
		},
	})
	mustRegister(t, table, op.Op[apiOnlyIn, apiOnlyOut]{
		Name: "test.api.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/api",
		Scope: op.ScopePublicRead, Summary: "Test api",
		Handler: func(_ context.Context, _ op.Principal, _ apiOnlyIn) (apiOnlyOut, error) {
			return apiOnlyOut{Hello: "world"}, nil
		},
	})
	mustRegister(t, table, op.Op[formIn, formOut]{
		Name: "test.form.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/test/form",
		Scope: op.ScopeRepoWrite, Summary: "Test form",
		Location: func(out formOut) string { return out.Path },
		Handler: func(_ context.Context, _ op.Principal, in formIn) (formOut, error) {
			c.mu.Lock()
			c.formExec++
			c.mu.Unlock()
			return formOut{Name: in.Body.Name, Path: "/test/page"}, nil
		},
	})
	mustRegister(t, table, op.Op[jsonCreateIn, jsonCreateOut]{
		Name: "test.json.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/test/json",
		Scope: op.ScopePublicRead, Summary: "Test json",
		Location: func(_ jsonCreateOut) string { return "/test/json" },
		Handler: func(_ context.Context, _ op.Principal, in jsonCreateIn) (jsonCreateOut, error) {
			return jsonCreateOut{Note: in.Body.Note}, nil
		},
	})
	pagedData := []string{"a", "b", "c", "d"}
	mustRegister(t, table, op.Op[pagedIn, pagedOut]{
		Name: "test.paged.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/paged",
		Scope: op.ScopePublicRead, Summary: "Test paged",
		Pagination: &op.Pagination[pagedOut]{Default: 2, Max: 5, Next: func(out pagedOut) (string, bool) {
			if len(out.Items) == 0 {
				return "", false
			}
			last := out.Items[len(out.Items)-1].Name
			for i, name := range pagedData {
				if name == last && i+1 < len(pagedData) {
					return pagedData[i+1], true
				}
			}
			return "", false
		}},
		Handler: func(_ context.Context, _ op.Principal, in pagedIn) (pagedOut, error) {
			start := 0
			if in.Cursor != "" {
				for i, name := range pagedData {
					if name == in.Cursor {
						start = i
						break
					}
				}
			}
			end := start + in.Limit
			if end > len(pagedData) {
				end = len(pagedData)
			}
			out := pagedOut{}
			for _, name := range pagedData[start:end] {
				out.Items = append(out.Items, pagedItem{Name: name})
			}
			return out, nil
		},
	})
	mustRegister(t, table, op.Op[guardedIn, guardedOut]{
		Name: "test.guarded.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/test/guarded",
		Scope: op.ScopeRepoWrite, Summary: "Test guarded",
		Handler: func(_ context.Context, _ op.Principal, _ guardedIn) (guardedOut, error) {
			c.mu.Lock()
			c.guardedExec++
			n := c.guardedExec
			c.mu.Unlock()
			return guardedOut{Executions: n}, nil
		},
	})
	mustRegister(t, table, op.Op[sunsetIn, sunsetOut]{
		Name: "test.sunset.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/sunset",
		Scope: op.ScopePublicRead, Summary: "Test sunset", Sunset: "Sun, 01 Nov 2026 00:00:00 GMT",
		Handler: func(_ context.Context, _ op.Principal, _ sunsetIn) (sunsetOut, error) {
			return sunsetOut{Now: "then"}, nil
		},
	})
	mustRegister(t, table, op.Op[updateIn, updateOut]{
		Name: "test.etag.update", Kind: op.KindMutation, Method: http.MethodPut, Path: "/test/etag/{id}",
		Scope: op.ScopeRepoWrite, Summary: "Test etag", Concurrency: op.ConcurrencyIfMatchRequired,
		ETag: func(out updateOut) string {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.revs[out.ID]
		},
		Handler: func(_ context.Context, _ op.Principal, in updateIn) (updateOut, error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			current, ok := c.revs[in.ID]
			if !ok {
				current = "r1"
				c.revs[in.ID] = current
			}
			if in.IfMatch != `"`+current+`"` {
				return updateOut{}, &op.Error{Code: op.CodeStale, Detail: "revision changed", Recovery: &op.Recovery{
					CurrentETag: `"` + current + `"`,
					Next:        "refetch the representation, then retry with a fresh If-Match",
				}}
			}
			c.revs[in.ID] = "r2"
			return updateOut{ID: in.ID, Title: in.Body.Title}, nil
		},
	})
	views := op.NewViews()
	op.PageView(views, "test.page.get", func(w io.Writer, _ op.ViewCtx, out pageOut) error {
		_, err := fmt.Fprintf(w, "<!DOCTYPE html><html><head><title>%s</title></head><body><h1>%s</h1></body></html>",
			template.HTMLEscapeString(out.Title), template.HTMLEscapeString(out.Title))
		return err
	})
	views.Form("test.form.create", func(w io.Writer, vc op.ViewCtx) error {
		_, err := fmt.Fprintf(w, "<!DOCTYPE html><html><body><form method=\"post\" action=\"/test/form\">"+
			"<input type=\"hidden\" name=\"_csrf\" value=\"%s\"><input name=\"name\"></form></body></html>", vc.CSRF)
		return err
	})
	var rows []op.AuditRow
	mux := http.NewServeMux()
	srv, err := op.Mount(mux, table, op.Config{
		PublicOrigin:  "http://example.com",
		Authenticator: stubAuth{granted: grantedPrincipal()},
		Idempotency:   op.NewMemoryIdempotency(),
		Audit: op.FuncAuditor(func(_ context.Context, row op.AuditRow) error {
			rows = append(rows, row)
			return nil
		}),
		CSRFKey: testCSRFKey,
		Views:   views,
		Title:   "test",
		Version: "0",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	_ = rows
	return srv, c
}

func mustRegister[In, Out any](t *testing.T, table *op.Table, o op.Op[In, Out]) {
	t.Helper()
	if err := op.Register(table, o); err != nil {
		t.Fatalf("register %s: %v", o.Name, err)
	}
}

func doRequest(srv *op.Server, method, target string, headers map[string]string, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func decodeProblem(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	return decoded
}

func TestTableRejectsDuplicateName(t *testing.T) {
	table := op.NewTable()
	mustRegister(t, table, op.Op[pageIn, pageOut]{
		Name: "test.page.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/page",
		Scope: op.ScopePublicRead, Summary: "Test page",
		Handler: func(_ context.Context, _ op.Principal, _ pageIn) (pageOut, error) { return pageOut{}, nil },
	})
	err := op.Register(table, op.Op[pageIn, pageOut]{
		Name: "test.page.get", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/other",
		Scope: op.ScopePublicRead, Summary: "Test page",
		Handler: func(_ context.Context, _ op.Principal, _ pageIn) (pageOut, error) { return pageOut{}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate operation name") {
		t.Fatalf("expected duplicate name error, got %v", err)
	}
	err = op.Register(table, op.Op[pageIn, pageOut]{
		Name: "test.page.list", Kind: op.KindRead, Method: http.MethodGet, Path: "/test/page",
		Scope: op.ScopePublicRead, Summary: "Test page",
		Handler: func(_ context.Context, _ op.Principal, _ pageIn) (pageOut, error) { return pageOut{}, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate route") {
		t.Fatalf("expected duplicate route error, got %v", err)
	}
	if err := table.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestNegotiationMatrix(t *testing.T) {
	srv, _ := buildTestServer(t)
	cases := []struct {
		headers     map[string]string
		name        string
		method      string
		target      string
		accept      string
		contentType string
		body        string
		wantCT      string
		wantInBody  string
		wantStatus  int
	}{
		{name: "absent accept yields json", method: "GET", target: "/test/page", wantStatus: 200, wantCT: "application/json", wantInBody: `"title":"hello"`},
		{name: "bare star yields json", method: "GET", target: "/test/page", accept: "*/*", wantStatus: 200, wantCT: "application/json", wantInBody: `"title":"hello"`},
		{name: "explicit json", method: "GET", target: "/test/page", accept: "application/json", wantStatus: 200, wantCT: "application/json"},
		{name: "browser header yields html", method: "GET", target: "/test/page", accept: "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", wantStatus: 200, wantCT: "text/html", wantInBody: "<h1>hello</h1>"},
		{name: "markdown", method: "GET", target: "/test/page", accept: "text/markdown", wantStatus: 200, wantCT: "text/markdown", wantInBody: "- **title**"},
		{name: "q values prefer json", method: "GET", target: "/test/page", accept: "text/markdown;q=0.9, application/json", wantStatus: 200, wantCT: "application/json"},
		{name: "unacceptable", method: "GET", target: "/test/page", accept: "application/xml", wantStatus: 406, wantCT: "application/problem+json"},
		{name: "html without view is 406", method: "GET", target: "/test/api", accept: "text/html", wantStatus: 406},
		{name: "html without view renders json", method: "GET", target: "/test/api", accept: "application/json", wantStatus: 200, wantCT: "application/json", wantInBody: `"hello":"world"`},
		{
			name: "form post negotiates html", method: "POST", target: "/test/form",
			contentType: "application/x-www-form-urlencoded", body: "name=formhtml&_idem=formhtml-1",
			headers:    map[string]string{"Authorization": "Bearer good-token"},
			wantStatus: 303, wantInBody: "",
		},
		{
			name: "urlencoded without form is 415", method: "POST", target: "/test/json",
			contentType: "application/x-www-form-urlencoded", body: "note=x",
			wantStatus: 415,
		},
		{
			name: "unknown top level key is 422", method: "POST", target: "/test/json",
			accept: "application/json", contentType: "application/json", body: `{"note":"x","bogus":1}`,
			headers:    map[string]string{"Idempotency-Key": "matrix-unknown-1"},
			wantStatus: 422, wantInBody: "body.bogus",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			for k, v := range tc.headers {
				headers[k] = v
			}
			if tc.accept != "" {
				headers["Accept"] = tc.accept
			}
			if tc.contentType != "" {
				headers["Content-Type"] = tc.contentType
			}
			rec := doRequest(srv, tc.method, tc.target, headers, tc.body)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCT != "" && !strings.HasPrefix(rec.Header().Get("Content-Type"), tc.wantCT) {
				t.Fatalf("content type %q, want prefix %q", rec.Header().Get("Content-Type"), tc.wantCT)
			}
			if tc.wantInBody != "" && !strings.Contains(rec.Body.String(), tc.wantInBody) {
				t.Fatalf("body missing %q: %s", tc.wantInBody, rec.Body.String())
			}
			if tc.name == "form post negotiates html" && rec.Header().Get("Location") != "/test/page" {
				t.Fatalf("location %q, want /test/page", rec.Header().Get("Location"))
			}
		})
	}
}

func TestIdempotencyAuthFailureThenRetryExecutes(t *testing.T) {
	srv, c := buildTestServer(t)
	key := "idem-auth-1"
	rec := doRequest(srv, "POST", "/test/guarded",
		map[string]string{"Content-Type": "application/json", "Accept": "application/json", "Idempotency-Key": key},
		`{"value":"v"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("first status %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if got := c.guardedCount(); got != 0 {
		t.Fatalf("executions %d, want 0", got)
	}
	rec = doRequest(srv, "POST", "/test/guarded",
		map[string]string{"Content-Type": "application/json", "Accept": "application/json", "Idempotency-Key": key, "Authorization": "Bearer good-token"},
		`{"value":"v"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("retry status %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if got := c.guardedCount(); got != 1 {
		t.Fatalf("executions %d, want 1", got)
	}
	first := rec.Body.String()
	rec = doRequest(srv, "POST", "/test/guarded",
		map[string]string{"Content-Type": "application/json", "Accept": "application/json", "Idempotency-Key": key, "Authorization": "Bearer good-token"},
		`{"value":"v"}`)
	if rec.Code != http.StatusCreated || rec.Body.String() != first {
		t.Fatalf("replay mismatch: %d %s", rec.Code, rec.Body.String())
	}
	if got := c.guardedCount(); got != 1 {
		t.Fatalf("executions %d, want 1 after replay", got)
	}
	rec = doRequest(srv, "POST", "/test/guarded",
		map[string]string{"Content-Type": "application/json", "Accept": "application/json", "Idempotency-Key": key, "Authorization": "Bearer good-token"},
		`{"value":"other"}`)
	if rec.Code != 422 {
		t.Fatalf("reused key status %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

func TestETagStrongQuoted(t *testing.T) {
	srv, _ := buildTestServer(t)
	rec := doRequest(srv, "GET", "/test/page", map[string]string{"Accept": "application/json"}, "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag != `"page-1"` {
		t.Fatalf("etag %q, want quoted strong validator", etag)
	}
	rec = doRequest(srv, "GET", "/test/page", map[string]string{"Accept": "application/json", "If-None-Match": `"page-1"`}, "")
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional status %d, want 304", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("304 must have no body")
	}
	rec = doRequest(srv, "PUT", "/test/etag/1",
		map[string]string{"Content-Type": "application/json", "Accept": "application/json", "Idempotency-Key": "etag-missing-1", "Authorization": "Bearer good-token"},
		`{"title":"x"}`)
	if rec.Code != 428 {
		t.Fatalf("missing if-match status %d, want 428: %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(srv, "PUT", "/test/etag/1",
		map[string]string{"Content-Type": "application/json", "Accept": "application/json", "If-Match": `"wrong"`, "Idempotency-Key": "etag-stale-1", "Authorization": "Bearer good-token"},
		`{"title":"x"}`)
	if rec.Code != 412 {
		t.Fatalf("stale status %d, want 412: %s", rec.Code, rec.Body.String())
	}
	decoded := decodeProblem(t, rec)
	recovery, _ := decoded["recovery"].(map[string]any)
	if recovery["current_etag"] != `"r1"` {
		t.Fatalf("recovery %v, want current_etag r1", recovery)
	}
	rec = doRequest(srv, "PUT", "/test/etag/1",
		map[string]string{"Content-Type": "application/json", "Accept": "application/json", "If-Match": `"r1"`, "Idempotency-Key": "etag-ok-1", "Authorization": "Bearer good-token"},
		`{"title":"x"}`)
	if rec.Code != 200 {
		t.Fatalf("match status %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"r2"` {
		t.Fatalf("etag after write %q, want r2", rec.Header().Get("ETag"))
	}
}

func TestPaginationLink(t *testing.T) {
	srv, _ := buildTestServer(t)
	rec := doRequest(srv, "GET", "/test/paged?limit=2", map[string]string{"Accept": "application/json"}, "")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	link := rec.Header().Get("Link")
	if !strings.Contains(link, `rel="next"`) {
		t.Fatalf("link %q missing rel=next", link)
	}
	u, err := url.Parse(strings.Split(link, ">")[0][1:])
	if err != nil {
		t.Fatalf("parse link: %v", err)
	}
	cursor := u.Query().Get("cursor")
	if cursor == "" {
		t.Fatalf("link %q missing cursor", link)
	}
	rec = doRequest(srv, "GET", "/test/paged?limit=2&cursor="+url.QueryEscape(cursor), map[string]string{"Accept": "application/json"}, "")
	if rec.Code != 200 {
		t.Fatalf("page two status %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"c"`) {
		t.Fatalf("page two body %s, want item c", rec.Body.String())
	}
	rec = doRequest(srv, "GET", "/test/paged?cursor=forged", map[string]string{"Accept": "application/json"}, "")
	if rec.Code != 400 {
		t.Fatalf("forged cursor status %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = doRequest(srv, "GET", "/test/paged?limit=99", map[string]string{"Accept": "application/json"}, "")
	if rec.Code != 400 {
		t.Fatalf("over-max limit status %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestCLIParsePaginatedOpUsesGlobalCursor(t *testing.T) {
	srv, _ := buildTestServer(t)
	root, err := op.BuildCommands(srv.Specs(), srv.Document())
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	inv, err := op.Parse(root, []string{"test", "paged", "list", "--cursor", "sealed", "--limit", "2"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if inv.Args["cursor"] != "sealed" {
		t.Fatalf("cursor arg = %v, want sealed", inv.Args["cursor"])
	}
	if inv.Args["limit"] != 2 {
		t.Fatalf("limit arg = %v, want 2", inv.Args["limit"])
	}
}

func TestProblemCatalogue(t *testing.T) {
	problems := op.Problems()
	if len(problems) != 27 {
		t.Fatalf("catalogue size %d, want 27", len(problems))
	}
	seen := map[string]bool{}
	for _, p := range problems {
		if seen[p.Code.Slug()] {
			t.Fatalf("duplicate code %q", p.Code.Slug())
		}
		seen[p.Code.Slug()] = true
		if !p.Code.Valid() {
			t.Fatalf("code %q fails Valid", p.Code.Slug())
		}
	}
	if got := (op.Code{}).Valid(); got {
		t.Fatalf("zero code must be invalid")
	}
	checks := []struct {
		code   op.Code
		status int
		exit   int
	}{
		{op.CodeInvalid, 400, 65},
		{op.CodeStale, 412, 3},
		{op.CodeConflict, 409, 2},
		{op.CodeUnavailable, 503, 4},
		{op.CodeCorrupt, 500, 5},
		{op.CodeRateLimited, 429, 4},
		{op.CodeConfigUnavailable, 503, 78},
	}
	for _, tc := range checks {
		p, ok := op.LookupProblem(tc.code)
		if !ok {
			t.Fatalf("missing %q", tc.code.Slug())
		}
		if p.Status != tc.status || p.Exit != tc.exit {
			t.Fatalf("%q: status %d exit %d, want %d %d", tc.code.Slug(), p.Status, p.Exit, tc.status, tc.exit)
		}
		if op.ExitFor(tc.code) != tc.exit {
			t.Fatalf("ExitFor %q mismatch", tc.code.Slug())
		}
	}
	ruk := map[string]op.Code{"E_STALE": op.CodeStale, "E_CLAIMED": op.CodeLeaseConflict, "E_REQUEST_REUSED": op.CodeKeyReused, "E_INVALID": op.CodeInvalid, "E_CAPACITY": op.CodeUnavailable}
	for rukCode, want := range ruk {
		if got, ok := op.FromRuk(rukCode); !ok || got != want {
			t.Fatalf("FromRuk(%q) = %v, want %v", rukCode, got, want)
		}
	}
}

func TestProblemRecoveryFields(t *testing.T) {
	srv, _ := buildTestServer(t)
	rec := doRequest(srv, "GET", "/nope", map[string]string{"Accept": "application/json", "X-Request-Id": "test-123"}, "")
	if rec.Code != 404 {
		t.Fatalf("status %d", rec.Code)
	}
	decoded := decodeProblem(t, rec)
	if decoded["trace_id"] != "test-123" || decoded["request_id"] != "test-123" {
		t.Fatalf("trace fields %v, want test-123", decoded)
	}
	recovery, ok := decoded["recovery"].(map[string]any)
	if !ok {
		t.Fatalf("missing recovery object: %v", decoded)
	}
	if recovery["docs"] != "/-/problems/not-found" {
		t.Fatalf("recovery.docs %v", recovery)
	}
}

func TestTokenBucketRetryAfter(t *testing.T) {
	limiter := access.NewLimiter()
	key := op.RateKey{Kind: op.RateToken, ID: "t1"}
	for range 600 {
		if d := limiter.Admit(t.Context(), key); !d.Allow {
			t.Fatalf("burst request denied early")
		}
	}
	denied := limiter.Admit(t.Context(), key)
	if denied.Allow {
		t.Fatalf("request past burst allowed")
	}
	if denied.RetryAfter <= 0 {
		t.Fatalf("missing Retry-After on 429")
	}
	git := op.RateKey{Kind: op.RateIPGit, ID: "10.0.0.1"}
	for range 60 {
		if d := limiter.Admit(t.Context(), git); !d.Allow {
			t.Fatalf("git probe denied early")
		}
	}
	cooled := limiter.Admit(t.Context(), git)
	if cooled.Allow || cooled.RetryAfter < 9*time.Minute {
		t.Fatalf("git cooldown missing: %+v", cooled)
	}
	login := op.RateKey{Kind: op.RateIPLogin, ID: "10.0.0.2"}
	for range 5 {
		if d := limiter.Admit(t.Context(), login); !d.Allow {
			t.Fatalf("login attempt denied early")
		}
	}
	if d := limiter.Admit(t.Context(), login); d.Allow {
		t.Fatalf("sixth login attempt allowed")
	}
}

func TestSunsetHeader(t *testing.T) {
	srv, _ := buildTestServer(t)
	rec := doRequest(srv, "GET", "/test/sunset", map[string]string{"Accept": "application/json"}, "")
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Sunset") != "Sun, 01 Nov 2026 00:00:00 GMT" {
		t.Fatalf("sunset %q", rec.Header().Get("Sunset"))
	}
	rec = doRequest(srv, "GET", "/test/page", map[string]string{"Accept": "application/json"}, "")
	if rec.Header().Get("Sunset") != "" {
		t.Fatalf("unexpected sunset %q", rec.Header().Get("Sunset"))
	}
}

func TestParityGate(t *testing.T) {
	srv, _ := buildTestServer(t)
	specs := srv.Specs()
	doc := srv.Document()
	if doc == nil {
		t.Fatalf("missing document")
	}
	tools, err := op.BuildTools(specs, doc, op.ToolFilter{Principal: grantedPrincipal()})
	if err != nil {
		t.Fatalf("tools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range tools {
		names[tool.Name] = true
	}
	for _, s := range specs {
		if !names[op.ToolName(s.Name)] {
			t.Fatalf("tool missing for %q", s.Name)
		}
		if _, ok := doc.Operations[s.Name]; !ok {
			t.Fatalf("openapi missing %q", s.Name)
		}
	}
	root, err := op.BuildCommands(specs, doc)
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	node := root
	for _, segment := range []string{"test", "page", "get"} {
		next, ok := node.Children[segment]
		if !ok {
			t.Fatalf("cli path missing %q", segment)
		}
		node = next
	}
	if node.Spec == nil || node.Spec.Name != "test.page.get" {
		t.Fatalf("cli leaf mismatch")
	}
	entries := op.ReferenceEntries(specs)
	if len(entries) != len(specs) {
		t.Fatalf("reference holds %d entries for %d ops", len(entries), len(specs))
	}
	var ref strings.Builder
	if err := op.RenderReference(&ref, op.ViewCtx{}, entries); err != nil {
		t.Fatalf("reference: %v", err)
	}
	rendered := ref.String()
	for _, s := range specs {
		if !strings.Contains(rendered, s.Name) {
			t.Fatalf("reference misses %q", s.Name)
		}
		if !strings.Contains(rendered, s.Path) {
			t.Fatalf("reference misses path %q", s.Path)
		}
	}
}

func TestOpenAPIGolden(t *testing.T) {
	srv, _ := buildTestServer(t)
	raw := srv.OpenAPI()
	var decoded any
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode openapi: %v", err)
	}
	const path = "testdata/openapi.golden.json"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		formatted, err := jsonv2.Marshal(decoded, jsonv1.DefaultOptionsV1(), jsontext.WithIndent("  "))
		if err != nil {
			t.Fatalf("format: %v", err)
		}
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, append(formatted, '\n'), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden: %v (run with UPDATE_GOLDEN=1)", err)
	}
	var wantDecoded any
	if err := jsonv2.Unmarshal(want, &wantDecoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	wantRaw, _ := jsonv2.Marshal(wantDecoded, jsonv1.DefaultOptionsV1())
	gotRaw, _ := jsonv2.Marshal(decoded, jsonv1.DefaultOptionsV1())
	if string(wantRaw) != string(gotRaw) {
		t.Fatalf("openapi drift: regenerate with UPDATE_GOLDEN=1 and review the diff")
	}
}
