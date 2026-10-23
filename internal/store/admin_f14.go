package store

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

// AuditEventRow is one row of the audit_events table for admin listing.
type AuditEventRow struct {
	Action   string
	Target   string
	IP       string
	Result   string
	ID       int64
	At       int64
	Actor    int64
	ActorSet bool
}

// AuditFilter scopes an admin audit listing.
type AuditFilter struct {
	Action string
	Target string
	Cursor string
	Actor  int64
	Since  int64
	Limit  int
}

type auditCursor struct {
	At int64
	ID int64
}

func parseAuditCursor(raw string) (auditCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return auditCursor{}, fmt.Errorf("cursor: %w: malformed audit cursor", ErrInvalid)
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return auditCursor{}, fmt.Errorf("cursor: %w: malformed audit cursor", ErrInvalid)
	}
	var at, id int64
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return auditCursor{}, fmt.Errorf("cursor: %w: malformed audit cursor", ErrInvalid)
		}
		at = at*10 + int64(c-'0')
	}
	for _, c := range parts[1] {
		if c < '0' || c > '9' {
			return auditCursor{}, fmt.Errorf("cursor: %w: malformed audit cursor", ErrInvalid)
		}
		id = id*10 + int64(c-'0')
	}
	return auditCursor{At: at, ID: id}, nil
}

func encodeAuditCursor(at, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%d", at, id)))
}

// ListAuditEvents returns audit rows newest first with keyset pagination.
func (s *Store) ListAuditEvents(ctx context.Context, filter AuditFilter) ([]AuditEventRow, string, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	var cursor auditCursor
	if filter.Cursor != "" {
		c, cerr := parseAuditCursor(filter.Cursor)
		if cerr != nil {
			return nil, "", cerr
		}
		cursor = c
	}
	var out []AuditEventRow
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		query := `SELECT id, at, actor_user_id, action, target_type, target_id, ip, result
			FROM audit_events WHERE 1 = 1`
		var args []any
		if filter.Actor != 0 {
			query += ` AND actor_user_id = ?`
			args = append(args, filter.Actor)
		}
		if filter.Action != "" {
			query += ` AND action = ?`
			args = append(args, filter.Action)
		}
		if filter.Target != "" {
			query += ` AND (target_type || ':' || target_id) LIKE ?`
			args = append(args, "%"+filter.Target+"%")
		}
		if filter.Since != 0 {
			query += ` AND at >= ?`
			args = append(args, filter.Since)
		}
		if filter.Cursor != "" {
			query += ` AND (at, id) < (?, ?)`
			args = append(args, cursor.At, cursor.ID)
		}
		query += ` ORDER BY at DESC, id DESC LIMIT ?`
		args = append(args, limit+1)
		rows, qerr := inner.QueryContext(tx.ctx, query, args...)
		if qerr != nil {
			return fmt.Errorf("ListAuditEvents: %w", mapErr(qerr))
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var row AuditEventRow
			var actor any
			var targetType, targetID string
			if serr := rows.Scan(&row.ID, &row.At, &actor, &row.Action, &targetType, &targetID, &row.IP, &row.Result); serr != nil {
				return fmt.Errorf("ListAuditEvents: %w", mapErr(serr))
			}
			if id, ok := actor.(int64); ok {
				row.Actor, row.ActorSet = id, true
			}
			if targetType != "" || targetID != "" {
				row.Target = targetType + ":" + targetID
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeAuditCursor(last.At, last.ID)
	}
	return out, next, nil
}

// AdminJobFilter scopes an admin job listing over the jobs table.
type AdminJobFilter struct {
	Kind   string
	State  string
	Cursor string
	Limit  int
}

// adminJobState maps the admin vocabulary onto stored job states.
func adminJobState(state string) (string, error) {
	switch state {
	case "", "all":
		return "", nil
	case "queued":
		return "queued", nil
	case "running":
		return "leased", nil
	case "succeeded":
		return "done", nil
	case "failed", "dead":
		return "dead", nil
	default:
		return "", fmt.Errorf("state: %w: must be queued, running, succeeded, failed, dead, or all", ErrInvalid)
	}
}

func parseJobCursor(raw string) (int64, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, "", fmt.Errorf("cursor: %w: malformed job cursor", ErrInvalid)
	}
	created, id, found := strings.Cut(string(decoded), "\x1f")
	if !found {
		return 0, "", fmt.Errorf("cursor: %w: malformed job cursor", ErrInvalid)
	}
	var at int64
	for _, c := range created {
		if c < '0' || c > '9' {
			return 0, "", fmt.Errorf("cursor: %w: malformed job cursor", ErrInvalid)
		}
		at = at*10 + int64(c-'0')
	}
	return at, id, nil
}

