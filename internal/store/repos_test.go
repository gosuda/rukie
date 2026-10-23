package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

type repoFixture struct {
	store *Store
	owner int64
}

func openMigrated(t *testing.T) *repoFixture {
	t.Helper()
	path := testPath(t)
	s, err := open(context.Background(), Options{Path: path}, embeddedMigrations)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	admin, err := s.InsertAuthUser(t.Context(), AuthUserCreate{
		Name: "owner", Lower: "owner", Email: "owner@example.test", Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return &repoFixture{store: s, owner: admin.ID}
}

func insertRepo(t *testing.T, s *Store, owner int64, name, visibility string) int64 {
	t.Helper()
	var id int64
	err := s.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		id, ierr = tx.InsertRepo(time.Now(), RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: owner, Valid: true},
			Name:          name,
			Visibility:    visibility,
			Topics:        "[]",
			DefaultBranch: "main",
			RukPath:       "seed/" + name + ".git",
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	return id
}

func TestRepoInsertDefaultsEmptyTopics(t *testing.T) {
	fx := openMigrated(t)
	s := fx.store
	var id int64
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		id, ierr = tx.InsertRepo(time.Now(), RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: fx.owner, Valid: true},
			Name:          "notopics",
			Visibility:    "private",
			DefaultBranch: "main",
			RukPath:       "seed/notopics.git",
		})
		return ierr
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var row RepoRow
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var gerr error
		row, gerr = tx.GetRepo(id)
		return gerr
	}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Topics != "[]" {
		t.Fatalf("topics = %q, want the JSON array default", row.Topics)
	}
}

func TestRepoRoundTrip(t *testing.T) {
	fx := openMigrated(t)
	s := fx.store
	id := insertRepo(t, s, fx.owner, "hello", "private")
	var row RepoRow
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var gerr error
		row, gerr = tx.GetRepo(id)
		return gerr
	}); err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.Name != "hello" || row.Visibility != "private" || row.DefaultBranch != "main" {
		t.Fatalf("row = %+v", row)
	}
	if !row.OwnerUserID.Valid || row.OwnerUserID.Int64 != fx.owner {
		t.Fatalf("owner = %+v", row.OwnerUserID)
	}
	if row.DeletedAt.Valid || row.Archived {
		t.Fatalf("fresh row reads deleted: %+v", row)
	}
	found := RepoRow{}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var ferr error
		found, ferr = tx.FindRepo(fx.owner, 0, "hello")
		return ferr
	}); err != nil {
		t.Fatalf("find: %v", err)
	}
	if found.ID != id {
		t.Fatalf("found = %+v, want id %d", found, id)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		_, ferr := tx.FindRepo(fx.owner, 0, "missing")
		if !errors.Is(ferr, ErrNotFound) {
			t.Fatalf("missing = %v, want not found", ferr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestRepoListCursor(t *testing.T) {
	fx := openMigrated(t)
	s := fx.store
	insertRepo(t, s, fx.owner, "b", "public")
	insertRepo(t, s, fx.owner, "a", "public")
	insertRepo(t, s, fx.owner, "c", "public")
	var first []RepoRow
	var next string
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var lerr error
		first, next, lerr = tx.ListRepos(RepoListFilter{OwnerUserID: fx.owner, Sort: "name", Limit: 2})
		return lerr
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first) != 2 || first[0].Name != "a" || first[1].Name != "b" {
		t.Fatalf("page one = %+v", first)
	}
	if next == "" {
		t.Fatal("page one misses a cursor")
	}
	var second []RepoRow
	var tail string
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var lerr error
		second, tail, lerr = tx.ListRepos(RepoListFilter{OwnerUserID: fx.owner, Sort: "name", Limit: 2, Cursor: next})
		return lerr
	}); err != nil {
		t.Fatalf("page two: %v", err)
	}
	if len(second) != 1 || second[0].Name != "c" {
		t.Fatalf("page two = %+v", second)
	}
	if tail != "" {
		t.Fatalf("tail cursor = %q, want empty", tail)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		_, _, lerr := tx.ListRepos(RepoListFilter{Cursor: "bogus"})
		if !errors.Is(lerr, ErrInvalid) {
			t.Fatalf("bogus cursor = %v, want invalid", lerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestRepoSoftDeleteRestore(t *testing.T) {
	fx := openMigrated(t)
	s := fx.store
	id := insertRepo(t, s, fx.owner, "gone", "private")
	now := time.Now()
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		return tx.SoftDeleteRepo(id, now, now.Add(time.Hour))
	}); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		if _, ferr := tx.FindRepo(fx.owner, 0, "gone"); !errors.Is(ferr, ErrNotFound) {
			t.Fatalf("deleted visible = %v", ferr)
		}
		row, gerr := tx.FindRepoWithDeleted(fx.owner, 0, "gone")
		if gerr != nil {
			t.Fatalf("with deleted: %v", gerr)
		}
		if !row.DeletedAt.Valid || !row.PurgeAfter.Valid {
			t.Fatalf("tombstone = %+v", row)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		return tx.SoftDeleteRepo(id, now, now.Add(time.Hour))
	}); err == nil {
		t.Fatal("second delete succeeded")
	}
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		return tx.RestoreRepo(id, now)
	}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		if rerr := tx.RestoreRepo(id, now); rerr == nil {
			t.Fatal("second restore succeeded")
		}
		return nil
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var due []RepoRow
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var lerr error
		due, lerr = tx.DuePurges(now.Add(2 * time.Hour))
		return lerr
	}); err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("restored repo is due: %+v", due)
	}
}

