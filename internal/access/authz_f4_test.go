package access

import (
	"testing"
)

func teamFixture() *MemResolver {
	m := baseFixture()
	m.TeamList[9] = &MemTeam{Org: org7, Repos: map[int64]Role{rOrgPriv: RoleWrite}, Members: map[int64]bool{uMember: true}}
	m.TeamList[10] = &MemTeam{Org: org7, Repos: map[int64]Role{rOrgPriv: RoleMaintain}, Members: map[int64]bool{uOutsider: true}}
	m.TeamList[11] = &MemTeam{Org: org8, Repos: map[int64]Role{rOrgPriv: RoleAdmin}, Members: map[int64]bool{uMember: true}}
	return m
}

func orgResource(id int64) Resource {
	return Resource{Kind: KindOrg, OrgID: id}
}

func teamResource(org, team int64) Resource {
	return Resource{Kind: KindTeam, OrgID: org, TeamID: team}
}

func TestAuthzWorkedResolutions(t *testing.T) {
	m := teamFixture()
	rows := []struct {
		name   string
		caller Caller
		action string
		res    Resource
		allow  bool
		reason string
	}{
		{"owner-team admin pushes private org repo", sessCaller(uOrgOwner), "repo.push", repoResource(rOrgPriv), true, ""},
		{"owner-team admin deletes private org repo", sessCaller(uOrgOwner), "repo.delete", repoResource(rOrgPriv), true, ""},
		{"team write pushes granted repo", sessCaller(uMember), "repo.push", repoResource(rOrgPriv), true, ""},
		{"team write views granted repo", sessCaller(uMember), "repo.view", repoResource(rOrgPriv), true, ""},
		{"maintain team pushes granted repo", sessCaller(uOutsider), "repo.push", repoResource(rOrgPriv), true, ""},
		{"member views via own-org team", sessCaller(uMember), "repo.view", repoResource(rOrgPriv), true, ""},
		{"stranger cannot view private org repo", sessCaller(uOutsider), "repo.view", repoResource(rUserPriv), false, "invisible"},
		{"anon cannot view private org", anonCaller(), "org.get", orgResource(org7), false, "invisible"},
		{"member views private org", sessCaller(uMember), "org.get", orgResource(org7), true, ""},
		{"owner views private org", sessCaller(uOrgOwner), "org.get", orgResource(org7), true, ""},
		{"token cannot create team", tokenCaller(uOrgOwner, adminScopes(), false), "team.create", teamResource(org7, 9), false, "session-required"},
		{"token cannot list teams", tokenCaller(uMember, readScopes(), false), "team.list", teamResource(org7, 9), false, "session-required"},
		{"token cannot create org", tokenCaller(uMember, adminScopes(), false), "org.create", orgResource(org7), false, "session-required"},
		{"session creates org", sessCaller(uMember), "org.create", orgResource(org7), true, ""},
		{"session creates org site-scoped", sessCaller(uMember), "org.create", Resource{Kind: KindSite}, true, ""},
		{"session lists orgs site-scoped", sessCaller(uMember), "org.list", Resource{Kind: KindSite}, true, ""},
		{"member lists teams", sessCaller(uMember), "team.list", teamResource(org7, 9), true, ""},
		{"nonmember cannot list teams", sessCaller(uOwner), "team.list", teamResource(org7, 9), false, "invisible"},
		{"owner manages team", sessCaller(uOrgOwner), "team.member.add", teamResource(org7, 9), true, ""},
		{"member cannot manage team", sessCaller(uMember), "team.member.add", teamResource(org7, 9), false, "precondition"},
		{"owner leaves guard allows member", sessCaller(uMember), "org.leave", orgResource(org7), true, ""},
		{"nonmember cannot leave", sessCaller(uOwner), "org.leave", orgResource(org7), false, "invisible"},
	}
	for _, row := range rows {
		d := Check(m, row.caller, row.action, row.res)
		expect(t, "worked/"+row.name, d, row.allow, row.reason)
	}
}

func TestOrgOwnersTeamAdmin(t *testing.T) {
	m := NewMemResolver()
	m.Repos[rOrgPriv] = RepoRecord{ID: rOrgPriv, OrgID: org7, Visibility: VisibilityPrivate}
	m.OrgMembers[org7] = map[int64]bool{uOrgOwner: true}
	m.OrgOwners[org7] = map[int64]bool{uOrgOwner: true}
	c := sessCaller(uOrgOwner)
	expect(t, "owners-admin/view", Check(m, c, "repo.view", repoResource(rOrgPriv)), true, "")
	expect(t, "owners-admin/push", Check(m, c, "repo.push", repoResource(rOrgPriv)), true, "")
	m.RuleSets[rOrgPriv] = nil
	m.Repos[rOrgInt] = RepoRecord{ID: rOrgInt, OrgID: org7, Visibility: VisibilityInternal}
	expect(t, "owners-admin/internal", Check(m, c, "repo.view", repoResource(rOrgInt)), true, "")
	plain := sessCaller(uMember)
	m.OrgMembers[org7] = map[int64]bool{uMember: true}
	expect(t, "owners-admin/nonowner", Check(m, plain, "repo.view", repoResource(rOrgPriv)), false, "invisible")
	expect(t, "owners-admin/nonowner-delete", Check(m, plain, "repo.delete", repoResource(rOrgPriv)), false, "invisible")
}

