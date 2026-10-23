package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"time"

	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
)

const (
	jobMaxIDLen   = 128
	jobMaxKindLen = 64
)

const jobMaxAttempts = 10

const (
	sqlJobInsert = `INSERT INTO jobs (id, kind, repo_id, payload, state, attempts, lease_owner, lease_expires_at, created_at, updated_at)
VALUES (?1, ?2, ?3, ?4, 'queued', 0, '', 0, ?5, ?5) ON CONFLICT (id) DO NOTHING`

	sqlJobClaim = `UPDATE jobs SET state = 'leased', lease_owner = ?1, lease_token = ?6, lease_expires_at = ?2, updated_at = ?3
WHERE id IN (SELECT id FROM jobs
WHERE (state = 'queued' OR (state = 'leased' AND lease_expires_at <= ?4)) AND run_after <= ?4
ORDER BY created_at, id LIMIT ?5)
RETURNING id, kind, repo_id, payload, state, attempts, lease_owner, lease_token, lease_expires_at, run_after, created_at, updated_at`

	sqlJobGet = `SELECT id, kind, repo_id, payload, state, attempts, lease_owner, lease_token, lease_expires_at, run_after, created_at, updated_at
FROM jobs WHERE id = ?1`

	sqlJobDone = `UPDATE jobs SET state = 'done', lease_owner = '', lease_token = '', lease_expires_at = 0, updated_at = ?1
WHERE id = ?2 AND state = 'leased' AND lease_owner = ?3 AND lease_token = ?4`
)

type JobSpec struct {
	At      time.Time
	Payload string
	ID      string
	Kind    string
	RepoID  int64
}

type Job struct {
	Payload        string
	LeaseOwner     string
	LeaseToken     string
	ID             string
	Kind           string
	State          string
	LeaseExpiresAt int64
	RunAfter       int64
	Attempts       int64
	RepoID         int64
	CreatedAt      int64
	UpdatedAt      int64
}

func checkJobSpec(spec JobSpec) error {
	if len(spec.ID) < 1 || len(spec.ID) > jobMaxIDLen {
		return fmt.Errorf("EnqueueJob: %w: id must be 1 to 128 bytes", ErrInvalid)
	}
	if len(spec.Kind) < 1 || len(spec.Kind) > jobMaxKindLen {
		return fmt.Errorf("EnqueueJob: %w: kind must be 1 to 64 bytes", ErrInvalid)
	}
	if spec.RepoID <= 0 {
		return fmt.Errorf("EnqueueJob: %w: repo id must be positive", ErrInvalid)
	}
	if len(spec.Payload) == 0 || !jsontext.Value(spec.Payload).IsValid() {
		return fmt.Errorf("EnqueueJob: %w: payload must be valid JSON", ErrInvalid)
	}
	if msOf(spec.At) <= 0 {
		return fmt.Errorf("EnqueueJob: %w: time must be after the Unix epoch", ErrInvalid)
	}
	return nil
}

func (tx *WriteTx) EnqueueJob(spec JobSpec) (bool, error) {
	if err := checkJobSpec(spec); err != nil {
		return false, err
	}
	inner, err := tx.live()
	if err != nil {
		return false, err
	}
	at := msOf(spec.At)
	moved, err := execAffected(tx.ctx, inner, sqlJobInsert, spec.ID, spec.Kind, spec.RepoID, spec.Payload, at)
	if err != nil {
		return false, fmt.Errorf("EnqueueJob: %w", mapErr(err))
	}
	return moved == 1, nil
}

func scanJob(rows interface {
	Scan(dest ...any) error
}) (Job, error) {
	var job Job
	var payload string
	if err := rows.Scan(&job.ID, &job.Kind, &job.RepoID, &payload, &job.State, &job.Attempts, &job.LeaseOwner, &job.LeaseToken, &job.LeaseExpiresAt, &job.RunAfter, &job.CreatedAt, &job.UpdatedAt); err != nil {
		return Job{}, err
	}
	job.Payload = payload
	return job, nil
}

