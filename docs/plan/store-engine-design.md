## Review disposition

I checked each aggregated finding against the source and the design. Accepted items are reflected in the artifact below. Rejected or no-op items are listed here so main does not chase them.

- **Accepted, changed the design:**
  - Outbox contiguity assertion (blocker). I5 is now monotonic non-reuse, not contiguity.
  - `RWMutex` in `Submit`/`Close` (real bug). Replaced with a `closing` channel plus an in-flight counter.
  - Manual `BEGIN` strings. Writer and reader transactions now use `database/sql` `Tx`. The writer uses `_txlock=immediate`. Savepoints remain raw SQL.
  - Migration foreign keys. Stay ON by default. Only explicitly declared table-rebuild migrations turn them OFF, and they must pass `foreign_key_check` before COMMIT.
  - `mmap_size` set to `0` until libc mmap support is checked and a benchmark justifies it. A fault past EOF on a mapped page is a crash risk.
  - DSN keys now use the driver's shorthand keys where they exist. `_pragma` is kept only for keys with no shorthand. `_dqs=0` is added.
  - Each pragma now has an explicit mechanism column.
  - libc pin rule added (`doc.go` "Fragile modernc.org/libc dependency").
  - Exact `OFDLocking`, `OFDLockingEnabled`, and `StrictPragmas` signatures added from `ofd.go` and `strictpragma.go`.
  - Identifier policy is now one canonical rule: `INTEGER PRIMARY KEY` everywhere, ULID as a `UNIQUE` public handle.
  - Stray `id` line removed from the outbox DDL.
  - VACUUM INTO test now covers a backup connection without `query_only`.
  - FTS startup probe now runs `rebuild`, `optimize`, and `integrity-check` on a temp external-content table.
- **Accepted, reworded:** lib error constants. `error.go` in v1.60.1 imports `modernc.org/sqlite/lib` and uses the primary codes. Extended codes are derived from those primaries with shifts. A test asserts them against real violations. I did not verify the "nine removed" claim from CHANGELOG, so it is not asserted.
- **Already satisfied:** `_journal_mode` is not on the reader DSN. OFD is process-wide, not a DSN key. The writer/read closure style is already chosen, with the compound-caller citation in §1.1.
- **Rejected:** enabling `_defensive=1`. The driver calls it hardening, not a sandbox. The probe covers FTS5 commands on the same build instead.
- **Rejected as no-op:** the two placeholder advisories ("dummy", "stay silent"). They contain no finding.
- **Stop-research advisory:** followed. No further tool calls; this is the final artifact.

---

# store-engine-design.md: rukie metadata store (`internal/store`)

Status: design proposal, 2026-10-20. Engine: `modernc.org/sqlite` v1.60.1 (SQLite 3.53.4), Go 1.27.

**Labels.** `[SRC]` = read in a fetched or local source. `[INF]` = inference, not observed. `VERIFY` = checked by a named test in the first implementation PR (§12).

**Sources.**
- modernc v1.60.1 source: `driver.go` (DSN contract, shorthand keys, `_pragma`, `_txlock`, `_dqs`, `_defensive`, `mode=ro`, apply order), `sqlite.go` (`applyQueryParams`; `interruptOnDone`, which calls `sqlite3_interrupt` on context cancel), `ofd.go`, `strictpragma.go`, `error.go`, `doc.go` (pool bounding, per-connection state, "Fragile modernc.org/libc dependency"), `go.mod` (`go 1.26.0`, `modernc.org/libc v1.77.1`).
- SQLite docs: `c3ref/interrupt.html` (an interrupted INSERT/UPDATE/DELETE inside an explicit transaction rolls back the whole transaction), `lang_vacuum.html` (VACUUM may change ROWIDs of tables without `INTEGER PRIMARY KEY`; `VACUUM INTO` target must not exist or must be empty).
- Planning dir: `research-metadata-store.md` §2, §3, §10; `research-go-stack.md` (SQLite and migration picks). `research-go-performance.md` is not present in the planning dir.
- `rukie-ruk-native-forge-plan.md` (settled SQLite-only decision, Go 1.27.0 / go1.27.2); `webhook-design.md` (§6.4 transactional enqueue; line 116 `Store.Webhooks()`); `auth-design.md:239` (`AuthStore.LookupSSHKey`); `inbox-design.md` (`domain_events` cursor and ULID `id`).

---

## 0. Decisions

| Topic | Decision | Reason |
|---|---|---|
| Public style | One `Store`. `Read(ctx, fn func(*ReadTx) error)` and `Write(ctx, fn func(*WriteTx) error)`. Transactions expose per-aggregate typed accessors. No SQL and no `database/sql` types in the API. | Compound callers need several aggregates plus outbox rows in one commit (§1.1). Group commit needs closures as savepoint units (§3.4). |
| Engine seam | None. `Store` is a concrete struct. Tests use a real temp database. | Settled one-engine verdict. Real SQLite in tests avoids a mock layer. |
| Identifier policy | Canonical physical key: `id INTEGER PRIMARY KEY` on every table. Public handle: `public_id TEXT NOT NULL UNIQUE`, a 26-char ULID. Only `public_id` crosses the store boundary. No table uses a ULID as its primary key. | §7.1. |
| Connections | Writer: one pinned `*sql.Conn`. Readers: R pinned `*sql.Conn`s, R = `clamp(GOMAXPROCS, 2, 64)`. Each has its own prepared statements. | Fixed sets, no pool churn, per-connection statement cache. |
| Transaction control | `database/sql` `Tx` for BEGIN/COMMIT/ROLLBACK. Writer uses `_txlock=immediate` on its DSN. No manual `BEGIN`/`COMMIT`/`ROLLBACK` strings. Savepoints are raw `SAVEPOINT`/`ROLLBACK TO`/`RELEASE`. | Driver-documented mechanism (`driver.go` `_txlock`). |
| Durability | WAL, `synchronous=FULL`. A request is acknowledged only after `Tx.Commit` returns. | Acknowledged implies fsynced (§2.2). |
| Cross-process lock | `sqlite.OFDLocking(true)` before the first DB file is opened. Refuse on Linux if unavailable (unless `AllowPOSIXLocks`). | `ofd.go`: in-process `close(2)` strips POSIX locks; OFD locks survive it. |
| Group commit | `MaxBatch` 64, `MaxLinger` 2 ms (adaptive, §3.3), queue depth 256. | Matches Ruk's publication lane. |
| Failure isolation | `SAVEPOINT` per request. | One failing request does not fail the batch. |
| Cancellation | Request ctx checked only at dequeue. Batch transaction and statements use `context.WithoutCancel`. | `sqlite3_interrupt` rolls back the whole transaction, and modernc calls it on ctx cancel. |
| Shutdown | `closing` channel plus in-flight counter. No `RWMutex`, no `close(reqs)`. | §3.2, §3.6. Close is bounded by its ctx. |
| Migrations | Embedded `NNNN_name.sql`. `schema_migrations(version, checksum, applied_at)`. One transaction per file. Foreign keys ON; table-rebuild files opt out with a directive and must pass `foreign_key_check` before COMMIT. Refuse newer or changed versions. In-tree runner, no goose. | §5. |
| Outbox | `outbox` table written in the same `WriteTx` as domain rows. `seq` (AUTOINCREMENT) is the cursor. Consumers advance cursors by CAS. Gaps allowed; consumers query `seq > cursor`. | §6. |
| Times | INTEGER Unix milliseconds, UTC. RFC 3339 only at API and webhook edges. | §7.2. |
| Search | FTS5 `trigram` external-content tables with triggers in the same transaction. Terms ≥3 runes go to FTS5. Shorter terms use an `instr()` predicate. | §8. |
| FTS5 check | Startup probe on a temp external-content table: insert, `rebuild`, `optimize`, `integrity-check`, MATCH, plus `json_valid`. Fail fast. | §8.3. |
| Hardening | `_dqs=0` on both DSNs (double-quoted strings are errors). `_defensive` not enabled. | `driver.go`: defensive mode is hardening, not a sandbox. |
| Backup | `VACUUM INTO` on a dedicated backup connection to `.partial`. Verify. Atomic rename. | §9. |
| libc | `go.mod` requires `modernc.org/libc v1.77.1`, the version pinned by `modernc.org/sqlite` v1.60.1. Not moved by `go get -u`. | `doc.go` "Fragile modernc.org/libc dependency". §2.5. |
| mmap | `mmap_size(0)` on both connections until libc mmap support is verified and a benchmark justifies a size. | §2.2, §12. |

---

## 1. Public API (`internal/store`)

### 1.1 Style choice, justified by callers

Choice: closure-based `Read`/`Write`, with typed accessors on the transaction objects.

