package api

import (
	"testing"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/op"
)

func TestTransferDestAuthorizes(t *testing.T) {
	owner := int64(1)
	repoID := int64(9)
	res := &access.MemResolver{
		Repos: map[int64]access.RepoRecord{
			repoID: {ID: repoID, OwnerUserID: owner, Visibility: access.VisibilityPrivate},
		},
	}
	caller := access.Caller{
		UserID: owner, Active: true, Authed: true, IsAdmin: true,
		Scopes: op.Scopes(0).Add(op.ScopeAdminWrite).Add(op.ScopeRepoWrite).Add(op.ScopePublicRead),
	}
	empty := access.Check(res, caller, "repo.transfer", access.Resource{Kind: access.KindRepo, RepoID: repoID})
	if empty.Allow {
		t.Fatalf("transfer with zero dest must be denied")
	}
	good := access.Check(res, caller, "repo.transfer", access.Resource{Kind: access.KindRepo, RepoID: repoID, DestOwnerID: owner})
	if !good.Allow {
		t.Fatalf("transfer to own namespace must be allowed: %v", good.Reason)
	}
	other := access.Check(res, caller, "repo.transfer", access.Resource{Kind: access.KindRepo, RepoID: repoID, DestOwnerID: 999})
	if other.Allow {
		t.Fatalf("transfer to another namespace must be denied")
	}
}
