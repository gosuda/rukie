package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// MirrorRow is one configured push mirror with its runtime status. Time
// fields are Unix milliseconds; zero means unset.
type MirrorRow struct {
	PublicID                 string
	Name                     string
	TargetURL                string
	HostKind                 string
	Username                 string
	Mode                     string
	RefSelection             string
	Globs                    string
	State                    string
	LastErrorClass           string
	LastError                string
	ID                       int64
	RepoID                   int64
	DebounceSeconds          uint64
	MaxDelaySeconds          uint64
	ReconcileIntervalSeconds uint64
	ConsecutiveFailures      int64
	LastHTTPStatus           int64
	LastAttemptAt            int64
	LastSuccessAt            int64
	NextRunAt                int64
	DirtySince               int64
	LastManualAt             int64
	EventCursor              int64
	CreatedAt                int64
	UpdatedAt                int64
	IncludeNotes             bool
	ProtectDefaultBranch     bool
	Enabled                  bool
	SyncOnRefUpdate          bool
	CreateIfMissing          bool
	PolicyAck                bool
	AdoptExistingRefs        bool
	Adopted                  bool
}

// MirrorInsert carries one new mirror's configuration plus its initial state.
type MirrorInsert struct {
	Name                     string
	TargetURL                string
	HostKind                 string
	Username                 string
	Mode                     string
	RefSelection             string
	Globs                    string
	State                    string
	RepoID                   int64
	DebounceSeconds          uint64
	MaxDelaySeconds          uint64
	ReconcileIntervalSeconds uint64
	IncludeNotes             bool
	ProtectDefaultBranch     bool
	Enabled                  bool
	SyncOnRefUpdate          bool
	CreateIfMissing          bool
	PolicyAck                bool
	AdoptExistingRefs        bool
}

// MirrorPatch carries a partial mirror update. A nil pointer leaves the
// column unchanged; a non-nil pointer replaces it, including zero values.
type MirrorPatch struct {
	Name                     *string
	TargetURL                *string
	HostKind                 *string
	Username                 *string
	Mode                     *string
	RefSelection             *string
	Globs                    *string
	IncludeNotes             *bool
	ProtectDefaultBranch     *bool
	Enabled                  *bool
	SyncOnRefUpdate          *bool
	CreateIfMissing          *bool
	PolicyAck                *bool
	AdoptExistingRefs        *bool
	DebounceSeconds          *uint64
	MaxDelaySeconds          *uint64
	ReconcileIntervalSeconds *uint64
}

// CredentialRow is one sealed mirror credential. Nonce and Ciphertext hold
// the AES-GCM seal output; Last4 is the plaintext token suffix.
type CredentialRow struct {
	Last4      string
	Nonce      []byte
	Ciphertext []byte
	MirrorID   int64
	KeyVersion int64
	CreatedAt  int64
	RotatedAt  int64
	LastUsedAt int64
}

// MirrorRunRow is one mirror sync run.
type MirrorRunRow struct {
	PublicID        string
	Kind            string
	State           string
	Trigger         string
	PlanDigest      string
	ConfirmedDigest string
	RukPlan         string
	OutcomeClass    string
	OutcomeSummary  string
	Counts          string
	ID              int64
	MirrorID        int64
	AttemptNo       int64
	StartedAt       int64
	FinishedAt      int64
	CreatedAt       int64
	UpdatedAt       int64
	DryRun          bool
}

// RefStateRow is one mirror ref's last observed local and remote state.
type RefStateRow struct {
	Ref                 string
	LocalOid            string
	RemoteOid           string
	Status              string
	Reason              string
	ReasonClass         string
	MirrorID            int64
	FirstFailedAt       int64
	LastAttemptAt       int64
	LastSuccessAt       int64
	ConsecutiveFailures int64
}

// AttemptRow is one daemon attempt of one run.
type AttemptRow struct {
	RukPlan    string
	Outcome    string
	Summary    string
	ID         int64
	RunID      int64
	AttemptNo  int64
	StartedAt  int64
	FinishedAt int64
}

// MirrorResult is a worker status update for one mirror. ClearDirty clears
// dirty_since; Adopted records a completed adoption.
type MirrorResult struct {
	State               string
	LastErrorClass      string
	LastError           string
	ConsecutiveFailures int64
	LastHTTPStatus      int64
	LastAttemptAt       int64
	LastSuccessAt       int64
	NextRunAt           int64
	EventCursor         int64
	Adopted             bool
	ClearDirty          bool
}

