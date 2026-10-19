package store

const (
	sqlSavepoint     = "SAVEPOINT req"
	sqlRollbackTo    = "ROLLBACK TO req"
	sqlRelease       = "RELEASE req"
	sqlWALCheckpoint = "PRAGMA wal_checkpoint(TRUNCATE)"
	sqlVacuumInto    = "VACUUM INTO ?"
	sqlSQLiteVersion = "SELECT sqlite_version()"
	sqlQuickCheck    = "PRAGMA quick_check"
	sqlAppID         = "PRAGMA application_id"
	sqlMaxMigration  = "SELECT COALESCE(max(version), 0) FROM schema_migrations"
	sqlFKOff         = "PRAGMA foreign_keys = OFF"
	sqlFKOn          = "PRAGMA foreign_keys = ON"
	sqlFKCheck       = "PRAGMA foreign_key_check"

	sqlCreateMigrations = `CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY CHECK (version > 0),
  checksum   TEXT    NOT NULL CHECK (length(checksum) = 64),
  applied_at INTEGER NOT NULL CHECK (applied_at > 0)
) STRICT`
	sqlAppliedMigrations = `SELECT version, checksum FROM schema_migrations`
	sqlMigrationRecord   = `INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (?1, ?2, ?3)`

	sqlConsumerCursor  = `SELECT cursor FROM outbox_consumers WHERE name = ?1`
	sqlConsumerAdvance = `UPDATE outbox_consumers SET cursor = ?1, updated_at = ?2 WHERE name = ?3 AND cursor = ?4`
	sqlConsumerCreate  = `INSERT INTO outbox_consumers (name, cursor, updated_at) VALUES (?1, ?2, ?3) ON CONFLICT (name) DO NOTHING`
	sqlOutboxInsert    = `INSERT INTO outbox (public_id, kind, body, occurred_at) VALUES (?1, ?2, ?3, ?4)`
	sqlOutboxAfter     = `SELECT seq, public_id, kind, body, occurred_at FROM outbox
WHERE seq > COALESCE((SELECT cursor FROM outbox_consumers WHERE name = ?1), 0)
ORDER BY seq LIMIT ?2`
)
