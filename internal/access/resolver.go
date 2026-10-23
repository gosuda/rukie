package access

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
