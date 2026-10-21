package store

import (
	"errors"
	"fmt"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	// ErrClosed reports a call on a store that has begun Close.
	ErrClosed = errors.New("store: closed")
	// ErrAlreadyOpen reports a second Open of a path in this process.
	ErrAlreadyOpen = errors.New("store: path already open in this process")
	// ErrNotFound reports a missing row.
	ErrNotFound = errors.New("store: not found")
	// ErrConflict reports a unique or primary key violation.
	ErrConflict = errors.New("store: unique or primary key conflict")
	// ErrReference reports a foreign key violation.
	ErrReference = errors.New("store: foreign key violation")
	// ErrInvalid reports input that fails a store precondition.
	ErrInvalid = errors.New("store: invalid input")
	// ErrStale reports a version or cursor mismatch.
	ErrStale = errors.New("store: version or cursor mismatch")
	// ErrRolledBack reports a request that did not commit; retry is safe.
	ErrRolledBack = errors.New("store: request not committed; safe to retry")
	// ErrOutcomeUnknown reports a failed COMMIT; verify before retry.
	ErrOutcomeUnknown = errors.New("store: COMMIT failed; outcome unknown; verify before retry")
	// ErrUnavailable reports a writer that cannot start a transaction.
	ErrUnavailable = errors.New("store: writer unavailable; retry with backoff")
	// ErrPanic reports a request function that panicked.
	ErrPanic = errors.New("store: request function panicked")
	// ErrTxClosed reports a transaction used after its callback returned.
	ErrTxClosed = errors.New("store: transaction used after callback returned")
	// ErrOFDUnavailable reports that OFD locks are unavailable.
	ErrOFDUnavailable = errors.New("store: OFD locks unavailable on this kernel or filesystem")
	// ErrSQLiteTooOld reports a SQLite library below 3.53.4.
	ErrSQLiteTooOld = errors.New("store: SQLite below 3.53.4")
	// ErrFTS5Unavailable reports that no trigram FTS5 tokenizer works.
	ErrFTS5Unavailable = errors.New("store: FTS5 trigram tokenizer unavailable in this build")
	// ErrAppIDMismatch reports a database owned by another application.
	ErrAppIDMismatch = errors.New("store: file application_id is not rukie's")
	// ErrSchemaTooNew reports a database migrated past this binary.
	ErrSchemaTooNew = errors.New("store: database schema is newer than this binary")
	// ErrMigrationChanged reports an applied migration whose bytes changed.
	ErrMigrationChanged = errors.New("store: applied migration checksum differs from embedded file")
	// ErrMigrationFailed reports a migration whose transaction rolled back.
	ErrMigrationFailed  = errors.New("store: migration failed; transaction rolled back")
	// ErrAdminExists reports a bootstrap refused because an admin already exists.
	ErrAdminExists = errors.New("store: admin already exists")
	ErrBackupInProgress = barrierRefusal{}
)

type barrierRefusal struct{}
	// ErrSoleOwner reports a user deletion refused because the user is a sole owner.
	ErrSoleOwner = errors.New("store: sole owner cannot be removed")

func (barrierRefusal) Error() string { return "store: backup in progress" }

func (barrierRefusal) BackupInProgress() bool { return true }

const (
)

const (
	sqliteConstraintCheck   = sqlite3.SQLITE_CONSTRAINT_CHECK
	sqliteConstraintFK      = sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
	sqliteConstraintNotNull = sqlite3.SQLITE_CONSTRAINT_NOTNULL
	sqliteConstraintPK      = sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	sqliteConstraintUnique  = sqlite3.SQLITE_CONSTRAINT_UNIQUE
)

var errSavepointMachinery = errors.New("store: savepoint machinery failed")

func mapErr(err error) error {
	se, ok := errors.AsType[*sqlite.Error](err)
	if !ok {
		return err
	}
	switch se.Code() {
	case sqliteConstraintUnique, sqliteConstraintPK:
		return fmt.Errorf("%w: %w", ErrConflict, err)
	case sqliteConstraintFK:
		return fmt.Errorf("%w: %w", ErrReference, err)
	case sqliteConstraintCheck, sqliteConstraintNotNull:
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	default:
		return err
	}
}
