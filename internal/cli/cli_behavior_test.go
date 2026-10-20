package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func TestCLIDryRunSendsNothing(t *testing.T) {
	e := buildCLIEnv(t)
	bootstrapUser(t, e, "dryrunner", testPassword(t))
	var hits atomic.Int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(stub.Close)
	before, err := e.svc.ListTokens(t.Context(), 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	c := newIO()
	argv := []string{"auth", "token", "create", "--name", "x", "--scopes", "repo:read", "--server", stub.URL, "--dry-run"}
	if code := runCLI(t, argv, c); code != 0 {
		t.Fatalf("dry-run exit %d: %s", code, c.stderr.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("dry-run sent %d requests", hits.Load())
	}
	after, err := e.svc.ListTokens(t.Context(), 1)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("dry-run minted a token")
	}
	if !strings.Contains(c.stdout.String(), "POST") || !strings.Contains(c.stdout.String(), "/settings/tokens") {
		t.Fatalf("dry-run output = %q", c.stdout.String())
	}
}

func TestCLIInputStdin(t *testing.T) {
	e := buildCLIEnv(t)
	pw := testPassword(t)
	bootstrapUser(t, e, "inputadmin", pw)
	if _, err := e.svc.CreateUser(t.Context(), 1, auth.CreateUserInput{Name: "aaron", Password: pw}, "127.0.0.1"); err != nil {
		t.Fatalf("create user: %v", err)
	}
	minted, err := e.svc.CreateToken(t.Context(), auth.CreateTokenInput{UserID: 1, Name: "input", Scopes: op.Scopes(0).Add(op.ScopeAdminRead)})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(minted.Secret+"\n"), 0o600); err != nil {
		t.Fatalf("token file: %v", err)
	}
	c := newIO()
	c.stdin = `{"after": "zzz-no-such-user"}`
	if code := runCLI(t, []string{"admin", "user", "list", "--server", e.url, "--token-file", tokenPath, "--json", "--input", "-"}, c); code != 0 {
		t.Fatalf("exit %d: stdout=%s stderr=%s", code, c.stdout.String(), c.stderr.String())
	}
	decoded := decodeStdout(t, c)
	if users, ok := decoded["users"].([]any); ok && len(users) != 0 {
		t.Fatalf("users = %v, want the empty page past zzz", decoded)
	} else if !ok && decoded["users"] != nil {
		t.Fatalf("users = %v", decoded)
	}
	flagged := newIO()
	flagged.stdin = `{"after": "zzz-no-such-user"}`
	if code := runCLI(t, []string{"admin", "user", "list", "--server", e.url, "--token-file", tokenPath, "--json", "--input", "-", "--after", "aaron"}, flagged); code != 0 {
		t.Fatalf("exit %d: %s", code, flagged.stderr.String())
	}
	flaggedDecoded := decodeStdout(t, flagged)
	if names := userNames(t, flaggedDecoded); len(names) != 1 || names[0] != "inputadmin" {
		t.Fatalf("flag must win over --input: %v", flaggedDecoded)
	}
	broken := newIO()
	broken.stdin = `{"limit": `
	if code := runCLI(t, []string{"admin", "user", "list", "--server", e.url, "--json", "--input", "-"}, broken); code != 64 {
		t.Fatalf("malformed input exit %d, want 64", code)
	}
}

func userNames(t *testing.T, decoded map[string]any) []string {
	t.Helper()
	users, ok := decoded["users"].([]any)
	if !ok {
		t.Fatalf("users = %v", decoded)
	}
	var out []string
	for _, raw := range users {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("user = %v", raw)
		}
		name, _ := entry["name"].(string)
		out = append(out, name)
	}
	return out
}

func TestCLIRetryHonorsRetryAfter(t *testing.T) {
	var hits atomic.Int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/api" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"rate-limited","status":429}`))
			return
		}
		_, _ = w.Write([]byte(`{"operations":[]}`))
	}))
	t.Cleanup(stub.Close)
	c := newIO()
	start := time.Now()
	if code := runCLI(t, []string{"api", "index", "--server", stub.URL, "--json"}, c); code != 0 {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	if hits.Load() != 2 {
		t.Fatalf("requests = %d, want exactly 2", hits.Load())
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("elapsed %v, want at least the advertised 1s backoff", elapsed)
	}
}