func TestRepoDuePurgeAndHardDelete(t *testing.T) {
	fx := openMigrated(t)
	s := fx.store
	id := insertRepo(t, s, fx.owner, "old", "private")
	now := time.Now()
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		return tx.SoftDeleteRepo(id, now, now.Add(time.Hour))
	}); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	var early []RepoRow
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var lerr error
		early, lerr = tx.DuePurges(now)
		return lerr
	}); err != nil {
		t.Fatalf("due early: %v", err)
	}
	if len(early) != 0 {
		t.Fatalf("early due = %+v", early)
	}
	var due []RepoRow
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		var lerr error
		due, lerr = tx.DuePurges(now.Add(2 * time.Hour))
		return lerr
	}); err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 1 || due[0].ID != id {
		t.Fatalf("due = %+v", due)
	}
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		return tx.HardDeleteRepo(id)
	}); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		_, gerr := tx.GetRepo(id)
		if !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("purged row = %v", gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestRepoGrantsAndRedirects(t *testing.T) {
	fx := openMigrated(t)
	s := fx.store
	teammate, err := fx.store.InsertAuthUser(t.Context(), AuthUserCreate{
		Name: "mate", Lower: "mate", Email: "mate@example.test", Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	id := insertRepo(t, s, fx.owner, "team", "private")
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		return tx.PutGrant(id, teammate.ID, "write")
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		if gerr := tx.PutGrant(id, teammate.ID, "super"); gerr == nil {
			t.Fatal("bad role granted")
		}
		return nil
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		if rerr := tx.DeleteGrant(id, teammate.ID+1000); !errors.Is(rerr, ErrNotFound) {
			t.Fatalf("missing grant = %v", rerr)
		}
		return tx.DeleteGrant(id, teammate.ID)
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	now := time.Now()
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		return tx.PutRedirect(fx.owner, "old", id, now)
	}); err != nil {
		t.Fatalf("redirect: %v", err)
	}
	if err := s.Read(t.Context(), func(tx *ReadTx) error {
		found, ferr := tx.FindRedirect(fx.owner, "old")
		if ferr != nil {
			t.Fatalf("find redirect: %v", ferr)
		}
		if found.RepoID != id {
			t.Fatalf("redirect = %+v", found)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}
