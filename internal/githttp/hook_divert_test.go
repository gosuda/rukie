package githttp

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func TestDivertNgUnlessCommitted(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "hook.sqlite")
	st, err := store.Open(t.Context(), store.Options{Path: dbPath})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	ada, err := st.InsertAuthUser(t.Context(), store.AuthUserCreate{
		Name: "ada", Lower: "ada", Email: "ada@example.test",
		Hash: "x", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var repoID int64
	if werr := st.Write(t.Context(), func(tx *store.WriteTx) error {
		var ierr error
		repoID, ierr = tx.InsertRepo(time.Unix(1, 0), store.RepoInsert{
			Name: "hello", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ada.ID, Valid: true},
		})
		return ierr
	}); werr != nil {
		t.Fatalf("seed repo: %v", werr)
	}
	resolver := access.NewMemResolver()
	resolver.Repos[repoID] = access.RepoRecord{ID: repoID, OwnerUserID: ada.ID, Visibility: access.VisibilityPrivate}
	resolver.Direct[repoID] = map[int64]access.Role{ada.ID: access.RoleAdmin}
	s := &Server{
		store:       st,
		resolver:    resolver,
		repoRoot:    dir,
		clock:       time.Now,
		hookTimeout: 10 * time.Second,
	}
	if err := st.Close(context.Background()); err != nil {
		t.Fatalf("store close: %v", err)
	}
	head := strings.Repeat("a", 64)
	v := s.judge(t.Context(), access.Caller{
		UserID: 1,
		Scopes: op.Scopes(0).Add(op.ScopeRepoRead),
		Active: true,
		Authed: true,
	}, repoID, wire.HookPreReceive, wire.HookCommand{
		Name:       "refs/for/main",
		New:        head,
		Authorized: true,
	})
	if v.Kind != wire.HookReject {
		t.Fatalf("committed verdict = %q, want reject on failed transaction", v.Kind)
	}
	if !strings.Contains(v.Reason, "was not created") {
		t.Fatalf("reason = %q, want the commit-failure reason", v.Reason)
	}
}
