package store

import (
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
)

func TestStoreAuthBoundary(t *testing.T) {
	ctx := t.Context()
	path := testPath(t)
	s, err := open(ctx, Options{Path: path}, embeddedMigrations)
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	svc, err := auth.Open(path)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	admin, err := svc.BootstrapAdmin(ctx, "owner", "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	meta, err := s.LookupUserByName(ctx, "owner")
	if err != nil {
		t.Fatalf("lookup by name: %v", err)
	}
	if meta.ID != admin.ID || meta.Name != admin.Name {
		t.Fatalf("user meta mismatch: %+v vs %+v", meta, admin)
	}
	byID, err := s.LookupUserByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("lookup by id: %v", err)
	}
	if byID.Name != admin.Name {
		t.Fatalf("lookup by id name mismatch")
	}
	secret, err := svc.CreateToken(ctx, auth.CreateTokenInput{
		Name:       "t",
		UserID:     admin.ID,
		Scopes:     op.Scopes(0).Add(op.ScopeRepoRead).Add(op.ScopeRepoWrite),
		TTL:        time.Hour,
		PublicOnly: true,
		Repos:      []int64{42},
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	repos, err := s.ListTokenRepos(ctx, secret.Token.Hash)
	if err != nil {
		t.Fatalf("list token repos: %v", err)
	}
	if len(repos) != 1 || repos[0] != 42 {
		t.Fatalf("token repos mismatch: %v", repos)
	}
	tok, err := s.LookupScopedTokenByHash(ctx, secret.Token.Hash)
	if err != nil {
		t.Fatalf("lookup token by hash: %v", err)
	}
	if tok.UserID != admin.ID || !tok.PublicOnly {
		t.Fatalf("token meta mismatch: %+v", tok)
	}
	if res := s.Resolver(); res == nil {
		t.Fatalf("resolver must not be nil")
	} else if !res.IsActive(admin.ID) {
		t.Fatalf("resolver must see active admin")
	}
}