Caller evidence:
- **Compound atomic fan-out.** `webhook-design.md` §6.4 says every domain mutation that fires an event (ref update, change transition, review insert, attestation admit, task insert/claim/close, CI state, release publish, mirror failure, repo create/delete) must persist its domain rows and its fanned-out webhook messages in one transaction. Services compose "mutation + outbox emit + webhook enqueue" themselves. A typed-only store would need one compound method per event site and would import webhook policy.
- **Lease grants.** A CI lease grants a job in one `BEGIN IMMEDIATE` transaction: fence bump, job state, runner row, outbox row (`research-ci-runner.md`, Lease RPCs). That is a multi-table unit with decisions in the middle.
- **Accessor shape already in the docs.** `Store.Webhooks()` (`webhook-design.md:116`) and `AuthStore.LookupSSHKey` (`auth-design.md:239`) are per-domain interfaces reached through one store. "Daemons never read the DB directly" is the same rule as "no SQL leaks to callers."
- **Group-commit isolation.** Each request must be an opaque unit that can be savepointed and rolled back alone. A closure is that unit. A command enum would need a serializable op language for no safety gain.

Rejected: typed-only methods (above); exposing `*sql.Tx`, `*sql.DB`, or raw `Query(string)` (forbidden by the requirement); a command enum for the writer.

Closure rules (review and tests enforce them):
1. A `Write` closure runs **exactly once**. The store never retries it. (Contrast bbolt `DB.Batch`, which may rerun its function, `research-metadata-store.md` §3.)
2. A closure does no I/O outside the database and does not block. Its only external side effect is `tx.Outbox().Emit`.
3. A closure must not call `Store.Write`. That is a guaranteed deadlock: the writer waits on its own reply. A closure may call `Store.Read`, but that sees the last committed snapshot, not the batch's uncommitted writes. Use the `WriteTx` accessors.
4. Requests in one batch run in arrival order. Each sees the effects of earlier requests in its batch.
5. A `ReadTx` or `WriteTx` used after its callback returns yields `ErrTxClosed`.

### 1.2 Types and signatures

```go
package store

// Options configures Open. Zero values select the defaults noted.
type Options struct {
	Path            string        // absolute path to meta.sqlite; parent dir 0700, file 0600
	ReadConns       int           // 0 => max(2, runtime.GOMAXPROCS(0)); clamped to [2, 64]
	MaxBatch        int           // 0 => 64 requests per COMMIT
	MaxLinger       time.Duration // 0 => 2ms (applied adaptively, §3.3)
	QueueDepth      int           // 0 => 256
	AllowPOSIXLocks bool          // false: refuse Open where OFD locks are unavailable (Linux)
	Logger          *slog.Logger  // nil => slog.Default()
}

// Stats is advisory, for metrics.
type Stats struct {
	QueueLen       int
	ReadersInUse   int
	Batches        uint64
	Requests       uint64
	LastBatchSize  int
	RolledBack     uint64 // requests whose savepoint was rolled back
	CommitFailures uint64
}

// Store is safe for concurrent use. At most one Store per process per path.
type Store struct{ /* unexported: writer, reader pool, DB handles, closing chans */ }

// Open performs §2.1 process setup, opens and verifies the database (§2.4), runs
// migrations (§5), starts the writer loop, and fills the reader pool.
// Errors: ErrAlreadyOpen, ErrOFDUnavailable, ErrSQLiteTooOld, ErrFTS5Unavailable,
// ErrAppIDMismatch, ErrSchemaTooNew, ErrMigrationChanged, ErrMigrationFailed.
func Open(ctx context.Context, opts Options) (*Store, error)

// Read runs fn in one read transaction on a pooled reader. All statements in fn
// share one snapshot. Returns ErrClosed once Close has begun.
func (s *Store) Read(ctx context.Context, fn func(tx *ReadTx) error) error

// Write enqueues fn for the single writer, waits for its batch's COMMIT, and
// returns fn's outcome. If Write returns ctx.Err(), fn never ran (§3.2).
func (s *Store) Write(ctx context.Context, fn func(tx *WriteTx) error) error

// Backup writes a verified copy to dst (must not exist). §9.
func (s *Store) Backup(ctx context.Context, dst string) (BackupInfo, error)

type BackupInfo struct {
	Path          string
	Bytes         int64
	SchemaVersion int64
	SHA256        string
	CreatedAt     time.Time
}

func (s *Store) Stats() Stats

// Close stops new work, drains queued writes, checkpoints, and closes. When ctx
// expires, undrained requests receive ErrClosed without running. Close returns
// within one batch of ctx expiry.
func (s *Store) Close(ctx context.Context) error

// ---- transactions ----

type ReadTx struct {
	ctx  context.Context // the Read ctx; cancel makes reads fail (safe: read-only)
	tx   *sql.Tx
	r    *readStmts
	done bool
}

func (tx *ReadTx) Repos() RepoReader
func (tx *ReadTx) Tasks() TaskReader
func (tx *ReadTx) Outbox() OutboxReader
func (tx *ReadTx) Search() SearchReader

type WriteTx struct {
	ctx  context.Context // batch ctx = context.WithoutCancel(...); never cancelable
	tx   *sql.Tx         // begun with BeginTx on the writer conn; _txlock=immediate
	r    *readStmts
	w    *writeStmts // embeds readStmts
	done bool
}

func (tx *WriteTx) Repos() RepoWriter
func (tx *WriteTx) Tasks() TaskWriter    // includes read methods
func (tx *WriteTx) Outbox() OutboxWriter // includes read methods
func (tx *WriteTx) Search() SearchReader
```

[904 more lines in resource. Use :1207 to continue]
```

`WriteTx` methods take no `context.Context`. A statement in a batch must not observe request cancellation (§3.4). `ReadTx` keeps its ctx for the same reason `sql.Tx` does; a cancelled read is harmless because it writes nothing.

### 1.3 Domain surface (examples; other aggregates follow the same pattern)

```go
// ID is a 26-char Crockford ULID. It is the only identifier that leaves the store.
type ID string

type TaskState string // "open" | "closed"

type Task struct {
	ID        ID
	RepoID    ID
	Number    int64 // per-repo, assigned in the writing transaction
	Title     string
	Body      string
	State     TaskState
	Version   int64 // optimistic-concurrency token (If-Match)
	CreatedAt time.Time // UTC, millisecond precision
	UpdatedAt time.Time
}

type TaskInput struct{ Title, Body string }

type TaskPatch struct {
	Title *string
	Body  *string
	State *TaskState
}

type TaskReader interface {
	Get(id ID) (Task, error)                                        // ErrNotFound
	GetByNumber(repo ID, number int64) (Task, error)                // ErrNotFound
	ListOpen(repo ID, afterNumber int64, limit int) ([]Task, error) // keyset by Number
}

type TaskWriter interface {
	TaskReader
	Create(repo ID, in TaskInput) (Task, error)                   // assigns Number; ErrReference if repo missing
	Update(id ID, expectVersion int64, p TaskPatch) (Task, error) // ErrStale on version mismatch
}

type Repo struct {
	ID        ID
	RukRepoID string // Ruk's ULID
	Owner     string
	Name      string
	CreatedAt time.Time
}

type RepoInput struct{ RukRepoID, Owner, Name string }

type RepoReader interface {
	Get(id ID) (Repo, error)
	ByOwnerName(owner, name string) (Repo, error)
}

type RepoWriter interface {
	RepoReader
	Create(in RepoInput) (Repo, error) // ErrConflict on duplicate owner/name or RukRepoID
}
```

Outbox and search:

```go
// Event is written in the same WriteTx as the mutation it describes.
type Event struct {
	Kind       string          // dotted, 1..64 bytes, e.g. "task.created"
	Body       json.RawMessage // JSON object; json.Valid in Go and CHECK json_valid in SQL
	OccurredAt time.Time
}

type OutboxRow struct {
	Seq        int64 // store-assigned, commit-ordered, never reused
	ID         ID
	Kind       string
	Body       json.RawMessage
	OccurredAt time.Time
}

type OutboxReader interface {
	// Cursor returns 0 for a consumer that has never advanced.
	Cursor(consumer string) (int64, error)
	// After returns up to limit rows with seq > the consumer's cursor, ascending.
	After(consumer string, limit int) ([]OutboxRow, error)
}

type OutboxWriter interface {
	OutboxReader
	Emit(ev Event) (OutboxRow, error) // same transaction as the caller's rows
	// Advance moves the cursor from -> to. ErrStale if the stored cursor != from.
	Advance(consumer string, from, to int64) error
}

type SearchReader interface {
	// Tasks searches one repo. Query is whitespace-split. ErrInvalid if empty
	// or if it has more than 8 terms.
	Tasks(repo ID, query string, limit int) ([]TaskHit, error)
}

