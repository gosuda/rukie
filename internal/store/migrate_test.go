package store

import (
	"errors"
	"testing"
	"testing/fstest"
)

func TestMigrationsContiguous(t *testing.T) {
	gap := fsWith(t, map[string]string{
		"migrations/0001_init.sql": "CREATE TABLE init(x INTEGER) STRICT;",
		"migrations/0003_gap.sql":  "CREATE TABLE gap(x INTEGER) STRICT;",
	})
	if ms, err := loadMigrations(gap); err != nil {
		t.Fatalf("loadMigrations rejected a reserved gap: %v", err)
	} else if len(ms) != 2 || ms[1].version != 3 {
		t.Fatalf("gap load = %+v, want versions 1 and 3", ms)
	}
	dup := fsWith(t, map[string]string{
		"migrations/0001_init.sql": "CREATE TABLE init(x INTEGER) STRICT;",
		"migrations/0002_a.sql":    "CREATE TABLE a(x INTEGER) STRICT;",
		"migrations/0002_b.sql":    "CREATE TABLE b(x INTEGER) STRICT;",
	})
	if _, err := loadMigrations(dup); err == nil {
		t.Fatal("loadMigrations accepted duplicate versions")
	}
	nostart := fstest.MapFS{
		"migrations/0002_nostart.sql": &fstest.MapFile{Data: []byte("CREATE TABLE n(x INTEGER) STRICT;")},
	}
	if _, err := loadMigrations(nostart); err == nil {
		t.Fatal("loadMigrations accepted a set that does not start at 1")
	}
	dupCheck := fstest.MapFS{
		"migrations/0001_init.sql": &fstest.MapFile{Data: []byte("CREATE TABLE init(x INTEGER) STRICT;")},
		"migrations/0002_a.sql":    &fstest.MapFile{Data: []byte("CREATE TABLE a(x INTEGER) STRICT;")},
		"migrations/0002_b.sql":    &fstest.MapFile{Data: []byte("CREATE TABLE b(x INTEGER) STRICT;")},
	}
	if _, err := loadMigrations(dupCheck); err == nil {
		t.Fatal("loadMigrations accepted duplicate versions across files")
	}
	if _, err := loadMigrations(embeddedMigrations); err != nil {
		t.Fatalf("embedded migrations rejected: %v", err)
	}
}

func TestMigrationFilesHaveNoTxControl(t *testing.T) {
	rejected := []string{
		"BEGIN;",
		"commit;",
		"ROLLBACK",
		"SAVEPOINT s",
		"RELEASE s",
		"PRAGMA foreign_keys = OFF",
		"CREATE TABLE x(a INTEGER) STRICT;\nBEGIN;",
	}
	for _, body := range rejected {
		fsys := fsWith(t, map[string]string{"migrations/0002_bad.sql": "CREATE TABLE x(a INTEGER) STRICT;\n" + body})
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("transaction control %q accepted", body)
		}
	}
	accepted := []string{
		"CREATE TABLE x(\n  release INTEGER,\n  begin_at INTEGER,\n  commit_at INTEGER\n) STRICT;",
		"-- note; commit later is only a comment\nCREATE TABLE y(a INTEGER) STRICT;",
		"CREATE TRIGGER t AFTER INSERT ON probe_rows BEGIN\n  SELECT 1;\nEND;",
	}
	for i, body := range accepted {
		fsys := fsWith(t, map[string]string{"migrations/0002_ok.sql": body})
		if _, err := loadMigrations(fsys); err != nil {
			t.Errorf("valid migration %d refused: %v", i, err)
		}
	}
}

func TestMigrationRollback(t *testing.T) {
	path := testPath(t)
	half := fsWith(t, map[string]string{
		"migrations/0002_half.sql": "CREATE TABLE half(x INTEGER) STRICT;\nINSERT INTO nosuch VALUES (1);\n",
	})
	_, err := open(t.Context(), Options{Path: path}, half)
	if !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("open with failing migration returned %v, want ErrMigrationFailed", err)
	}
	db := rawDB(t, path)
	if n := rawInt(t, db, `SELECT count(*) FROM sqlite_master WHERE name = 'half'`); n != 0 {
		t.Fatalf("failed migration left table half")
	}
	if n := rawInt(t, db, `SELECT count(*) FROM schema_migrations WHERE version = 2`); n != 0 {
		t.Fatalf("failed migration left a schema_migrations row")
	}
	if n := rawInt(t, db, `SELECT count(*) FROM schema_migrations WHERE version = 1`); n != 1 {
		t.Fatalf("0001 was not kept after 0002 failed: %d rows", n)
	}
	if got := rawInt(t, db, `PRAGMA application_id`); got != appID {
		t.Fatalf("application_id = %d after 0001, want %d", got, appID)
	}
}

func TestSchemaTooNew(t *testing.T) {
	path := testPath(t)
	first := openWith(t, path, fixtureFS(t), Options{})
	if err := first.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := open(t.Context(), Options{Path: path}, fsWith(t, nil))
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("open with older binary returned %v, want ErrSchemaTooNew", err)
	}
}