const sqlMirrorColumns = `id, public_id, repo_id, name, target_url, host_kind, username, mode,
ref_selection, globs_json, include_notes, protect_default_branch, enabled, sync_on_ref_update,
create_if_missing, policy_ack, adopt_existing_refs, adopted, debounce_seconds, max_delay_seconds,
reconcile_interval_seconds, state, consecutive_failures, last_error_class, last_error,
last_http_status, last_attempt_at, last_success_at, next_run_at, dirty_since, last_manual_at,
event_cursor, created_at, updated_at`

const sqlRunColumns = `id, public_id, mirror_id, kind, state, trigger, dry_run, plan_digest,
confirmed_digest, attempt_no, ruk_plan, outcome_class, outcome_summary, counts_json,
started_at, finished_at, created_at, updated_at`

const sqlRefStateColumns = `mirror_id, ref, local_oid, remote_oid, status, reason, reason_class,
first_failed_at, last_attempt_at, last_success_at, consecutive_failures`

const sqlAttemptColumns = `id, run_id, attempt_no, ruk_plan, started_at, finished_at, outcome, summary`

const sqlSealColumns = `mirror_id, key_version, nonce, ciphertext, last4, created_at, rotated_at, last_used_at`

func scanMirrorRow(scan func(dest ...any) error) (MirrorRow, error) {
	var row MirrorRow
	var includeNotes, protectDefault, enabled, syncOnUpdate, createIfMissing, policyAck,
		adoptExisting, adopted int64
	var debounceSeconds, maxDelaySeconds, reconcileIntervalSeconds int64
	err := scan(
		&row.ID, &row.PublicID, &row.RepoID, &row.Name, &row.TargetURL, &row.HostKind,
		&row.Username, &row.Mode, &row.RefSelection, &row.Globs,
		&includeNotes, &protectDefault, &enabled, &syncOnUpdate, &createIfMissing,
		&policyAck, &adoptExisting, &adopted, &debounceSeconds, &maxDelaySeconds, &reconcileIntervalSeconds,
		&row.State, &row.ConsecutiveFailures, &row.LastErrorClass, &row.LastError,
		&row.LastHTTPStatus, &row.LastAttemptAt, &row.LastSuccessAt, &row.NextRunAt,
		&row.DirtySince, &row.LastManualAt, &row.EventCursor, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		return MirrorRow{}, err
	}
	row.IncludeNotes = includeNotes == 1
	row.ProtectDefaultBranch = protectDefault == 1
	row.Enabled = enabled == 1
	row.SyncOnRefUpdate = syncOnUpdate == 1
	row.CreateIfMissing = createIfMissing == 1
	row.PolicyAck = policyAck == 1
	row.AdoptExistingRefs = adoptExisting == 1
	row.Adopted = adopted == 1
	debounce, derr := u64fromStored(debounceSeconds, "debounce_seconds")
	if derr != nil {
		return MirrorRow{}, derr
	}
	maxDelay, derr := u64fromStored(maxDelaySeconds, "max_delay_seconds")
	if derr != nil {
		return MirrorRow{}, derr
	}
	reconcile, derr := u64fromStored(reconcileIntervalSeconds, "reconcile_interval_seconds")
	if derr != nil {
		return MirrorRow{}, derr
	}
	row.DebounceSeconds = debounce
	row.MaxDelaySeconds = maxDelay
	row.ReconcileIntervalSeconds = reconcile
	return row, nil
}

func u64fromStored(v int64, what string) (uint64, error) {
	if v < 0 {
		return 0, fmt.Errorf("mirror row: %w: stored %s is negative", ErrInvalid, what)
	}
	return uint64(v), nil
}

func i64fromConfig(v uint64, what string) (int64, error) {
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("mirror config: %w: %s exceeds int64", ErrInvalid, what)
	}
	return int64(v), nil
}

func scanRunRow(scan func(dest ...any) error) (MirrorRunRow, error) {
	var row MirrorRunRow
	var dryRun int64
	err := scan(
		&row.ID, &row.PublicID, &row.MirrorID, &row.Kind, &row.State, &row.Trigger,
		&dryRun, &row.PlanDigest, &row.ConfirmedDigest, &row.AttemptNo, &row.RukPlan,
		&row.OutcomeClass, &row.OutcomeSummary, &row.Counts,
		&row.StartedAt, &row.FinishedAt, &row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		return MirrorRunRow{}, err
	}
	row.DryRun = dryRun == 1
	return row, nil
}

