package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RepoRow struct {
	Description   string
	Website       string
	Topics        string
	Name          string
	Visibility    string
	DefaultBranch string
	RukPath       string
	OwnerUserID   sql.NullInt64
	DeletedAt     sql.NullInt64
	PurgeAfter    sql.NullInt64
	OrgID         int64
	ID            int64
	CreatedAt     int64
	UpdatedAt     int64
	Archived      bool
}

type GrantRow struct {
	Username string
	Role     string
	UserID   int64
}

type RedirectRow struct {
	OldName    string
	OldOwnerID int64
	RepoID     int64
	CreatedAt  int64
}

const sqlRepoColumns = `id, owner_user_id, org_id, name, visibility, description, website,
	topics, default_branch, archived, deleted_at, purge_after, ruk_path, created_at, updated_at`

func scanRepoRow(scan func(dest ...any) error) (RepoRow, error) {
	var row RepoRow
	var archived int64
	err := scan(
		&row.ID, &row.OwnerUserID, &row.OrgID, &row.Name, &row.Visibility,
		&row.Description, &row.Website, &row.Topics, &row.DefaultBranch,
		&archived, &row.DeletedAt, &row.PurgeAfter, &row.RukPath,
		&row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		return RepoRow{}, err
	}
	row.Archived = archived == 1
	return row, nil
}

