package githttp

import (
	"database/sql"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func openLookupStore(t *testing.T) (*store.Store, int64) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/lookup.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	ada, err := st.InsertAuthUser(ctx, store.AuthUserCreate{
		Name: "ada", Lower: "ada", Email: "ada@example.test",
		Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var live int64
	if werr := st.Write(ctx, func(tx *store.WriteTx) error {
		liveID, ierr := tx.InsertRepo(time.Unix(1, 0), store.RepoInsert{
			Name: "hello", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ada.ID, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		live = liveID
		goneID, ierr := tx.InsertRepo(time.Unix(1, 0), store.RepoInsert{
			Name: "gone", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ada.ID, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		return tx.SoftDeleteRepo(goneID, time.Unix(2, 0), time.Unix(3, 0))
	}); werr != nil {
		t.Fatalf("seed repos: %v", werr)
	}
	return st, live
}

func TestLookupRepoHidesSoftDeleted(t *testing.T) {
	st, live := openLookupStore(t)
	s := &Server{store: st}
	if id, ok := s.lookupRepo(t.Context(), "ada", "hello"); !ok || id != live {
		t.Fatalf("live repo lookup = %d, %v, want %d, true", id, ok, live)
	}
	if id, ok := s.lookupRepo(t.Context(), "ada", "gone"); ok || id != 0 {
		t.Fatalf("soft-deleted repo lookup = %d, %v, want 0, false", id, ok)
	}
}