func scanRefStateRow(scan func(dest ...any) error) (RefStateRow, error) {
	var row RefStateRow
	err := scan(
		&row.MirrorID, &row.Ref, &row.LocalOid, &row.RemoteOid, &row.Status,
		&row.Reason, &row.ReasonClass, &row.FirstFailedAt, &row.LastAttemptAt,
		&row.LastSuccessAt, &row.ConsecutiveFailures,
	)
	if err != nil {
		return RefStateRow{}, err
	}
	return row, nil
}

func scanAttemptRow(scan func(dest ...any) error) (AttemptRow, error) {
	var row AttemptRow
	err := scan(
		&row.ID, &row.RunID, &row.AttemptNo, &row.RukPlan,
		&row.StartedAt, &row.FinishedAt, &row.Outcome, &row.Summary,
	)
	if err != nil {
		return AttemptRow{}, err
	}
	return row, nil
}

func scanCredentialRow(scan func(dest ...any) error) (CredentialRow, error) {
	var row CredentialRow
	err := scan(
		&row.MirrorID, &row.KeyVersion, &row.Nonce, &row.Ciphertext, &row.Last4,
		&row.CreatedAt, &row.RotatedAt, &row.LastUsedAt,
	)
	if err != nil {
		return CredentialRow{}, err
	}
	return row, nil
}

func clampMirrorLimit(limit int) int {
	if limit <= 0 {
		return 30
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func encodeMirrorCursor(createdAt, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%020d\x1f%d", createdAt, id)))
}

func parseMirrorCursor(raw string) (int64, int64, error) {
	decoded, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil {
		return 0, 0, fmt.Errorf("cursor: %w: malformed mirror cursor", ErrInvalid)
	}
	value, idRaw, ok := strings.Cut(string(decoded), "\x1f")
	if !ok {
		return 0, 0, fmt.Errorf("cursor: %w: malformed mirror cursor", ErrInvalid)
	}
	var created, id int64
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, 0, fmt.Errorf("cursor: %w: malformed mirror cursor", ErrInvalid)
		}
		created = created*10 + int64(c-'0')
	}
	for _, c := range idRaw {
		if c < '0' || c > '9' {
			return 0, 0, fmt.Errorf("cursor: %w: malformed mirror cursor", ErrInvalid)
		}
		id = id*10 + int64(c-'0')
	}
	return created, id, nil
}

func encodeRefCursor(ref string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(ref))
}

func parseRefCursor(raw string) (string, error) {
	decoded, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil {
		return "", fmt.Errorf("cursor: %w: malformed mirror cursor", ErrInvalid)
	}
	return string(decoded), nil
}

func mirrorNameConflict(op string, err error, name string) error {
	err = mapErr(err)
	if errors.Is(err, ErrConflict) {
		return fmt.Errorf("%s: %w: duplicate mirror name %q", op, ErrInvalid, name)
	}
	return fmt.Errorf("%s: %w", op, err)
}