// ListJobsAdmin returns jobs newest first with keyset pagination.
func (s *Store) ListJobsAdmin(ctx context.Context, filter AdminJobFilter) ([]Job, string, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	state, serr := adminJobState(filter.State)
	if serr != nil {
		return nil, "", serr
	}
	var at int64
	var afterID string
	if filter.Cursor != "" {
		c, id, cerr := parseJobCursor(filter.Cursor)
		if cerr != nil {
			return nil, "", cerr
		}
		at, afterID = c, id
	}
	var out []Job
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		query := `SELECT id, kind, repo_id, payload, state, attempts, lease_owner, lease_expires_at, created_at, updated_at
			FROM jobs WHERE 1 = 1`
		var args []any
		if filter.Kind != "" {
			query += ` AND kind = ?`
			args = append(args, filter.Kind)
		}
		if state != "" {
			query += ` AND state = ?`
			args = append(args, state)
		}
		if filter.Cursor != "" {
			query += ` AND (created_at, id) < (?, ?)`
			args = append(args, at, afterID)
		}
		query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
		args = append(args, limit+1)
		rows, qerr := inner.QueryContext(tx.ctx, query, args...)
		if qerr != nil {
			return fmt.Errorf("ListJobsAdmin: %w", mapErr(qerr))
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var job Job
			var payload string
			if serr := rows.Scan(&job.ID, &job.Kind, &job.RepoID, &payload, &job.State, &job.Attempts,
				&job.LeaseOwner, &job.LeaseExpiresAt, &job.CreatedAt, &job.UpdatedAt); serr != nil {
				return fmt.Errorf("ListJobsAdmin: %w", mapErr(serr))
			}
			job.Payload = payload
			out = append(out, job)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d\x1f%s", last.CreatedAt, last.ID)))
	}
	return out, next, nil
}

// CountTerminalJobs counts done and dead jobs settled before the cutoff.
func (s *Store) CountTerminalJobs(ctx context.Context, beforeMillis int64) (int64, error) {
	var n int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM jobs
WHERE state IN ('done','dead') AND updated_at < ?`, beforeMillis).Scan(&n); qerr != nil {
			return fmt.Errorf("CountTerminalJobs: %w", mapErr(qerr))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// PruneTerminalJobs deletes done and dead jobs settled before the cutoff.
// Retries re-enqueue under the same id through ResetJob, so old terminal
// rows are pure history.
func (s *Store) PruneTerminalJobs(ctx context.Context, beforeMillis int64) (int64, error) {
	var n int64
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		res, derr := inner.ExecContext(tx.ctx, `DELETE FROM jobs
WHERE state IN ('done','dead') AND updated_at < ?`, beforeMillis)
		if derr != nil {
			return fmt.Errorf("PruneTerminalJobs: %w", mapErr(derr))
		}
		affected, aerr := res.RowsAffected()
		if aerr != nil {
			return fmt.Errorf("PruneTerminalJobs: %w", mapErr(aerr))
		}
		n = affected
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// CountJobsByState returns live job counts keyed by stored state.
func (s *Store) CountJobsByState(ctx context.Context) (map[string]int64, error) {
	counts := map[string]int64{}
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		rows, qerr := inner.QueryContext(tx.ctx, `SELECT state, COUNT(*) FROM jobs GROUP BY state`)
		if qerr != nil {
			return fmt.Errorf("CountJobsByState: %w", mapErr(qerr))
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var state string
			var n int64
			if serr := rows.Scan(&state, &n); serr != nil {
				return fmt.Errorf("CountJobsByState: %w", mapErr(serr))
			}
			counts[state] = n
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return counts, nil
}

// SoftDeleteAuthUser tombstones a live user: the row stays with is_active = 0
// until purge_after, when the sweeper hard-deletes it. It refuses the last
// active admin and sole org owners with the same errors as DeleteAuthUser so
// callers map them identically.
func (s *Store) SoftDeleteAuthUser(ctx context.Context, userID int64, now time.Time, purgeAfter time.Time) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var sole int64
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM team_members m
			JOIN teams t ON t.id = m.team_id
			WHERE t.lower_name = 'owners' AND m.user_id = ?
			AND (SELECT COUNT(*) FROM team_members m2 WHERE m2.team_id = t.id) <= 1`, userID).Scan(&sole); qerr != nil {
			return fmt.Errorf("SoftDeleteAuthUser: %w", qerr)
		}
		if sole > 0 {
			return fmt.Errorf("SoftDeleteAuthUser: %w", ErrSoleOwner)
		}
		var lastAdmin int64
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM users
			WHERE id = ? AND is_admin = 1 AND is_active = 1
			AND (SELECT COUNT(*) FROM users WHERE is_admin = 1 AND is_active = 1) <= 1`, userID).Scan(&lastAdmin); qerr != nil {
			return fmt.Errorf("SoftDeleteAuthUser: %w", qerr)
		}
		if lastAdmin > 0 {
			return fmt.Errorf("SoftDeleteAuthUser: %w", ErrLastAdmin)
		}
		moved, merr := execAffected(tx.ctx, inner, `UPDATE users
