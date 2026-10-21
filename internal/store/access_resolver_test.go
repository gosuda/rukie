package store

import (
	"database/sql"
	"testing"
	"time"
)

func openResolverStore(t *testing.T) (*Store, int64, int64, int64) {
	t.Helper()
	ctx := t.Context()
	s, err := open(ctx, Options{Path: testPath(t)}, embeddedMigrations)
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(ctx) })
	ada, err := s.InsertAuthUser(ctx, AuthUserCreate{
		Name: "ada", Lower: "ada", Email: "ada@example.test",
		Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var live, gone int64
	if werr := s.Write(ctx, func(tx *WriteTx) error {
		liveID, ierr := tx.InsertRepo(time.Unix(1, 0), RepoInsert{
			Name: "hello", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ada.ID, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		live = liveID
		goneID, ierr := tx.InsertRepo(time.Unix(1, 0), RepoInsert{
			Name: "gone", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ada.ID, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		gone = goneID
		return tx.SoftDeleteRepo(goneID, time.Unix(2, 0), time.Unix(3, 0))
	}); werr != nil {
		t.Fatalf("seed repos: %v", werr)
	}
	return s, ada.ID, live, gone
}

func TestStoreResolverResolvesDeletedRepo(t *testing.T) {
	s, _, live, gone := openResolverStore(t)
	res := s.Resolver()
	if _, ok := res.Repo(live); !ok {
		t.Fatalf("live repo %d is invisible", live)
	}
	if _, ok := res.Repo(gone); !ok {
		t.Fatalf("soft-deleted repo %d must resolve by id; callers filter deletion", gone)
	}
}

func TestStoreResolverLoadsArchivedFlag(t *testing.T) {
	ctx := t.Context()
	s, owner, live, _ := openResolverStore(t)
	res := s.Resolver()
	row, ok := res.Repo(live)
	if !ok {
		t.Fatalf("live repo %d is invisible", live)
	}
	if row.Archived {
		t.Fatalf("live repo reports archived")
	}
	frozen := true
	var archivedID int64
	if werr := s.Write(ctx, func(tx *WriteTx) error {
		id, ierr := tx.InsertRepo(time.Unix(1, 0), RepoInsert{
			Name: "frozen", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: owner, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		archivedID = id
		return tx.UpdateRepo(id, time.Unix(1, 0), RepoUpdate{Archived: &frozen})
	}); werr != nil {
		t.Fatalf("seed archived repo: %v", werr)
	}
	archived, ok := res.Repo(archivedID)
	if !ok {
		t.Fatalf("archived repo %d is invisible", archivedID)
	}
	if !archived.Archived {
		t.Fatalf("archived repo %d reports archived false", archivedID)
	}
}
