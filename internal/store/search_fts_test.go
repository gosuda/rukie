package store_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func openSearchStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), store.Options{Path: t.TempDir() + "/search.sqlite"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(t.Context()) })
	return st
}

func seedSearchUser(t *testing.T, st *store.Store, name, email, full string) int64 {
	t.Helper()
	u, err := st.InsertAuthUser(t.Context(), store.AuthUserCreate{
		Name: name, Lower: name, Email: email, FullName: full,
		Hash: "test-hash", Algo: "test", Now: time.Now().UnixMilli(),
	})
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return u.ID
}

func seedSearchRepo(t *testing.T, st *store.Store, owner int64, name, desc string) int64 {
	t.Helper()
	var id int64
	err := st.Write(t.Context(), func(tx *store.WriteTx) error {
		var ierr error
		id, ierr = tx.InsertRepo(time.Now(), store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: owner, Valid: true},
			Name:          name,
			Visibility:    "public",
			Description:   desc,
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

func TestSearchFTSTriggers(t *testing.T) {
	st := openSearchStore(t)
	ada := seedSearchUser(t, st, "ada", "ada-secret@example.test", "Ada Admin")
	seedSearchRepo(t, st, ada, "hello", "Hello world service")
	err := st.Read(t.Context(), func(tx *store.ReadTx) error {
		users, uerr := tx.SearchUsersFTS("admin", "", 0, 10)
		if uerr != nil {
			return uerr
		}
		if len(users) != 1 || users[0].Name != "ada" {
			t.Fatalf("full-name search = %+v, want ada", users)
		}
		probed, perr := tx.SearchUsersFTS("ada-secret", "", 0, 10)
		if perr != nil {
			return perr
		}
		if len(probed) != 0 {
			t.Fatalf("email probe matched %+v, want no rows", probed)
		}
		repos, rerr := tx.SearchReposFTS("world", "", 0, 10)
		if rerr != nil {
			return rerr
		}
		if len(repos) != 1 || repos[0].Name != "hello" {
			t.Fatalf("description search = %+v, want hello", repos)
		}
		short, serr := tx.SearchReposFTS("o", "", 0, 10)
		if serr != nil {
			return serr
		}
		if len(short) != 1 {
			t.Fatalf("one-char search = %d rows, want 1", len(short))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
}

func TestSearchFTSRenameSync(t *testing.T) {
	st := openSearchStore(t)
	ada := seedSearchUser(t, st, "ada", "ada@example.test", "Ada Admin")
	id := seedSearchRepo(t, st, ada, "oldname", "old description")
	err := st.Write(t.Context(), func(tx *store.WriteTx) error {
		return tx.RenameRepo(id, ada, "newname", "seed/newname.git", time.Now())
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	err = st.Read(t.Context(), func(tx *store.ReadTx) error {
		stale, serr := tx.SearchReposFTS("oldname", "", 0, 10)
		if serr != nil {
			return serr
		}
		if len(stale) != 0 {
			t.Fatalf("old name still matches %+v", stale)
		}
		fresh, ferr := tx.SearchReposFTS("newname", "", 0, 10)
		if ferr != nil {
			return ferr
		}
		if len(fresh) != 1 {
			t.Fatalf("new name matches %d rows, want 1", len(fresh))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
}
