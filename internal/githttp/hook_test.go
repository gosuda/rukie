package githttp

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func TestRecordPushAttemptStashesWant(t *testing.T) {
	s := &Server{clock: time.Now}
	call := wire.HookCall{
		Phase: wire.HookPreReceive,
		Commands: []wire.HookCommand{
			{Name: "refs/heads/main", Old: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", New: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			{Name: "refs/heads/dev", Old: "cccccccccccccccccccccccccccccccccccccccc", New: "dddddddddddddddddddddddddddddddddddddddd"},
		},
	}
	verdicts := []wire.HookRefVerdict{
		{Name: "refs/heads/main", Verdict: wire.AcceptVerdict()},
		{Name: "refs/heads/dev", Verdict: wire.RejectVerdict("denied")},
	}
	s.recordPushAttempt(context.Background(), access.Caller{}, 7, "push-want-1", call, verdicts)
	want := s.takeWant("push-want-1")
	if len(want) != 1 || want["refs/heads/main"] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("want = %v, want only the accepted main target", want)
	}
	if again := s.takeWant("push-want-1"); again != nil {
		t.Fatalf("second take = %v, want nil: reconcile consumes the targets once", again)
	}
}

func openJudgeStore(t *testing.T, archived bool) (int64, *store.Store) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/judge.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	ada, err := st.InsertAuthUser(ctx, store.AuthUserCreate{
		Name: "ada", Lower: "ada", Email: "ada@example.test",
		Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var repoID int64
	if werr := st.Write(ctx, func(tx *store.WriteTx) error {
		id, ierr := tx.InsertRepo(time.Unix(1, 0), store.RepoInsert{
			Name: "hello", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ada.ID, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		repoID = id
		return tx.UpdateRepo(id, time.Unix(1, 0), store.RepoUpdate{Archived: &archived})
	}); werr != nil {
		t.Fatalf("seed judge repo: %v", werr)
	}
	return repoID, st
}

func judgeCaller() access.Caller {
	return access.Caller{UserID: 1, Active: true, Authed: true}
}

func judgeCommand() wire.HookCommand {
	return wire.HookCommand{
		Name:       "refs/heads/main",
		Old:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		New:        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Authorized: true,
	}
}

func TestJudgeAcceptsLiveRepoPush(t *testing.T) {
	repoID, st := openJudgeStore(t, false)
	s := &Server{resolver: st.Resolver()}
	if verdict := s.judge(judgeCaller(), repoID, wire.HookPreReceive, judgeCommand()); verdict.Kind != wire.HookAccept {
		t.Fatalf("live push verdict = %+v, want accept", verdict)
	}
}

func TestJudgeRejectsArchivedRepoPush(t *testing.T) {
	repoID, st := openJudgeStore(t, true)
	s := &Server{resolver: st.Resolver()}
	if verdict := s.judge(judgeCaller(), repoID, wire.HookPreReceive, judgeCommand()); verdict.Kind == wire.HookAccept {
		t.Fatalf("archived push verdict = %+v, want reject", verdict)
	}
}
