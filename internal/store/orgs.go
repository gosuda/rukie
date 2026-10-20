package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const ownersTeamName = "Owners"

type OrgRow struct {
	DisplayName string
	Description string
	Website     string
	Name        string
	LowerName   string
	Visibility  string
	ID          int64
	CreatedAt   int64
	UpdatedAt   int64
}

type TeamRow struct {
	Description  string
	Name         string
	LowerName    string
	Permission   string
	RepoScope    string
	OrgID        int64
	ID           int64
	CreatedAt    int64
	UpdatedAt    int64
	CanCreateRep bool
}

type OrgMemberRow struct {
	Username string
	Role     string
	Teams    []string
	UserID   int64
}

const sqlOrgColumns = `id, name, lower_name, display_name, description, website, visibility, created_at, updated_at`

func scanOrg(scan func(dest ...any) error) (OrgRow, error) {
	var row OrgRow
	err := scan(&row.ID, &row.Name, &row.LowerName, &row.DisplayName, &row.Description,
		&row.Website, &row.Visibility, &row.CreatedAt, &row.UpdatedAt)
	return row, err
}

const sqlTeamColumns = `id, org_id, name, lower_name, description, permission, repo_scope, can_create_repo, created_at, updated_at`

func scanTeam(scan func(dest ...any) error) (TeamRow, error) {
	var row TeamRow
	var canCreate int64
	err := scan(&row.ID, &row.OrgID, &row.Name, &row.LowerName, &row.Description,
		&row.Permission, &row.RepoScope, &canCreate, &row.CreatedAt, &row.UpdatedAt)
	row.CanCreateRep = canCreate == 1
	return row, err
}

func lowerName(name string) string {
	return strings.ToLower(name)
}

func (tx *ReadTx) GetOrg(id int64) (OrgRow, error) {
	inner, err := tx.live()
	if err != nil {
		return OrgRow{}, err
	}
	row, serr := scanOrg(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlOrgColumns+` FROM orgs WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return OrgRow{}, fmt.Errorf("GetOrg: %w: org %d", ErrNotFound, id)
	}
	return row, serr
}

func (tx *ReadTx) FindOrgByName(name string) (OrgRow, error) {
	inner, err := tx.live()
	if err != nil {
		return OrgRow{}, err
	}
	row, serr := scanOrg(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlOrgColumns+` FROM orgs WHERE lower_name = ?`, lowerName(name)).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return OrgRow{}, fmt.Errorf("FindOrgByName: %w: %s", ErrNotFound, name)
	}
	return row, serr
}

func (tx *ReadTx) ListOrgsForUser(userID int64, after string, limit int) ([]OrgRow, error) {
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
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlOrgColumns+` FROM orgs WHERE id IN
(SELECT DISTINCT t.org_id FROM teams t JOIN team_members m ON m.team_id = t.id WHERE m.user_id = ?)
AND lower_name > ? ORDER BY lower_name LIMIT ?`, userID, after, limit)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []OrgRow
	for rows.Next() {
		row, serr := scanOrg(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func countOrgRepos(ctx context.Context, inner *sql.Tx, orgID int64) (int64, error) {
	var n int64
	if serr := inner.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM repos WHERE org_id = ? AND deleted_at IS NULL`, orgID).Scan(&n); serr != nil {
		return 0, serr
	}
	return n, nil
}

func (tx *ReadTx) CountOrgRepos(orgID int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	return countOrgRepos(tx.ctx, inner, orgID)
}

func (tx *WriteTx) CountOrgRepos(orgID int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	return countOrgRepos(tx.ctx, inner, orgID)
}

type OrgInsert struct {
	Name        string
	DisplayName string
	Description string
	Website     string
	Visibility  string
}

type TeamInsert struct {
	Name        string
	Description string
	Permission  string
	RepoScope   string
	OrgID       int64
	CanCreate   bool
}