// InsertMirror creates one mirror and returns its stored row.
func (tx *WriteTx) InsertMirror(now time.Time, in MirrorInsert) (MirrorRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRow{}, err
	}
	if in.RepoID <= 0 {
		return MirrorRow{}, fmt.Errorf("InsertMirror: %w: repo id must be positive", ErrInvalid)
	}
	if in.Name == "" {
		return MirrorRow{}, fmt.Errorf("InsertMirror: %w: name must not be empty", ErrInvalid)
	}
	globs := in.Globs
	if globs == "" {
		globs = "[]"
	}
	state := in.State
	if state == "" {
		state = "idle"
	}
	debounce, derr := i64fromConfig(in.DebounceSeconds, "debounce_seconds")
	if derr != nil {
		return MirrorRow{}, fmt.Errorf("InsertMirror: %w", derr)
	}
	maxDelay, derr := i64fromConfig(in.MaxDelaySeconds, "max_delay_seconds")
	if derr != nil {
		return MirrorRow{}, fmt.Errorf("InsertMirror: %w", derr)
	}
	reconcile, derr := i64fromConfig(in.ReconcileIntervalSeconds, "reconcile_interval_seconds")
	if derr != nil {
		return MirrorRow{}, fmt.Errorf("InsertMirror: %w", derr)
	}
	at := msOf(now)
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO mirrors
(public_id, repo_id, name, target_url, host_kind, username, mode, ref_selection, globs_json,
include_notes, protect_default_branch, enabled, sync_on_ref_update, create_if_missing,
policy_ack, adopt_existing_refs, adopted, debounce_seconds, max_delay_seconds,
reconcile_interval_seconds, state, consecutive_failures, last_error_class, last_error,
last_http_status, last_attempt_at, last_success_at, next_run_at, dirty_since, last_manual_at,
event_cursor, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?, ?, 0, '', '', 0, 0, 0, 0, 0, 0, 0, ?, ?)`,
		string(NewID(now)), in.RepoID, in.Name, in.TargetURL, in.HostKind, in.Username,
		in.Mode, in.RefSelection, globs,
		boolInt(in.IncludeNotes), boolInt(in.ProtectDefaultBranch), boolInt(in.Enabled),
		boolInt(in.SyncOnRefUpdate), boolInt(in.CreateIfMissing), boolInt(in.PolicyAck),
		boolInt(in.AdoptExistingRefs), debounce, maxDelay,
		reconcile, state, at, at)
	if ierr != nil {
		return MirrorRow{}, mirrorNameConflict("InsertMirror", ierr, in.Name)
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return MirrorRow{}, fmt.Errorf("InsertMirror: %w", ierr)
	}
	return tx.getMirrorForUpdate(id)
}

func (tx *WriteTx) getMirrorForUpdate(id int64) (MirrorRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRow{}, err
	}
	row, serr := scanMirrorRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlMirrorColumns+` FROM mirrors WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return MirrorRow{}, fmt.Errorf("getMirrorForUpdate: %w: mirror %d", ErrNotFound, id)
	}
	return row, serr
}

// UpdateMirror applies a partial update to one mirror and returns its row.
func (tx *WriteTx) UpdateMirror(now time.Time, id int64, p MirrorPatch) (MirrorRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRow{}, err
	}
	if p.Name != nil && *p.Name == "" {
		return MirrorRow{}, fmt.Errorf("UpdateMirror: %w: name must not be empty", ErrInvalid)
	}
	query := `UPDATE mirrors SET updated_at = ?`
	args := []any{msOf(now)}
	if p.Name != nil {
		query += `, name = ?`
		args = append(args, *p.Name)
	}
	if p.TargetURL != nil {
		query += `, target_url = ?`
		args = append(args, *p.TargetURL)
	}
	if p.HostKind != nil {
		query += `, host_kind = ?`
		args = append(args, *p.HostKind)
	}
	if p.Username != nil {
		query += `, username = ?`
		args = append(args, *p.Username)
	}
	if p.Mode != nil {
		query += `, mode = ?`
		args = append(args, *p.Mode)
	}
	if p.RefSelection != nil {
		query += `, ref_selection = ?`
		args = append(args, *p.RefSelection)
	}
	if p.Globs != nil {
		query += `, globs_json = ?`
		args = append(args, *p.Globs)
	}
	if p.IncludeNotes != nil {
		query += `, include_notes = ?`
		args = append(args, boolInt(*p.IncludeNotes))
	}
	if p.ProtectDefaultBranch != nil {
		query += `, protect_default_branch = ?`
		args = append(args, boolInt(*p.ProtectDefaultBranch))
	}
	if p.Enabled != nil {
		query += `, enabled = ?`
		args = append(args, boolInt(*p.Enabled))
	}
	if p.SyncOnRefUpdate != nil {
		query += `, sync_on_ref_update = ?`
		args = append(args, boolInt(*p.SyncOnRefUpdate))
	}
	if p.CreateIfMissing != nil {
		query += `, create_if_missing = ?`
		args = append(args, boolInt(*p.CreateIfMissing))
	}
	if p.PolicyAck != nil {
		query += `, policy_ack = ?`
		args = append(args, boolInt(*p.PolicyAck))
	}
	if p.AdoptExistingRefs != nil {
		query += `, adopt_existing_refs = ?`
		args = append(args, boolInt(*p.AdoptExistingRefs))
	}
	if p.DebounceSeconds != nil {
		debounce, derr := i64fromConfig(*p.DebounceSeconds, "debounce_seconds")
		if derr != nil {
			return MirrorRow{}, fmt.Errorf("UpdateMirror: %w", derr)
		}
		query += `, debounce_seconds = ?`
		args = append(args, debounce)
	}
	if p.MaxDelaySeconds != nil {
		maxDelay, derr := i64fromConfig(*p.MaxDelaySeconds, "max_delay_seconds")
		if derr != nil {
			return MirrorRow{}, fmt.Errorf("UpdateMirror: %w", derr)
		}
		query += `, max_delay_seconds = ?`
		args = append(args, maxDelay)
	}
	if p.ReconcileIntervalSeconds != nil {
		reconcile, derr := i64fromConfig(*p.ReconcileIntervalSeconds, "reconcile_interval_seconds")
		if derr != nil {
			return MirrorRow{}, fmt.Errorf("UpdateMirror: %w", derr)
		}
		query += `, reconcile_interval_seconds = ?`
		args = append(args, reconcile)
	}
	query += ` WHERE id = ?`
	args = append(args, id)
	moved, merr := execAffected(tx.ctx, inner, query, args...)
	if merr != nil {
		name := ""
		if p.Name != nil {
			name = *p.Name
		}
		return MirrorRow{}, mirrorNameConflict("UpdateMirror", merr, name)
	}
	if moved == 0 {
		return MirrorRow{}, fmt.Errorf("UpdateMirror: %w: mirror %d", ErrNotFound, id)
	}
	return tx.getMirrorForUpdate(id)
}

