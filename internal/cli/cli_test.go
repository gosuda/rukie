package cli_test

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	_ "modernc.org/sqlite"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/cli"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

type cliEnv struct {
	server *op.Server
	svc    *auth.Service
	live   *httptest.Server
	url    string
	dbPath string
}

func buildCLIServer(t *testing.T) (*op.Server, *auth.Service, string) {
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
	deps := api.Deps{Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache()}
	table := op.NewTable()
	docs := &api.DocStore{}
	registerCLIAPI(t, table, docs, deps)
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	var key [32]byte
	copy(key[:], "k8-cli-test-key-0123456789abcdef")
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
	docs.Set(server.OpenAPI())
	return server, svc, path
}

func registerCLIAPI(t *testing.T, table *op.Table, docs *api.DocStore, deps api.Deps) {
	t.Helper()
	if err := api.RegisterAll(table, docs); err != nil {
		t.Fatalf("register index: %v", err)
	}
	if err := api.RegisterAuth(table, deps); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	if err := api.RegisterUser(table, deps); err != nil {
		t.Fatalf("register user: %v", err)
	}
	if err := api.RegisterAdmin(table, deps); err != nil {
		t.Fatalf("register admin: %v", err)
	}
}

func buildCLIEnv(t *testing.T) *cliEnv {
	t.Helper()
	server, svc, path := buildCLIServer(t)
	live := httptest.NewServer(server)
	t.Cleanup(live.Close)
	return &cliEnv{server: server, svc: svc, url: live.URL, dbPath: path, live: live}
}

type cliIO struct {
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	env    map[string]string
	stdin  string
}

func (c *cliIO) io() cli.IO {
	return cli.IO{
		Stdin:  strings.NewReader(c.stdin),
		Stdout: c.stdout,
		Stderr: c.stderr,
		Getenv: func(k string) string { return c.env[k] },
	}
}

func newIO() *cliIO {
	return &cliIO{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, env: map[string]string{}}
}

func runCLI(t *testing.T, argv []string, c *cliIO) int {
	t.Helper()
	return cli.Run(t.Context(), c.io(), argv)
}

func bootstrapUser(t *testing.T, e *cliEnv, name, pw string) {
	t.Helper()
	if _, err := e.svc.BootstrapAdmin(t.Context(), name, pw, "127.0.0.1"); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
}

func testPassword(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 12)
	if _, err := cryptorand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "Aa1!" + base64.RawURLEncoding.EncodeToString(raw)
}

func decodeStdout(t *testing.T, c *cliIO) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(c.stdout.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode stdout: %v\n%s", err, c.stdout.String())
	}
	return decoded
}

func TestCLIJSONGoldens(t *testing.T) {
	e := buildCLIEnv(t)
	c := newIO()
	code := runCLI(t, []string{"api", "index", "--server", e.url, "--json"}, c)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	decoded := decodeStdout(t, c)
	ops, ok := decoded["operations"].([]any)
	if !ok || len(ops) == 0 {
		t.Fatalf("operations = %v", decoded)
	}
	names := map[string]bool{}
	for _, raw := range ops {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("entry = %v", raw)
		}
		name, _ := entry["Name"].(string)
		if name == "" {
			t.Fatalf("nameless entry = %v", entry)
		}
		method, _ := entry["Method"].(string)
		opPath, _ := entry["Path"].(string)
		if method == "" || opPath == "" {
			t.Fatalf("entry %q misses method/path: %v", name, entry)
		}
		names[name] = true
	}
	specs := e.server.Specs()
	hidden := map[string]bool{}
	for _, s := range specs {
		if s.Hidden {
			hidden[s.Name] = true
		}
	}
	root, err := op.BuildCommands(specs, e.server.Document())
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	for name := range names {
		if hidden[name] {
			continue
		}
		node := root
		for segment := range strings.SplitSeq(name, ".") {
			next, ok := node.Children[segment]
			if !ok {
				t.Fatalf("op %q has no CLI leaf", name)
			}
			node = next
		}
		if node.Spec == nil || node.Spec.Name != name {
			t.Fatalf("CLI path for %q resolves to %v", name, node.Spec)
		}
	}
	for _, want := range []string{"api.index", "api.openapi", "auth.login", "auth.token.create", "user.get", "admin.user.list"} {
		if !names[want] {
			t.Fatalf("index misses %q", want)
		}
	}

	missing := newIO()
	code = runCLI(t, []string{"user", "get", "nobody-here", "--server", e.url, "--json"}, missing)
	if code != 1 {
		t.Fatalf("missing user exit %d, want 1", code)
	}
	problem := decodeStdout(t, missing)
	delete(problem, "request_id")
	delete(problem, "trace_id")
	want := map[string]any{
		"type": "/-/problems/not-found", "title": "Not found",
		"status": float64(404), "code": "not-found",
		"retryable": false,
		"recovery":  map[string]any{"docs": "/-/problems/not-found"},
	}
	for k, v := range want {
		got, ok := problem[k]
		if !ok {
			t.Fatalf("problem misses %q: %v", k, problem)
		}
		rawWant, _ := jsonv2.Marshal(v, jsonv1.DefaultOptionsV1())
		rawGot, _ := jsonv2.Marshal(got, jsonv1.DefaultOptionsV1())
		if string(rawWant) != string(rawGot) {
			t.Fatalf("problem[%q] = %s, want %s", k, rawGot, rawWant)
		}
	}

	usage := newIO()
	if code := runCLI(t, []string{"frobnicate"}, usage); code != 64 {
		t.Fatalf("unknown command exit %d, want 64", code)
	}
	if usage.stderr.Len() == 0 {
		t.Fatalf("unknown command must explain itself on stderr")
	}
}