func (tx *WriteTx) InsertOrg(now time.Time, in OrgInsert) (OrgRow, error) {
	name, displayName, description, website, visibility := in.Name, in.DisplayName, in.Description, in.Website, in.Visibility
	inner, err := tx.live()
	if err != nil {
		return OrgRow{}, err
	}
	at := msOf(now)
	var row OrgRow
	serr := inner.QueryRowContext(tx.ctx, `INSERT INTO orgs
(name, lower_name, display_name, description, website, visibility, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING `+sqlOrgColumns,
		name, lowerName(name), displayName, description, website, visibility, at, at).Scan(
		&row.ID, &row.Name, &row.LowerName, &row.DisplayName, &row.Description,
		&row.Website, &row.Visibility, &row.CreatedAt, &row.UpdatedAt)
	if serr != nil {
		return OrgRow{}, mapErr(serr)
	}
	if _, terr := inner.ExecContext(tx.ctx, `INSERT INTO teams
(org_id, name, lower_name, description, permission, repo_scope, can_create_repo, created_at, updated_at)
VALUES (?, ?, ?, '', 'admin', 'all', 1, ?, ?)`,
		row.ID, ownersTeamName, lowerName(ownersTeamName), at, at); terr != nil {
		return OrgRow{}, mapErr(terr)
	}
	return row, nil
}

func (tx *WriteTx) AddOwnersMember(orgID, userID int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	var teamID int64
	if serr := inner.QueryRowContext(tx.ctx,
		`SELECT id FROM teams WHERE org_id = ? AND lower_name = ?`, orgID, lowerName(ownersTeamName)).Scan(&teamID); serr != nil {
		return serr
	}
	_, ierr := inner.ExecContext(tx.ctx,
		`INSERT INTO team_members (team_id, user_id) VALUES (?, ?)
ON CONFLICT (team_id, user_id) DO NOTHING`, teamID, userID)
	return ierr
}

func (tx *WriteTx) UpdateOrg(id int64, now time.Time, displayName, description, website, visibility *string) (OrgRow, error) {
	inner, err := tx.live()
	if err != nil {
		return OrgRow{}, err
	}
	query := `UPDATE orgs SET updated_at = ?`
	args := []any{msOf(now)}
	if displayName != nil {
		query += `, display_name = ?`
		args = append(args, *displayName)
	}
	if description != nil {
		query += `, description = ?`
		args = append(args, *description)
	}
	if website != nil {
		query += `, website = ?`
		args = append(args, *website)
	}
	if visibility != nil {
		query += `, visibility = ?`
		args = append(args, *visibility)
	}
	args = append(args, id)
	res, uerr := inner.ExecContext(tx.ctx, query+` WHERE id = ?`, args...)
	if uerr != nil {
		return OrgRow{}, mapErr(uerr)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return OrgRow{}, fmt.Errorf("UpdateOrg: %w: org %d", ErrNotFound, id)
	}
	return tx.GetOrgForUpdate(id)
}