// DeleteMirror removes one mirror. Credentials, runs, attempts, and ref
// state cascade; an in-flight run finishes on its own.
func (tx *WriteTx) DeleteMirror(id int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `DELETE FROM mirrors WHERE id = ?`, id)
	if merr != nil {
		return fmt.Errorf("DeleteMirror: %w", mapErr(merr))
	}
	if moved == 0 {
		return fmt.Errorf("DeleteMirror: %w: mirror %d", ErrNotFound, id)
	}
	return nil
}

// TouchMirrorDirty marks every eligible mirror of one repo dirty and returns
// the touched count. Only enabled mirrors with sync_on_ref_update set and no
// pending dirty marker are touched.
func (tx *WriteTx) TouchMirrorDirty(now time.Time, repoID int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	at := msOf(now)
	moved, merr := execAffected(tx.ctx, inner, `UPDATE mirrors
SET dirty_since = ?, updated_at = ?
WHERE repo_id = ? AND enabled = 1 AND sync_on_ref_update = 1 AND dirty_since = 0`,
		at, at, repoID)
	if merr != nil {
		return 0, fmt.Errorf("TouchMirrorDirty: %w", mapErr(merr))
	}
	return moved, nil
}

// SetMirrorResult records one worker status update for one mirror.
func (tx *WriteTx) SetMirrorResult(now time.Time, id int64, r MirrorResult) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	query := `UPDATE mirrors SET state = ?, consecutive_failures = ?, last_error_class = ?,
last_error = ?, last_http_status = ?, last_attempt_at = ?, last_success_at = ?,
next_run_at = ?, event_cursor = ?, adopted = ?`
	args := []any{r.State, r.ConsecutiveFailures, r.LastErrorClass, r.LastError,
		r.LastHTTPStatus, r.LastAttemptAt, r.LastSuccessAt, r.NextRunAt,
		r.EventCursor, boolInt(r.Adopted)}
	if r.ClearDirty {
		query += `, dirty_since = 0`
	}
	query += `, updated_at = ? WHERE id = ?`
	args = append(args, msOf(now), id)
	moved, merr := execAffected(tx.ctx, inner, query, args...)
	if merr != nil {
		return fmt.Errorf("SetMirrorResult: %w", mapErr(merr))
	}
	if moved == 0 {
		return fmt.Errorf("SetMirrorResult: %w: mirror %d", ErrNotFound, id)
	}
	return nil
}

// SetMirrorManual records one manual sync start for throttle checks.
func (tx *WriteTx) SetMirrorManual(now time.Time, id int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE mirrors
SET last_manual_at = ?, updated_at = ? WHERE id = ?`,
		msOf(now), msOf(now), id)
	if merr != nil {
		return fmt.Errorf("SetMirrorManual: %w", mapErr(merr))
	}
	if moved == 0 {
		return fmt.Errorf("SetMirrorManual: %w: mirror %d", ErrNotFound, id)
	}
	return nil
}

// PutCredential stores one sealed credential, replacing any prior seal for
// the mirror while keeping the original created_at. Callers pass the stored
// LastUsedAt through on rotation.
func (tx *WriteTx) PutCredential(c CredentialRow) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	if c.MirrorID <= 0 {
		return fmt.Errorf("PutCredential: %w: mirror id must be positive", ErrInvalid)
	}
	_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO mirror_credentials
(mirror_id, key_version, nonce, ciphertext, last4, created_at, rotated_at, last_used_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (mirror_id) DO UPDATE SET
key_version = excluded.key_version, nonce = excluded.nonce,
ciphertext = excluded.ciphertext, last4 = excluded.last4,
rotated_at = excluded.rotated_at, last_used_at = excluded.last_used_at`,
		c.MirrorID, c.KeyVersion, c.Nonce, c.Ciphertext, c.Last4,
		c.CreatedAt, c.RotatedAt, c.LastUsedAt)
	if ierr != nil {
		return fmt.Errorf("PutCredential: %w", mapErr(ierr))
	}
	return nil
}

