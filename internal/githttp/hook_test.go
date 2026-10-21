package githttp

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/ruk/wire"
	_ "modernc.org/sqlite"
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

func openJudgeDB(t *testing.T, archived int64) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/judge.sqlite")
	if err != nil {
		t.Fatalf("open judge db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stmts := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, is_active INTEGER NOT NULL DEFAULT 1)`,
		`CREATE TABLE repos (id INTEGER PRIMARY KEY, owner_user_id INTEGER NULL, org_id INTEGER NOT NULL DEFAULT 0, visibility TEXT NOT NULL, name TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0, deleted_at INTEGER NULL)`,
		`CREATE TABLE repo_grants (repo_id INTEGER NOT NULL, user_id INTEGER NOT NULL, role TEXT NOT NULL)`,
		`CREATE TABLE bookmark_rules (id INTEGER PRIMARY KEY, repo_id INTEGER NOT NULL, pattern TEXT NOT NULL, mode TEXT NOT NULL, bypass_admin INTEGER NOT NULL DEFAULT 0, push_allow TEXT NOT NULL DEFAULT '[]', land_allow TEXT NOT NULL DEFAULT '[]', required TEXT NOT NULL DEFAULT '[]', version INTEGER NOT NULL DEFAULT 1)`,
		`INSERT INTO users (id, is_active) VALUES (1, 1)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed judge db: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO repos (id, owner_user_id, org_id, visibility, name, archived, deleted_at) VALUES (7, 1, 0, 'private', 'hello', ?, NULL)`, archived); err != nil {
		t.Fatalf("seed judge repo: %v", err)
	}
	return db
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
	s := &Server{resolver: access.NewSQLResolver(openJudgeDB(t, 0), nil)}
	if verdict := s.judge(judgeCaller(), 7, wire.HookPreReceive, judgeCommand()); verdict.Kind != wire.HookAccept {
		t.Fatalf("live push verdict = %+v, want accept", verdict)
	}
}

func TestJudgeRejectsArchivedRepoPush(t *testing.T) {
	s := &Server{resolver: access.NewSQLResolver(openJudgeDB(t, 1), nil)}
	if verdict := s.judge(judgeCaller(), 7, wire.HookPreReceive, judgeCommand()); verdict.Kind == wire.HookAccept {
		t.Fatalf("archived push verdict = %+v, want reject", verdict)
	}
}