func (tx *WriteTx) GetOrgForUpdate(id int64) (OrgRow, error) {
	inner, err := tx.live()
	if err != nil {
		return OrgRow{}, err
	}
	row, serr := scanOrg(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlOrgColumns+` FROM orgs WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return OrgRow{}, fmt.Errorf("GetOrgForUpdate: %w: org %d", ErrNotFound, id)
	}
	return row, serr
}

func (tx *WriteTx) RenameOrg(id int64, now time.Time, name string) (OrgRow, error) {
	inner, err := tx.live()
	if err != nil {
		return OrgRow{}, err
	}
	current, cerr := tx.GetOrgForUpdate(id)
	if cerr != nil {
		return OrgRow{}, cerr
	}
	res, uerr := inner.ExecContext(tx.ctx,
		`UPDATE orgs SET name = ?, lower_name = ?, updated_at = ? WHERE id = ?`,
		name, lowerName(name), msOf(now), id)
	if uerr != nil {
		return OrgRow{}, mapErr(uerr)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return OrgRow{}, fmt.Errorf("RenameOrg: %w: org %d", ErrNotFound, id)
	}
	if _, rerr := inner.ExecContext(tx.ctx,
		`INSERT INTO org_redirects (old_lower_name, org_id, created_at) VALUES (?, ?, ?)
ON CONFLICT (old_lower_name) DO UPDATE SET org_id = excluded.org_id, created_at = excluded.created_at`,
		current.LowerName, id, msOf(now)); rerr != nil {
		return OrgRow{}, mapErr(rerr)
	}
	return tx.GetOrgForUpdate(id)
}

func (tx *ReadTx) FollowOrgRedirect(name string) (OrgRow, error) {
	inner, err := tx.live()
	if err != nil {
		return OrgRow{}, err
	}
	var id int64
	if serr := inner.QueryRowContext(tx.ctx,
		`SELECT org_id FROM org_redirects WHERE old_lower_name = ?`, lowerName(name)).Scan(&id); serr != nil {
		if errors.Is(serr, sql.ErrNoRows) {
			return OrgRow{}, fmt.Errorf("FollowOrgRedirect: %w: %s", ErrNotFound, name)
		}
		return OrgRow{}, serr
	}
	return tx.GetOrg(id)
}

func (tx *WriteTx) DeleteOrg(id int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	res, derr := inner.ExecContext(tx.ctx, `DELETE FROM orgs WHERE id = ?`, id)
	if derr != nil {
		return mapErr(derr)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("DeleteOrg: %w: org %d", ErrNotFound, id)
	}
	return nil
}

func (tx *ReadTx) GetTeam(id int64) (TeamRow, error) {
	inner, err := tx.live()
	if err != nil {
		return TeamRow{}, err
	}
	row, serr := scanTeam(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlTeamColumns+` FROM teams WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return TeamRow{}, fmt.Errorf("GetTeam: %w: team %d", ErrNotFound, id)
	}
	return row, serr
}

func (tx *ReadTx) FindTeam(orgID int64, name string) (TeamRow, error) {
	inner, err := tx.live()
	if err != nil {
		return TeamRow{}, err
	}
	row, serr := scanTeam(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlTeamColumns+` FROM teams WHERE org_id = ? AND lower_name = ?`,
		orgID, lowerName(name)).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return TeamRow{}, fmt.Errorf("FindTeam: %w: %s", ErrNotFound, name)
	}
	return row, serr
}