// InsertRun records one mirror sync run, minting a public id when empty.
func (tx *WriteTx) InsertRun(now time.Time, r MirrorRunRow) (MirrorRunRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRunRow{}, err
	}
	if r.MirrorID <= 0 {
		return MirrorRunRow{}, fmt.Errorf("InsertRun: %w: mirror id must be positive", ErrInvalid)
	}
	if r.PublicID == "" {
		r.PublicID = string(NewID(now))
	}
	counts := r.Counts
	if counts == "" {
		counts = "{}"
	}
	at := msOf(now)
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO mirror_runs
(public_id, mirror_id, kind, state, trigger, dry_run, plan_digest, confirmed_digest,
attempt_no, ruk_plan, outcome_class, outcome_summary, counts_json,
started_at, finished_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.PublicID, r.MirrorID, r.Kind, r.State, r.Trigger, boolInt(r.DryRun),
		r.PlanDigest, r.ConfirmedDigest, r.AttemptNo, r.RukPlan,
		r.OutcomeClass, r.OutcomeSummary, counts,
		r.StartedAt, r.FinishedAt, at, at)
	if ierr != nil {
		return MirrorRunRow{}, fmt.Errorf("InsertRun: %w", mapErr(ierr))
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return MirrorRunRow{}, fmt.Errorf("InsertRun: %w", ierr)
	}
	return tx.getRunForUpdate(id)
}

func (tx *WriteTx) getRunForUpdate(id int64) (MirrorRunRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRunRow{}, err
	}
	row, serr := scanRunRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlRunColumns+` FROM mirror_runs WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return MirrorRunRow{}, fmt.Errorf("getRunForUpdate: %w: run %d", ErrNotFound, id)
	}
	return row, serr
}

// UpdateRun replaces one run's mutable columns.
func (tx *WriteTx) UpdateRun(now time.Time, id int64, r MirrorRunRow) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE mirror_runs SET
kind = ?, state = ?, trigger = ?, dry_run = ?, plan_digest = ?, confirmed_digest = ?,
attempt_no = ?, ruk_plan = ?, outcome_class = ?, outcome_summary = ?, counts_json = ?,
started_at = ?, finished_at = ?, updated_at = ? WHERE id = ?`,
		r.Kind, r.State, r.Trigger, boolInt(r.DryRun), r.PlanDigest, r.ConfirmedDigest,
		r.AttemptNo, r.RukPlan, r.OutcomeClass, r.OutcomeSummary, r.Counts,
		r.StartedAt, r.FinishedAt, msOf(now), id)
	if merr != nil {
		return fmt.Errorf("UpdateRun: %w", mapErr(merr))
	}
	if moved == 0 {
		return fmt.Errorf("UpdateRun: %w: run %d", ErrNotFound, id)
	}
	return nil
}

// InsertAttempt records one daemon attempt of one run.
func (tx *WriteTx) InsertAttempt(a AttemptRow) (AttemptRow, error) {
	inner, err := tx.live()
	if err != nil {
		return AttemptRow{}, err
	}
	if a.RunID <= 0 {
		return AttemptRow{}, fmt.Errorf("InsertAttempt: %w: run id must be positive", ErrInvalid)
	}
	res, ierr := inner.ExecContext(tx.ctx, `INSERT INTO mirror_attempts
(run_id, attempt_no, ruk_plan, started_at, finished_at, outcome, summary)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		a.RunID, a.AttemptNo, a.RukPlan, a.StartedAt, a.FinishedAt, a.Outcome, a.Summary)
	if ierr != nil {
		return AttemptRow{}, fmt.Errorf("InsertAttempt: %w", mapErr(ierr))
	}
	id, ierr := res.LastInsertId()
	if ierr != nil {
		return AttemptRow{}, fmt.Errorf("InsertAttempt: %w", ierr)
	}
	row, serr := scanAttemptRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlAttemptColumns+` FROM mirror_attempts WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return AttemptRow{}, fmt.Errorf("InsertAttempt: %w: attempt %d", ErrNotFound, id)
	}
	return row, serr
}