type TaskHit struct {
	Task    Task
	Snippet string
	Rank    float64 // bm25; lower is better
}
```

### 1.4 Errors and driver-code mapping

```go
var (
	ErrClosed           = errors.New("store: closed")
	ErrAlreadyOpen      = errors.New("store: path already open in this process")
	ErrNotFound         = errors.New("store: not found")
	ErrConflict         = errors.New("store: unique or primary key conflict")
	ErrReference        = errors.New("store: foreign key violation")
	ErrInvalid          = errors.New("store: invalid input")
	ErrStale            = errors.New("store: version or cursor mismatch")
	ErrRolledBack       = errors.New("store: request not committed; safe to retry")
	ErrOutcomeUnknown   = errors.New("store: COMMIT failed; outcome unknown; verify before retry")
	ErrUnavailable      = errors.New("store: writer unavailable; retry with backoff")
	ErrPanic            = errors.New("store: request function panicked")
	ErrTxClosed         = errors.New("store: transaction used after callback returned")
	ErrOFDUnavailable   = errors.New("store: OFD locks unavailable on this kernel or filesystem")
	ErrSQLiteTooOld     = errors.New("store: SQLite below 3.53.4")
	ErrFTS5Unavailable  = errors.New("store: FTS5 trigram tokenizer unavailable in this build")
	ErrAppIDMismatch    = errors.New("store: file application_id is not rukie's")
	ErrSchemaTooNew     = errors.New("store: database schema is newer than this binary")
	ErrMigrationChanged = errors.New("store: applied migration checksum differs from embedded file")
	ErrMigrationFailed  = errors.New("store: migration failed; transaction rolled back")
)
```

Driver codes come from the lib constants that modernc's own `error.go` (v1.60.1) imports and uses: `sqlite3.SQLITE_BUSY`, `sqlite3.SQLITE_CONSTRAINT`, `sqlite3.SQLITE_INTERRUPT`. Extended codes are derived from the primary with SQLite's documented shifts, so nothing is hand-typed:

```go
import sqlite3 "modernc.org/sqlite/lib"

const (
	sqliteConstraintCheck   = sqlite3.SQLITE_CONSTRAINT | 1<<8 // CHECK
	sqliteConstraintFK      = sqlite3.SQLITE_CONSTRAINT | 3<<8 // FOREIGN KEY
	sqliteConstraintNotNull = sqlite3.SQLITE_CONSTRAINT | 5<<8 // NOT NULL
	sqliteConstraintPK      = sqlite3.SQLITE_CONSTRAINT | 6<<8 // PRIMARY KEY
	sqliteConstraintUnique  = sqlite3.SQLITE_CONSTRAINT | 8<<8 // UNIQUE
)

// mapErr converts a driver error to a store error. The driver error type is
// *sqlite.Error; Code() returns the extended result code.
func mapErr(err error) error
```

The lib import is a compile-time guard: a renamed or removed primary breaks the build. I did not verify the CHANGELOG list of removed `lib` constants, so this section does not assert which ones were removed. `VERIFY` by compile and by `TestErrorCodeConstants` (§10.4), which triggers real violations and checks the codes.

---

## 2. Connection setup

### 2.1 Process-level setup (in `Open`, before any database file is opened)

1. `sqlite.StrictPragmas(true)`. Exact signature (`strictpragma.go`, v1.60.1):
   ```go
   func StrictPragmas(on bool) (prev bool)
   func StrictPragmasEnabled() bool
   var ErrMultiStatementPragma error
   ```
   With it on, a `_pragma` value containing more than one statement fails during the validation phase. It is process-wide and set before any DSN is used.
2. `sqlite.OFDLocking(true)`. Exact signature (`ofd.go`, v1.60.1):
   ```go
   func OFDLocking(on bool) (prev bool, err error)
   func OFDLockingEnabled() bool
   var ErrOFDLockingUnavailable error // not Linux, or kernel/filesystem rejected OFD locks
   var ErrOFDLockingTooLate error     // a DB file lock was already attempted in this process
   ```
   - `ErrOFDLockingTooLate` → return it. Something opened a DB file first; that is a bug.
   - `ErrOFDLockingUnavailable` → return `ErrOFDUnavailable` unless `AllowPOSIXLocks`.
   - After the call, check `OFDLockingEnabled()`. It reports false after the kernel-rejection fallback.
   - Env var `MODERNC_SQLITE_OFD_LOCK` (`ofd.go` doc comment) is read once at library initialization. The call overrides it. Use the call, not `os.Setenv`.
   - Process-wide and not a DSN key: "POSIX and OFD locks taken by one process are different owners to the kernel" (`ofd.go`).
   - Scope limit (`ofd.go`): OFD covers locks on the database file. WAL coordination through `-shm` stays POSIX. Rule: no rukie code may `os.Open`, stat-and-close, or hash `meta.sqlite`, `-wal`, or `-shm`. Backup reads only through SQLite.
3. Process registry: `map[cleanAbsPath]*Store` under a mutex. A second `Open` on the same path returns `ErrAlreadyOpen`.

### 2.2 DSN strings, shorthand keys, and pragma values

Both DSNs use the `file:` form. Only `file:` names honor SQLite URI parameters such as `mode=ro`; a plain path silently drops them (`driver.go`). Build with `(&url.URL{Scheme: "file", Path: abs}).String()` plus `?params`.

Mechanisms available in v1.60.1 (`driver.go`):
- Shorthand keys validated before any apply: `_busy_timeout`/`_timeout`, `_foreign_keys`/`_fk`, `_journal_mode`/`_journal`, `_synchronous`/`_sync`, `_auto_vacuum`/`_vacuum`, `_query_only`, `_txlock` (`deferred` default, `immediate`, `exclusive`), `_dqs`, `_defensive`, `_error_rc`.
- `_pragma=<name>(<value>)`: executed verbatim with `PRAGMA` prepended. Not validated; runs in order after the shorthand keys that apply first.
- Apply order (`driver.go`): `_busy_timeout` and `_auto_vacuum` first, then `_pragma` values, then remaining shorthand keys, then `_query_only` last.
- Mechanism per pragma is explicit in the table. Every value is checked on every physical connection by `verifyConn` (§2.3).

**Writer DSN:**
```
file:///var/lib/rukie/meta.sqlite?_txlock=immediate&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000&_dqs=0&_pragma=temp_store(MEMORY)&_pragma=mmap_size(0)&_pragma=cache_size(-32768)&_pragma=journal_size_limit(67108864)&_pragma=trusted_schema(OFF)
```

**Reader DSN:**
```
file:///var/lib/rukie/meta.sqlite?mode=ro&_query_only=on&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000&_dqs=0&_pragma=temp_store(MEMORY)&_pragma=mmap_size(0)&_pragma=cache_size(-16384)&_pragma=trusted_schema(OFF)
```

**Backup source DSN** (used only by `Backup`, §9): the reader DSN without `_query_only=on`, still `mode=ro`.

| PRAGMA | Writer | Reader | Mechanism | Scope | Reason |
|---|---|---|---|---|---|
| `journal_mode` | `WAL` | not set | `_journal_mode=WAL` (writer only) | persistent | Set once by the writer. A reader with `mode=ro` that tries to set it fails with `SQLITE_READONLY`. |
| `synchronous` | `FULL` | `FULL` | `_synchronous=FULL` | per connection | WAL+FULL fsyncs each commit, so acknowledged writes survive power loss. NORMAL can lose recent commits (`research-metadata-store.md` §2). The reader value is harmless and verified. |
| `foreign_keys` | `ON` | `ON` | `_foreign_keys=on` | per connection | Connection-local. Cannot change inside a transaction (§5). |
| `busy_timeout` | 5000 ms | 5000 ms | `_busy_timeout=5000` | per connection | Integer milliseconds; `"5s"` fails (`sqlite.go`). Absorbs cross-process lock waits. |
| `temp_store` | `MEMORY` | `MEMORY` | `_pragma` (no shorthand) | per connection | FTS and sort scratch in RAM. |
| `mmap_size` | `0` | `0` | `_pragma` (no shorthand) | per connection | Off. Enable only after (a) libc mmap support is checked in `modernc.org/libc` v1.77.1, and (b) a benchmark shows a gain. A fault past EOF on a mapped page is delivered as SIGBUS; a process that does not expect it crashes. `[INF]` Go treats that fault as fatal. |
| `cache_size` | `-32768` (32 MiB) | `-16384` (16 MiB) | `_pragma` (no shorthand) | per connection | Bounded memory: 32 MiB + R × 16 MiB. Each modernc connection has its own page cache (`doc.go`). |
| `journal_size_limit` | 67108864 (64 MiB) | not set | `_pragma` (no shorthand) | per connection | Caps `-wal` size after checkpoints. Only the writer checkpoints. |
| `trusted_schema` | `OFF` | `OFF` | `_pragma` (no shorthand) | per connection | Schema comes only from our migrations. Defense in depth. |
| `query_only` | off | `ON` | `_query_only=on` | per connection | Belt and braces with `mode=ro`. |
| `DQS` | off | off | `_dqs=0` | per connection | Double-quoted strings are errors, so typos fail instead of becoming string literals. All SQL in the store uses single quotes. |
| open mode | read-write, create | `mode=ro` | `file:` URI parameter | open flag | A reader bug cannot write. |
| `wal_autocheckpoint` | default (1000) | n/a | not overridden | per connection | Tunable if commit latency spikes. |
| `auto_vacuum` | NONE (default) | n/a | not set | persistent | The live DB is never VACUUMed. Backup uses `VACUUM INTO` (§9). |
| `application_id` | `1381321545` (0x52554B49) | n/a | `PRAGMA` in migration 0001 | persistent header | Refuse files with a different non-zero value. |

Not used:
- `_defensive`: not enabled. The driver describes it as hardening, not a sandbox (`driver.go`). Its FTS5 effects are proven by the probe in §8.3 if it is ever turned on.
- `_time_format`, `_time_integer_format`: times are stored as integers (§7.2).
- `RegisterConnectionHook`: process-wide. DSN parameters cover per-connection state (`doc.go`).
- `_journal_mode` on the reader DSN: never. See the table.

### 2.3 Per-connection verification

Every physical connection, including reconnects, runs `verifyConn(ctx, *sql.Conn, role)`. It reads `PRAGMA foreign_keys`, `synchronous`, `busy_timeout`, `temp_store`, `cache_size`, `mmap_size`, `trusted_schema`, `query_only` (readers), `journal_mode` (writer must return `wal`), and `dqs` checks (a double-quoted identifier must fail). A mismatch discards the connection and fails `Open`. This catches a silently ignored DSN key.

### 2.4 Startup sequence (fail fast)

1. §2.1 steps 1–3.
2. Create the directory (0700) and the file (0600, `O_CREATE|O_EXCL` if absent).
3. `wdb, _ := sql.Open("sqlite", writerDSN)`; `wdb.SetMaxOpenConns(1)`; `SetMaxIdleConns(1)`; `SetConnMaxLifetime(0)`; `SetConnMaxIdleTime(0)`. `wc, _ := wdb.Conn(ctx)`; `verifyConn`.
4. `SELECT sqlite_version()`. Must be ≥ `3.53.4`, else `ErrSQLiteTooOld`.
5. FTS5 and JSON probe on `wc` (§8.3). Failure → `ErrFTS5Unavailable`. Exit non-zero.
6. `PRAGMA application_id`. Non-zero and not `1381321545` → `ErrAppIDMismatch`.
7. Migrations on `wc` (§5).
8. Prepare `writeStmts` on `wc` (`sql.Conn.PrepareContext`). Start the writer loop (§3).
9. `rdb, _ := sql.Open("sqlite", readerDSN)`; `SetMaxOpenConns(R)`; `SetMaxIdleConns(R)`; `SetConnMaxLifetime(0)`; `SetConnMaxIdleTime(0)`. Acquire R `sql.Conn`s, `verifyConn` each, prepare `readStmts` on each, and put them on the reader channel.

The writer opens first. It creates `-wal` and `-shm`, which the `mode=ro` readers need.

### 2.5 modernc.org/libc pin

`modernc.org/sqlite` v1.60.1 documents: "you should use in your go.mod file the exact same version of modernc.org/libc as seen in the go.mod file of this repository" (`doc.go`, "Fragile modernc.org/libc dependency"). Its `go.mod` pins `modernc.org/libc v1.77.1`.

Rules:
- rukie's `go.mod` requires `modernc.org/libc v1.77.1` exactly, with a comment naming the sqlite pin.
- CI asserts `go list -m -f '{{.Version}}' modernc.org/libc` equals `v1.77.1`. A routine `go get -u` must fail that check.
- Note: `doc.go`'s performance figures were measured with libc v1.75.7. Re-measure on 1.77.1 (§10.6).

---

## 3. Write path

### 3.1 Types and signatures

```go
type writeReq struct {
	ctx  context.Context // read once, at dequeue
	fn   func(*WriteTx) error
	done chan error // cap 1; the writer sends exactly once, always
}