func TestTeamMemberAddImpliesOrgMember(t *testing.T) {
	m := NewMemResolver()
	m.Repos[rOrgPriv] = RepoRecord{ID: rOrgPriv, OrgID: org7, Visibility: VisibilityPrivate}
	m.TeamList[21] = &MemTeam{Org: org7, Repos: map[int64]Role{}, Members: map[int64]bool{uMember: true}}
	if !m.IsOrgMember(org7, uMember) {
		t.Fatal("team member is not an org member")
	}
	if m.IsOrgOwner(org7, uMember) {
		t.Fatal("plain team member reads as owner")
	}
	c := sessCaller(uMember)
	expect(t, "implied/leaves", Check(m, c, "org.leave", orgResource(org7)), true, "")
	expect(t, "implied/teams", Check(m, c, "team.list", teamResource(org7, 21)), true, "")
	expect(t, "implied/no-grant", Check(m, c, "repo.view", repoResource(rOrgPriv)), false, "invisible")
	m.TeamList[22] = &MemTeam{Org: org7, Repos: map[int64]Role{rOrgPriv: RoleRead}, Members: map[int64]bool{uMember: true}}
	expect(t, "implied/grant", Check(m, c, "repo.view", repoResource(rOrgPriv)), true, "")
}

func TestLastOwnerGuard(t *testing.T) {
	if err := GuardLastOwner([]int64{uOrgOwner}, uOrgOwner); err == nil {
		t.Fatal("sole owner removal allowed")
	}
	if err := GuardLastOwner([]int64{uOrgOwner, uMember}, uOrgOwner); err != nil {
		t.Fatalf("shared owner removal blocked: %v", err)
	}
	if err := GuardLastOwner([]int64{uOrgOwner, uMember}, uMember); err != nil {
		t.Fatalf("non-owner removal blocked: %v", err)
	}
	if err := GuardLastOwner([]int64{uOrgOwner, uMember}, uOutsider); err != nil {
		t.Fatalf("absent user removal blocked: %v", err)
	}
	if err := GuardLastOwner(nil, uOrgOwner); err == nil {
		t.Fatal("empty owner removal allowed")
	}
}

func TestTeamGrantProperties(t *testing.T) {
	t.Run("max-over-grants", func(t *testing.T) {
		m := teamFixture()
		d := Check(m, sessCaller(uOutsider), "repo.view", repoResource(rOrgPriv))
		expect(t, "team-max/view", d, true, "")
		if got, ok := m.TeamRole(rOrgPriv, uOutsider); !ok || got != RoleMaintain {
			t.Fatalf("TeamRole = %v, %v, want maintain true", got, ok)
		}
		if got, ok := m.TeamRole(rOrgPriv, uMember); !ok || got != RoleWrite {
			t.Fatalf("TeamRole = %v, %v, want write true", got, ok)
		}
	})
	t.Run("grant-confined-to-repo", func(t *testing.T) {
		m := teamFixture()
		if _, ok := m.TeamRole(rOrgInt, uMember); ok {
			t.Fatal("team grant leaked to an ungranted repo")
		}
		d := Check(m, sessCaller(uMember), "repo.view", repoResource(rOrgInt))
		expect(t, "team-confine/view", d, true, "")
		other := sessCaller(uMember)
		_ = other
		d2 := Check(m, sessCaller(uOutsider), "repo.push", repoResource(rOrgInt))
		if d2.Allow {
			t.Fatal("ungranted push allowed")
		}
	})
	t.Run("grant-confined-to-org", func(t *testing.T) {
		m := teamFixture()
		if _, ok := m.TeamRole(rOrgPriv, uMember); !ok {
			t.Fatal("org7 grant missing")
		}
		d := Check(m, sessCaller(uMember), "repo.view", repoResource(rOrgPriv))
		expect(t, "team-org/view", d, true, "")
	})
	t.Run("scope-ceiling-holds", func(t *testing.T) {
		m := teamFixture()
		tok := tokenCaller(uMember, readScopes(), false)
		d := Check(m, tok, "repo.view", repoResource(rOrgPriv))
		expect(t, "team-scope/view", d, true, "")
		d2 := Check(m, tok, "repo.push", repoResource(rOrgPriv))
		expect(t, "team-scope/push", d2, false, "")
		if d2.Allow {
			t.Fatal("read token pushed through a write team grant")
		}
	})
	t.Run("owners-team-ignores-repo-list", func(t *testing.T) {
		m := NewMemResolver()
		m.Repos[200] = RepoRecord{ID: 200, OrgID: org7, Visibility: VisibilityPrivate}
		m.OrgMembers[org7] = map[int64]bool{uOrgOwner: true}
		m.OrgOwners[org7] = map[int64]bool{uOrgOwner: true}
		d := Check(m, sessCaller(uOrgOwner), "repo.delete", repoResource(200))
		expect(t, "owners-any-repo", d, true, "")
	})
	t.Run("session-required-everywhere", func(t *testing.T) {
		m := teamFixture()
		full := tokenCaller(uOrgOwner, adminScopes(), false)
		for _, action := range []string{"org.create", "org.rename", "org.delete", "org.member.remove", "org.leave", "org.list",
			"team.list", "team.create", "team.get", "team.update", "team.delete",
			"team.member.list", "team.member.add", "team.member.remove",
			"team.repo.list", "team.repo.add", "team.repo.remove"} {
			res := orgResource(org7)
			if len(action) >= 4 && action[:4] == "team" {
				res = teamResource(org7, 9)
			}
			d := Check(m, full, action, res)
			if d.Allow || d.Reason != "session-required" {
				t.Fatalf("%s: allow=%v reason=%q, want session-required", action, d.Allow, d.Reason)
			}
		}
	})
}