func (tx *WriteTx) ClaimJobs(owner, token string, now time.Time, lease time.Duration, limit int) ([]Job, error) {
	if len(owner) == 0 {
		return nil, fmt.Errorf("ClaimJobs: %w: owner must not be empty", ErrInvalid)
	}
	if len(token) == 0 {
		return nil, fmt.Errorf("ClaimJobs: %w: lease token must not be empty", ErrInvalid)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("ClaimJobs: %w: limit must be positive", ErrInvalid)
	}
	if lease <= 0 {
		return nil, fmt.Errorf("ClaimJobs: %w: lease must be positive", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	at := msOf(now)
	if at <= 0 {
		return nil, fmt.Errorf("ClaimJobs: %w: time must be after the Unix epoch", ErrInvalid)
	}
	rows, err := inner.QueryContext(tx.ctx, sqlJobClaim, owner, at+lease.Milliseconds(), at, at, limit, token)
	if err != nil {
		return nil, fmt.Errorf("ClaimJobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		job, serr := scanJob(rows)
		if serr != nil {
			return nil, fmt.Errorf("ClaimJobs: scan: %w", serr)
		}
		out = append(out, job)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("ClaimJobs: %w", err)
	}
	return out, nil
}

func (tx *WriteTx) ClaimKindJobs(owner, token string, kinds []string, now time.Time, lease time.Duration, limit int) ([]Job, error) {
	if len(owner) == 0 {
		return nil, fmt.Errorf("ClaimKindJobs: %w: owner must not be empty", ErrInvalid)
	}
	if len(token) == 0 {
		return nil, fmt.Errorf("ClaimKindJobs: %w: lease token must not be empty", ErrInvalid)
	}
	if len(kinds) == 0 {
		return nil, fmt.Errorf("ClaimKindJobs: %w: kinds must not be empty", ErrInvalid)
	}
	if limit <= 0 {
		return nil, fmt.Errorf("ClaimKindJobs: %w: limit must be positive", ErrInvalid)
	}
	if lease <= 0 {
		return nil, fmt.Errorf("ClaimKindJobs: %w: lease must be positive", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	at := msOf(now)
	if at <= 0 {
		return nil, fmt.Errorf("ClaimKindJobs: %w: time must be after the Unix epoch", ErrInvalid)
	}
	kindsJSON, jerr := jsonv2.Marshal(kinds)
	if jerr != nil {
		return nil, fmt.Errorf("ClaimKindJobs: %w", jerr)
	}
	const query = `UPDATE jobs SET state = 'leased', lease_owner = ?1, lease_token = ?8, lease_expires_at = ?2, updated_at = ?3
WHERE id IN (SELECT id FROM jobs
WHERE kind IN (SELECT value FROM json_each(?4)) AND (state = 'queued' OR (state = 'leased' AND lease_expires_at <= ?5)) AND run_after <= ?5
AND (kind != 'repo_purge' OR json_extract(payload, '$.not_before') <= ?6)
ORDER BY created_at, id LIMIT ?7)
RETURNING id, kind, repo_id, payload, state, attempts, lease_owner, lease_token, lease_expires_at, run_after, created_at, updated_at`
	args := []any{owner, at + lease.Milliseconds(), at, string(kindsJSON), at, at, limit, token}
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, fmt.Errorf("ClaimKindJobs: %w", qerr)
	}
	defer func() { _ = rows.Close() }()
	var out []Job
	for rows.Next() {
		job, serr := scanJob(rows)
		if serr != nil {
			return nil, fmt.Errorf("ClaimKindJobs: scan: %w", serr)
		}
		out = append(out, job)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("ClaimKindJobs: %w", err)
	}
	return out, nil
}

func (tx *ReadTx) GetJob(id string) (Job, error) {
	inner, err := tx.live()
	if err != nil {
		return Job{}, err
	}
	job, serr := scanJob(inner.QueryRowContext(tx.ctx, sqlJobGet, id))
	if serr != nil {
		return Job{}, fmt.Errorf("GetJob: %w", serr)
	}
	return job, nil
}

func (tx *WriteTx) RenewJobs(owner, token string, ids []string, now time.Time, lease time.Duration) (int64, error) {
	if len(owner) == 0 {
		return 0, fmt.Errorf("RenewJobs: %w: owner must not be empty", ErrInvalid)
	}
	if len(token) == 0 {
		return 0, fmt.Errorf("RenewJobs: %w: lease token must not be empty", ErrInvalid)
	}
	if len(ids) == 0 {
		return 0, fmt.Errorf("RenewJobs: %w: ids must not be empty", ErrInvalid)
	}
	if lease <= 0 {
		return 0, fmt.Errorf("RenewJobs: %w: lease must be positive", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	at := msOf(now)
	if at <= 0 {
		return 0, fmt.Errorf("RenewJobs: %w: time must be after the Unix epoch", ErrInvalid)
	}
	query := `UPDATE jobs SET lease_expires_at = ?1, updated_at = ?2
WHERE state = 'leased' AND lease_owner = ?3 AND lease_token = ?4 AND id IN (?` + repeatPlaceholders(len(ids)) + `)`
	args := make([]any, 0, len(ids)+4)
	args = append(args, at+lease.Milliseconds(), at, owner, token)
	for _, id := range ids {
		args = append(args, id)
	}
	moved, err := execAffected(tx.ctx, inner, query, args...)
	if err != nil {
		return 0, fmt.Errorf("RenewJobs: %w", mapErr(err))
	}
	return moved, nil
}

func repeatPlaceholders(n int) string {
	out := ""
	for i := 1; i < n; i++ {
		out += ", ?"
	}
	return out
}

func (tx *WriteTx) CompleteJob(owner, id, token string, now time.Time) error {
	return tx.finishLease(sqlJobDone, "CompleteJob", owner, id, token, now)
}

// DeleteJob drops a job row so a fixed id can be enqueued afresh later.
func (tx *WriteTx) DeleteJob(id string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM jobs WHERE id = ?1`, id); derr != nil {
		return fmt.Errorf("DeleteJob: %w", mapErr(derr))
	}
	return nil
}

// ResetJob revives a queued, leased, or dead job under a new payload and run
// time. Callers that re-queue a fixed job id while a stale row still exists
// (after a restore, for instance) need this because EnqueueJob leaves an
// existing row alone.
func (tx *WriteTx) ResetJob(spec JobSpec, now time.Time) error {
	if err := checkJobSpec(spec); err != nil {
		return err
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	at := msOf(spec.At)
	moved, merr := execAffected(tx.ctx, inner, `UPDATE jobs
SET kind = ?2, repo_id = ?3, payload = ?4, state = 'queued', attempts = 0,
    lease_owner = '', lease_token = '', lease_expires_at = 0, run_after = ?5, updated_at = ?6
WHERE id = ?1`,
		spec.ID, spec.Kind, spec.RepoID, spec.Payload, at, msOf(now))
	if merr != nil {
		return fmt.Errorf("ResetJob: %w", mapErr(merr))
	}
	if moved == 0 {
		return fmt.Errorf("ResetJob: %w: job %s", ErrNotFound, spec.ID)
	}
	return nil
}

// RequeueJob records one handler failure: attempts grows, the next run
// waits out failBackoff, and at jobMaxAttempts the row moves to dead so
// a poison job stops spinning the single writer. Only the lease-token
// holder requeues; a fenced-out worker reports ErrStale and exits
// cleanly instead of running the job twice.
func (tx *WriteTx) RequeueJob(owner, id, token string, now time.Time) error {
	if len(owner) == 0 || len(id) == 0 || len(token) == 0 {
		return fmt.Errorf("RequeueJob: %w: owner, id, and lease token must not be empty", ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	at := msOf(now)
	if at <= 0 {
		return fmt.Errorf("RequeueJob: %w: time must be after the Unix epoch", ErrInvalid)
	}
	var attempts int64
	err = inner.QueryRowContext(tx.ctx,
		`SELECT attempts FROM jobs WHERE id = ?1 AND state = 'leased' AND lease_owner = ?2 AND lease_token = ?3`,
		id, owner, token).Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("RequeueJob: %w: job is not leased to this owner", ErrStale)
	}
	if err != nil {
		return fmt.Errorf("RequeueJob: %w", mapErr(err))
	}
	next := attempts + 1
	if next >= jobMaxAttempts {
		moved, merr := execAffected(tx.ctx, inner,
			`UPDATE jobs SET state = 'dead', attempts = ?1, lease_owner = '', lease_token = '', lease_expires_at = 0, run_after = 0, updated_at = ?2
WHERE id = ?3 AND state = 'leased' AND lease_owner = ?4 AND lease_token = ?5`,
			next, at, id, owner, token)
		if merr != nil {
			return fmt.Errorf("RequeueJob: %w", mapErr(merr))
		}
		if moved == 0 {
			return fmt.Errorf("RequeueJob: %w: job is not leased to this owner", ErrStale)
		}
		// A dead repo_fork job leaves its fork row terminal: the row moves to
		// deleted and the source counter gives the increment back, so a
		// retry-exhausted fork cannot sit in forking forever.
		if _, derr := execAffected(tx.ctx, inner,
			`UPDATE repos SET state = 'deleted', deleted_at = ?1
			 WHERE id = (SELECT repo_id FROM jobs WHERE id = ?2 AND kind = 'repo_fork')
			   AND state = 'forking'`,
			at, id); derr != nil {
			return fmt.Errorf("RequeueJob: %w: dead fork cleanup: %w", mapErr(derr), derr)
		}
		if _, derr := execAffected(tx.ctx, inner,
			`UPDATE repos SET forks_count = CASE WHEN forks_count > 0 THEN forks_count - 1 ELSE 0 END
			 WHERE id = (SELECT fork_of_repo_id FROM repos
			              WHERE id = (SELECT repo_id FROM jobs WHERE id = ?1 AND kind = 'repo_fork')
			                AND state = 'deleted'
			                AND fork_of_repo_id IS NOT NULL)`,
			id); derr != nil {
			return fmt.Errorf("RequeueJob: %w: dead fork counter: %w", mapErr(derr), derr)
		}
		return nil
	}
	wait := at + failBackoff(next, id).Milliseconds()
	moved, merr := execAffected(tx.ctx, inner,
		`UPDATE jobs SET state = 'queued', attempts = ?1, lease_owner = '', lease_token = '', lease_expires_at = 0, run_after = ?2, updated_at = ?3
WHERE id = ?4 AND state = 'leased' AND lease_owner = ?5 AND lease_token = ?6`,
		next, wait, at, id, owner, token)
	if merr != nil {
		return fmt.Errorf("RequeueJob: %w", mapErr(merr))
	}
	if moved == 0 {
		return fmt.Errorf("RequeueJob: %w: job is not leased to this owner", ErrStale)
	}
	return nil
}

func failBackoff(attempts int64, id string) time.Duration {
	shift := attempts
	if shift > 5 {
		shift = 5
	}
	base := 30 * time.Second << shift
	if base > 15*time.Minute {
		base = 15 * time.Minute
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return base + time.Duration(h.Sum32()%10)*time.Second
}

func (tx *WriteTx) finishLease(query, op, owner, id, token string, now time.Time) error {
	if len(owner) == 0 || len(id) == 0 || len(token) == 0 {
		return fmt.Errorf("%s: %w: owner, id, and lease token must not be empty", op, ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	at := msOf(now)
	if at <= 0 {
		return fmt.Errorf("%s: %w: time must be after the Unix epoch", op, ErrInvalid)
	}
	moved, err := execAffected(tx.ctx, inner, query, at, id, owner, token)
	if err != nil {
		return fmt.Errorf("%s: %w", op, mapErr(err))
	}
	if moved == 0 {
		return fmt.Errorf("%s: %w: job is not leased to this owner", op, ErrStale)
	}
	return nil
}

func queryTips(ctx context.Context, inner *sql.Tx, repoID int64) (map[string]string, error) {
	rows, err := inner.QueryContext(ctx, sqlTipsList, repoID)
	if err != nil {
		return nil, fmt.Errorf("RefTips: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var name, target string
		if err = rows.Scan(&name, &target); err != nil {
			return nil, fmt.Errorf("RefTips: scan: %w", err)
		}
		out[name] = target
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("RefTips: %w", err)
	}
	return out, nil
}