// UpdateAttempt records one attempt's completion.
func (tx *WriteTx) UpdateAttempt(id int64, finishedAt int64, outcome, summary string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE mirror_attempts
SET finished_at = ?, outcome = ?, summary = ? WHERE id = ?`,
		finishedAt, outcome, summary, id)
	if merr != nil {
		return fmt.Errorf("UpdateAttempt: %w", mapErr(merr))
	}
	if moved == 0 {
		return fmt.Errorf("UpdateAttempt: %w: attempt %d", ErrNotFound, id)
	}
	return nil
}

// ReplaceRefStates swaps one mirror's ref state to exactly the given rows.
func (tx *WriteTx) ReplaceRefStates(mirrorID int64, rows []RefStateRow) error {
	if _, gerr := tx.getMirrorForUpdate(mirrorID); gerr != nil {
		return gerr
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	if _, derr := inner.ExecContext(tx.ctx,
		`DELETE FROM mirror_refstate WHERE mirror_id = ?`, mirrorID); derr != nil {
		return fmt.Errorf("ReplaceRefStates: %w", mapErr(derr))
	}
	for _, row := range rows {
		if row.Ref == "" {
			return fmt.Errorf("ReplaceRefStates: %w: ref must not be empty", ErrInvalid)
		}
		_, ierr := inner.ExecContext(tx.ctx, `INSERT INTO mirror_refstate
(mirror_id, ref, local_oid, remote_oid, status, reason, reason_class,
first_failed_at, last_attempt_at, last_success_at, consecutive_failures)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			mirrorID, row.Ref, row.LocalOid, row.RemoteOid, row.Status,
			row.Reason, row.ReasonClass, row.FirstFailedAt, row.LastAttemptAt,
			row.LastSuccessAt, row.ConsecutiveFailures)
		if ierr != nil {
			return fmt.Errorf("ReplaceRefStates: %w", mapErr(ierr))
		}
	}
	return nil
}

