package cli_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
)

func TestCLISessionFile(t *testing.T) {
	e := buildCLIEnv(t)
	pw := testPassword(t)
	bootstrapUser(t, e, "clitester", pw)
	sessionPath := filepath.Join(t.TempDir(), "session")

	login := newIO()
	login.stdin = pw
	if code := runCLI(t, []string{"auth", "login", "--server", e.url, "--username", "clitester", "--password-stdin", "--session-file", sessionPath}, login); code != 0 {
		t.Fatalf("login exit %d: %s", code, login.stderr.String())
	}
	info, err := os.Stat(sessionPath)
	if err != nil {
		t.Fatalf("session file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("session mode %o, want 600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if cookie := strings.TrimSpace(string(raw)); !strings.HasPrefix(cookie, "__Host-rukie_session=") {
		t.Fatalf("session content = %q", string(raw))
	}

	authed := newIO()
	if code := runCLI(t, []string{"auth", "session", "list", "--server", e.url, "--session-file", sessionPath, "--json"}, authed); code != 0 {
		t.Fatalf("session list exit %d: %s", code, authed.stderr.String())
	}
	listed := decodeStdout(t, authed)
	sessions, ok := listed["sessions"].([]any)
	if !ok || len(sessions) != 1 {
		t.Fatalf("sessions = %v", listed)
	}

	if err := os.Chmod(sessionPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	relogin := newIO()
	relogin.stdin = pw
	if code := runCLI(t, []string{"auth", "login", "--server", e.url, "--username", "clitester", "--password-stdin", "--session-file", sessionPath}, relogin); code != 0 {
		t.Fatalf("relogin exit %d: %s", code, relogin.stderr.String())
	}
	if info, err := os.Stat(sessionPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("relogin must repair the mode: %v %v", info, err)
	}

	bad := newIO()
	if err := os.WriteFile(sessionPath, []byte("__Host-rukie_session=deadbeef\n"), 0o600); err != nil {
		t.Fatalf("write bad session: %v", err)
	}
	if code := runCLI(t, []string{"auth", "session", "list", "--server", e.url, "--session-file", sessionPath, "--json"}, bad); code != 3 {
		t.Fatalf("dead session exit %d, want 3", code)
	}

	xdg := newIO()
	xdg.stdin = pw
	xdg.env["XDG_STATE_HOME"] = t.TempDir()
	if code := runCLI(t, []string{"auth", "login", "--server", e.url, "--username", "clitester", "--password-stdin"}, xdg); code != 0 {
		t.Fatalf("xdg login exit %d: %s", code, xdg.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(xdg.env["XDG_STATE_HOME"], "rukie", "session")); err != nil {
		t.Fatalf("default session path: %v", err)
	}

	wrong := newIO()
	wrong.stdin = "wrong-password-000"
	if code := runCLI(t, []string{"auth", "login", "--server", e.url, "--username", "clitester", "--password-stdin", "--session-file", sessionPath}, wrong); code != 3 {
		t.Fatalf("wrong password exit %d, want 3", code)
	}
}

func TestExitCodeTable(t *testing.T) {
	for _, p := range op.Problems() {
		body := `{"code":"` + p.Code.Slug() + `","status":` + strconv.Itoa(p.Status) + `}`
		stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(p.Status)
			_, _ = w.Write([]byte(body))
		}))
		c := newIO()
		code := runCLI(t, []string{"api", "index", "--server", stub.URL, "--json"}, c)
		stub.Close()
		if code != p.Exit {
			t.Fatalf("code %q exits %d, want %d", p.Code.Slug(), code, p.Exit)
		}
	}
	for _, body := range []string{"", "{}", `{"code":"nope"}`, "not json"} {
		stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(500)
			_, _ = w.Write([]byte(body))
		}))
		c := newIO()
		code := runCLI(t, []string{"api", "index", "--server", stub.URL, "--json"}, c)
		stub.Close()
		if code != 1 {
			t.Fatalf("body %q exits %d, want 1", body, code)
		}
	}

	for _, tc := range []struct {
		argv []string
		want int
	}{
		{[]string{"nope"}, 64},
		{[]string{"auth"}, 64},
		{[]string{"api", "index"}, 64},
		{[]string{"admin", "user", "list", "--config", filepath.Join(t.TempDir(), "missing.toml")}, 78},
	} {
		c := newIO()
		if code := runCLI(t, tc.argv, c); code != tc.want {
			t.Fatalf("%v exit %d, want %d (%s)", tc.argv, code, tc.want, c.stderr.String())
		}
	}
}