func (tx *ReadTx) ListTeams(orgID int64, after string, limit int) ([]TeamRow, error) {
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
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlTeamColumns+` FROM teams
WHERE org_id = ? AND lower_name > ? ORDER BY lower_name LIMIT ?`, orgID, after, limit)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []TeamRow
	for rows.Next() {
		row, serr := scanTeam(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (tx *ReadTx) IsOwnersTeam(teamID int64) (bool, error) {
	team, err := tx.GetTeam(teamID)
	if err != nil {
		return false, err
	}
	return team.LowerName == lowerName(ownersTeamName), nil
}

func (tx *WriteTx) InsertTeam(now time.Time, in TeamInsert) (TeamRow, error) {
	inner, err := tx.live()
	if err != nil {
		return TeamRow{}, err
	}
	name, description, permission, repoScope := in.Name, in.Description, in.Permission, in.RepoScope
	if lowerName(name) == lowerName(ownersTeamName) {
		return TeamRow{}, fmt.Errorf("InsertTeam: %w: owners team is reserved", ErrInvalid)
	}
	at := msOf(now)
	create := 0
	if in.CanCreate {
		create = 1
	}
	var row TeamRow
	var created int64
	serr := inner.QueryRowContext(tx.ctx, `INSERT INTO teams
(org_id, name, lower_name, description, permission, repo_scope, can_create_repo, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING `+sqlTeamColumns,
		in.OrgID, name, lowerName(name), description, permission, repoScope, create, at, at).Scan(
		&row.ID, &row.OrgID, &row.Name, &row.LowerName, &row.Description,
		&row.Permission, &row.RepoScope, &created, &row.CreatedAt, &row.UpdatedAt)
	if serr != nil {
		return TeamRow{}, mapErr(serr)
	}
	row.CanCreateRep = created == 1
	return row, nil
}

func (tx *WriteTx) UpdateTeam(id int64, now time.Time, name, description, permission, repoScope *string) (TeamRow, error) {
	inner, err := tx.live()
	if err != nil {
		return TeamRow{}, err
	}
	current, cerr := tx.GetTeamForUpdate(id)
	if cerr != nil {
		return TeamRow{}, cerr
	}
	if current.LowerName == lowerName(ownersTeamName) {
		if (name != nil && lowerName(*name) != current.LowerName) ||
			(permission != nil && *permission != current.Permission) ||
			(repoScope != nil && *repoScope != current.RepoScope) {
			return TeamRow{}, fmt.Errorf("UpdateTeam: %w: owners team is immutable", ErrInvalid)
		}
	}
	query := `UPDATE teams SET updated_at = ?`
	args := []any{msOf(now)}
	if name != nil {
		if lowerName(*name) == lowerName(ownersTeamName) {
			return TeamRow{}, fmt.Errorf("UpdateTeam: %w: owners team is reserved", ErrInvalid)
		}
		query += `, name = ?, lower_name = ?`
		args = append(args, *name, lowerName(*name))
	}
	if description != nil {
		query += `, description = ?`
		args = append(args, *description)
	}
	if permission != nil {
		query += `, permission = ?`
		args = append(args, *permission)
	}
	if repoScope != nil {
		query += `, repo_scope = ?`
		args = append(args, *repoScope)
	}
	args = append(args, id)
	if _, uerr := inner.ExecContext(tx.ctx, query+` WHERE id = ?`, args...); uerr != nil {
		return TeamRow{}, mapErr(uerr)
	}
	return tx.GetTeamForUpdate(id)
}

func (tx *WriteTx) GetTeamForUpdate(id int64) (TeamRow, error) {
	inner, err := tx.live()
	if err != nil {
		return TeamRow{}, err
	}
	row, serr := scanTeam(inner.QueryRowContext(tx.ctx,
		`SELECT `+sqlTeamColumns+` FROM teams WHERE id = ?`, id).Scan)
	if errors.Is(serr, sql.ErrNoRows) {
		return TeamRow{}, fmt.Errorf("GetTeamForUpdate: %w: team %d", ErrNotFound, id)
	}
	return row, serr
}

func (tx *WriteTx) DeleteTeam(id int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	current, cerr := tx.GetTeamForUpdate(id)
	if cerr != nil {
		return cerr
	}
	if current.LowerName == lowerName(ownersTeamName) {
		return fmt.Errorf("DeleteTeam: %w: owners team cannot be deleted", ErrInvalid)
	}
	res, derr := inner.ExecContext(tx.ctx, `DELETE FROM teams WHERE id = ?`, id)
	if derr != nil {
		return mapErr(derr)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("DeleteTeam: %w: team %d", ErrNotFound, id)
	}
	return nil
}

func (tx *ReadTx) ListTeamMembers(teamID int64, after string, limit int) ([]OrgMemberRow, error) {
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
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT u.id, u.name FROM team_members m
JOIN users u ON u.id = m.user_id WHERE m.team_id = ? AND u.lower_name > ?
ORDER BY u.lower_name LIMIT ?`, teamID, after, limit)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []OrgMemberRow
	for rows.Next() {
		var row OrgMemberRow
		if serr := rows.Scan(&row.UserID, &row.Username); serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (tx *ReadTx) CountTeamMembers(teamID int64) (int64, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, err
	}
	var n int64
	if serr := inner.QueryRowContext(tx.ctx,
		`SELECT COUNT(*) FROM team_members WHERE team_id = ?`, teamID).Scan(&n); serr != nil {
		return 0, serr
	}
	return n, nil
}

func (tx *ReadTx) ListOrgMembers(orgID int64, after string, limit int) ([]OrgMemberRow, error) {
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
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT u.id, u.name,
(SELECT GROUP_CONCAT(t.name, ',') FROM team_members m JOIN teams t ON t.id = m.team_id
 WHERE t.org_id = ? AND m.user_id = u.id) AS teams
FROM users u WHERE u.id IN
(SELECT DISTINCT m.user_id FROM team_members m JOIN teams t ON t.id = m.team_id WHERE t.org_id = ?)
AND u.lower_name > ? ORDER BY u.lower_name LIMIT ?`, orgID, orgID, after, limit)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	owners, oerr := tx.orgOwnerIDs(orgID)
	if oerr != nil {
		return nil, oerr
	}
	var out []OrgMemberRow
	for rows.Next() {
		var row OrgMemberRow
		var teams sql.NullString
		if serr := rows.Scan(&row.UserID, &row.Username, &teams); serr != nil {
			return nil, serr
		}
		if teams.Valid && teams.String != "" {
			row.Teams = strings.Split(teams.String, ",")
		}
		row.Role = "member"
		if owners[row.UserID] {
			row.Role = "owner"
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (tx *ReadTx) orgOwnerIDs(orgID int64) (map[int64]bool, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT m.user_id FROM team_members m
JOIN teams t ON t.id = m.team_id WHERE t.org_id = ? AND t.lower_name = ?`,
		orgID, lowerName(ownersTeamName))
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if serr := rows.Scan(&id); serr != nil {
			return nil, serr
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (tx *ReadTx) IsOrgOwnerAtRead(orgID, userID int64) (bool, error) {
	owners, err := tx.orgOwnerIDs(orgID)
	if err != nil {
		return false, err
	}
	return owners[userID], nil
}

func (tx *ReadTx) IsOrgMemberAtRead(orgID, userID int64) (bool, error) {
	inner, err := tx.live()
	if err != nil {
		return false, err
	}
	var n int64
	if serr := inner.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM team_members m
JOIN teams t ON t.id = m.team_id WHERE t.org_id = ? AND m.user_id = ?`, orgID, userID).Scan(&n); serr != nil {
		return false, serr
	}
	return n > 0, nil
}

func userTeamsInOrg(ctx context.Context, inner *sql.Tx, orgID, userID int64) ([]TeamRow, error) {
	rows, qerr := inner.QueryContext(ctx, `SELECT `+sqlTeamColumns+` FROM teams
WHERE org_id = ? AND id IN (SELECT team_id FROM team_members WHERE user_id = ?)
ORDER BY lower_name`, orgID, userID)
	if qerr != nil {
		return nil, qerr
	}
	defer func() { _ = rows.Close() }()
	var out []TeamRow
	for rows.Next() {
		row, serr := scanTeam(rows.Scan)
		if serr != nil {
			return nil, serr
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (tx *ReadTx) UserTeamsInOrg(orgID, userID int64) ([]TeamRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	return userTeamsInOrg(tx.ctx, inner, orgID, userID)
}

func (tx *WriteTx) UserTeamsInOrg(orgID, userID int64) ([]TeamRow, error) {
	inner, err := tx.live()
	if err != nil {
		return nil, err
	}
	return userTeamsInOrg(tx.ctx, inner, orgID, userID)
}

func (tx *WriteTx) AddTeamMember(teamID, userID int64) (bool, error) {
	inner, err := tx.live()
	if err != nil {
		return false, err
	}
	res, ierr := inner.ExecContext(tx.ctx,
		`INSERT INTO team_members (team_id, user_id) VALUES (?, ?)
ON CONFLICT (team_id, user_id) DO NOTHING`, teamID, userID)
	if ierr != nil {
		return false, mapErr(ierr)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (tx *WriteTx) RemoveTeamMember(teamID, userID int64) (bool, error) {
	inner, err := tx.live()
	if err != nil {
		return false, err
	}
	team, terr := tx.GetTeamForUpdate(teamID)
	if terr != nil {
		return false, terr
	}
	if team.LowerName == lowerName(ownersTeamName) {
		var n int64
		if serr := inner.QueryRowContext(tx.ctx,
			`SELECT COUNT(*) FROM team_members WHERE team_id = ?`, teamID).Scan(&n); serr != nil {
			return false, serr
		}
		var isOwner int64
		if serr := inner.QueryRowContext(tx.ctx,
			`SELECT COUNT(*) FROM team_members WHERE team_id = ? AND user_id = ?`,
			teamID, userID).Scan(&isOwner); serr != nil {
			return false, serr
		}
		if isOwner == 1 && n <= 1 {
			return false, fmt.Errorf("RemoveTeamMember: %w: last owner", ErrInvalid)
		}
	}
	res, derr := inner.ExecContext(tx.ctx,
		`DELETE FROM team_members WHERE team_id = ? AND user_id = ?`, teamID, userID)
	if derr != nil {
		return false, mapErr(derr)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (tx *ReadTx) ListTeamRepos(teamID int64, after string, limit int) ([]RepoRow, error) {
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
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlRepoColumns+` FROM repos
WHERE id IN (SELECT repo_id FROM team_repos WHERE team_id = ?) AND deleted_at IS NULL
AND name > ? ORDER BY name LIMIT ?`, teamID, after, limit)
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

func (tx *ReadTx) ListOrgRepos(orgID int64, limit int) ([]RepoRow, error) {
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
	rows, qerr := inner.QueryContext(tx.ctx, `SELECT `+sqlRepoColumns+` FROM repos
WHERE org_id = ? AND deleted_at IS NULL ORDER BY name LIMIT ?`, orgID, limit)
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

func (tx *WriteTx) AddTeamRepo(teamID, repoID int64) error {
	inner, err := tx.live()
	if err != nil {
		return err
	}
	team, terr := tx.GetTeamForUpdate(teamID)
	if terr != nil {
		return terr
	}
	if team.RepoScope == "all" {
		return fmt.Errorf("AddTeamRepo: %w: team grants every repo", ErrInvalid)
	}
	var orgID int64
	if serr := inner.QueryRowContext(tx.ctx,
		`SELECT org_id FROM repos WHERE id = ? AND deleted_at IS NULL`, repoID).Scan(&orgID); serr != nil {
		if errors.Is(serr, sql.ErrNoRows) {
			return fmt.Errorf("AddTeamRepo: %w: repo %d", ErrNotFound, repoID)
		}
		return serr
	}
	if orgID != team.OrgID {
		return fmt.Errorf("AddTeamRepo: %w: repo is outside the team org", ErrInvalid)
	}
	_, ierr := inner.ExecContext(tx.ctx,
		`INSERT INTO team_repos (team_id, repo_id) VALUES (?, ?)
ON CONFLICT (team_id, repo_id) DO NOTHING`, teamID, repoID)
	return mapErr(ierr)
}

func (tx *WriteTx) RemoveTeamRepo(teamID, repoID int64) (bool, error) {
	inner, err := tx.live()
	if err != nil {
		return false, err
	}
	team, terr := tx.GetTeamForUpdate(teamID)
	if terr != nil {
		return false, terr
	}
	if team.RepoScope == "all" {
		return false, fmt.Errorf("RemoveTeamRepo: %w: team grants every repo", ErrInvalid)
	}
	res, derr := inner.ExecContext(tx.ctx,
		`DELETE FROM team_repos WHERE team_id = ? AND repo_id = ?`, teamID, repoID)
	if derr != nil {
		return false, mapErr(derr)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
