package api

import (
	"testing"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/op"
)

func TestProtectedBranchNeedsRef(t *testing.T) {
	rule := access.BookmarkRule{ID: 1, RepoID: 7, Pattern: "refs/heads/main", Mode: "LandOnly"}
	res := &access.MemResolver{
		Repos:    map[int64]access.RepoRecord{7: {ID: 7, OwnerUserID: 1, Visibility: access.VisibilityPublic}},
		Direct:   map[int64]map[int64]access.Role{7: {2: access.RoleWrite}},
		RuleSets: map[int64][]access.BookmarkRule{7: {rule}},
	}
	caller := access.Caller{
		UserID: 2, Active: true, Authed: true,
		Scopes:  op.Scopes(0).Add(op.ScopeRepoWrite).Add(op.ScopePublicRead),
		IsToken: true,
	}
	empty := access.Check(res, caller, "repo.push", access.Resource{Kind: access.KindRepo, RepoID: 7})
	if !empty.Allow {
		t.Fatalf("empty ref unexpectedly denied: %v", empty.Reason)
	}
	real := access.Check(res, caller, "repo.push", access.Resource{Kind: access.KindRepo, RepoID: 7, Ref: "refs/heads/main"})
	if real.Allow {
		t.Fatalf("protected branch push with real ref must be denied")
	}
}
