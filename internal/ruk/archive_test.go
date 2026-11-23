package ruk

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/ruk/internal/fakedaemon"
)

func TestArchiveReopensStaleRepoAndSession(t *testing.T) {
	const path = "/repo"
	const oldHandle = "handle-old"
	const newHandle = "handle-new"
	var replaced atomic.Bool
	var staleCalls atomic.Int64
	var freshCalls atomic.Int64
	cfg := fakedaemon.Config{ValidRepo: func(repo string) (string, string) {
		switch repo {
		case path:
			if replaced.Load() {
				return newHandle, ""
			}
			return oldHandle, ""
		case oldHandle:
			if replaced.Load() {
				staleCalls.Add(1)
				return "", "E_NOT_FOUND"
			}
			return oldHandle, ""
		case newHandle:
			freshCalls.Add(1)
			return newHandle, ""
		default:
			return "", "E_NOT_FOUND"
		}
	}}
	cfg.RequireAuth = true
	cfg.Refuse = map[string]string{"session.resume": "E_NOT_FOUND"}
	cfg.ValidSession = func(session string) string {
		if session != "session-test" {
			return "E_NOT_FOUND"
		}
		return ""
	}
	socket, _ := startFake(t, cfg)
	client := dialTest(t, socket)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	repo, err := client.OpenRepo(ctx, path)
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	entry, err := client.ensureReplaySession(ctx, repo)
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	client.sessions.put(path, &sessionEntry{id: "session-old", secret: entry.secret, active: true})
	replaced.Store(true)
	var body bytes.Buffer
	result, err := Archive(ctx, client, repo, ArchiveArgs{Rev: ArchiveRefRev("refs/heads/main")}, &body)
	if err != nil {
		t.Fatalf("archive after handle replacement: %v", err)
	}
	if staleCalls.Load() != 1 || freshCalls.Load() != 1 {
		t.Fatalf("archive attempts used stale handle %d times and fresh handle %d times, want one each",
			staleCalls.Load(), freshCalls.Load())
	}
	if body.Len() == 0 || result.Bytes != int64(body.Len()) {
		t.Fatalf("archive wrote %d bytes and reported %d", body.Len(), result.Bytes)
	}
}
