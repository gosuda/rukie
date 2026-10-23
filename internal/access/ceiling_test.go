package access

import (
	"testing"

	"github.com/gosuda/rukie/internal/op"
)

func hookScopes() op.Scopes {
	return op.Scopes(0).Add(op.ScopeWebhooksWrite)
}

func TestTokenScopeCeiling(t *testing.T) {
	m := baseFixture()
	m.Direct[rUserPriv] = map[int64]Role{uOutsider: RoleAdmin}
	m.Direct[rUserPub] = map[int64]Role{uOutsider: RoleWrite}
	m.TeamList[30] = &MemTeam{Org: org7, Repos: map[int64]Role{rOrgPriv: RoleAdmin}, Members: map[int64]bool{uOutsider: true}}
	priv := repoResource(rOrgPriv)
	owned := repoResource(rUserPriv)
	read := tokenCaller(uOutsider, readScopes(), false)
	write := tokenCaller(uOutsider, writeScopes(), false)
	create := tokenCaller(uOutsider, createScopes(), false)
	hooked := tokenCaller(uOutsider, hookScopes(), false)
	cases := []struct {
		name   string
		action string
		reason string
		res    Resource
		caller Caller
		allow  bool
	}{
		{name: "read token pushes own write grant", caller: read, action: "repo.push", res: owned, allow: false, reason: "scope"},
		{name: "write token pushes own write grant", caller: write, action: "repo.push", res: owned, allow: true},
		{name: "write token views", caller: write, action: "repo.view", res: owned, allow: true},
		{name: "create token pushes", caller: create, action: "repo.push", res: owned, allow: false, reason: "scope"},
		{name: "create token lands", caller: create, action: "change.land", res: owned, allow: false, reason: "scope"},
		{name: "create token creates own namespace", caller: create, action: "repo.create_user", res: Resource{Kind: KindSite, OwnerID: uOutsider}, allow: true},
		{name: "create token creates foreign namespace", caller: create, action: "repo.create_user", res: Resource{Kind: KindSite, OwnerID: uOwner}, allow: false, reason: "precondition"},
		{name: "read token manages hooks with admin grant", caller: read, action: "hook.manage", res: priv, allow: false, reason: "scope"},
		{name: "read token views mirrors with admin grant", caller: read, action: "mirror.view", res: priv, allow: false, reason: "scope"},
		{name: "write token manages hooks with admin grant", caller: hooked, action: "hook.manage", res: priv, allow: true},
		{name: "read token widens visibility", caller: read, action: "repo.visibility_widen", res: owned, allow: false, reason: "scope"},
		{name: "session widens visibility on own repo", caller: sessCaller(uOwner), action: "repo.visibility_widen", res: owned, allow: true},
		{name: "admin session manages hooks", caller: adminCaller(), action: "hook.manage", res: priv, allow: false, reason: "invisible"},
	}
	for _, c := range cases {
		d := Check(m, c.caller, c.action, c.res)
		expect(t, "ceiling/"+c.name, d, c.allow, c.reason)
	}
	admin := tokenCaller(uAdmin, adminScopes(), false)
	expect(t, "ceiling", Check(m, admin, "repo.delete", Resource{Kind: KindRepo, RepoID: rOrgPriv}), false, "invisible")
	wide := op.Scopes(0).Add(op.ScopeRepoWrite).Add(op.ScopeAdminWrite)
	expect(t, "ceiling", Check(m, tokenCaller(uAdmin, wide, false), "repo.delete", Resource{Kind: KindRepo, RepoID: rOrgPriv}), false, "invisible")
}

func TestPublicOnlyTokenSees404(t *testing.T) {
	m := baseFixture()
	m.Direct[rOrgPriv] = map[int64]Role{uOutsider: RoleAdmin}
	m.Direct[rUserPub] = map[int64]Role{uOutsider: RoleWrite}
	priv := repoResource(rOrgPriv)
	po := tokenCaller(uOutsider, adminScopes(), true)
	for _, name := range []string{"repo.view", "repo.push", "change.view", "release.view"} {
		d := Check(m, po, name, priv)
		expect(t, "public-only/"+name, d, false, "invisible")
		if d.NotFound == false {
			t.Fatalf("public-only/%s: 403 instead of 404", name)
		}
	}
	pub := repoResource(rUserPub)
	read := tokenCaller(uOutsider, readScopes(), false)
	d := Check(m, po, "repo.view", pub)
	expect(t, "public-only/public-view", d, true, "")
	expect(t, "public-only/public-push", Check(m, po, "repo.push", pub), true, "")
	d = Check(m, read, "repo.push", pub)
	if d.Allow || d.NotFound {
		t.Fatalf("read-token/public-push = %v (404 %v), want 403 scope", d.Allow, d.NotFound)
	}
	if d.Reason != "scope" {
		t.Fatalf("read-token/public-push reason = %q", d.Reason)
	}
}
