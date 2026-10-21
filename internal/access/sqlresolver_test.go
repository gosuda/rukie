package access

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openProbeDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/probe.sqlite")
	if err != nil {
		t.Fatalf("open probe db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedProbeRepos(t *testing.T, db *sql.DB) (live, deleted int64) {
	t.Helper()
	stmts := []string{
		`CREATE TABLE users (id INTEGER PRIMARY KEY, lower_name TEXT NOT NULL)`,
		`CREATE TABLE repos (id INTEGER PRIMARY KEY, owner_user_id INTEGER NULL, org_id INTEGER NOT NULL DEFAULT 0, visibility TEXT NOT NULL, name TEXT NOT NULL, archived INTEGER NOT NULL DEFAULT 0, deleted_at INTEGER NULL)`,
		`INSERT INTO users (id, lower_name) VALUES (1, 'ada')`,
		`INSERT INTO repos (id, owner_user_id, org_id, visibility, name, archived, deleted_at) VALUES (11, 1, 0, 'private', 'hello', 0, NULL)`,
		`INSERT INTO repos (id, owner_user_id, org_id, visibility, name, archived, deleted_at) VALUES (12, 1, 0, 'private', 'gone', 0, 1735689600000)`,
		`INSERT INTO repos (id, owner_user_id, org_id, visibility, name, archived, deleted_at) VALUES (13, 1, 0, 'private', 'frozen', 1, NULL)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed probe db: %v", err)
		}
	}
	return 11, 12
}

func TestSQLResolverHidesSoftDeletedRepo(t *testing.T) {
	db := openProbeDB(t)
	live, deleted := seedProbeRepos(t, db)
	resolver := NewSQLResolver(db, nil)
	if _, ok := resolver.Repo(live); !ok {
		t.Fatalf("live repo %d is invisible", live)
	}
	if _, ok := resolver.Repo(deleted); ok {
		t.Fatalf("soft-deleted repo %d is visible", deleted)
	}
}

func TestSQLResolverLoadsArchivedFlag(t *testing.T) {
	db := openProbeDB(t)
	seedProbeRepos(t, db)
	resolver := NewSQLResolver(db, nil)
	live, ok := resolver.Repo(11)
	if !ok || live.Archived {
		t.Fatalf("live repo = %+v, %v, want archived false", live, ok)
	}
	frozen, ok := resolver.Repo(13)
	if !ok || !frozen.Archived {
		t.Fatalf("archived repo = %+v, %v, want archived true", frozen, ok)
	}
}
