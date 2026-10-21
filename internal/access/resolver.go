package access

import (
	"context"
	"database/sql"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

type RepoRecord struct {
	Visibility  Visibility
	ID          int64
	OwnerUserID int64
	OrgID       int64
	Archived    bool
}

type Resolver interface {
	Repo(id int64) (RepoRecord, bool)
	DirectRole(repoID, userID int64) (Role, bool)
	TeamRole(repoID, userID int64) (Role, bool)
	Rules(repoID int64) []BookmarkRule
	IsOrgMember(orgID, userID int64) bool
	IsOrgOwner(orgID, userID int64) bool
	CanCreateRepo(orgID, userID int64) bool
	IsTeamMember(teamID, userID int64) bool
	TeamOrg(teamID int64) (int64, bool)
	OrgPublic(orgID int64) bool
	IsSiteAdmin(userID int64) bool
	IsActive(userID int64) bool
}

type MemTeam struct {
	Repos   map[int64]Role
	Members map[int64]bool
	Org     int64
}

type MemResolver struct {
	Repos        map[int64]RepoRecord
	Direct       map[int64]map[int64]Role
	TeamList     map[int64]*MemTeam
	RuleSets     map[int64][]BookmarkRule
	OrgMembers   map[int64]map[int64]bool
	OrgOwners    map[int64]map[int64]bool
	RepoCreators map[int64]map[int64]bool
	PublicOrgs   map[int64]bool
	Admins       map[int64]bool
	Inactive     map[int64]bool
}

func NewMemResolver() *MemResolver {
	return &MemResolver{
		Repos:        map[int64]RepoRecord{},
		Direct:       map[int64]map[int64]Role{},
		TeamList:     map[int64]*MemTeam{},
		RuleSets:     map[int64][]BookmarkRule{},
		OrgMembers:   map[int64]map[int64]bool{},
		OrgOwners:    map[int64]map[int64]bool{},
		RepoCreators: map[int64]map[int64]bool{},
		PublicOrgs:   map[int64]bool{},
		Admins:       map[int64]bool{},
		Inactive:     map[int64]bool{},
	}
}

func (m *MemResolver) Repo(id int64) (RepoRecord, bool) {
	r, ok := m.Repos[id]
	return r, ok
}

func (m *MemResolver) DirectRole(repoID, userID int64) (Role, bool) {
	r, ok := m.Direct[repoID][userID]
	return r, ok
}

func (m *MemResolver) TeamRole(repoID, userID int64) (Role, bool) {
	repo, ok := m.Repos[repoID]
	if !ok {
		return RoleNone, false
	}
	best := RoleNone
	found := false
	for _, team := range m.TeamList {
		if team.Org != repo.OrgID {
			continue
		}
		if !team.Members[userID] {
			continue
		}
		if r, ok := team.Repos[repoID]; ok {
			if !found || r > best {
				best = r
				found = true
			}
		}
	}
	return best, found
}

func (m *MemResolver) Rules(repoID int64) []BookmarkRule {
	return m.RuleSets[repoID]
}

func (m *MemResolver) IsOrgMember(orgID, userID int64) bool {
	if m.OrgMembers[orgID][userID] || m.OrgOwners[orgID][userID] {
		return true
	}
	for _, team := range m.TeamList {
		if team.Org == orgID && team.Members[userID] {
			return true
		}
	}
	return false
}

func (m *MemResolver) IsOrgOwner(orgID, userID int64) bool {
	return m.OrgOwners[orgID][userID]
}

func (m *MemResolver) CanCreateRepo(orgID, userID int64) bool {
	return m.RepoCreators[orgID][userID] || m.OrgOwners[orgID][userID]
}

func (m *MemResolver) IsTeamMember(teamID, userID int64) bool {
	team, ok := m.TeamList[teamID]
	return ok && team.Members[userID]
}

func (m *MemResolver) TeamOrg(teamID int64) (int64, bool) {
	team, ok := m.TeamList[teamID]
	if !ok {
		return 0, false
	}
	return team.Org, true
}

func (m *MemResolver) OrgPublic(orgID int64) bool {
	return m.PublicOrgs[orgID]
}

func (m *MemResolver) IsSiteAdmin(userID int64) bool {
	return m.Admins[userID]
}

func (m *MemResolver) IsActive(userID int64) bool {
	return !m.Inactive[userID]
}

type SQLResolver struct {
	db    *sql.DB
	teams Resolver
}

func NewSQLResolver(db *sql.DB, teams Resolver) *SQLResolver {
	if teams == nil {
		teams = NewMemResolver()
	}
	return &SQLResolver{db: db, teams: teams}
}

func (s *SQLResolver) Repo(id int64) (RepoRecord, bool) {
	var r RepoRecord
	var vis string
	var owner sql.NullInt64
	var archived int64
	ctx := context.Background()
	err := s.db.QueryRowContext(ctx, `SELECT id, owner_user_id, org_id, visibility, archived FROM repos WHERE id = ? AND deleted_at IS NULL`, id).Scan(
		&r.ID, &owner, &r.OrgID, &vis, &archived)
	if err != nil {
		return RepoRecord{}, false
	}
	r.Archived = archived != 0
	if owner.Valid {
		r.OwnerUserID = owner.Int64
	}
	r.Visibility = Visibility(vis)
	return r, true
}

func (s *SQLResolver) DirectRole(repoID, userID int64) (Role, bool) {
	var role string
	ctx := context.Background()
	err := s.db.QueryRowContext(ctx, `SELECT role FROM repo_grants WHERE repo_id = ? AND user_id = ?`, repoID, userID).Scan(&role)
	if err != nil {
		return RoleNone, false
	}
	r, ok := ParseRole(role)
	return r, ok
}

func (s *SQLResolver) TeamRole(repoID, userID int64) (Role, bool) {
	return s.teams.TeamRole(repoID, userID)
}

func (s *SQLResolver) Rules(repoID int64) []BookmarkRule {
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT id, repo_id, pattern, mode, bypass_admin,
		push_allow, land_allow, required, version FROM bookmark_rules WHERE repo_id = ?`, repoID)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var out []BookmarkRule
	for rows.Next() {
		var rule BookmarkRule
		var bypass int64
		var pushRaw, landRaw, reqRaw string
		if err := rows.Scan(&rule.ID, &rule.RepoID, &rule.Pattern, &rule.Mode, &bypass,
			&pushRaw, &landRaw, &reqRaw, &rule.Version); err != nil {
			return nil
		}
		rule.BypassAdmin = bypass != 0
		rule.PushAllow = parseAllowList(pushRaw)
		rule.LandAllow = parseAllowList(landRaw)
		rule.Required = parseRequiredList(reqRaw)
		out = append(out, rule)
	}
	return out
}

type allowJSON struct {
	Users []int64  `json:"users"`
	Teams []int64  `json:"teams"`
	Roles []string `json:"roles"`
}

type requiredJSON struct {
	Kind       string    `json:"kind"`
	Result     string    `json:"result"`
	Trusted    allowJSON `json:"trusted"`
	MinSigners int       `json:"min_signers"`
}

func allowFromJSON(a allowJSON) AllowEntry {
	var e AllowEntry
	e.Users = append(e.Users, a.Users...)
	e.Teams = append(e.Teams, a.Teams...)
	for _, r := range a.Roles {
		if role, ok := ParseRole(r); ok {
			e.Roles = append(e.Roles, role)
		}
	}
	return e
}

func parseAllowList(raw string) []AllowEntry {
	var items []allowJSON
	if err := jsonv2.Unmarshal([]byte(raw), &items, jsonv1.DefaultOptionsV1()); err != nil {
		return nil
	}
	var out []AllowEntry
	for _, item := range items {
		out = append(out, allowFromJSON(item))
	}
	return out
}

func parseRequiredList(raw string) []AttestationReq {
	var items []requiredJSON
	if err := jsonv2.Unmarshal([]byte(raw), &items, jsonv1.DefaultOptionsV1()); err != nil {
		return nil
	}
	var out []AttestationReq
	for _, item := range items {
		out = append(out, AttestationReq{
			Kind:       item.Kind,
			Result:     item.Result,
			MinSigners: item.MinSigners,
			Trusted:    allowFromJSON(item.Trusted),
		})
	}
	return out
}

func (s *SQLResolver) IsOrgMember(orgID, userID int64) bool {
	return s.teams.IsOrgMember(orgID, userID)
}

func (s *SQLResolver) IsOrgOwner(orgID, userID int64) bool {
	return s.teams.IsOrgOwner(orgID, userID)
}

func (s *SQLResolver) CanCreateRepo(orgID, userID int64) bool {
	return s.teams.CanCreateRepo(orgID, userID)
}

func (s *SQLResolver) IsTeamMember(teamID, userID int64) bool {
	return s.teams.IsTeamMember(teamID, userID)
}

func (s *SQLResolver) TeamOrg(teamID int64) (int64, bool) {
	return s.teams.TeamOrg(teamID)
}

func (s *SQLResolver) OrgPublic(orgID int64) bool {
	return s.teams.OrgPublic(orgID)
}

func (s *SQLResolver) IsSiteAdmin(userID int64) bool {
	var n int
	ctx := context.Background()
	_ = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ? AND is_admin != 0`, userID).Scan(&n)
	return n > 0
}

func (s *SQLResolver) IsActive(userID int64) bool {
	var active int
	ctx := context.Background()
	err := s.db.QueryRowContext(ctx, `SELECT is_active FROM users WHERE id = ?`, userID).Scan(&active)
	return err == nil && active != 0
}
