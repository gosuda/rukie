//go:build e2e

package e2e

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/sysops"
)

func TestPushDuringBackupBarrier(t *testing.T) {
	s := newStack(t, nil)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addRepo(t, "ada", "hello", "private")
	work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	pushURL := s.repoURL("ada", "hello", "ada", s.users["ada"].token)
	b, err := sysops.BeginBarrier(s.dir)
	if err != nil {
		t.Fatalf("BeginBarrier: %v", err)
	}
	target := strings.TrimSuffix(s.url, "/") + "/ada/hello.git/git-receive-pack"
	reqCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, strings.NewReader("0000"))
	if err != nil {
		_ = b.Close()
		t.Fatalf("request: %v", err)
	}
	req.SetBasicAuth("ada", s.users["ada"].token)
	req.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		_ = b.Close()
		t.Fatalf("post during barrier: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		_ = b.Close()
		t.Fatalf("barrier push status = %d, want 503, body %q", resp.StatusCode, string(body))
	}
	if got := resp.Header.Get("Retry-After"); got != "10" {
		_ = b.Close()
		t.Fatalf("Retry-After = %q, want 10", got)
	}
	if !strings.Contains(string(body), "/-/problems/unavailable") {
		_ = b.Close()
		t.Fatalf("barrier body = %q, want unavailable problem", string(body))
	}
	pctx, pcancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer pcancel()
	if out, err := gitRun(pctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err == nil {
		_ = b.Close()
		t.Fatalf("git push succeeded under barrier, want 503 refusal, output %q", out)
	} else if !strings.Contains(out, "503") {
		_ = b.Close()
		t.Fatalf("git push error = %v, output %q, want 503 in output", err, out)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	rctx, rcancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer rcancel()
	if out, err := gitRun(rctx, t, work, "push", pushURL, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push after release: %v\n%s", err, out)
	}
	got := s.lsRemote(t, pushURL)
	if got["refs/heads/main"] != oid {
		t.Fatalf("main = %q, want %q after barrier release", got["refs/heads/main"], oid)
	}
}
