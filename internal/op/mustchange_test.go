package op

import "testing"

func TestMustChangePasswordOperationAllowlist(t *testing.T) {
	p := Principal{MustChangePassword: true, Grants: Scopes(0).Add(ScopePublicRead).Add(ScopeRepoRead)}
	if !mustChangePasswordAllows(p, "auth.password.change") {
		t.Fatal("mandatory password change was denied")
	}
	if !mustChangePasswordAllows(p, "auth.logout") {
		t.Fatal("logout was denied")
	}
	if mustChangePasswordAllows(p, "repo.list") {
		t.Fatal("non-password operation was allowed")
	}
	p.Grants = Scopes(0).Add(ScopePublicRead)
	if mustChangePasswordAllows(p, "auth.password.change") {
		t.Fatal("principal without password-change scope was allowed")
	}
}