type writer struct {
	reqs      chan *writeReq // cap QueueDepth; never closed
	closing   chan struct{}  // closed once by Close; new submits return ErrClosed
	abort     chan struct{}  // closed by context.AfterFunc when Close's ctx expires
	done      chan struct{}  // closed when loop returns
	inflight  atomic.Int64   // submitters between entry and return
	maxBatch  int
	maxLinger time.Duration
	lastBatch int         // loop-owned; drives adaptive linger
	wdb       *sql.DB     // MaxOpenConns(1)
	conn      *writerConn // loop-owned; nil after discard
	log       *slog.Logger
	stats     writerStats // atomics
}

type writerConn struct {
	conn  *sql.Conn
	stmts *writeStmts
}

func (w *writer) submit(ctx context.Context, r *writeReq) error
func (w *writer) loop()
func (w *writer) drainAndExit()
func (w *writer) collect(batch []*writeReq) []*writeReq
func (w *writer) execBatch(batch []*writeReq)
func (w *writer) runRequest(bctx context.Context, tx *sql.Tx, wtx *WriteTx, r *writeReq) error
func (w *writer) ensureConn(bctx context.Context) error
func (w *writer) discardConn()
func (w *writer) aborting() bool
func (w *writer) closingNow() bool
```

```go
func (s *Store) Write(ctx context.Context, fn func(*WriteTx) error) error {
	if fn == nil {
		return ErrInvalid
	}
	r := &writeReq{ctx: ctx, fn: fn, done: make(chan error, 1)}
	if err := s.writer.submit(ctx, r); err != nil {
		return err // ctx error or ErrClosed; fn was never enqueued
	}
	return <-r.done // the writer always replies exactly once; no ctx here
}
```

Pre-allocate the batch slice to `MaxBatch` and reuse it. Clear references (`clear(batch)`) after each batch.

### 3.2 Submit, cancellation, backpressure, and close

```go
func (w *writer) submit(ctx context.Context, r *writeReq) error {
	w.inflight.Add(1)
	defer w.inflight.Add(-1)
	select {
	case <-w.closing:
		return ErrClosed
	default:
	}
	select {
	case w.reqs <- r:
		return nil
	case <-w.closing:
		return ErrClosed // fn not enqueued (or enqueued and drained; see below)
	case <-ctx.Done():
		return ctx.Err() // fn not enqueued
	}
}
```

Why there is no `RWMutex` and no `close(reqs)`:
- `closing` is closed by `Close` without waiting on anyone. A blocked submitter wakes on `closing` or on its own ctx, so Close and submitters never wait on each other's locks.
- A submitter that wins the select send after `closing` has closed is still counted in `inflight`, so the loop drains it and replies. Its `Write` returns the reply, not `ErrClosed`.
- Ordering argument for the drain: the loop reads `quiet := inflight == 0` *before* it polls `reqs`. A submitter that increments after that read must have checked `closing` after the increment, so it returns `ErrClosed` without sending. A submitter that sent before its decrement has its item visible to the poll. So `quiet` plus an empty poll means no item can still arrive.

Cancellation contract:
- `Write` returns `ctx.Err()` only when fn was never enqueued, or was skipped at dequeue (§3.4). In both cases fn never runs.
- If `Write` returns a reply, fn ran or its outcome is in the reply table (§3.4).

Backpressure: when the queue is full, `Write` blocks until its ctx expires. The HTTP layer sets a write budget (proposed 2 s). On expiry the API returns 503 with `Retry-After: 1`. `Stats().QueueLen` is the saturation signal.

### 3.3 Collection (group commit)

```go
// collect extends batch (which holds the first request) up to maxBatch.
//   1) take everything already queued, without waiting;
//   2) if still short AND w.lastBatch > 1 AND not closing, wait up to maxLinger.
// An isolated write (lastBatch <= 1) commits immediately.
func (w *writer) collect(batch []*writeReq) []*writeReq
```

- Defaults: `maxBatch` 64, `maxLinger` 2 ms, the same as Ruk's publication lane.
- Adaptive linger is a deliberate deviation from Ruk's fixed 2 ms. It removes the 2 ms floor from solo writes. `[INF]` Batching under bursts is kept, because the previous batch is then large.
- During `Close`, linger is off, so the queue drains at full speed.
- Timer: `time.NewTimer` with `defer t.Stop()`. Tested with `testing/synctest` (§10.2).

```go
func (w *writer) loop() {
	defer close(w.done)
	for {
		select {
		case r := <-w.reqs:
			w.execBatch(w.collect([]*writeReq{r}))
		case <-w.closing:
			w.drainAndExit()
			return
		}
	}
}

