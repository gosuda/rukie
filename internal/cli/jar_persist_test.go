package cli_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gosuda/rukie/internal/auth"
)

// TestRemoteInvokerJarPersistsToFile pins rotation survival: the dispatch
// path writes the jar's session cookies back to the login session file, so
// a later process authenticates with the rotated session and never logs in
// again.
func TestRemoteInvokerJarPersistsToFile(t *testing.T) {
	name := auth.SessionCookieName()
	var mu sync.Mutex
	var seen []string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Cookie"))
		mu.Unlock()
		cookie := r.Header.Get("Cookie")
		if !strings.Contains(cookie, "S1") && !strings.Contains(cookie, "S2") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized","detail":"no session"}`))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: name, Value: "S2", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(stub.Close)

	sessionFile := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(sessionFile, []byte(name+"=S1\n"), 0o600); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	// Process 1 presents S1; the stub rotates to S2.
	first := newIO()
	if code := runCLI(t, []string{"api", "index", "--server", stub.URL, "--session-file", sessionFile, "--json"}, first); code != 0 {
		t.Fatalf("first call exit %d: %s", code, first.stderr.String())
	}
	raw, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if !strings.Contains(string(raw), "S2") || strings.Contains(string(raw), "S1") {
		t.Fatalf("session file was not rotated: %q", raw)
	}
	if info, err := os.Stat(sessionFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode = %v, want 0600", info.Mode())
	}
	mu.Lock()
	firstCookie := ""
	if len(seen) > 0 {
		firstCookie = seen[0]
	}
	mu.Unlock()
	if !strings.Contains(firstCookie, "S1") {
		t.Fatalf("first call sent %q, want the S1 session", firstCookie)
	}
	// Process 2 is a fresh invoker that only reads the file: it must present
	// the rotated session and succeed without logging in again.
	second := newIO()
	if code := runCLI(t, []string{"api", "index", "--server", stub.URL, "--session-file", sessionFile, "--json"}, second); code != 0 {
		t.Fatalf("second call exit %d: %s", code, second.stderr.String())
	}
	mu.Lock()
	lastCookie := ""
	if len(seen) > 0 {
		lastCookie = seen[len(seen)-1]
	}
	mu.Unlock()
	if !strings.Contains(lastCookie, "S2") {
		t.Fatalf("second call sent %q, want the rotated S2 session", lastCookie)
	}
}

// TestRemoteInvokerJarSkipsTokenCalls pins that bearer-token calls never
// touch the session file: there is no session to rotate.
func TestRemoteInvokerJarSkipsTokenCalls(t *testing.T) {
	name := auth.SessionCookieName()
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "S9", Path: "/"})
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(stub.Close)

	sessionFile := filepath.Join(t.TempDir(), "session")
	before := []byte("marker\n")
	if err := os.WriteFile(sessionFile, before, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("rk_test\n"), 0o600); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	c := newIO()
	if code := runCLI(t, []string{"api", "index", "--server", stub.URL, "--token-file", tokenFile, "--session-file", sessionFile, "--json"}, c); code != 0 {
		t.Fatalf("exit %d: %s", code, c.stderr.String())
	}
	after, err := os.ReadFile(sessionFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("token call rewrote the session file: %q", after)
	}
}
