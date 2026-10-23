package access

import (
	"testing"

	"github.com/gosuda/rukie/internal/op"
)

const (
	uOwner    = 11
	uOutsider = 12
	uAdmin    = 13
	uMember   = 14
	uOrgOwner = 15

	rUserPriv = 101
	rUserPub  = 102
	rOrgInt   = 103
	rOrgPriv  = 104
	org7      = 7
	org8      = 8
)

func anonCaller() Caller {
	return Caller{Active: true}
}

func sessCaller(id int64) Caller {
	return Caller{UserID: id, Active: true, Authed: true}
}

func adminCaller() Caller {
	return Caller{UserID: uAdmin, Active: true, Authed: true, IsAdmin: true}
}

func tokenCaller(id int64, scopes op.Scopes, publicOnly bool) Caller {
	return Caller{UserID: id, Active: true, Authed: true, IsToken: true, Scopes: scopes, PublicOnly: publicOnly}
}

func readScopes() op.Scopes {
	return op.Scopes(0).Add(op.ScopeRepoRead)
}

func writeScopes() op.Scopes {
	return op.Scopes(0).Add(op.ScopeRepoRead).Add(op.ScopeRepoWrite)
}

func createScopes() op.Scopes {
	return op.Scopes(0).Add(op.ScopeRepoCreate)
}

func adminScopes() op.Scopes {
	var out op.Scopes
	for _, s := range op.AllScopes() {
		out = out.Add(s)
	}
	return out
}

func baseFixture() *MemResolver {
	m := NewMemResolver()
	m.Repos[rUserPriv] = RepoRecord{ID: rUserPriv, OwnerUserID: uOwner, Visibility: VisibilityPrivate}
	m.Repos[rUserPub] = RepoRecord{ID: rUserPub, OwnerUserID: uOwner, Visibility: VisibilityPublic}
	m.Repos[rOrgInt] = RepoRecord{ID: rOrgInt, OrgID: org7, Visibility: VisibilityInternal}
	m.Repos[rOrgPriv] = RepoRecord{ID: rOrgPriv, OrgID: org7, Visibility: VisibilityPrivate}
	m.OrgMembers[org7] = map[int64]bool{uMember: true, uOrgOwner: true}
	m.OrgOwners[org7] = map[int64]bool{uOrgOwner: true}
	m.Admins[uAdmin] = true
	return m
}

func repoResource(id int64) Resource {
	return Resource{Kind: KindRepo, RepoID: id}
}

func expect(t *testing.T, inv string, d Decision, allow bool, reason string) {
	t.Helper()
	if d.Allow != allow {
		t.Fatalf("%s: allow = %v, want %v (reason %q)", inv, d.Allow, allow, d.Reason)
	}
	if !allow && reason != "" && d.Reason != reason {
		t.Fatalf("%s: reason = %q, want %q", inv, d.Reason, reason)
	}
	if !allow && reason == "" && d.NotFound == (d.Reason != "invisible") {
		t.Fatalf("%s: 404/403 mismatch for reason %q", inv, d.Reason)
	}
}

