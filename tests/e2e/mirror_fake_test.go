//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mirrorFake struct {
	server *httptest.Server
	root   string
	URL    string

	posts      atomic.Int64
	entered    chan struct{}
	release    chan struct{}
	rateLimit  int64
	retryAfter int
	hold       bool

	mode        string
	user        string
	token       string
	enteredOnce sync.Once
}

func newMirrorFake(t *testing.T, mode, user, token string) *mirrorFake {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, gitBin(t), "--exec-path").Output()
	if err != nil {
		t.Fatalf("git --exec-path: %v", err)
	}
	backend := filepath.Join(strings.TrimSpace(string(out)), "git-http-backend")

	root := t.TempDir()
	if res, err := gitRun(ctx, t, root, "init", "--bare", "repo.git"); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, res)
	}
	repoDir := filepath.Join(root, "repo.git")
	for _, cfg := range [][2]string{
		{"http.receivepack", "true"},
		{"receive.hideRefs", "refs/pull"},
		{"uploadpack.hideRefs", "refs/pull"},
	} {
		if res, err := gitRun(ctx, t, repoDir, "config", cfg[0], cfg[1]); err != nil {
			t.Fatalf("git config %s: %v\n%s", cfg[0], err, res)
		}
	}

	f := &mirrorFake{
		root:    root,
		entered: make(chan struct{}),
		release: make(chan struct{}),
		mode:    mode,
		user:    user,
		token:   token,
	}

	cgiHandler := &cgi.Handler{
		Path: backend,
		Dir:  root,
		Env: []string{
			"GIT_PROJECT_ROOT=" + root,
			"GIT_HTTP_EXPORT_ALL=1",
			"GIT_CONFIG_NOSYSTEM=1",
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch f.mode {
		case "open":
		case "authed":
			u, p, ok := r.BasicAuth()
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="mirror"`)
				http.Error(w, "authentication required", http.StatusUnauthorized)
				return
			}
			if u != f.user || p != f.token {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		case "always401":
			w.Header().Set("WWW-Authenticate", `Basic realm="mirror"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		default:
			http.Error(w, "unknown fake mode", http.StatusInternalServerError)
			return
		}

		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
			n := f.posts.Add(1)
			if f.rateLimit > 0 && n <= f.rateLimit {
				w.Header().Set("Retry-After", fmt.Sprint(f.retryAfter))
				http.Error(w, "rate limited", http.StatusTooManyRequests)
				return
			}
			if f.hold {
				f.enteredOnce.Do(func() { close(f.entered) })
				select {
				case <-f.release:
				case <-r.Context().Done():
					return
				}
			}
		}
		cgiHandler.ServeHTTP(w, r)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.server = srv
	f.URL = srv.URL + "/repo.git"
	return f
}

func (f *mirrorFake) authURL() string {
	if f.user == "" {
		return f.URL
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return f.URL
	}
	u.User = url.UserPassword(f.user, f.token)
	return u.String()
}

func (f *mirrorFake) refs(t *testing.T) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	out, err := gitRun(ctx, t, f.root, "--git-dir="+filepath.Join(f.root, "repo.git"), "for-each-ref", "--format=%(objectname)%09%(refname)")
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	m := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		oid, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		m[name] = oid
	}
	return m
}

func (f *mirrorFake) hook(t *testing.T, script string) {
	t.Helper()
	if !strings.HasPrefix(script, "#!") {
		script = "#!/bin/sh\n" + script
	}
	p := filepath.Join(f.root, "repo.git", "hooks", "pre-receive")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write hook: %v", err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatalf("chmod hook: %v", err)
	}
}

func (f *mirrorFake) protect(t *testing.T) {
	t.Helper()
	f.hook(t, `#!/bin/sh
zero="0000000000000000000000000000000000000000"
while read old new ref; do
  if [ "$ref" = "refs/heads/main" ] && [ "$new" = "$zero" ]; then
    echo "protected branch: deletion of main is not allowed" >&2
    exit 1
  fi
  if [ "$old" != "$zero" ] && [ "$new" != "$zero" ]; then
    if ! git merge-base --is-ancestor "$old" "$new"; then
      echo "protected branch: non-fast-forward update is not allowed" >&2
      exit 1
    fi
  fi
done
`)
}

func (f *mirrorFake) limitBlobs(t *testing.T, n int) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
limit=%d
zero="0000000000000000000000000000000000000000"
while read old new ref; do
  if [ "$new" = "$zero" ]; then
    continue
  fi
  if [ "$old" = "$zero" ]; then
    range="$new"
  else
    range="$old..$new"
  fi
  for oid in $(git rev-list --objects "$range" 2>/dev/null | cut -d' ' -f1); do
    [ -z "$oid" ] && continue
    kind=$(git cat-file -t "$oid" 2>/dev/null) || continue
    if [ "$kind" = "blob" ]; then
      size=$(git cat-file -s "$oid" 2>/dev/null) || continue
      if [ "$size" -gt "$limit" ]; then
        echo "file size limit exceeded: blob $oid is $size bytes (limit $limit bytes)" >&2
        exit 1
      fi
    fi
  done
done
`, n)
	f.hook(t, script)
}

func TestMirrorFakeSmartHTTP(t *testing.T) {
	const (
		fakeUser  = "canary-user"
		fakeToken = "CANARY-token-f3-mirror-fake-001"
	)

	f := newMirrorFake(t, "authed", fakeUser, fakeToken)

	if !strings.Contains(f.URL, "127.0.0.1") {
		t.Fatalf("fake URL = %q, want local 127.0.0.1", f.URL)
	}

	if got := f.refs(t); len(got) != 0 {
		t.Fatalf("fresh fake refs = %v, want empty", got)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()

	if out, err := gitRun(ctx, t, t.TempDir(), "ls-remote", f.URL); err == nil {
		t.Fatalf("unauthenticated ls-remote succeeded, want 401:\n%s", out)
	}

	work := t.TempDir()
	if out, err := gitRun(ctx, t, work, "init", "-b", "main", "."); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("mirror fake canary\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if out, err := gitRun(ctx, t, work, "add", "-A"); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := gitRun(ctx, t, work, "commit", "-m", "seed"); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
	headOut, err := gitRun(ctx, t, work, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, headOut)
	}
	head := strings.TrimSpace(headOut)

	if out, err := gitRun(ctx, t, work, "push", f.authURL(), "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("authenticated push: %v\n%s", err, out)
	}

	badURL := strings.Replace(f.URL, "://", "://"+fakeUser+":CANARY-wrong-token@", 1)
	if out, err := gitRun(ctx, t, work, "push", badURL, "HEAD:refs/heads/other"); err == nil {
		t.Fatalf("mismatched-credential push succeeded, want 403:\n%s", out)
	}

	if out, err := gitRun(ctx, t, work, "ls-remote", f.authURL()); err != nil || !strings.Contains(out, head) {
		t.Fatalf("authenticated ls-remote: %v\n%s", err, out)
	}
	refs := f.refs(t)
	if refs["refs/heads/main"] != head {
		t.Fatalf("refs/heads/main = %q, want %q (refs %v)", refs["refs/heads/main"], head, refs)
	}
	if n := f.posts.Load(); n < 1 {
		t.Fatalf("receive-pack posts = %d, want >= 1", n)
	}
}