// GetMirror loads one mirror by id for reads.
func (tx *ReadTx) GetMirror(id int64) (MirrorRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRow{}, err
	}
	row, serr := scanMirrorRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlMirrorColumns+` FROM mirrors WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return MirrorRow{}, fmt.Errorf("GetMirror: %w: mirror %d", ErrNotFound, id)
	}
	return row, serr
}

// GetMirrorByPublicID loads one mirror by public id for reads.
func (tx *ReadTx) GetMirrorByPublicID(pub string) (MirrorRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRow{}, err
	}
	row, serr := scanMirrorRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlMirrorColumns+` FROM mirrors WHERE public_id = ?`, pub).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return MirrorRow{}, fmt.Errorf("GetMirrorByPublicID: %w: %s", ErrNotFound, pub)
	}
	return row, serr
}

// GetMirrorByRepoName loads one repo mirror by name for reads.
func (tx *ReadTx) GetMirrorByRepoName(repoID int64, name string) (MirrorRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRow{}, err
	}
	row, serr := scanMirrorRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlMirrorColumns+` FROM mirrors WHERE repo_id = ? AND name = ?`, repoID, name).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return MirrorRow{}, fmt.Errorf("GetMirrorByRepoName: %w: %s", ErrNotFound, name)
	}
	return row, serr
}

// ListMirrors lists one repo's mirrors, oldest first, with a next cursor.
func (tx *ReadTx) ListMirrors(repoID int64, after string, limit int) ([]MirrorRow, string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, "", err
	}
	limit = clampMirrorLimit(limit)
	query := `SELECT ` + sqlMirrorColumns + ` FROM mirrors WHERE repo_id = ?`
	args := []any{repoID}
	if after != "" {
		created, id, perr := parseMirrorCursor(after)
		if perr != nil {
			return nil, "", perr
		}
		query += ` AND (created_at, id) > (?, ?)`
		args = append(args, created, id)
	}
	query += ` ORDER BY created_at, id LIMIT ?`
	args = append(args, limit+1)
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, "", qerr
	}
	defer func() { _ = rows.Close() }()
	var out []MirrorRow
	for rows.Next() {
		row, serr := scanMirrorRow(rows.Scan)
		if serr != nil {
			return nil, "", serr
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, "", rerr
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeMirrorCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// DueMirrors lists enabled mirrors needing a run: a dirty marker older than
// its per-mirror debounce, or a reached next_run_at. Zero next_run_at means
// unscheduled and never due.
func (tx *ReadTx) DueMirrors(nowMs int64, limit int) ([]MirrorRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	limit = clampMirrorLimit(limit)
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlMirrorColumns+` FROM mirrors
WHERE enabled = 1 AND ((dirty_since <> 0 AND dirty_since + debounce_seconds * 1000 <= ?) OR (next_run_at <> 0 AND next_run_at <= ?))
ORDER BY next_run_at, id LIMIT ?`, nowMs, nowMs, limit)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []MirrorRow
	for rows.Next() {
		row, serr := scanMirrorRow(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// GetCredential loads one mirror's sealed credential for reads.
func (tx *ReadTx) GetCredential(mirrorID int64) (CredentialRow, error) {
	inner, err := tx.live()
	if err != nil {
		return CredentialRow{}, err
	}
	row, serr := scanCredentialRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlSealColumns+` FROM mirror_credentials WHERE mirror_id = ?`, mirrorID).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return CredentialRow{}, fmt.Errorf("GetCredential: %w: mirror %d", ErrNotFound, mirrorID)
	}
	return row, serr
}

// GetRunByPublicID loads one run by public id for reads.
func (tx *ReadTx) GetRunByPublicID(pub string) (MirrorRunRow, error) {
	inner, err := tx.live()
	if err != nil {
		return MirrorRunRow{}, err
	}
	row, serr := scanRunRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlRunColumns+` FROM mirror_runs WHERE public_id = ?`, pub).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return MirrorRunRow{}, fmt.Errorf("GetRunByPublicID: %w: %s", ErrNotFound, pub)
	}
	return row, serr
}

// ListRuns lists one mirror's runs, oldest first, with a next cursor. A
// non-empty state filters to that state.
func (tx *ReadTx) ListRuns(mirrorID int64, state, after string, limit int) ([]MirrorRunRow, string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, "", err
	}
	limit = clampMirrorLimit(limit)
	query := `SELECT ` + sqlRunColumns + ` FROM mirror_runs WHERE mirror_id = ?`
	args := []any{mirrorID}
	if state != "" {
		query += ` AND state = ?`
		args = append(args, state)
	}
	if after != "" {
		created, id, perr := parseMirrorCursor(after)
		if perr != nil {
			return nil, "", perr
		}
		query += ` AND (created_at, id) > (?, ?)`
		args = append(args, created, id)
	}
	query += ` ORDER BY created_at, id LIMIT ?`
	args = append(args, limit+1)
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, "", qerr
	}
	defer func() { _ = rows.Close() }()
	var out []MirrorRunRow
	for rows.Next() {
		row, serr := scanRunRow(rows.Scan)
		if serr != nil {
			return nil, "", serr
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, "", rerr
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeMirrorCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// ListRefStates lists one mirror's ref state by ref, with a next cursor.
func (tx *ReadTx) ListRefStates(mirrorID int64, after string, limit int) ([]RefStateRow, string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, "", err
	}
	limit = clampMirrorLimit(limit)
	query := `SELECT ` + sqlRefStateColumns + ` FROM mirror_refstate WHERE mirror_id = ?`
	args := []any{mirrorID}
	if after != "" {
		ref, perr := parseRefCursor(after)
		if perr != nil {
			return nil, "", perr
		}
		query += ` AND ref > ?`
		args = append(args, ref)
	}
	query += ` ORDER BY ref LIMIT ?`
	args = append(args, limit+1)
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, "", qerr
	}
	defer func() { _ = rows.Close() }()
	var out []RefStateRow
	for rows.Next() {
		row, serr := scanRefStateRow(rows.Scan)
		if serr != nil {
			return nil, "", serr
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, "", rerr
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = encodeRefCursor(out[len(out)-1].Ref)
	}
	return out, next, nil
}

// CountMirrors counts one repo's mirrors.
func (tx *ReadTx) CountMirrors(repoID int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	var n int64
	if serr := inner.QueryRowContext(tx.ctx,
		`SELECT count(*) FROM mirrors WHERE repo_id = ?`, repoID).Scan(&n); serr != nil {
		return 0, serr
	}
	return n, nil
}