SET is_active = 0, deleted_at = ?, purge_after = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`, msOf(now), msOf(purgeAfter), msOf(now), userID)
		if merr != nil {
			return fmt.Errorf("SoftDeleteAuthUser: %w", mapErr(merr))
		}
		if moved == 0 {
			return fmt.Errorf("SoftDeleteAuthUser: %w: user %d", ErrNotFound, userID)
		}
		return nil
	})
}

// SoftDeleteUserCascade tombstones one user and suspends everything they
// could act with in a single transaction: the user row, their sessions,
// their scoped tokens, and their live user-namespace repos. Org repos keep
// their owners. It refuses the last active admin and sole org owners with
// the same errors as DeleteAuthUser so callers map them identically.
func (s *Store) SoftDeleteUserCascade(ctx context.Context, userID int64, now time.Time, purgeAfter time.Time) error {
	return s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		exec := func(query string, args ...any) error {
			if _, eerr := inner.ExecContext(tx.ctx, query, args...); eerr != nil {
				return mapErr(eerr)
			}
			return nil
		}
		var sole int64
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM team_members m
			JOIN teams t ON t.id = m.team_id
			WHERE t.lower_name = 'owners' AND m.user_id = ?
			AND (SELECT COUNT(*) FROM team_members m2 WHERE m2.team_id = t.id) <= 1`, userID).Scan(&sole); qerr != nil {
			return fmt.Errorf("SoftDeleteUserCascade: %w", qerr)
		}
		if sole > 0 {
			return fmt.Errorf("SoftDeleteUserCascade: %w", ErrSoleOwner)
		}
		var lastAdmin int64
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM users
			WHERE id = ? AND is_admin = 1 AND is_active = 1
			AND (SELECT COUNT(*) FROM users WHERE is_admin = 1 AND is_active = 1) <= 1`, userID).Scan(&lastAdmin); qerr != nil {
			return fmt.Errorf("SoftDeleteUserCascade: %w", qerr)
		}
		if lastAdmin > 0 {
			return fmt.Errorf("SoftDeleteUserCascade: %w", ErrLastAdmin)
		}
		at, horizon, stamp := msOf(now), msOf(purgeAfter), msOf(now)
		moved, merr := execAffected(tx.ctx, inner, `UPDATE users
SET is_active = 0, deleted_at = ?, purge_after = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`, at, horizon, stamp, userID)
		if merr != nil {
			return fmt.Errorf("SoftDeleteUserCascade: %w", mapErr(merr))
		}
		if moved == 0 {
			return fmt.Errorf("SoftDeleteUserCascade: %w: user %d", ErrNotFound, userID)
		}
		if eerr := exec(`DELETE FROM sessions WHERE user_id = ?`, userID); eerr != nil {
			return fmt.Errorf("SoftDeleteUserCascade: %w", eerr)
		}
		if eerr := exec(`UPDATE scoped_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, stamp, userID); eerr != nil {
			return fmt.Errorf("SoftDeleteUserCascade: %w", eerr)
		}
		if eerr := exec(`UPDATE repos
SET deleted_at = ?, purge_after = ?, state = 'deleted', updated_at = ?
WHERE owner_user_id = ? AND org_id = 0 AND deleted_at IS NULL`, at, horizon, stamp, userID); eerr != nil {
			return fmt.Errorf("SoftDeleteUserCascade: %w", eerr)
		}
		return nil
	})
}

// DueUserPurges lists tombstoned users whose purge horizon has passed.
func (tx *ReadTx) DueUserPurges(now time.Time) ([]AuthUser, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlAuthUserColumns+
		` FROM users WHERE deleted_at IS NOT NULL AND purge_after IS NOT NULL AND purge_after <= ? ORDER BY id`, msOf(now))
	if qerr != nil {
		return nil, fmt.Errorf("DueUserPurges: %w", mapErr(qerr))
	}
	defer func() { _ = rows.Close }()
	var out []AuthUser
	for rows.Next() {
		var u AuthUser
		if serr := scanAuthUser(rows, &u, nil); serr != nil {
			return nil, fmt.Errorf("DueUserPurges: %w", mapErr(serr))
		}
		out = append(out, u)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("DueUserPurges: %w", mapErr(rerr))
	}
	return out, nil
}

