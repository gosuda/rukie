//go:build integration

package gitssh

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestSSHMultiRoundFetchCompletes(t *testing.T) {
	s := newSSHStack(t)
	ada := s.addAdmin(t, "ada")
	adaKey := s.addKey(t, ada)
	s.addRepo(t, ada, "hello", "private")
	work, _ := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	url := "ssh://ada@" + s.addr + "/ada/hello.git"
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	for i := range 60 {
		if out, err := sshGitRun(ctx, t, s, adaKey.file, work, "commit", "--allow-empty", "-m", "fill"); err != nil {
			t.Fatalf("fill %d: %v\n%s", i, err, out)
		}
	}
	if out, err := sshGitRun(ctx, t, s, adaKey.file, work, "push", url, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	cloneParent := t.TempDir()
	if out, err := sshGitRun(ctx, t, s, adaKey.file, cloneParent, "clone", url, "hello"); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	cloneDir := cloneParent + "/hello"
	if out, err := sshGitRun(ctx, t, s, adaKey.file, work, "commit", "--allow-empty", "-m", "second"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	if out, err := sshGitRun(ctx, t, s, adaKey.file, work, "push", url, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push second: %v\n%s", err, out)
	}
	want, err := sshGitRun(ctx, t, s, adaKey.file, work, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	want = strings.TrimSpace(want)
	fetchCtx, stop := context.WithTimeout(t.Context(), 60*time.Second)
	defer stop()
	start := time.Now()
	if out, err := sshGitRun(fetchCtx, t, s, adaKey.file, cloneDir, "fetch", "origin"); err != nil {
		t.Fatalf("fetch stalled or failed after %v:\n%s", time.Since(start), out)
	}
	got, err := sshGitRun(ctx, t, s, adaKey.file, cloneDir, "rev-parse", "origin/main")
	if err != nil {
		t.Fatalf("rev-parse origin/main: %v", err)
	}
	if strings.TrimSpace(got) != want {
		t.Fatalf("origin/main = %q, want %q", strings.TrimSpace(got), want)
	}
}