func (tx *ReadTx) GetRepo(id int64) (RepoRow, error) {
	inner, err := tx.live()
	if err != nil {
		return RepoRow{}, err
	}
	row, serr := scanRepoRow(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlRepoColumns+` FROM repos WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return RepoRow{}, fmt.Errorf("GetRepo: %w: repo %d", ErrNotFound, id)
	}
	return row, serr
}

func (tx *ReadTx) FindRepo(ownerUserID, orgID int64, name string) (RepoRow, error) {
	return tx.findRepo(ownerUserID, orgID, name, false)
}

func (tx *ReadTx) FindRepoWithDeleted(ownerUserID, orgID int64, name string) (RepoRow, error) {
	return tx.findRepo(ownerUserID, orgID, name, true)
}

func (tx *ReadTx) findRepo(ownerUserID, orgID int64, name string, includeDeleted bool) (RepoRow, error) {
	inner, err := tx.live()
	if err != nil {
		return RepoRow{}, err
	}
	query := `SELECT ` + sqlRepoColumns + ` FROM repos
WHERE owner_user_id IS ? AND org_id = ? AND name = ?`
	if ownerUserID != 0 {
		query = `SELECT ` + sqlRepoColumns + ` FROM repos
WHERE owner_user_id = ? AND org_id = ? AND name = ?`
	}
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	var row RepoRow
	var serr error
	if ownerUserID != 0 {
		row, serr = scanRepoRow(inner.QueryRowContext(tx.ctx, query, ownerUserID, orgID, name).Scan)
	} else {
		row, serr = scanRepoRow(inner.QueryRowContext(tx.ctx, query, nil, orgID, name).Scan)
	}
	if errors.Is(serr, sql.ErrNoRows) {
		return RepoRow{}, fmt.Errorf("FindRepo: %w: %s", ErrNotFound, name)
	}
	return row, serr
}

func (tx *WriteTx) GetRepoForUpdate(id int64, includeDeleted bool) (RepoRow, error) {
	inner, err := tx.live()
	if err != nil {
		return RepoRow{}, err
	}
	query := `SELECT ` + sqlRepoColumns + ` FROM repos WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	row, serr := scanRepoRow(inner.QueryRowContext(tx.ctx, query, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return RepoRow{}, fmt.Errorf("GetRepoForUpdate: %w: repo %d", ErrNotFound, id)
	}
	return row, serr
}

type RepoListFilter struct {
	Visibility  string
	Sort        string
	Cursor      string
	OwnerUserID int64
	Limit       int
}

type repoCursor struct {
	Value string
	ID    int64
}

func parseRepoCursor(raw string) (repoCursor, error) {
	if raw == "" {
		return repoCursor{}, nil
	}
	decoded, derr := base64.RawURLEncoding.DecodeString(raw)
	if derr != nil {
		return repoCursor{}, fmt.Errorf("cursor: %w: malformed repo cursor", ErrInvalid)
	}
	value, idRaw, ok := strings.Cut(string(decoded), "\x1f")
	if !ok {
		return repoCursor{}, fmt.Errorf("cursor: %w: malformed repo cursor", ErrInvalid)
	}
	var id int64
	for _, c := range idRaw {
		if c < '0' || c > '9' {
			return repoCursor{}, fmt.Errorf("cursor: %w: malformed repo cursor", ErrInvalid)
		}
		id = id*10 + int64(c-'0')
	}
	return repoCursor{Value: value, ID: id}, nil
}

func encodeRepoCursor(value string, id int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s\x1f%d", value, id)))
}

func repoListValue(row RepoRow, sort string) string {
	switch sort {
	case "name":
		return row.Name
	case "created":
		return fmt.Sprintf("%020d", row.CreatedAt)
	default:
		return fmt.Sprintf("%020d", row.UpdatedAt)
	}
}

func (tx *ReadTx) ListRepos(filter RepoListFilter) ([]RepoRow, string, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, "", err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	cursor, cerr := parseRepoCursor(filter.Cursor)
	if cerr != nil {
		return nil, "", cerr
	}
	query := `SELECT ` + sqlRepoColumns + ` FROM repos WHERE deleted_at IS NULL`
	var args []any
	if filter.OwnerUserID != 0 {
		query += ` AND owner_user_id = ? AND org_id = 0`
		args = append(args, filter.OwnerUserID)
	}
	if filter.Visibility != "" {
		query += ` AND visibility = ?`
		args = append(args, filter.Visibility)
	}
	if filter.Cursor != "" {
		switch filter.Sort {
		case "name":
			query += ` AND (name, id) > (?, ?)`
		case "created":
			query += ` AND (created_at, id) > (?, ?)`
		default:
			query += ` AND (updated_at, id) > (?, ?)`
		}
		if filter.Sort == "name" {
			args = append(args, cursor.Value, cursor.ID)
		} else {
			var ms int64
			for _, c := range cursor.Value {
				if c < '0' || c > '9' {
					return nil, "", fmt.Errorf("cursor: %w: malformed repo cursor", ErrInvalid)
				}
				ms = ms*10 + int64(c-'0')
			}
			args = append(args, ms, cursor.ID)
		}
	}
	switch filter.Sort {
	case "name":
		query += ` ORDER BY name, id LIMIT ?`
	case "created":
		query += ` ORDER BY created_at, id LIMIT ?`
	default:
		query += ` ORDER BY updated_at, id LIMIT ?`
	}
	args = append(args, limit+1)
	rows, qerr := inner.QueryContext(tx.ctx, query, args...)
	if qerr != nil {
		return nil, "", qerr
	}
	defer func() { _ = rows.Close() }()
	var out []RepoRow
	for rows.Next() {
		row, serr := scanRepoRow(rows.Scan)
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
		next = encodeRepoCursor(repoListValue(last, filter.Sort), last.ID)
	}
	return out, next, nil
}

func (tx *ReadTx) LiveRepos(limit int) ([]RepoRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 1000
	}
	rows, qerr := inner.QueryContext(tx.ctx,
		`SELECT `+sqlRepoColumns+` FROM repos WHERE deleted_at IS NULL ORDER BY id LIMIT ?`, limit)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []RepoRow
	for rows.Next() {
		row, serr := scanRepoRow(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (tx *ReadTx) DuePurges(now time.Time) ([]RepoRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx,
		`SELECT `+sqlRepoColumns+` FROM repos
WHERE deleted_at IS NOT NULL AND purge_after IS NOT NULL AND purge_after <= ? ORDER BY id`, msOf(now))
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []RepoRow
	for rows.Next() {
		row, serr := scanRepoRow(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

type RepoInsert struct {
	Description   string
	Website       string
	Topics        string
	Name          string
	Visibility    string
	DefaultBranch string
	RukPath       string
	OwnerUserID   sql.NullInt64
	OrgID         int64
}

func (tx *WriteTx) InsertRepo(now time.Time, in RepoInsert) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	at := msOf(now)
	topics := in.Topics
	if topics == "" {
		topics = "[]"
	}
	var id int64
	serr := inner.QueryRowContext(tx.ctx, `INSERT INTO repos
(owner_user_id, org_id, name, visibility, description, website, topics,
 default_branch, archived, deleted_at, purge_after, ruk_path, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, NULL, NULL, ?, ?, ?)
RETURNING id`,
		in.OwnerUserID, in.OrgID, in.Name, in.Visibility, in.Description,
		in.Website, topics, in.DefaultBranch, in.RukPath, at, at).Scan(&id)
	if serr != nil {
		return 0, mapErr(serr)
	}
	return id, nil
}

type RepoUpdate struct {
	Description   *string
	Website       *string
	Topics        *string
	DefaultBranch *string
	Visibility    *string
	Archived      *bool
}

func (tx *WriteTx) UpdateRepo(id int64, now time.Time, up RepoUpdate) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	query := `UPDATE repos SET updated_at = ?`
	args := []any{msOf(now)}
	if up.Description != nil {
		query += `, description = ?`
		args = append(args, *up.Description)
	}
	if up.Website != nil {
		query += `, website = ?`
		args = append(args, *up.Website)
	}
	if up.Topics != nil {
		query += `, topics = ?`
		args = append(args, *up.Topics)
	}
	if up.DefaultBranch != nil {
		query += `, default_branch = ?`
		args = append(args, *up.DefaultBranch)
	}
	if up.Visibility != nil {
		query += `, visibility = ?`
		args = append(args, *up.Visibility)
	}
	if up.Archived != nil {
		archived := 0
		if *up.Archived {
			archived = 1
		}
		query += `, archived = ?`
		args = append(args, archived)
	}
	query += ` WHERE id = ? AND deleted_at IS NULL`
	args = append(args, id)
	moved, merr := execAffected(tx.ctx, inner, query, args...)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("UpdateRepo: %w: repo %d", ErrNotFound, id)
	}
	return nil
}

func (tx *WriteTx) RenameRepo(id, newOwner int64, newName, newPath string, now time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE repos
SET owner_user_id = ?, name = ?, ruk_path = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`, newOwner, newName, newPath, msOf(now), id)
	if merr != nil {
		return mapErr(merr)
	}
	if moved == 0 {
		return fmt.Errorf("RenameRepo: %w: repo %d", ErrNotFound, id)
	}
	return nil
}

func (tx *WriteTx) SoftDeleteRepo(id int64, now time.Time, purgeAfter time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE repos
SET deleted_at = ?, purge_after = ?, updated_at = ?
WHERE id = ? AND deleted_at IS NULL`, msOf(now), msOf(purgeAfter), msOf(now), id)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("SoftDeleteRepo: %w: repo %d", ErrNotFound, id)
	}
	return nil
}

func (tx *WriteTx) RestoreRepo(id int64, now time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner, `UPDATE repos
SET deleted_at = NULL, purge_after = NULL, updated_at = ?
WHERE id = ? AND deleted_at IS NOT NULL`, msOf(now), id)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("RestoreRepo: %w: repo %d is not deleted", ErrNotFound, id)
	}
	return nil
}