func (w *writer) drainAndExit() {
	for {
		quiet := w.inflight.Load() == 0 // read BEFORE polling; see §3.2
		select {
		case r := <-w.reqs:
			w.execBatch(w.collect([]*writeReq{r})) // no linger: closingNow() is true
			continue
		default:
		}
		if quiet {
			return
		}
		runtime.Gosched() // an in-flight submitter is about to send or observe closing
	}
}
```

### 3.4 Batch execution

```go
func (w *writer) execBatch(batch []*writeReq) {
	live := batch[:0]
	for _, r := range batch {
		switch {
		case r.ctx.Err() != nil:
			r.done <- r.ctx.Err() // skipped; fn never runs
		case w.aborting():
			r.done <- ErrClosed // Close deadline passed; fn never runs
		default:
			live = append(live, r)
		}
	}
	if len(live) == 0 {
		return
	}
	bctx := context.WithoutCancel(context.Background())
	if err := w.ensureConn(bctx); err != nil {
		replyAll(live, ErrUnavailable)
		return
	}
	tx, err := w.conn.conn.BeginTx(bctx, nil) // _txlock=immediate => BEGIN IMMEDIATE
	if err != nil {
		w.discardConn()
		replyAll(live, ErrUnavailable) // nothing committed
		return
	}
	wtx := &WriteTx{ctx: bctx, tx: tx, r: w.conn.stmts.readStmts, w: w.conn.stmts}
	errs := make([]error, len(live))
	for i, r := range live {
		errs[i] = w.runRequest(bctx, tx, wtx, r)
		if errors.Is(errs[i], errSavepointMachinery) {
			wtx.done = true
			_ = tx.Rollback()
			w.discardConn()
			replyAll(live, ErrRolledBack) // whole batch undone; nothing committed
			return
		}
	}
	wtx.done = true
	if err := tx.Commit(); err != nil {
		w.stats.commitFailures.Add(1)
		w.discardConn()
		for i := range errs {
			if errs[i] == nil {
				errs[i] = fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
			}
		}
	}
	for i, r := range live {
		r.done <- errs[i] // after Commit: nil means fsynced and visible
	}
	w.lastBatch = len(live)
}

func (w *writer) runRequest(bctx context.Context, tx *sql.Tx, wtx *WriteTx, r *writeReq) (err error) {
	if _, e := tx.ExecContext(bctx, "SAVEPOINT req"); e != nil {
		return errSavepointMachinery
	}
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("%w: %v", ErrPanic, p)
		}
		if err != nil {
			if _, e := tx.ExecContext(bctx, "ROLLBACK TO req"); e != nil {
				err = errSavepointMachinery
			}
			w.stats.rolledBack.Add(1)
		}
		if _, e := tx.ExecContext(bctx, "RELEASE req"); e != nil && err == nil {
			err = errSavepointMachinery
		}
	}()
	return mapErr(r.fn(wtx))
}
```

Notes:
- The only transaction strings are the savepoint statements. `BEGIN`, `COMMIT`, and `ROLLBACK` are `Tx` methods, and `_txlock=immediate` supplies `BEGIN IMMEDIATE`.
- `ROLLBACK TO` then `RELEASE` keeps the transaction open, so the batch continues. Only the failing request's writes are undone.
- SQL constraint errors already roll back only their statement (`ON CONFLICT ABORT` default). The explicit savepoint rollback covers closure-level errors.
- Every statement uses `bctx`, never `r.ctx`. Passing `r.ctx` would let a cancel call `sqlite3_interrupt`, which rolls back the whole batch (`c3ref/interrupt.html`; `sqlite.go` `interruptOnDone`).
- `WriteTx` statements go through `tx.StmtContext(bctx, stmt)` on the conn-prepared statement set. `VERIFY` whether `Tx.StmtContext` reuses the conn-bound driver statement or re-prepares it. If it re-prepares, drop `writeStmts` and use `tx.ExecContext`/`tx.QueryContext` with the same SQL constants. The benchmark (§10.6) decides.
- Replies go out after `Commit`, never before. A nil reply means fsynced and visible to later `Read` calls.

Outcome table (what `Write` returns):

| Situation | Returned | Committed? | Caller action |
|---|---|---|---|
| Success | `nil` | yes, fsynced | none |
| ctx done before dequeue | `ctx.Err()` | no; fn never ran | may retry |
| queue closing or Close deadline passed | `ErrClosed` | no | retry after restart |
| fn returned error | error, wrapped | no (savepoint rolled back) | fix input |
| fn panicked | `ErrPanic` | no | bug report |
| constraint inside fn | `ErrConflict`, `ErrReference`, or `ErrInvalid` | no | fix input |
| BEGIN failed or writer conn unavailable | `ErrUnavailable` | no | retry with backoff |
| savepoint machinery failed; batch rolled back | `ErrRolledBack` | no | retry |
| `Commit` returned error | `ErrOutcomeUnknown` | unknown | check idempotency key or receipt, then retry |

Idempotency: the engine has no idempotency key. Domain code that needs one writes a receipts row inside the closure (`request_id UNIQUE`, Ruk's receipt-ledger pattern). A retry after `ErrOutcomeUnknown` finds the row and returns the stored result.

### 3.5 Failure handling and reconnect

- `discardConn`: close prepared statements, then `conn.Raw(func(any) error { return driver.ErrBadConn })`, then `conn.Close()`. `VERIFY` that database/sql closes the connection when a `Raw` callback returns `driver.ErrBadConn` (§12).
- `ensureConn(bctx)`: `wdb.Conn(bctx)`, `verifyConn`, prepare `writeStmts`. Failures back off 50 ms, doubling to a 1 s cap, across batches. Requests get `ErrUnavailable` in the meantime.
- Panics inside `fn` are recovered in `runRequest`. A panic anywhere else in the writer loop is a bug and crashes the process; do not recover it.
- Slow batches: WARN when one batch exceeds 50 ms, with batch size and the slowest request's duration.

### 3.6 Shutdown

```go
func (s *Store) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		close(s.writer.closing) // new submits return ErrClosed
		close(s.readClosing)    // new reads return ErrClosed
	})
	stop := context.AfterFunc(ctx, func() { close(s.writer.abort) })
	defer stop()
	// 1. <-s.writer.done: the loop drains queued requests. After ctx expiry (abort)
	//    it replies ErrClosed to the rest and exits after the current batch.
	// 2. Collect all R reader tokens from s.readers.ch, bounded by ctx. Readers that
	//    are still in use when ctx expires are closed on release (releaseReader sees readClosing).
	// 3. Close reader stmts, conns, and rdb.
	// 4. Writer: PRAGMA wal_checkpoint(TRUNCATE) via the writer conn; close stmts;
	//    conn.Close(); wdb.Close().
	// 5. Remove from the process registry. Return errors.Join of step errors.
}
```

Close never waits on a lock that a submitter holds, so a `Write` blocked on a full queue cannot hold Close past its ctx.

---

## 4. Read path

```go
type readerConn struct {
	conn  *sql.Conn
	stmts *readStmts
}

type readerPool struct {
	ch chan *readerConn // cap R; one reader per in-flight Read
	db *sql.DB
	n  int
}

