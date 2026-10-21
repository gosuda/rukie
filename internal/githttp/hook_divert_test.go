package githttp

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func TestDivertNgUnlessCommitted(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hook.sqlite")
	st, err := store.Open(t.Context(), store.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO users
(name, lower_name, email, password_hash, password_algo, password_changed_at, created_at, updated_at)
VALUES ('ada', 'ada', 'ada@example.test', 'x', 'argon2id-v19', 1, 1, 1)`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO repos
(owner_user_id, org_id, name, visibility, created_at, updated_at)
VALUES (1, 0, 'hello', 'private', 1, 1)`); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	s := &Server{
		store:       st,
		db:          db,
		resolver:    access.NewSQLResolver(db, nil),
		repoRoot:    dir,
		clock:       time.Now,
		hookTimeout: 10 * time.Second,
	}
	if err := st.Close(context.Background()); err != nil {
		t.Fatalf("store close: %v", err)
	}
	head := strings.Repeat("a", 64)
	v := s.judge(t.Context(), access.Caller{
		UserID: 1,
		Scopes: op.Scopes(0).Add(op.ScopeRepoRead),
		Active: true,
		Authed: true,
	}, 1, wire.HookPreReceive, wire.HookCommand{
		Name:       "refs/for/main",
		New:        head,
		Authorized: true,
	})
	if v.Kind != wire.HookReject {
		t.Fatalf("committed verdict = %q, want reject on failed transaction", v.Kind)
	}
	if !strings.Contains(v.Reason, "was not created") {
		t.Fatalf("reason = %q, want the commit-failure reason", v.Reason)
	}
}
