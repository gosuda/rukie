package store

import (
	"testing"
	"time"
)

func TestStoreAuthBoundary(t *testing.T) {
	ctx := t.Context()
	path := testPath(t)
	s, err := open(ctx, Options{Path: path}, embeddedMigrations)
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	admin, err := s.InsertAuthUser(ctx, AuthUserCreate{
		Name: "owner", Lower: "owner", Email: "owner@example.test",
		Hash: "test-hash", Algo: "argon2id-v19", Now: 1, Admin: true,
	})
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
	now := time.Now().UnixMilli()
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if cerr := s.CreateToken(ctx, TokenCreate{
		Hash: hash, Prefix: hash[:12], UserID: admin.ID, Name: "t",
		Scopes:    "repo:read repo:write",
		ExpiresAt: now + int64(time.Hour/time.Millisecond), CreatedAt: now,
		PublicOnly: true, RepoIDs: []int64{42},
	}); cerr != nil {
		t.Fatalf("create token: %v", cerr)
	}
	repos, err := s.ListTokenRepos(ctx, hash)
	if err != nil {
		t.Fatalf("list token repos: %v", err)
	}
	if len(repos) != 1 || repos[0] != 42 {
		t.Fatalf("token repos mismatch: %v", repos)
	}
	tok, err := s.LookupScopedTokenByHash(ctx, hash)
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