func TestPermissionInvariants(t *testing.T) {
	t.Run("INV-1/scope-never-widens", func(t *testing.T) {
		m := baseFixture()
		m.Direct[rUserPriv] = map[int64]Role{uOutsider: RoleWrite}
		actions := []Resource{
			repoResource(rUserPriv), repoResource(rUserPub), repoResource(rOrgInt),
		}
		names := []string{"repo.view", "repo.push", "repo.clone", "change.view", "ci.view"}
		scopeSets := []op.Scopes{readScopes(), writeScopes(), createScopes(), adminScopes(), op.Scopes(0)}
		for _, res := range actions {
			for _, name := range names {
				for _, scopes := range scopeSets {
					tok := tokenCaller(uOutsider, scopes, false)
					if d := Check(m, tok, name, res); d.Allow {
						if d2 := Check(m, sessCaller(uOutsider), name, res); !d2.Allow {
							t.Fatalf("INV-1: token allow without session allow (%s %v)", name, res)
						}
					}
				}
			}
		}
		inactive := Caller{UserID: uOwner, Active: false, Authed: true, IsToken: true, Scopes: readScopes()}
		self := Resource{Kind: KindUserAcct, OwnerID: uOwner}
		expect(t, "INV-1", Check(m, inactive, "self.read", self), true, "")
		bare := Caller{UserID: uOwner, Active: false, Authed: true, IsToken: true}
		expect(t, "INV-1", Check(m, bare, "self.read", self), false, "scope")
		expect(t, "INV-1", Check(m, inactive, "repo.view", repoResource(rUserPub)), false, "inactive")
	})
	t.Run("INV-2/admin-token-clamped", func(t *testing.T) {
		m := baseFixture()
		m.Direct[rUserPub] = map[int64]Role{uAdmin: RoleWrite}
		actions := []struct {
			name   string
			res    Resource
			scopes op.Scopes
			allow  bool
		}{
			{"repo.push", repoResource(rUserPub), readScopes(), false},
			{"repo.push", repoResource(rUserPub), writeScopes(), true},
			{"repo.visibility_widen", repoResource(rUserPub), readScopes(), false},
			{"repo.view", repoResource(rUserPriv), readScopes(), false},
			{"site.admin_console", Resource{Kind: KindRepo, RepoID: rUserPriv}, adminScopes(), true},
			{"site.admin_console", Resource{Kind: KindRepo, RepoID: rUserPriv}, readScopes(), false},
		}
		for _, a := range actions {
			d := Check(m, tokenCaller(uAdmin, a.scopes, false), a.name, a.res)
			expect(t, "INV-2", d, a.allow, "")
		}
	})
	t.Run("INV-3/private-invisible", func(t *testing.T) {
		m := baseFixture()
		callers := []Caller{anonCaller(), sessCaller(uOutsider), adminCaller()}
		for _, c := range callers {
			expect(t, "INV-3", Check(m, c, "repo.view", repoResource(rOrgPriv)), false, "invisible")
			expect(t, "INV-3", Check(m, c, "repo.push", repoResource(rOrgPriv)), false, "invisible")
		}
		d := Check(m, adminCaller(), "site.admin_console", Resource{Kind: KindRepo, RepoID: rOrgPriv})
		expect(t, "INV-3", d, true, "")
	})
	t.Run("INV-4/internal-hidden", func(t *testing.T) {
		m := baseFixture()
		expect(t, "INV-4", Check(m, anonCaller(), "repo.view", repoResource(rOrgInt)), false, "invisible")
		po := tokenCaller(uOutsider, readScopes(), true)
		expect(t, "INV-4", Check(m, po, "repo.view", repoResource(rOrgInt)), false, "invisible")
		expect(t, "INV-4", Check(m, sessCaller(uOutsider), "repo.view", repoResource(rOrgInt)), true, "")
	})
	t.Run("INV-5/listing-subset-of-viewable", func(t *testing.T) {
		m := baseFixture()
		all := []int64{rUserPriv, rUserPub, rOrgInt, rOrgPriv}
		callers := []Caller{anonCaller(), sessCaller(uOutsider), sessCaller(uOwner), adminCaller(), sessCaller(uMember)}
		for _, c := range callers {
			for _, id := range FilterVisible(m, c, all) {
				if d := Check(m, c, "repo.view", repoResource(id)); !d.Allow {
					t.Fatalf("INV-5: listed %d not viewable", id)
				}
			}
		}
		if got := FilterVisible(m, anonCaller(), all); len(got) != 1 || got[0] != rUserPub {
			t.Fatalf("INV-5: anon sees %v", got)
		}
	})
	t.Run("INV-6/fork-monotonicity", func(t *testing.T) {
		vis := []Visibility{VisibilityPublic, VisibilityInternal, VisibilityPrivate}
		for _, src := range vis {
			for _, fork := range vis {
				want := src == VisibilityPublic || (src == VisibilityInternal && fork != VisibilityPublic) || src == fork
				if got := ForkVisibilityOK(src, fork); got != want {
					t.Fatalf("INV-6: source %s fork %s = %v", src, fork, got)
				}
			}
		}
	})
	t.Run("INV-7/membership-alone-is-nothing", func(t *testing.T) {
		m := baseFixture()
		c := sessCaller(uMember)
		expect(t, "INV-7", Check(m, c, "repo.view", repoResource(rOrgInt)), true, "")
		for _, name := range []string{"repo.push", "change.review", "change.land", "ci.trigger"} {
			d := Check(m, c, name, repoResource(rOrgInt))
			if d.Allow {
				t.Fatalf("INV-7: member allowed %s", name)
			}
		}
	})
	t.Run("INV-8/owner-team-total", func(t *testing.T) {
		m := baseFixture()
		c := sessCaller(uOrgOwner)
		for _, id := range []int64{rOrgInt, rOrgPriv} {
			expect(t, "INV-8", Check(m, c, "repo.delete", Resource{Kind: KindRepo, RepoID: id}), true, "")
		}
		m.Repos[105] = RepoRecord{ID: 105, OrgID: org7, Visibility: VisibilityPrivate}
		expect(t, "INV-8", Check(m, c, "repo.view", repoResource(105)), true, "")
		expect(t, "INV-8", Check(m, c, "repo.delete", repoResource(105)), true, "")
	})
	t.Run("INV-9/team-confinement", func(t *testing.T) {
		m := baseFixture()
		m.TeamList[9] = &MemTeam{Org: org8, Repos: map[int64]Role{rOrgPriv: RoleAdmin}, Members: map[int64]bool{uOutsider: true}}
		expect(t, "INV-9", Check(m, sessCaller(uOutsider), "repo.view", repoResource(rOrgPriv)), false, "invisible")
		m.TeamList[10] = &MemTeam{Org: org7, Repos: map[int64]Role{rOrgPriv: RoleWrite}, Members: map[int64]bool{uOutsider: true}}
		expect(t, "INV-9", Check(m, sessCaller(uOutsider), "repo.push", repoResource(rOrgPriv)), true, "")
	})
	t.Run("INV-10/collaborator-confinement", func(t *testing.T) {
		m := baseFixture()
		m.Direct[rOrgPriv] = map[int64]Role{uOutsider: RoleAdmin}
		expect(t, "INV-10", Check(m, sessCaller(uOutsider), "repo.view", repoResource(rOrgPriv)), false, "invisible")
		m.Direct[rUserPriv] = map[int64]Role{uOutsider: RoleRead}
		expect(t, "INV-10", Check(m, sessCaller(uOutsider), "repo.view", repoResource(rUserPriv)), true, "")
	})
	t.Run("INV-11/land-requires-facts", func(t *testing.T) {
		m := baseFixture()
		m.Direct[rUserPriv] = map[int64]Role{uOwner: RoleAdmin}
		rule := BookmarkRule{ID: 1, RepoID: rUserPriv, Pattern: "main", Mode: "Open",
			LandAllow: []AllowEntry{{Roles: []Role{RoleWrite}}},
			Required:  []AttestationReq{{Kind: "review", Result: "approved", MinSigners: 1, Trusted: AllowEntry{Roles: []Role{RoleWrite}}}}}
		m.RuleSets[rUserPriv] = []BookmarkRule{rule}
		good := Resource{Kind: KindRepo, RepoID: rUserPriv, Ref: "main", OID: "abc", ChangeAuthor: uOutsider,
			Attestations: []Attestation{{SignerID: uOwner, SignerRole: RoleAdmin, Kind: "review", Result: "approved", OID: "abc"}}}
		expect(t, "INV-11", Check(m, sessCaller(uOwner), "change.land", good), true, "")
		stale := good
		stale.OID = "def"
		expect(t, "INV-11", Check(m, sessCaller(uOwner), "change.land", stale), false, "protected:attestation")
	})
	t.Run("INV-12/no-self-approval", func(t *testing.T) {
		m := baseFixture()
		m.Direct[rUserPriv] = map[int64]Role{uOutsider: RoleWrite, uOwner: RoleAdmin}
		rule := BookmarkRule{ID: 1, RepoID: rUserPriv, Pattern: "main", Mode: "Open",
			Required: []AttestationReq{{Kind: "review", Result: "approved", MinSigners: 1}}}
		m.RuleSets[rUserPriv] = []BookmarkRule{rule}
		self := Resource{Kind: KindRepo, RepoID: rUserPriv, Ref: "main", OID: "abc", ChangeAuthor: uOutsider,
			Attestations: []Attestation{{SignerID: uOutsider, SignerRole: RoleWrite, Kind: "review", Result: "approved", OID: "abc"}}}
		expect(t, "INV-12", Check(m, sessCaller(uOwner), "change.land", self), false, "protected:attestation")
		other := self
		other.Attestations = []Attestation{{SignerID: uOwner, SignerRole: RoleAdmin, Kind: "review", Result: "approved", OID: "abc"}}
		expect(t, "INV-12", Check(m, sessCaller(uOwner), "change.land", other), true, "")
	})
	t.Run("INV-13/landonly-denies-push", func(t *testing.T) {
		m := baseFixture()
		rule := BookmarkRule{ID: 1, RepoID: rUserPub, Pattern: "main", Mode: "LandOnly"}
		m.RuleSets[rUserPub] = []BookmarkRule{rule}
		m.Direct[rUserPub] = map[int64]Role{uAdmin: RoleWrite}
		push := Resource{Kind: KindRepo, RepoID: rUserPub, Ref: "main"}
		for _, c := range []Caller{sessCaller(uOwner), adminCaller(), tokenCaller(uOwner, writeScopes(), false)} {
			expect(t, "INV-13", Check(m, c, "repo.push", push), false, "protected:allowlist")
		}
		rule.BypassAdmin = true
		rule.PushAllow = []AllowEntry{{Users: []int64{uOwner}}}
		m.RuleSets[rUserPub] = []BookmarkRule{rule}
		expect(t, "INV-13", Check(m, sessCaller(uOwner), "repo.push", push), true, "")
		expect(t, "INV-13", Check(m, adminCaller(), "repo.push", push), false, "protected:allowlist")
	})
	t.Run("INV-14/claim-hold", func(t *testing.T) {
		m := baseFixture()
		m.TeamList[11] = &MemTeam{Org: org7, Repos: map[int64]Role{rOrgPriv: RoleTriage}, Members: map[int64]bool{uMember: true, uOutsider: true}}
		held := Resource{Kind: KindRepo, RepoID: rOrgPriv, ClaimHolder: uMember, LeaseHeld: true}
		expect(t, "INV-14", Check(m, sessCaller(uMember), "task.claim", held), false, "lease-hold")
		expect(t, "INV-14", Check(m, sessCaller(uOutsider), "task.unclaim", held), false, "lease-hold")
		expect(t, "INV-14", Check(m, sessCaller(uMember), "task.unclaim", held), true, "")
		free := Resource{Kind: KindRepo, RepoID: rOrgPriv}
		expect(t, "INV-14", Check(m, sessCaller(uMember), "task.claim", free), true, "")
		m.TeamList[13] = &MemTeam{Org: org7, Repos: map[int64]Role{rOrgPriv: RoleMaintain}, Members: map[int64]bool{uOutsider: true}}
		expect(t, "INV-14", Check(m, sessCaller(uOutsider), "task.unclaim", held), true, "")
	})
	t.Run("INV-15/child-scope-bounded", func(t *testing.T) {
		m := baseFixture()
		self := Resource{Kind: KindTokenAcct, OwnerID: uOwner}
		parent := tokenCaller(uOwner, writeScopes(), false)
		narrow := self
		narrow.NewScopes = readScopes()
		expect(t, "INV-15", Check(m, parent, "token.mint", narrow), true, "")
		wide := self
		wide.NewScopes = adminScopes()
		expect(t, "INV-15", Check(m, parent, "token.mint", wide), false, "scope")
		pub := self
		pub.NewScopes = op.Scopes(0).Add(op.ScopePublicRead)
		expect(t, "INV-15", Check(m, parent, "token.mint", pub), false, "scope")
		sess := Resource{Kind: KindTokenAcct, OwnerID: uOwner, NewScopes: adminScopes()}
		expect(t, "INV-15", Check(m, sessCaller(uOwner), "token.mint", sess), true, "")
	})
	t.Run("INV-16/404-before-403", func(t *testing.T) {
		m := baseFixture()
		wouldBeRole := Check(m, sessCaller(uMember), "repo.push", repoResource(rOrgPriv))
		wouldBeScope := Check(m, tokenCaller(uMember, readScopes(), false), "repo.push", repoResource(rOrgPriv))
		expect(t, "INV-16", wouldBeRole, false, "invisible")
		expect(t, "INV-16", wouldBeScope, false, "invisible")
	})
	t.Run("INV-17/social-implies-view", func(t *testing.T) {
		m := baseFixture()
		star := repoResource(rUserPub)
		expect(t, "INV-17", Check(m, anonCaller(), "social.star", star), false, "anonymous-mutate")
		expect(t, "INV-17", Check(m, anonCaller(), "repo.fork", star), false, "anonymous-mutate")
		m.Direct[rUserPub] = map[int64]Role{uOutsider: RoleRead}
		fork := Resource{Kind: KindRepo, RepoID: rUserPub, DestOwnerID: uOutsider}
		expect(t, "INV-17", Check(m, tokenCaller(uOutsider, readScopes(), false), "repo.fork", fork), false, "scope")
		expect(t, "INV-17", Check(m, tokenCaller(uOutsider, writeScopes(), false), "repo.fork", fork), true, "")
		expect(t, "INV-17", Check(m, sessCaller(uOutsider), "social.star", star), true, "")
	})
	t.Run("INV-18/config-secrecy", func(t *testing.T) {
		m := baseFixture()
		m.TeamList[15] = &MemTeam{Org: org7, Repos: map[int64]Role{rOrgPriv: RoleAdmin}, Members: map[int64]bool{uOutsider: true}}
		res := repoResource(rOrgPriv)
		for _, name := range []string{"hook.view", "hook.manage", "mirror.view", "mirror.manage", "ci.config"} {
			expect(t, "INV-18", Check(m, tokenCaller(uOutsider, readScopes(), false), name, res), false, "scope")
		}
		hooked := tokenCaller(uOutsider, op.Scopes(0).Add(op.ScopeWebhooksWrite), false)
		expect(t, "INV-18", Check(m, hooked, "hook.manage", res), true, "")
	})
	t.Run("INV-19/last-owner-guard", func(t *testing.T) {
		if err := GuardLastOwner([]int64{uOrgOwner}, uOrgOwner); err == nil {
			t.Fatal("INV-19: last owner removable")
		}
		if err := GuardLastOwner([]int64{uOrgOwner, uMember}, uOrgOwner); err != nil {
			t.Fatalf("INV-19: non-last owner blocked: %v", err)
		}
		if err := GuardLastAdmin([]int64{uAdmin}, uAdmin); err == nil {
			t.Fatal("INV-19: last admin deactivatable")
		}
		if err := GuardLastAdmin([]int64{uAdmin, uOwner}, uAdmin); err != nil {
			t.Fatalf("INV-19: non-last admin blocked: %v", err)
		}
	})
	t.Run("INV-20/delete-needs-admin-namespace", func(t *testing.T) {
		m := baseFixture()
		m.Direct[rUserPriv] = map[int64]Role{uOutsider: RoleMaintain}
		expect(t, "INV-20", Check(m, sessCaller(uOutsider), "repo.delete", repoResource(rUserPriv)), false, "role-floor")
		m.Direct[rUserPriv][uOutsider] = RoleAdmin
		expect(t, "INV-20", Check(m, sessCaller(uOutsider), "repo.delete", repoResource(rUserPriv)), false, "precondition")
		expect(t, "INV-20", Check(m, sessCaller(uOwner), "repo.delete", repoResource(rUserPriv)), true, "")
		expect(t, "INV-20", Check(m, sessCaller(uOrgOwner), "repo.delete", repoResource(rOrgPriv)), true, "")
		expect(t, "INV-20", Check(m, adminCaller(), "repo.delete", repoResource(rOrgPriv)), false, "invisible")
	})
	t.Run("INV-21/non-repo-rows-decide", func(t *testing.T) {
		m := baseFixture()
		m.PublicOrgs[org8] = true
		own := Resource{Kind: KindSite, OwnerID: uOutsider}
		foreign := Resource{Kind: KindSite, OwnerID: uOwner}
		c := tokenCaller(uOutsider, createScopes(), false)
		expect(t, "INV-21", Check(m, c, "repo.create_user", own), true, "")
		expect(t, "INV-21", Check(m, c, "repo.create_user", foreign), false, "precondition")
		wsess := sessCaller(uOutsider)
		expect(t, "INV-21", Check(m, wsess, "repo.create_user", own), true, "")
		orgRes := Resource{Kind: KindOrg, OrgID: org7}
		expect(t, "INV-21", Check(m, sessCaller(uOrgOwner), "repo.create_org", orgRes), true, "")
		expect(t, "INV-21", Check(m, sessCaller(uMember), "repo.create_org", orgRes), false, "precondition")
		foreignOrg := Resource{Kind: KindOrg, OrgID: org8}
		expect(t, "INV-21", Check(m, sessCaller(uOrgOwner), "repo.create_org", foreignOrg), false, "precondition")
		selfTok := Resource{Kind: KindTokenAcct, OwnerID: uOutsider}
		expect(t, "INV-21", Check(m, tokenCaller(uOutsider, writeScopes(), false), "token.list", selfTok), true, "")
		otherTok := Resource{Kind: KindTokenAcct, OwnerID: uOwner}
		expect(t, "INV-21", Check(m, tokenCaller(uOutsider, writeScopes(), false), "token.list", otherTok), false, "precondition")
		inbox := Resource{Kind: KindInbox, AddresseeID: uOutsider}
		expect(t, "INV-21", Check(m, sessCaller(uOutsider), "inbox.read", inbox), true, "")
		expect(t, "INV-21", Check(m, sessCaller(uOwner), "inbox.read", inbox), false, "invisible")
		expect(t, "INV-21", Check(m, adminCaller(), "inbox.read", inbox), false, "invisible")
		other := Resource{Kind: KindUserAcct, OwnerID: uOwner}
		expect(t, "INV-21", Check(m, sessCaller(uOutsider), "self.read", other), false, "precondition")
	})
	t.Run("INV-22/rule-order-deterministic", func(t *testing.T) {
		glob := BookmarkRule{ID: 1, Pattern: "*-stable", Mode: "Open"}
		exact := BookmarkRule{ID: 2, Pattern: "main", Mode: "LandOnly"}
		short := BookmarkRule{ID: 3, Pattern: "m*", Mode: "Open"}
		star := BookmarkRule{ID: 4, Pattern: "*", Mode: "Open"}
		long := BookmarkRule{ID: 5, Pattern: "x-*", Mode: "Open"}
		orders := [][]BookmarkRule{
			{glob, exact, short, star, long},
			{long, star, short, glob, exact},
			{exact, long, glob, star, short},
			{star, short, long, exact, glob},
		}
		for _, order := range orders {
			if winner := MatchRule(order, "main"); winner == nil || winner.ID != 2 {
				t.Fatalf("INV-22: main winner = %+v", winner)
			}
			if winner := MatchRule(order, "x-stable"); winner == nil || winner.ID != 1 {
				t.Fatalf("INV-22: x-stable winner = %+v", winner)
			}
		}
	})
	t.Run("INV-23/console-bypasses-floor", func(t *testing.T) {
		m := baseFixture()
		m.TeamList[99] = &MemTeam{Org: org7, Members: map[int64]bool{}}
		c := adminCaller()
		for _, r := range []Resource{
			{Kind: KindRepo, RepoID: rOrgPriv},
			{Kind: KindOrg, OrgID: org7},
			{Kind: KindTeam, TeamID: 99},
			{Kind: KindInbox, AddresseeID: uOutsider},
		} {
			if d := Check(m, c, "site.admin_console", r); !d.Allow {
				t.Fatalf("INV-23: console denied on %+v", r)
			}
		}
		expect(t, "INV-23", Check(m, c, "repo.view", repoResource(rOrgPriv)), false, "invisible")
		expect(t, "INV-23", Check(m, sessCaller(uOutsider), "site.admin_console", repoResource(rOrgPriv)), false, "precondition")
	})
	t.Run("INV-24/team-visibility-splits", func(t *testing.T) {
		m := baseFixture()
		m.TeamList[20] = &MemTeam{Org: org7, Members: map[int64]bool{uMember: true}}
		m.PublicOrgs[org8] = true
		m.TeamList[21] = &MemTeam{Org: org8, Members: map[int64]bool{}}
		pub := Resource{Kind: KindTeam, TeamID: 21, OrgID: org8}
		priv := Resource{Kind: KindTeam, TeamID: 20, OrgID: org7}
		expect(t, "INV-24", Check(m, sessCaller(uOutsider), "team.view", priv), false, "invisible")
		expect(t, "INV-24", Check(m, sessCaller(uOutsider), "team.view", pub), false, "precondition")
		expect(t, "INV-24", Check(m, sessCaller(uMember), "team.view", priv), true, "")
	})
	t.Run("INV-25/destination-bound", func(t *testing.T) {
		m := baseFixture()
		m.Direct[rUserPub] = map[int64]Role{uOutsider: RoleRead}
		src := Resource{Kind: KindRepo, RepoID: rUserPub}
		bad := []Resource{
			{Kind: KindRepo, RepoID: rUserPub},
			{Kind: KindRepo, RepoID: rUserPub, DestOwnerID: uOwner},
			{Kind: KindRepo, RepoID: rUserPub, DestOwnerID: uOutsider, DestOrgID: org7},
			{Kind: KindRepo, RepoID: rUserPub, DestOrgID: org8},
		}
		_ = src
		for i, r := range bad {
			if d := Check(m, tokenCaller(uOutsider, writeScopes(), false), "repo.fork", r); d.Allow {
				t.Fatalf("INV-25: fork %d allowed", i)
			} else if d.Reason != "precondition" {
				t.Fatalf("INV-25: fork %d reason %q", i, d.Reason)
			}
		}
		good := Resource{Kind: KindRepo, RepoID: rUserPub, DestOwnerID: uOutsider}
		expect(t, "INV-25", Check(m, tokenCaller(uOutsider, writeScopes(), false), "repo.fork", good), true, "")
		transfer := Resource{Kind: KindRepo, RepoID: rUserPriv, DestOwnerID: uOwner}
		expect(t, "INV-25", Check(m, tokenCaller(uOwner, adminScopes(), false), "repo.transfer", transfer), true, "")
	})
	t.Run("INV-0/unknown-denies", func(t *testing.T) {
		m := baseFixture()
		expect(t, "INV-0", Check(m, adminCaller(), "repo.nope", repoResource(rUserPub)), false, "deny:D1")
	})
}