func (s *Store) acquireReader(ctx context.Context) (*readerConn, error) // ctx.Err() | ErrClosed
func (s *Store) releaseReader(rc *readerConn, broken bool)
```

`Read`:
1. `acquireReader`: select on `ch`, `ctx.Done()` (→ `ctx.Err()`), and `readClosing` (→ `ErrClosed`).
2. `tx, err := rc.conn.BeginTx(ctx, nil)`. The default is deferred. The snapshot is taken at the first page read, and every statement in `fn` shares it.
3. `fn(&ReadTx{ctx: ctx, tx: tx, r: rc.stmts})`. Recover panics.
4. `tx.Rollback()`. Readers write nothing, so rollback is the correct end. Database/sql rolls back on its own if ctx is cancelled mid-read, and later calls on `tx` return an error.
5. `releaseReader(rc, broken)`. On a broken connection, close its statements and discard it. `db.Conn` opens a replacement, which goes through `verifyConn` and prepares fresh statements.

Prepared statements: `readStmts` is prepared once per physical connection with `sql.Conn.PrepareContext`. Statement SQL lives in `stmts.go` as named constants, one field per query. Statements inside a transaction go through `tx.StmtContext` (same `VERIFY` as §3.4).

Sizing and budgets:
- `R = clamp(GOMAXPROCS, 2, 64)`. WAL allows unlimited concurrent readers, so R bounds memory and modernc's per-connection state, not correctness.
- Long reads hold back checkpoints and grow the WAL. `Read` logs WARN past 1 s.
- `doc.go`: do not issue a periodic query before the previous one has returned. Reader checkout enforces this.
- Every query has an `EXPLAIN QUERY PLAN` test. Keyset lists must not show `USE TEMP B-TREE`. `doc.go`: modernc is about 2× C on sorts, so indexes matter.

---

## 5. Migrations

### 5.1 Layout

- Files: `internal/store/migrations/NNNN_name.sql`. Pattern `^([0-9]{4})_([a-z][a-z0-9_]*)\.sql$`. Versions are 1-based and contiguous. Embedded with `//go:embed migrations/*.sql` into an `embed.FS`.
- The runner owns the transaction. A file body must not contain `BEGIN`, `COMMIT`, `ROLLBACK`, `SAVEPOINT`, or `PRAGMA foreign_keys`. A test enforces this with a regex. The only allowed foreign-key control is the header directive in §5.3.
- Migrations are immutable once merged. `checksum` = lowercase hex SHA-256 of the file bytes.

Bookkeeping table (created before any file; not in `embed.FS`):

```sql
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    INTEGER PRIMARY KEY CHECK (version > 0),
  checksum   TEXT    NOT NULL CHECK (length(checksum) = 64),
  applied_at INTEGER NOT NULL -- unix ms
) STRICT;
```

### 5.2 Signatures

```go
type migration struct {
	version  int64
	name     string
	sql      string
	checksum string
	rebuild  bool // header directive: -- rukie:foreign_keys=off
}

// loadMigrations validates names, contiguity (1..N), non-empty bodies, and the
// no-transaction-control rule.
func loadMigrations(fsys fs.FS) ([]migration, error)

// applyMigrations runs pending migrations on conn (the writer conn, before the writer loop starts).
func applyMigrations(ctx context.Context, conn *sql.Conn, ms []migration, now func() time.Time) error
```

### 5.3 Algorithm

[301 more lines in resource. Use :1810 to continue]
### 5.3 Algorithm

1. Create `schema_migrations` if absent (autocommit).
2. Read applied `(version, checksum)` pairs.
3. Refuse with `ErrSchemaTooNew` if the highest applied version exceeds the highest embedded version.
4. Refuse with `ErrMigrationChanged` if an applied version's checksum differs from its embedded file.
5. Refuse with `ErrMigrationFailed` if an applied version is missing from the embedded set (gap or unknown). No downgrades.
6. For each pending version, in order:
   a. Ordinary migration (no directive): foreign keys stay ON. Begin with `conn.BeginTx(ctx, nil)` (`_txlock=immediate`). Run the body with `tx.ExecContext`. Insert the `schema_migrations` row. Run `PRAGMA foreign_key_check` through `tx.QueryContext`; any row → rollback, `ErrMigrationFailed`. Commit.
   b. Rebuild migration (`-- rukie:foreign_keys=off` as the first line): `conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF")` before the transaction, because the pragma is a no-op inside one. Then steps as in (a). The `foreign_key_check` before COMMIT validates the final state, which is the point of the rebuild. Restore `PRAGMA foreign_keys=ON` on every path via `defer`.
   c. A migration that needs deferred checks may begin with `PRAGMA defer_foreign_keys=ON`. `[INF]` The pragma resets at COMMIT or ROLLBACK; `VERIFY`.

Why rebuilds turn foreign keys off: SQLite's documented table-rebuild procedure needs it. With foreign keys ON, `DROP TABLE` on a parent performs an implicit delete that fires ON DELETE actions on children. `defer_foreign_keys` does not stop that. Ordinary migrations never turn enforcement off. A rebuild's invalid intermediate writes can only commit if the final state passes `foreign_key_check`, so the end state is still enforced before COMMIT.

Migration 0001 starts with `PRAGMA application_id = 1381321545;`. `[INF]` That is valid inside a transaction. `VERIFY` (§12).

---

## 6. Outbox pattern

### 6.1 Schema

```sql
CREATE TABLE outbox (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT, -- cursor; never reused
  public_id   TEXT    NOT NULL UNIQUE,           -- ULID
  kind        TEXT    NOT NULL CHECK (length(kind) BETWEEN 1 AND 64),
  body        TEXT    NOT NULL CHECK (json_valid(body)),
  occurred_at INTEGER NOT NULL CHECK (occurred_at > 0) -- unix ms
) STRICT;

CREATE TABLE outbox_consumers (
  name       TEXT    PRIMARY KEY CHECK (length(name) BETWEEN 1 AND 64),
  cursor     INTEGER NOT NULL DEFAULT 0 CHECK (cursor >= 0),
  updated_at INTEGER NOT NULL
) STRICT;
```

`seq` is the table's `INTEGER PRIMARY KEY`, consistent with §7.1. It is the outbox cursor, and it is also the physical key.

### 6.2 Semantics

- `Emit` runs inside the caller's `WriteTx`, so the event commits or rolls back with the mutation. There is no publish-then-enqueue window.
- Ordering: seq order equals commit order. The writer serializes transactions, and `AUTOINCREMENT` allocates inside the transaction. Across processes, SQLite's write lock serializes them the same way. A consumer reading `seq > cursor` never misses an event that commits later.
- Gaps are allowed. Retention deletes create them, and rolled-back allocations may too. Consumers never assume contiguity. The invariant is strict increase and non-reuse (§10.3, I5).
- `Advance(consumer, from, to)`:
  ```sql
  INSERT INTO outbox_consumers(name, cursor, updated_at) VALUES (?1, ?2, ?3)
  ON CONFLICT(name) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at
  WHERE outbox_consumers.cursor = ?4;
  ```
  Zero rows affected → `ErrStale`. For a new consumer, `from` must be 0.
- Retention sweep (a periodic `Write`): `DELETE FROM outbox WHERE seq <= (SELECT min(cursor) FROM outbox_consumers) AND occurred_at < ?`. With no consumers, nothing is deleted. AUTOINCREMENT means deleted seq values are never reused.
- Consumers (webhook delivery, inbox projection, CI notifier) are separate modules. They read with `Read` and, in one `Write`, update their own tables and call `Advance`. Per-endpoint fan-out (webhook §6.4) happens in the webhook module's accessor inside the same `WriteTx`, not in the engine.

---

## 7. Identifiers and time

### 7.1 Identifier policy (one canonical rule)

- Canonical physical key: every table has `id INTEGER PRIMARY KEY` (a rowid alias). It is the only key used by foreign keys, FTS5 `content_rowid`, joins, and the outbox cursor. Nothing else is a primary key.
- Public handle: `public_id TEXT NOT NULL UNIQUE`, a 26-char Crockford-base32 ULID (48-bit ms plus 80-bit random). It is the only identifier that crosses the store boundary. Every public lookup resolves `public_id` to `id` through its UNIQUE index.
- The requirement offers "ULID text or integer rowid". This design picks the integer rowid as the key and keeps the ULID only as a public handle. A ULID is never a primary key.
- Why integer rowid as the key: `VACUUM` may change rowids of tables without `INTEGER PRIMARY KEY` (`lang_vacuum.html`). External-content FTS5 would then point at the wrong rows, silently. Integer keys also keep every index and FK 8 bytes instead of 26.
- Why a ULID handle: it is opaque, not enumerable, sorts by creation time, and matches the planning docs (`event_id`, `msg_<ULID>`) and Ruk's ID format.
- Generator (in-tree, no dependency):
  ```go
  // NewID returns a ULID for now. Monotonic within the process: same-ms IDs
  // increment the random part under a mutex. Uses crypto/rand.
  func NewID(now time.Time) ID
  ```
- Repos: `repos.ruk_repo_id TEXT NOT NULL UNIQUE` holds Ruk's ULID, so Ruk and rukie identify a repo the same way.

### 7.2 Time

- Storage: INTEGER Unix milliseconds, UTC, with `CHECK (x > 0)` on timestamp columns.
- Helpers:
  ```go
  func msOf(t time.Time) int64    // t.UnixMilli()
  func timeOf(ms int64) time.Time // time.UnixMilli(ms).UTC()
  ```
- Domain code truncates to milliseconds before comparing. Sub-millisecond precision is not stored.
- Never bind a `time.Time` as a SQL parameter. The bind helper takes int64 only. This avoids the `_time_format` monotonic-suffix trap (`research-go-stack.md`), and the DSN sets no time format.
- Edges: RFC 3339 nano UTC in the API. Webhook `timestamp` header is Unix seconds (`webhook-design.md`).
- Reconciliation: `TIMESTAMPTZ` (webhook §6.2) becomes INTEGER ms.

