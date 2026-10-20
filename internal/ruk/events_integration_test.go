//go:build integration

package ruk

import (
	"path/filepath"
	"testing"
)

func openSeededRepo(t *testing.T, d *daemon) Repo {
	t.Helper()
	repoDir := filepath.Join(filepath.Dir(d.socket), "roots", "repo")
	seedStore(t, repoDir)
	client := dialDaemon(t, d.socket)
	ctx, cancel := testCtx(t)
	defer cancel()
	repo, err := client.OpenRepo(ctx, filepath.Join(repoDir, ".ruk"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	return repo
}

func TestEventsTailAgainstDaemon(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	client := dialDaemon(t, d.socket)
	repo := openSeededRepo(t, d)
	ctx, cancel := testCtx(t)
	defer cancel()
	after := uint64(0)
	reply, err := EventsTail(ctx, client, repo, EventsTailArgs{After: &after, Limit: 10, WaitMs: 0})
	if err != nil {
		t.Fatalf("events.tail: %v", err)
	}
	if reply.Status != TailStatusOK {
		t.Fatalf("status = %q, want ok", reply.Status)
	}
	if len(reply.Events) != 0 {
		t.Fatalf("fresh repo returned %d events", len(reply.Events))
	}
}

func TestRefsListAgainstDaemon(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	client := dialDaemon(t, d.socket)
	repo := openSeededRepo(t, d)
	ctx, cancel := testCtx(t)
	defer cancel()
	reply, err := RefsList(ctx, client, repo, RefsListArgs{Prefix: "refs/heads/"})
	if err != nil {
		t.Fatalf("refs.list: %v", err)
	}
	if len(reply.Refs.Items) != 0 {
		t.Fatalf("fresh repo listed %d refs", len(reply.Refs.Items))
	}
}