func TestCLIIgnoresAmbientProbes(t *testing.T) {
	var hits atomic.Int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/-/api" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"code":"rate-limited","status":429}`))
			return
		}
		_, _ = w.Write([]byte(`{"operations":[]}`))
	}))
	t.Cleanup(stub.Close)
	probe := &http.Client{Timeout: 5 * time.Second}
	var wg sync.WaitGroup
	var probed atomic.Int64
	for range 10 {
		wg.Go(func() {
			resp, err := probe.Get(stub.URL + "/")
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			probed.Add(1)
		})
	}
	c := newIO()
	if code := runCLI(t, []string{"api", "index", "--server", stub.URL, "--json"}, c); code != 0 {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	wg.Wait()
	if probed.Load() == 0 {
		t.Fatal("ambient probes never landed")
	}
	if hits.Load() != 2 {
		t.Fatalf("requests = %d, want exactly 2", hits.Load())
	}
}

func TestCLITransportFailureExits4(t *testing.T) {
	shut := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	url := shut.URL
	shut.Close()
	c := newIO()
	if code := runCLI(t, []string{"api", "index", "--server", url, "--json"}, c); code != 4 {
		t.Fatalf("dead server exit %d, want 4", code)
	}
}

func TestCLIAllMergesPages(t *testing.T) {
	var hits atomic.Int64
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			w.Header().Set("Link", `</admin/users?after=c1>; rel="next"`)
			_, _ = w.Write([]byte(`{"users":[{"name":"a"}],"next":"c1"}`))
			return
		}
		if r.URL.Query().Get("after") == "" && r.URL.Query().Get("cursor") == "" {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write([]byte(`{"users":[{"name":"b"}]}`))
	}))
	t.Cleanup(stub.Close)
	c := newIO()
	if code := runCLI(t, []string{"admin", "user", "list", "--server", stub.URL, "--json", "--all"}, c); code != 0 {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	decoded := decodeStdout(t, c)
	users, ok := decoded["users"].([]any)
	if !ok || len(users) != 2 {
		t.Fatalf("users = %v", decoded)
	}
	if hits.Load() != 2 {
		t.Fatalf("requests = %d, want 2", hits.Load())
	}
}

func TestCLILocalAdminMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "local.sqlite")
	ctx := t.Context()
	st, err := store.Open(ctx, store.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	svc, err := auth.Open(dbPath)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	if _, berr := svc.BootstrapAdmin(ctx, "localadmin", testPassword(t), "127.0.0.1"); berr != nil {
		t.Fatalf("bootstrap: %v", berr)
	}
	minted, err := svc.CreateToken(ctx, auth.CreateTokenInput{UserID: 1, Name: "local", Scopes: op.Scopes(0).Add(op.ScopeAdminRead)})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_ = st.Close(context.Background())
	keyPath := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(keyPath, bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatalf("key: %v", err)
	}
	cfgPath := filepath.Join(dir, "rukie.toml")
	cfgText := "data_dir = \"" + dir + "\"\nsecret_key_file = \"" + keyPath + "\"\n[store.sqlite]\npath = \"" + dbPath + "\"\n"
	if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
		t.Fatalf("config: %v", err)
	}
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte(minted.Secret+"\n"), 0o600); err != nil {
		t.Fatalf("token file: %v", err)
	}

	local := newIO()
	if code := runCLI(t, []string{"admin", "user", "list", "--config", cfgPath, "--token-file", tokenPath, "--json"}, local); code != 0 {
		t.Fatalf("local admin exit %d: %s", code, local.stderr.String())
	}
	if !strings.Contains(local.stdout.String(), "localadmin") {
		t.Fatalf("local admin output = %s", local.stdout.String())
	}

	remoteOnly := newIO()
	if code := runCLI(t, []string{"api", "index", "--config", cfgPath, "--token-file", tokenPath, "--json"}, remoteOnly); code != 64 {
		t.Fatalf("non-admin local exit %d, want 64", code)
	}

	anon := newIO()
	if code := runCLI(t, []string{"admin", "user", "list", "--config", cfgPath, "--json"}, anon); code != 3 {
		t.Fatalf("anonymous local exit %d, want 3", code)
	}
}