---

## 8. Full-text search (FTS5)

### 8.1 Table and triggers (external content, maintained in the same transaction)

```sql
CREATE VIRTUAL TABLE tasks_fts USING fts5(
  title, body,
  content='tasks', content_rowid='id',
  tokenize='trigram case_sensitive 0'  -- VERIFY option name and default (§12)
);

CREATE TRIGGER tasks_ai AFTER INSERT ON tasks BEGIN
  INSERT INTO tasks_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
CREATE TRIGGER tasks_ad AFTER DELETE ON tasks BEGIN
  INSERT INTO tasks_fts(tasks_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
END;
CREATE TRIGGER tasks_au AFTER UPDATE OF title, body ON tasks BEGIN
  INSERT INTO tasks_fts(tasks_fts, rowid, title, body) VALUES ('delete', old.id, old.title, old.body);
  INSERT INTO tasks_fts(rowid, title, body) VALUES (new.id, new.title, new.body);
END;
```

- Rowid stability: `tasks.id` is `INTEGER PRIMARY KEY` (§7.1), so `VACUUM` and `VACUUM INTO` cannot break the index.
- Admin repair and checks: `INSERT INTO tasks_fts(tasks_fts) VALUES('rebuild')` and `VALUES('integrity-check')`. All SQL uses single-quoted string literals (`_dqs=0`).
- Trigram needs terms of at least 3 characters. Matching is substring-based and works for CJK, with no tokenizer dependency.

### 8.2 Query construction

```go
// searchTerms splits on Unicode whitespace, drops empties, de-duplicates, caps at 8.
// Terms with >= 3 runes are FTS5 phrases: "term" with inner double quotes doubled.
// Terms with < 3 runes become instr() predicates (Korean two-syllable words land here).
func searchTerms(q string) (long []string, short []string, err error)

// buildSearchSQL returns SQL for one repo scope:
//   long terms present: WHERE tasks_fts MATCH ? AND t.repo_id = ? [AND instr(...)...]
//   only short terms:   WHERE t.repo_id = ? AND instr(...)...   (scan of one repo, bounded by LIMIT)
func buildSearchSQL(long, short []string) (sql string, args []any)
```

Long-term example:

```sql
SELECT t.id, t.public_id, t.repo_id, t.number, t.title, t.body, t.state, t.version,
       t.created_at, t.updated_at,
       snippet(tasks_fts, 1, '[', ']', '…', 12) AS snip,
       bm25(tasks_fts) AS rank
FROM tasks_fts
JOIN tasks t ON t.id = tasks_fts.rowid
WHERE tasks_fts MATCH ?1 AND t.repo_id = ?2
ORDER BY rank
LIMIT ?3;
```

- Short-term predicate: `instr(lower(t.title || ' ' || t.body), lower(?)) > 0`. SQLite's `lower()` is ASCII-only. Documented limitation.
- Empty query or more than 8 terms → `ErrInvalid`.
- The short-only path scans one repo's tasks. `[INF]` Acceptable for ≤10⁵ tasks per repo; revisit with a benchmark.

### 8.3 Startup probe (fail fast)

Runs on the writer connection before migrations, in the temp schema, so it never touches the database file. It exercises the same FTS5 commands the store uses on `tasks_fts`:

```sql
CREATE TABLE temp.fts_probe_src(id INTEGER PRIMARY KEY, body TEXT NOT NULL);
CREATE VIRTUAL TABLE temp.fts_probe USING fts5(
  body, content='fts_probe_src', content_rowid='id', tokenize='trigram');
INSERT INTO temp.fts_probe_src(id, body) VALUES (1, 'rukie probe');
INSERT INTO temp.fts_probe(rowid, body) VALUES (1, 'rukie probe');
INSERT INTO temp.fts_probe(temp.fts_probe) VALUES('rebuild');
INSERT INTO temp.fts_probe(temp.fts_probe) VALUES('optimize');
INSERT INTO temp.fts_probe(temp.fts_probe) VALUES('integrity-check');
SELECT count(*) FROM temp.fts_probe WHERE fts_probe MATCH 'probe';  -- must be 1
SELECT json_valid('{}');                                            -- must be 1
DROP TABLE temp.fts_probe;
DROP TABLE temp.fts_probe_src;
```

Any error, or a wrong result → `ErrFTS5Unavailable`, with the underlying error wrapped. A missing `fts5` module fails here, not on the first search. `[INF]` Table-qualified commands in `INSERT ... VALUES` are accepted by FTS5. `VERIFY`.

Defensive mode is not enabled. If it is enabled later, this same probe must pass under it, and the `rebuild`/`optimize`/`integrity-check` commands are the test (the driver doc says direct shadow-table writes fail under defensive mode, and says nothing about FTS5 commands).

---

## 9. Online backup

```go
// Backup: dst must not exist.
//   1. Create dst+".partial" with O_CREATE|O_EXCL, mode 0600 (empty file).
//   2. Open a dedicated sql.DB with the backup source DSN (mode=ro, no _query_only).
//      On its single conn (autocommit; no transaction): VACUUM INTO ?   (the .partial path).
//   3. fsync the .partial file.
//   4. Open .partial with a separate mode=ro sql.DB. PRAGMA quick_check = 'ok';
//      schema_migrations max version = embedded max; the §8.3 probe passes.
//   5. rename .partial -> dst; fsync the parent directory.
//   6. Return BackupInfo with the SHA-256 of the final file.
// On any failure after step 1, remove .partial.
```

- The backup uses its own connection, not a reader, because `query_only=1` on a reader might veto the destination write. `VERIFY` both: `VACUUM INTO` on the backup connection, and that a reader with `query_only` fails the same way (which is why the reader is not used).
- `VACUUM INTO` cannot run inside a transaction, so the backup connection runs it in autocommit. `lang_vacuum.html`.
- `VACUUM` (not `VACUUM INTO`) is a write operation; `VACUUM INTO` is a snapshot copy. `lang_vacuum.html`.
- Why `VACUUM INTO` and not modernc's backup API: the rationale does not depend on that API. `VACUUM INTO` yields a compact, consistent snapshot, purges deleted content, and needs no page-copy loop. Modernc's backup API hangs off its unexported connection type, not `database/sql`, so using it would mean `sql.Conn.Raw` access to internals. We do not do that.
- Long reads delay checkpoints. Schedule backups off-peak. The admin CLI prints WAL size before and after.
- Admin CLI: `rukie admin db backup --to PATH`. It opens a `Store` in-process. Cross-process writes are allowed (§3, SQLite locking).

---

## 10. Tests

### 10.1 Writer race and behavior (`-race`, real DB in `t.TempDir()`)

- `TestWriteRace_ManyWriters`: 32 goroutines × 200 writes. Mix: 80% success, 10% closure error, 5% panic, 5% pre-cancelled ctx. Assert each `Write` returns exactly once; row counts match outcomes; panics map to `ErrPanic`; goroutine count returns to baseline after `Close`.
- `TestWriteRace_CloseDuringTraffic`: continuous writers while `Close(ctx 5 s)` runs. Every `Write` returns. A nil reply implies the row exists. `ErrClosed` implies the row is absent. No hang (test timeout).
- `TestSubmitHonorsCtxDuringClose`: fill the queue with a blocking closure. Submit a second request with a 50 ms ctx while `Close` is waiting. The submit returns `context.DeadlineExceeded` within ~50 ms. `Close` returns within its ctx plus one batch. Regression test for the old `RWMutex` design.
- `TestCanceledBeforeDequeue`: a blocking closure holds the writer. Enqueue a cancelled request. Release. Assert its fn never ran and `Write` returned `context.Canceled`.
- `TestBatchSavepointIsolation`: `MaxBatch` 8, `MaxLinger` 50 ms; a barrier forces 8 requests into one batch. Request 3 fails. Assert the other 7 commit, request 3's rows are absent, and `Stats().Batches == 1`.
- `TestReadSnapshotIsolation`: a `Read` holds its snapshot while a `Write` commits. A second query in the same `fn` returns the pre-commit state.
- `TestNestedWriteDocumented`: documents the deadlock with a timeout guard. Asserts the test detects the hang within 2 s.

### 10.2 Batching policy (`testing/synctest`, no SQLite)

`collect` and `drainAndExit` are tested against channels and a fake clock inside `synctest.Test`:
- Isolated request (`lastBatch == 1`): commits without waiting.
- Burst (`lastBatch > 1`): requests at t = 0, 1 ms, 3 ms → batch of 2 with a 2 ms linger. `synctest.Wait` drives the timer.
- `maxBatch` reached before linger expires: immediate return.
- Closing: no linger; the drain loop exits only when `inflight == 0` and the queue is empty.

