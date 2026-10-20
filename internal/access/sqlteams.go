package access

import (
	"context"
	"database/sql"
)

type StoreTeams struct {
	Resolver
	db *sql.DB
}

func NewStoreTeams(db *sql.DB, inner Resolver) *StoreTeams {
	if inner == nil {
		inner = NewMemResolver()
	}
	return &StoreTeams{Resolver: inner, db: db}
}

func (s *StoreTeams) TeamRole(repoID, userID int64) (Role, bool) {
	rows, err := s.db.QueryContext(context.Background(), `SELECT t.permission FROM teams t
JOIN team_members m ON m.team_id = t.id
JOIN repos r ON r.id = ? AND r.deleted_at IS NULL
WHERE m.user_id = ? AND t.org_id = r.org_id
AND (t.repo_scope = 'all' OR EXISTS
 (SELECT 1 FROM team_repos tr WHERE tr.team_id = t.id AND tr.repo_id = r.id))`,
		repoID, userID)
	if err != nil {
		return RoleNone, false
	}
	defer func() { _ = rows.Close() }()
	best := RoleNone
	found := false
	for rows.Next() {
		var raw string
		if serr := rows.Scan(&raw); serr != nil {
			return RoleNone, false
		}
		if r, ok := ParseRole(raw); ok {
			if !found || r > best {
				best = r
				found = true
			}
		}
	}
	if rerr := rows.Err(); rerr != nil {
		return RoleNone, false
	}
	return best, found
}

func (s *StoreTeams) IsOrgMember(orgID, userID int64) bool {
	var n int64
	err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM team_members m
JOIN teams t ON t.id = m.team_id WHERE t.org_id = ? AND m.user_id = ?`,
		orgID, userID).Scan(&n)
	return err == nil && n > 0
}

func (s *StoreTeams) IsOrgOwner(orgID, userID int64) bool {
	var n int64
	err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM team_members m
JOIN teams t ON t.id = m.team_id
WHERE t.org_id = ? AND t.lower_name = 'owners' AND m.user_id = ?`,
		orgID, userID).Scan(&n)
	return err == nil && n > 0
}

func (s *StoreTeams) CanCreateRepo(orgID, userID int64) bool {
	if s.IsOrgOwner(orgID, userID) {
		return true
	}
	var n int64
	err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM team_members m
JOIN teams t ON t.id = m.team_id
WHERE t.org_id = ? AND t.can_create_repo = 1 AND m.user_id = ?`,
		orgID, userID).Scan(&n)
	return err == nil && n > 0
}

func (s *StoreTeams) IsTeamMember(teamID, userID int64) bool {
	var n int64
	err := s.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM team_members WHERE team_id = ? AND user_id = ?`,
		teamID, userID).Scan(&n)
	return err == nil && n > 0
}

func (s *StoreTeams) TeamOrg(teamID int64) (int64, bool) {
	var org int64
	err := s.db.QueryRowContext(context.Background(),
		`SELECT org_id FROM teams WHERE id = ?`, teamID).Scan(&org)
	if err != nil {
		return 0, false
	}
	return org, true
}

func (s *StoreTeams) OrgPublic(orgID int64) bool {
	var vis string
	err := s.db.QueryRowContext(context.Background(),
		`SELECT visibility FROM orgs WHERE id = ?`, orgID).Scan(&vis)
	if err != nil {
		return false
	}
	return vis == "public"
}
