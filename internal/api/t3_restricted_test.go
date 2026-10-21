package api

import (
	"testing"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func TestRestrictedSessionCannotReadPrivate(t *testing.T) {
	ctx := t.Context()
	path := t.TempDir() + "/t3.sqlite"
	st, err := store.Open(ctx, store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	svc := auth.New(st)
	admin, err := svc.BootstrapAdmin(ctx, "owner", "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	d := Deps{Service: svc, Store: st}
	restricted := op.Principal{
		UserID:  admin.ID,
		Kind:    op.PrincipalHuman,
		TokenID: -1,
		Grants:  op.Scopes(0).Add(op.ScopePublicRead),
	}
	rq, err := d.requester(ctx, restricted)
	if err != nil {
		t.Fatalf("requester restricted: %v", err)
	}
	if !rq.caller.PublicOnly {
		t.Fatalf("restricted session must set PublicOnly")
	}
	full := op.Principal{
		UserID:  admin.ID,
		Kind:    op.PrincipalHuman,
		TokenID: -1,
		Grants:  auth.ScopesAll(),
	}
	rf, err := d.requester(ctx, full)
	if err != nil {
		t.Fatalf("requester full: %v", err)
	}
	if rf.caller.PublicOnly {
		t.Fatalf("full session must not set PublicOnly")
	}
	priv := access.RepoRecord{ID: 1, OwnerUserID: admin.ID, Visibility: access.VisibilityPrivate}
	res := &access.MemResolver{
		Repos: map[int64]access.RepoRecord{1: priv},
	}
	if got := access.Check(res, rq.caller, "repo.view", access.Resource{Kind: access.KindRepo, RepoID: 1}); got.Allow {
		t.Fatalf("restricted session must not read private repo")
	}
	if got := access.Check(res, rf.caller, "repo.view", access.Resource{Kind: access.KindRepo, RepoID: 1}); !got.Allow {
		t.Fatalf("full session must read own private repo: %v", got.Reason)
	}
}
