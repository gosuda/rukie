package githttp

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openLookupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/lookup.sqlite")
	if err != nil {
		t.Fatalf("open lookup db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	stmts := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, lower_name TEXT NOT NULL)`,
		`CREATE TABLE repos (id INTEGER PRIMARY KEY, owner_user_id INTEGER NULL, org_id INTEGER NOT NULL DEFAULT 0, visibility TEXT NOT NULL, name TEXT NOT NULL, deleted_at INTEGER NULL)`,
		`INSERT INTO users (id, lower_name) VALUES (1, 'ada')`,
		`INSERT INTO repos (id, owner_user_id, org_id, visibility, name, deleted_at) VALUES (11, 1, 0, 'private', 'hello', NULL)`,
		`INSERT INTO repos (id, owner_user_id, org_id, visibility, name, deleted_at) VALUES (12, 1, 0, 'private', 'gone', 1735689600000)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed lookup db: %v", err)
		}
	}
	return db
}

func TestLookupRepoHidesSoftDeleted(t *testing.T) {
	s := &Server{db: openLookupDB(t)}
	if id, ok := s.lookupRepo(t.Context(), "ada", "hello"); !ok || id != 11 {
		t.Fatalf("live repo lookup = %d, %v, want 11, true", id, ok)
	}
	if id, ok := s.lookupRepo(t.Context(), "ada", "gone"); ok || id != 0 {
		t.Fatalf("soft-deleted repo lookup = %d, %v, want 0, false", id, ok)
	}
}