### 10.3 Crash safety (child process killed mid-batch)

`TestCrashMidBatch`:
- Parent re-executes the test binary with `-test.run=^TestCrashChild$`, `RUKIE_STORE_CRASH_CHILD=1`, and `RUKIE_STORE_CRASH_DB=<path>`.
- Child: opens the Store and runs 8 goroutines. Each request inserts one task and 3 outbox rows, all tagged with the request tag in the body. Some requests fail on purpose after inserting rows (savepoint path). After each `Write` returns, the child prints `ACK <tag>` or `NACK <tag>` and flushes.
- Parent: reads lines. After K ACKs (random K in [5, 200]), sends SIGKILL and waits.
- Parent reopens with `Open` and checks:
  - I1: `PRAGMA integrity_check` = `ok`.
  - I2: every ACK tag has its full row set. An acknowledged write survives SIGKILL.
  - I3: every NACK tag has zero rows.
  - I4: every tag has 0 rows or its full row set. No partial requests.
  - I5: committed outbox `seq` values are unique and strictly increasing in commit order. No gap assertions. Non-reuse: after reopen, one new `Emit` gets a seq greater than every committed seq, and `sqlite_sequence` for `outbox` is ≥ the maximum seq.
  - I6: `INSERT INTO tasks_fts(tasks_fts) VALUES('integrity-check')` succeeds.
  - I7: reopening performs a no-op migration pass, and the max version matches the embedded max.
- Iterations: 40 (`-short`: 8). Run under `-race` in CI.
- Limit: SIGKILL simulates an application crash, not power loss. Power-loss durability rests on `synchronous=FULL` and is not testable in-process.

### 10.4 Pragmas, locks, error codes, and migrations

- `TestConnPragmas`: opens R+1 concurrent readers and asserts `verifyConn` on each. Proves per-connection DSN application, including `_dqs=0` (a double-quoted identifier fails).
- `TestOFDLockingEnabled` (Linux): after `Open`, `OFDLockingEnabled()` is true where the kernel permits it.
- `TestErrorCodeConstants`: real UNIQUE, PRIMARY KEY, FOREIGN KEY, CHECK, and NOT NULL violations map to the derived constants in §1.4.
- `TestMigrationsContiguous`; `TestMigrationFilesHaveNoTxControl` (regex); `TestSchemaTooNew`; `TestChecksumChanged`.
- `TestMigrationRollback`: a file that fails halfway leaves no tables and no `schema_migrations` row.
- `TestForeignKeysOnOrdinaryMigration`: an ordinary migration that writes a dangling FK fails, because enforcement is still on.
- `TestRebuildMigrationFKCheck`: a rebuild migration that leaves a dangling FK fails at `foreign_key_check` and rolls back. A clean rebuild commits and restores `foreign_keys=ON`.

### 10.5 FTS5 and backup

- `TestFTSProbe`; `TestFTSProbeErrorPath` (bad tokenizer name → `ErrFTS5Unavailable`).
- `TestSearchLongAndShortTerms`: a 2-character Korean term, a 3-character English term, and a mixed query.
- `TestFTSSurvivesVacuumInto`: after `Backup`, search results in the backup equal the source's.
- `TestBackupVacuumIntoWithoutQueryOnly` and `TestBackupFromQueryOnlyReaderFails`: pin the §9 behavior under `query_only`.
- `TestBackupRefusesExisting`; `TestBackupCleansPartial` (forced failure after step 1).

### 10.6 Benchmarks (recorded in the PR; no gate yet)

- `BenchmarkWriteSerial` (one caller); `BenchmarkWriteGroupCommit` at 8 and 64 parallel callers; `BenchmarkReadTask`.
- Report commits per second and fsyncs per request. Group commit's effect shows here.
- Compare `tx.StmtContext` with conn-prepared statements against `tx.ExecContext` with the raw constants (§3.4 `VERIFY`).
- Measure `mmap_size` 0 vs 256 MiB, only after the libc check in §12.
- Profile with `runtime/pprof`; modernc frames appear as `lib.Xsqlite3_step` and `lib._balance_nonroot` (`doc.go`).

---

## 11. Reconciliation with planning docs

| Document | Says | This design |
|---|---|---|
| `inbox-design.md` | `id TEXT PRIMARY KEY` (ULID); `seq BIGINT UNIQUE` | `id INTEGER PRIMARY KEY` as the physical key; `public_id` ULID UNIQUE as the handle; `seq` is the outbox AUTOINCREMENT cursor. ULID is never a primary key. |
| `webhook-design.md` §6.2 | `TIMESTAMPTZ`; `id INTEGER PK`; `msg_id TEXT UNIQUE` | INTEGER ms; `msg_id` becomes `public_id`. |
| `webhook-design.md:116` | "identical logical schema on SQLite / PostgreSQL / Pebble" | Superseded. One engine: SQLite. |
| `research-go-stack.md` | "Keep the store behind a small interface so ncruces can replace it" | Superseded by the settled one-engine verdict. The closure API is the seam; there is no engine interface. |
| `research-go-stack.md` | In-tree migration runner, not goose | Adopted (§5). |
| `research-metadata-store.md` §3 | Single writer goroutine with batching | Adopted (§3). |

---

## 12. Risks and VERIFY list

Each item is checked by the named test in the first PR. The fallback applies if the check fails.

1. **FTS5 trigram option names and default.** `tokenize='trigram case_sensitive 0'`. Test: `TestFTSProbe` plus a case test. Fallback: `trigram` without options.
2. **`VACUUM INTO` on a connection with `query_only=1`, and on a `mode=ro` connection without it.** Test: `TestBackupVacuumIntoWithoutQueryOnly` and `TestBackupFromQueryOnlyReaderFails`. Fallback: the backup connection is already separate (§9); if `VACUUM INTO` needs write access, open the backup destination through a dedicated read-write connection with no writes to the source tables.
3. **Multi-statement `ExecContext` for migration files.** Test: `TestMigrationRollback`. Fallback: split on `;` outside string literals, or one `Exec` per statement.
4. **`Conn.Raw` returning `driver.ErrBadConn` discards the connection.** Test: `TestCommitFailureDiscardsConn` (fault-injection hook in `writer`). Fallback: close and reopen `wdb`.
5. **`PRAGMA application_id` inside a transaction.** Test: `TestMigrationRollback` checks the header after 0001. Fallback: set it before the first transaction.
6. **`Tx.StmtContext` reuse vs re-prepare.** Benchmark §10.6. Fallback: raw SQL constants through `tx.ExecContext`/`tx.QueryContext`.
7. **`mmap_size` under modernc, including libc mmap support.** Check the libc source for `mmap` before any non-zero size. Benchmark. Keep 0 unless both pass.
8. **`trusted_schema=OFF` with FTS5 triggers and `json_valid` CHECKs.** Test: `TestConnPragmas` plus the migration suite. Fallback: remove the pragma and document why.
9. **libc version.** `go.mod` requires `modernc.org/libc v1.77.1`. The CI check in §2.5 enforces it. Re-measure §10.6 on that version.
10. **`defer_foreign_keys` reset at COMMIT/ROLLBACK.** Test: `TestRebuildMigrationFKCheck`.
11. **Cross-process writers.** A separate process writing during a server burst relies on `busy_timeout=5000`. Test: a separate-process write during a server write burst.
12. **Nested `Write` deadlock.** No compile-time guard. Documented; tested by `TestNestedWriteDocumented`.

---

### Critical Files for Implementation
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/rukie-ruk-native-forge-plan.md` — settled SQLite-only decision, Go 1.27.0 / go1.27.2 pins, and the modernc v1.60.1 pin.
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/webhook-design.md` — §6.4 transactional enqueue (the reason for closure-based `Write`) and `Store.Webhooks()` at line 116.
- `/home/alpha/.omp/agent/sessions/-rewrite-git-renewed/2026-10-19T17-57-23-121Z_01a1554f-9c31-70c4-8dba-e5f309cf7513/local/research-metadata-store.md` — durability, pool, and single-writer evidence (§2, §3, §10).
- `https://gitlab.com/cznic/sqlite/-/raw/v1.60.1/driver.go` — DSN contract: shorthand keys, `_pragma`, `_txlock`, `_dqs`, `mode=ro`, apply order.
- `https://gitlab.com/cznic/sqlite/-/raw/v1.60.1/ofd.go` — exact `OFDLocking` and `OFDLockingEnabled` signatures, the process-wide ordering rule, and the `-shm` scope limit.

[advisor] <advisory advisor="glm-flash" severity="concern" guidance="weigh, don't blindly obey"> Fix `OutboxWriter.Advance`: the …