// ListOwnedLiveRepoIDs returns live user-namespace repos owned by one user.
func (tx *ReadTx) ListOwnedLiveRepoIDs(userID int64) ([]int64, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT id FROM repos
WHERE owner_user_id = ? AND org_id = 0 AND deleted_at IS NULL ORDER BY id`, userID)
	if qerr != nil {
		return nil, fmt.Errorf("ListOwnedLiveRepoIDs: %w", mapErr(qerr))
	}
	defer func() { _ = rows.Close }()
	var out []int64
	for rows.Next() {
		var id int64
		if serr := rows.Scan(&id); serr != nil {
			return nil, fmt.Errorf("ListOwnedLiveRepoIDs: %w", mapErr(serr))
		}
		out = append(out, id)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("ListOwnedLiveRepoIDs: %w", mapErr(rerr))
	}
	return out, nil
}

// ListAllOrgs lists every org by name for the admin console.
func (tx *ReadTx) ListAllOrgs(after string, limit int) ([]OrgRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlOrgColumns+` FROM orgs
WHERE lower_name > ? ORDER BY lower_name LIMIT ?`, after, limit)
	if qerr != nil {
		return nil, fmt.Errorf("ListAllOrgs: %w", mapErr(qerr))
	}
	defer func() { _ = rows.Close }()
	var out []OrgRow
	for rows.Next() {
		row, serr := scanOrg(rows.Scan)
		if serr != nil {
			return nil, fmt.Errorf("ListAllOrgs: %w", mapErr(serr))
		}
		out = append(out, row)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("ListAllOrgs: %w", mapErr(rerr))
	}
	return out, nil
}

// CountUsers returns the number of user rows, including tombstones.
func (s *Store) CountUsers(ctx context.Context) (int64, error) {
	var n int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM users`).Scan(&n); qerr != nil {
			return fmt.Errorf("CountUsers: %w", mapErr(qerr))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// CountLiveRepos returns the number of live repository rows.
func (s *Store) CountLiveRepos(ctx context.Context) (int64, error) {
	var n int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM repos WHERE deleted_at IS NULL`).Scan(&n); qerr != nil {
			return fmt.Errorf("CountLiveRepos: %w", mapErr(qerr))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// CountOrgs returns the number of org rows.
func (s *Store) CountOrgs(ctx context.Context) (int64, error) {
	var n int64
	err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM orgs`).Scan(&n); qerr != nil {
			return fmt.Errorf("CountOrgs: %w", mapErr(qerr))
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// CountUserRepos counts every repository row owned by one user, live or
// soft-deleted. The user purge sweep waits for zero.
func (tx *ReadTx) CountUserRepos(userID int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	var n int64
	if qerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM repos WHERE owner_user_id = ?`, userID).Scan(&n); qerr != nil {
		return 0, fmt.Errorf("CountUserRepos: %w", mapErr(qerr))
	}
	return n, nil
}

// SearchSyncReport describes one admin search index reconciliation.
type SearchSyncReport struct {
	ReposChecked int64
	ReposFixed   int64
	UsersChecked int64
	UsersFixed   int64
}

// SyncSearchIndex repairs FTS rows missing for live repos and users.
// The triggers keep the index current; this pass only heals drift
// (for example rows written while a trigger was dropped).
func (s *Store) SyncSearchIndex(ctx context.Context) (SearchSyncReport, error) {
	var rep SearchSyncReport
	err := s.Write(ctx, func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		var total int64
		if cerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM repos`).Scan(&total); cerr != nil {
			return fmt.Errorf("SyncSearchIndex: %w", mapErr(cerr))
		}
		rep.ReposChecked = total
		if cerr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM users`).Scan(&total); cerr != nil {
			return fmt.Errorf("SyncSearchIndex: %w", mapErr(cerr))
		}
		rep.UsersChecked = total
		// The AFTER UPDATE triggers only refresh existing FTS rows, so
		// missing rows are inserted directly with the same body expression
		// the insert triggers use.
		fixed, ferr := execAffected(tx.ctx, inner, `INSERT INTO repos_fts(rowid, body)
SELECT r.id, lower(r.name || ' ' || coalesce(r.description, '') || ' ' || coalesce(r.topics, '') || ' ' || coalesce(r.website, ''))
FROM repos r LEFT JOIN repos_fts f ON f.rowid = r.id WHERE f.rowid IS NULL`)
		if ferr != nil {
			return fmt.Errorf("SyncSearchIndex: %w", mapErr(ferr))
		}
		rep.ReposFixed = fixed
		ufixed, uerr := execAffected(tx.ctx, inner, `INSERT INTO users_fts(rowid, body)
SELECT u.id, lower(u.name || ' ' || coalesce(u.full_name, '')) FROM users u
LEFT JOIN users_fts f ON f.rowid = u.id WHERE f.rowid IS NULL`)
		if uerr != nil {
			return fmt.Errorf("SyncSearchIndex: %w", mapErr(uerr))
		}
		rep.UsersFixed = ufixed
		return nil
	})
	if err != nil {
		return SearchSyncReport{}, err
	}
	return rep, nil
}
