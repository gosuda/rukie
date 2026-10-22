package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func openReleaseTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.Context(), Options{Path: t.TempDir() + "/releases.sqlite"})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	return st
}

func seedReleaseRepo(t *testing.T, st *Store) int64 {
	t.Helper()
	now := time.Now().Unix()
	user, uerr := st.InsertAuthUser(t.Context(), AuthUserCreate{Name: "relana", Lower: "relana", Email: "relana@example.test", Hash: "x", Algo: "argon2id", Now: now})
	if uerr != nil {
		t.Fatalf("seed user: %v", uerr)
	}
	var repoID int64
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		repoID, ierr = tx.InsertRepo(time.Now(), RepoInsert{OwnerUserID: sql.NullInt64{Int64: user.ID, Valid: true}, Name: "ships", Visibility: "public", DefaultBranch: "main", RukPath: "relana/ships.git"})
		return ierr
	}); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	return repoID
}

func TestReleaseLiveTagPartialUniqueness(t *testing.T) {
	st := openReleaseTestStore(t)
	repoID := seedReleaseRepo(t, st)
	now := time.Now()
	insert := func(tag string) error {
		return st.Write(t.Context(), func(tx *WriteTx) error {
			_, ierr := tx.InsertRelease(ReleaseInsert{RepoID: repoID, AuthorID: 1, PublicID: "rel-" + tag, Tag: tag, Title: tag, CreatedAt: now})
			return ierr
		})
	}
	if err := insert("v1"); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := insert("V1"); err == nil {
		t.Fatal("case-insensitive duplicate live tag inserted")
	}
	var doomed ReleaseRow
	if rerr := st.Read(t.Context(), func(tx *ReadTx) error {
		var gerr error
		doomed, gerr = tx.GetRelease(repoID, "v1")
		return gerr
	}); rerr != nil {
		t.Fatalf("lookup: %v", rerr)
	}
	if werr := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.DeleteRelease(doomed.ID, now)
	}); werr != nil {
		t.Fatalf("soft delete: %v", werr)
	}
	if err := insert("V1"); err != nil {
		t.Fatalf("recreate after delete: %v", err)
	}
}

func TestReleaseMissingRowTransitionsFail(t *testing.T) {
	st := openReleaseTestStore(t)
	seedReleaseRepo(t, st)
	now := time.Now()
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.PublishRelease(4242, "abc", "lightweight", "<p>x</p>", now)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("publish missing = %v, want not found", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.DeleteRelease(4242, now)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing = %v, want not found", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.DeleteAssetByPublicID("asset-nope")
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete asset missing = %v, want not found", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		_, gerr := tx.GetRelease(1, "ghost")
		return gerr
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing = %v, want not found", err)
	}
}

func TestReleaseDiscardDraft(t *testing.T) {
	st := openReleaseTestStore(t)
	repoID := seedReleaseRepo(t, st)
	now := time.Now()
	var releaseID int64
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		releaseID, ierr = tx.InsertRelease(ReleaseInsert{RepoID: repoID, AuthorID: 1, PublicID: "rel-discard", Tag: "gone", Title: "gone", CreatedAt: now})
		return ierr
	}); err != nil {
		t.Fatalf("insert release: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.InsertAsset(AssetRow{PublicID: "asset-gone", ReleaseID: releaseID, UploaderID: 1, Name: "f.bin", SHA256: "x", Size: 1, ContentType: "application/octet-stream", State: "draft", Path: "aa/bb/x", CreatedAt: now})
	}); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if derr := tx.DeleteReleaseAssets(releaseID); derr != nil {
			return derr
		}
		return tx.HardDeleteRelease(releaseID)
	}); err != nil {
		t.Fatalf("discard: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		_, gerr := tx.GetRelease(repoID, "gone")
		return gerr
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("discarded get = %v, want not found", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.HardDeleteRelease(releaseID)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second discard = %v, want not found", err)
	}
}

func TestReleaseAssetUUIDLookupAndPublishedImmutability(t *testing.T) {
	st := openReleaseTestStore(t)
	repoID := seedReleaseRepo(t, st)
	now := time.Now()
	var releaseID int64
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		releaseID, ierr = tx.InsertRelease(ReleaseInsert{RepoID: repoID, AuthorID: 1, PublicID: "rel-uuid", Tag: "v2", Title: "v2", CreatedAt: now})
		return ierr
	}); err != nil {
		t.Fatalf("insert release: %v", err)
	}
	asset := AssetRow{PublicID: "asset-uuid-1", ReleaseID: releaseID, UploaderID: 1, Name: "ships.tar", SHA256: "abc", Size: 3, ContentType: "application/x-tar", State: "draft", Path: "aa/bb/abc", CreatedAt: now}
	if err := st.Write(t.Context(), func(tx *WriteTx) error { return tx.InsertAsset(asset) }); err != nil {
		t.Fatalf("insert asset: %v", err)
	}
	got, err := st.AssetByPublicID(t.Context(), "asset-uuid-1")
	if err != nil {
		t.Fatalf("uuid lookup: %v", err)
	}
	if got.Name != "ships.tar" || got.ReleaseID != releaseID {
		t.Fatalf("uuid lookup = %+v, want ships.tar on release %d", got, releaseID)
	}
	if perr := st.Write(t.Context(), func(tx *WriteTx) error {
		if ierr := tx.PublishRelease(releaseID, "abc", "lightweight", "<p>v2</p>", now); ierr != nil {
			return ierr
		}
		return tx.PublishAssets(releaseID, now)
	}); perr != nil {
		t.Fatalf("publish: %v", perr)
	}
	if derr := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.DeleteAssetByPublicID("asset-uuid-1")
	}); !errors.Is(derr, ErrNotFound) {
		t.Fatalf("delete published asset = %v, want not found", derr)
	}
	kept, err := st.AssetByPublicID(t.Context(), "asset-uuid-1")
	if err != nil {
		t.Fatalf("published asset retained: %v", err)
	}
	if kept.State != "published" {
		t.Fatalf("asset state = %q, want published", kept.State)
	}
}