func TestChecksumChanged(t *testing.T) {
	path := testPath(t)
	first := openWith(t, path, fixtureFS(t), Options{})
	if err := first.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	edited := fsWith(t, map[string]string{"migrations/0002_probe.sql": fixtureMigration + "\n-- edited after merge\n"})
	_, err := open(t.Context(), Options{Path: path}, edited)
	if !errors.Is(err, ErrMigrationChanged) {
		t.Fatalf("open with edited migration returned %v, want ErrMigrationChanged", err)
	}
}

func TestForeignKeysOnOrdinaryMigration(t *testing.T) {
	dangling := fsWith(t, map[string]string{"migrations/0002_dangling.sql": `CREATE TABLE q(id INTEGER PRIMARY KEY) STRICT;
CREATE TABLE p(id INTEGER PRIMARY KEY, r INTEGER NOT NULL REFERENCES q (id)) STRICT;
INSERT INTO p (r) VALUES (5);
`})
	_, err := open(t.Context(), Options{Path: testPath(t)}, dangling)
	if !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("ordinary migration with dangling FK returned %v, want ErrMigrationFailed", err)
	}
}

const rebuildChildren = `CREATE TABLE probe_children_new (
  id     INTEGER PRIMARY KEY,
  row_id INTEGER NOT NULL REFERENCES probe_rows (id)
) STRICT;
`

func TestRebuildMigrationFKCheck(t *testing.T) {
	dangling := "-- rukie:foreign_keys=off\n" + rebuildChildren +
		"INSERT INTO probe_children_new (id, row_id) VALUES (1, 424242);\n" +
		"DROP TABLE probe_children;\n" +
		"ALTER TABLE probe_children_new RENAME TO probe_children;\n"
	path := testPath(t)
	bad := fsWith(t, map[string]string{
		"migrations/0002_probe.sql":   fixtureMigration,
		"migrations/0003_rebuild.sql": dangling,
	})
	_, err := open(t.Context(), Options{Path: path}, bad)
	if !errors.Is(err, ErrMigrationFailed) {
		t.Fatalf("rebuild with dangling FK returned %v, want ErrMigrationFailed", err)
	}
	db := rawDB(t, path)
	if n := rawInt(t, db, `SELECT count(*) FROM sqlite_master WHERE name = 'probe_children'`); n != 1 {
		t.Fatalf("failed rebuild left probe_children missing")
	}

	clean := "-- rukie:foreign_keys=off\n" +
		"INSERT INTO probe_rows (id, tag, body) VALUES (1, 'parent', 'kept');\n" +
		"INSERT INTO probe_children (id, row_id) VALUES (7, 1);\n" +
		rebuildChildren +
		"INSERT INTO probe_children_new (id, row_id) SELECT id, row_id FROM probe_children;\n" +
		"DROP TABLE probe_children;\n" +
		"ALTER TABLE probe_children_new RENAME TO probe_children;\n"
	s := openWith(t, testPath(t), fsWith(t, map[string]string{
		"migrations/0002_probe.sql":   fixtureMigration,
		"migrations/0003_rebuild.sql": clean,
	}), Options{})
	if got := readInt(t, s, `SELECT count(*) FROM schema_migrations`); got != 3 {
		t.Fatalf("schema_migrations rows = %d, want 3", got)
	}
	if got := readInt(t, s, `SELECT count(*) FROM probe_children WHERE id = 7 AND row_id = 1`); got != 1 {
		t.Fatalf("child row did not survive the rebuild: %d", got)
	}
	if got := readInt(t, s, `PRAGMA foreign_keys`); got != 1 {
		t.Fatalf("foreign_keys = %d after clean rebuild, want 1", got)
	}
	if got := foreignKeyViolations(t, s); got != 0 {
		t.Fatalf("foreign_key_check reports %d violations after rebuild", got)
	}
}

func foreignKeyViolations(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	err := s.Read(t.Context(), func(tx *ReadTx) error {
		rows, err := tx.tx.QueryContext(tx.ctx, sqlFKCheck)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			n++
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	return n
}

func TestAppIDMismatch(t *testing.T) {
	if err := setupProcess(true); err != nil {
		t.Fatalf("setupProcess: %v", err)
	}
	path := testPath(t)
	raw, err := sqlOpenRaw(path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err = raw.Exec(`PRAGMA application_id = 5`); err != nil {
		t.Fatalf("set application_id: %v", err)
	}
	if _, err = raw.Exec(`CREATE TABLE stranger(x INTEGER)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err = raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}
	_, err = open(t.Context(), Options{Path: path}, fixtureFS(t))
	if !errors.Is(err, ErrAppIDMismatch) {
		t.Fatalf("open of foreign file returned %v, want ErrAppIDMismatch", err)
	}
}

func TestOpenRefusesSamePath(t *testing.T) {
	path := testPath(t)
	first := openWith(t, path, fixtureFS(t), Options{})
	_, err := open(t.Context(), Options{Path: path}, fixtureFS(t))
	if !errors.Is(err, ErrAlreadyOpen) {
		t.Fatalf("second open returned %v, want ErrAlreadyOpen", err)
	}
	if err = first.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := open(t.Context(), Options{Path: path}, fixtureFS(t))
	if err != nil {
		t.Fatalf("open after Close: %v", err)
	}
	if err = reopened.Close(t.Context()); err != nil {
		t.Fatalf("Close reopened: %v", err)
	}
}