func (tx *WriteTx) HardDeleteRepo(id int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	if _, derr := inner.ExecContext(tx.ctx, `DELETE FROM repo_redirects WHERE repo_id = ?`, id); derr != nil {
		return derr
	}
	moved, merr := execAffected(tx.ctx, inner, `DELETE FROM repos WHERE id = ?`, id)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("HardDeleteRepo: %w: repo %d", ErrNotFound, id)
	}
	return nil
}

func (tx *ReadTx) ListGrants(repoID int64) ([]GrantRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT g.user_id, g.role, u.name
FROM repo_grants g JOIN users u ON u.id = g.user_id
WHERE g.repo_id = ? ORDER BY u.name`, repoID)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []GrantRow
	for rows.Next() {
		var grant GrantRow
		if serr := rows.Scan(&grant.UserID, &grant.Role, &grant.Username); serr != nil {
			return nil, serr
		}
		out = append(out, grant)
	}
	return out, rows.Err()
}

func (tx *WriteTx) PutGrant(repoID, userID int64, role string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	switch role {
	case "read", "triage", "write", "maintain", "admin":
	default:
		return fmt.Errorf("PutGrant: %w: unknown role %q", ErrInvalid, role)
	}
	_, merr := inner.ExecContext(tx.ctx, `INSERT INTO repo_grants (repo_id, user_id, role)
VALUES (?, ?, ?) ON CONFLICT (repo_id, user_id) DO UPDATE SET role = excluded.role`,
		repoID, userID, role)
	return merr
}

func (tx *WriteTx) DeleteGrant(repoID, userID int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	moved, merr := execAffected(tx.ctx, inner,
		`DELETE FROM repo_grants WHERE repo_id = ? AND user_id = ?`, repoID, userID)
	if merr != nil {
		return merr
	}
	if moved == 0 {
		return fmt.Errorf("DeleteGrant: %w: grant for user %d", ErrNotFound, userID)
	}
	return nil
}

// defaultLandRequired is the default bookmark requirement: one review/approve
// from a trusted signer who is not the change author.
const defaultLandRequired = `[{"kind":"review/approve","result":"approved","trusted":{},"min_signers":1}]`

func (tx *WriteTx) InsertDefaultRule(repoID int64, now time.Time, pattern string) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	_, merr := inner.ExecContext(tx.ctx, `INSERT INTO bookmark_rules
(repo_id, pattern, mode, bypass_admin, push_allow, land_allow, required, version, created_at)
VALUES (?, ?, 'LandOnly', 0, '[]', '[]', ?, 1, ?)`,
		repoID, pattern, defaultLandRequired, msOf(now))
	return merr
}

func (tx *WriteTx) PutRedirect(oldOwner int64, oldName string, repoID int64, now time.Time) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	_, merr := inner.ExecContext(tx.ctx, `INSERT INTO repo_redirects
(old_owner_user_id, old_name, repo_id, created_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (old_owner_user_id, old_name) DO UPDATE SET repo_id = excluded.repo_id`,
		oldOwner, oldName, repoID, msOf(now))
	return merr
}

func (tx *ReadTx) FindRedirect(oldOwner int64, oldName string) (RedirectRow, error) {
	inner, err := tx.live()
	if err != nil {
		return RedirectRow{}, err
	}
	var row RedirectRow
	serr := inner.QueryRowContext(tx.ctx, `SELECT old_owner_user_id, old_name, repo_id, created_at
FROM repo_redirects WHERE old_owner_user_id = ? AND old_name = ?`, oldOwner, oldName).Scan(
		&row.OldOwnerID, &row.OldName, &row.RepoID, &row.CreatedAt)
	if errors.Is(serr, sql.ErrNoRows) {
		return RedirectRow{}, fmt.Errorf("FindRedirect: %w: %s", ErrNotFound, oldName)
	}
	return row, serr
}
