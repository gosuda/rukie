package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"encoding/json/jsontext"
)

const (
	jobMaxIDLen   = 128
	jobMaxKindLen = 64
)

const (
	sqlJobInsert = `INSERT INTO jobs (id, kind, repo_id, payload, state, attempts, lease_owner, lease_expires_at, created_at, updated_at)
VALUES (?1, ?2, ?3, ?4, 'queued', 0, '', 0, ?5, ?5) ON CONFLICT (id) DO NOTHING`

	sqlJobClaim = `UPDATE jobs SET state = 'leased', lease_owner = ?1, lease_expires_at = ?2, updated_at = ?3
WHERE id IN (SELECT id FROM jobs
WHERE state = 'queued' OR (state = 'leased' AND lease_expires_at <= ?4)
ORDER BY created_at, id LIMIT ?5)
RETURNING id, kind, repo_id, payload, state, attempts, lease_owner, lease_expires_at, created_at, updated_at`

	sqlJobGet = `SELECT id, kind, repo_id, payload, state, attempts, lease_owner, lease_expires_at, created_at, updated_at
FROM jobs WHERE id = ?1`

	sqlJobDone = `UPDATE jobs SET state = 'done', lease_owner = '', lease_expires_at = 0, updated_at = ?1
WHERE id = ?2 AND state = 'leased' AND lease_owner = ?3`

	sqlJobFail = `UPDATE jobs SET state = 'queued', attempts = attempts + 1, lease_owner = '', lease_expires_at = 0, updated_at = ?1
WHERE id = ?2 AND state = 'leased' AND lease_owner = ?3`
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
	ID             string
	Kind           string
	State          string
	LeaseExpiresAt int64
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
	if err := rows.Scan(&job.ID, &job.Kind, &job.RepoID, &payload, &job.State, &job.Attempts, &job.LeaseOwner, &job.LeaseExpiresAt, &job.CreatedAt, &job.UpdatedAt); err != nil {
		return Job{}, err
	}
	job.Payload = payload
	return job, nil
}

func (tx *WriteTx) ClaimJobs(owner string, now time.Time, lease time.Duration, limit int) ([]Job, error) {
	if len(owner) == 0 {
		return nil, fmt.Errorf("ClaimJobs: %w: owner must not be empty", ErrInvalid)
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
	rows, err := inner.QueryContext(tx.ctx, sqlJobClaim, owner, at+lease.Milliseconds(), at, at, limit)
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

func (tx *WriteTx) ClaimKindJobs(owner string, kinds []string, now time.Time, lease time.Duration, limit int) ([]Job, error) {
	if len(owner) == 0 {
		return nil, fmt.Errorf("ClaimKindJobs: %w: owner must not be empty", ErrInvalid)
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
	numbered := make([]string, 0, len(kinds))
	for i := range kinds {
		numbered = append(numbered, fmt.Sprintf("?%d", 4+i))
	}
	atPos := 4 + len(kinds)
	duePos := 5 + len(kinds)
	limitPos := 6 + len(kinds)
	query := `UPDATE jobs SET state = 'leased', lease_owner = ?1, lease_expires_at = ?2, updated_at = ?3
WHERE id IN (SELECT id FROM jobs
WHERE kind IN (` + strings.Join(numbered, ", ") + `) AND (state = 'queued' OR (state = 'leased' AND lease_expires_at <= ?` + fmt.Sprintf("%d", atPos) + `))
AND (kind != 'repo_purge' OR json_extract(payload, '$.not_before') <= ?` + fmt.Sprintf("%d", duePos) + `)
ORDER BY created_at, id LIMIT ?` + fmt.Sprintf("%d", limitPos) + `)
RETURNING id, kind, repo_id, payload, state, attempts, lease_owner, lease_expires_at, created_at, updated_at`
	args := make([]any, 0, len(kinds)+6)
	args = append(args, owner, at+lease.Milliseconds(), at)
	for _, kind := range kinds {
		args = append(args, kind)
	}
	args = append(args, at, at, limit)
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

func (tx *WriteTx) RenewJobs(owner string, ids []string, now time.Time, lease time.Duration) (int64, error) {
	if len(owner) == 0 {
		return 0, fmt.Errorf("RenewJobs: %w: owner must not be empty", ErrInvalid)
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
WHERE state = 'leased' AND lease_owner = ?3 AND id IN (?` + repeatPlaceholders(len(ids)) + `)`
	args := make([]any, 0, len(ids)+3)
	args = append(args, at+lease.Milliseconds(), at, owner)
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

func (tx *WriteTx) CompleteJob(owner, id string, now time.Time) error {
	return tx.finishLease(sqlJobDone, "CompleteJob", owner, id, now)
}

func (tx *WriteTx) RequeueJob(owner, id string, now time.Time) error {
	return tx.finishLease(sqlJobFail, "RequeueJob", owner, id, now)
}

func (tx *WriteTx) finishLease(query, op, owner, id string, now time.Time) error {
	if len(owner) == 0 || len(id) == 0 {
		return fmt.Errorf("%s: %w: owner and id must not be empty", op, ErrInvalid)
	}
	inner, err := tx.live()
	if err != nil {
		return err
	}
	at := msOf(now)
	if at <= 0 {
		return fmt.Errorf("%s: %w: time must be after the Unix epoch", op, ErrInvalid)
	}
	moved, err := execAffected(tx.ctx, inner, query, at, id, owner)
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
