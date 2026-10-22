package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"
)

func openMirrorTest(t *testing.T) (*Store, time.Time) {
	t.Helper()
	now := time.Now()
	st, err := Open(t.Context(), Options{Path: t.TempDir() + "/mirror.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(t.Context()) })
	return st, now
}

func seedMirrorRepo(t *testing.T, st *Store, now time.Time, name string) (int64, int64) {
	t.Helper()
	var repoID, userID int64
	err := st.Write(t.Context(), func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if qerr := inner.QueryRowContext(tx.ctx,
			`SELECT id FROM users WHERE lower_name = 'ada'`).Scan(&userID); qerr != nil {
			if !errors.Is(qerr, sql.ErrNoRows) {
				return qerr
			}
			if qerr = inner.QueryRowContext(tx.ctx, `INSERT INTO users
(name, lower_name, email, password_hash, password_algo, password_changed_at, is_admin, created_at, updated_at)
VALUES ('ada', 'ada', 'ada@example.test', 'x', 'argon2id-v19', 1, 1, 1, 1)
RETURNING id`).Scan(&userID); qerr != nil {
				return qerr
			}
		}
		id, ierr := tx.InsertRepo(now, RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: userID, Valid: true},
			Name:          name,
			Visibility:    "private",
			DefaultBranch: "main",
			RukPath:       "ada/" + name + ".git",
		})
		if ierr != nil {
			return ierr
		}
		repoID = id
		return tx.InsertDefaultRule(id, now, "main")
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	return repoID, userID
}

func insertMirrorFixture(t *testing.T, st *Store, now time.Time, repoID int64, name string) MirrorRow {
	t.Helper()
	var row MirrorRow
	err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		row, ierr = tx.InsertMirror(now, MirrorInsert{
			RepoID:       repoID,
			Name:         name,
			TargetURL:    "https://mirror.example.test/" + name + ".git",
			HostKind:     "generic",
			Mode:         "exact",
			RefSelection: "all",
			Enabled:      true,
			State:        "idle",
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("insert mirror %q: %v", name, err)
	}
	return row
}

func TestMirrorCRUD(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	row := insertMirrorFixture(t, st, now, repoID, "edge")
	if row.ID == 0 || row.PublicID == "" {
		t.Fatalf("insert = %+v, want ids assigned", row)
	}
	if row.DebounceSeconds != 0 || row.MaxDelaySeconds != 0 || row.ReconcileIntervalSeconds != 0 {
		t.Fatalf("insert = %+v, want zero scheduler intervals", row)
	}
	if row.State != "idle" || !row.Enabled || row.Globs != "[]" {
		t.Fatalf("insert = %+v, want idle enabled with default globs", row)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		byID, gerr := tx.GetMirror(row.ID)
		if gerr != nil {
			return gerr
		}
		byPub, gerr := tx.GetMirrorByPublicID(row.PublicID)
		if gerr != nil {
			return gerr
		}
		byName, gerr := tx.GetMirrorByRepoName(repoID, "edge")
		if gerr != nil {
			return gerr
		}
		if byID.ID != row.ID || byPub.ID != row.ID || byName.ID != row.ID {
			t.Fatalf("reads = %d %d %d, want %d", byID.ID, byPub.ID, byName.ID, row.ID)
		}
		if _, gerr := tx.GetMirror(row.ID + 999); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("missing mirror err = %v, want ErrNotFound", gerr)
		}
		if _, gerr := tx.GetMirrorByPublicID("ZZZZ"); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("missing public err = %v, want ErrNotFound", gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("reads: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		updated, uerr := tx.UpdateMirror(now, row.ID, MirrorPatch{
			TargetURL:                new("https://mirror.example.test/edge2.git"),
			Enabled:                  new(false),
			DebounceSeconds:          new(uint64(10)),
			MaxDelaySeconds:          new(uint64(300)),
			ReconcileIntervalSeconds: new(uint64(600)),
		})
		if uerr != nil {
			return uerr
		}
		if updated.TargetURL != "https://mirror.example.test/edge2.git" || updated.Enabled {
			t.Fatalf("updated = %+v, want new url and disabled", updated)
		}
		if updated.DebounceSeconds != 10 || updated.MaxDelaySeconds != 300 || updated.ReconcileIntervalSeconds != 600 {
			t.Fatalf("updated = %+v, want scheduler intervals", updated)
		}
		if updated.Name != "edge" || updated.Mode != "exact" {
			t.Fatalf("updated = %+v, want untouched fields kept", updated)
		}
		if _, uerr := tx.UpdateMirror(now, row.ID+999, MirrorPatch{Enabled: new(true)}); !errors.Is(uerr, ErrNotFound) {
			t.Fatalf("missing update err = %v, want ErrNotFound", uerr)
		}
		return tx.DeleteMirror(row.ID)
	}); err != nil {
		t.Fatalf("update/delete: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		if _, gerr := tx.GetMirror(row.ID); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("deleted read err = %v, want ErrNotFound", gerr)
		}
		if n, cerr := tx.CountMirrors(repoID); cerr != nil || n != 0 {
			t.Fatalf("count = %d, %v; want 0", n, cerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("post-delete: %v", err)
	}
}

func TestMirrorDuplicateNameInvalid(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	insertMirrorFixture(t, st, now, repoID, "edge")
	err := st.Write(t.Context(), func(tx *WriteTx) error {
		_, ierr := tx.InsertMirror(now, MirrorInsert{
			RepoID: repoID, Name: "edge", TargetURL: "https://mirror.example.test/other.git",
			HostKind: "generic", Mode: "exact", RefSelection: "all", State: "idle",
		})
		return ierr
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate insert err = %v, want ErrInvalid", err)
	}
	otherID, _ := seedMirrorRepo(t, st, now, "world")
	row := insertMirrorFixture(t, st, now, otherID, "edge")
	if row.RepoID != otherID {
		t.Fatalf("cross-repo same name repo = %d, want %d", row.RepoID, otherID)
	}
	var secondID int64
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		second, gerr := tx.InsertMirror(now, MirrorInsert{
			RepoID: repoID, Name: "plain", TargetURL: "https://mirror.example.test/plain.git",
			HostKind: "generic", Mode: "exact", RefSelection: "all", State: "idle",
		})
		if gerr != nil {
			return gerr
		}
		secondID = second.ID
		return nil
	}); err != nil {
		t.Fatalf("seed plain: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		_, uerr := tx.UpdateMirror(now, secondID, MirrorPatch{Name: new("edge")})
		return uerr
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate rename err = %v, want ErrInvalid", err)
	}
}

func TestMirrorDeleteCascades(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	row := insertMirrorFixture(t, st, now, repoID, "edge")
	var runID int64
	var runPub string
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if perr := tx.PutCredential(CredentialRow{
			MirrorID: row.ID, KeyVersion: 1,
			Nonce: []byte("0123456789ab"), Ciphertext: []byte("sealed"),
			Last4: "CANARY-abcd", CreatedAt: msOf(now),
		}); perr != nil {
			return perr
		}
		run, rerr := tx.InsertRun(now, MirrorRunRow{
			MirrorID: row.ID, Kind: "sync", State: "queued", Trigger: "manual",
		})
		if rerr != nil {
			return rerr
		}
		runID, runPub = run.ID, run.PublicID
		if _, aerr := tx.InsertAttempt(AttemptRow{
			RunID: run.ID, AttemptNo: 1, StartedAt: msOf(now),
		}); aerr != nil {
			return aerr
		}
		return tx.ReplaceRefStates(row.ID, []RefStateRow{
			{MirrorID: row.ID, Ref: "refs/heads/main", Status: "in_sync"},
		})
	}); err != nil {
		t.Fatalf("seed relations: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.DeleteMirror(row.ID)
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		if _, gerr := tx.GetCredential(row.ID); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("credential err = %v, want ErrNotFound", gerr)
		}
		if _, gerr := tx.GetRunByPublicID(runPub); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("run err = %v, want ErrNotFound", gerr)
		}
		got, _, lerr := tx.ListRefStates(row.ID, "", 10)
		if lerr != nil {
			return lerr
		}
		if len(got) != 0 {
			t.Fatalf("refstates = %+v, want empty", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("cascade: %v", err)
	}
	_ = runID
}

func TestMirrorTouchDirtyGating(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	otherID, _ := seedMirrorRepo(t, st, now, "world")
	var plain, nosync, off, other MirrorRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		mk := func(repo int64, name string, enabled, sync bool) MirrorRow {
			var row MirrorRow
			row, ierr = tx.InsertMirror(now, MirrorInsert{
				RepoID: repo, Name: name, TargetURL: "https://mirror.example.test/" + name + ".git",
				HostKind: "generic", Mode: "exact", RefSelection: "all",
				Enabled: enabled, SyncOnRefUpdate: sync, State: "idle",
			})
			return row
		}
		plain = mk(repoID, "plain", true, true)
		nosync = mk(repoID, "nosync", true, false)
		off = mk(repoID, "off", false, true)
		other = mk(otherID, "plain", true, true)
		return ierr
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	touch := func(repo int64) int64 {
		t.Helper()
		var n int64
		if err := st.Write(t.Context(), func(tx *WriteTx) error {
			var terr error
			n, terr = tx.TouchMirrorDirty(now, repo)
			return terr
		}); err != nil {
			t.Fatalf("touch: %v", err)
		}
		return n
	}
	if n := touch(repoID); n != 1 {
		t.Fatalf("touched = %d, want 1", n)
	}
	if n := touch(repoID); n != 0 {
		t.Fatalf("re-touch = %d, want 0", n)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		for _, tc := range []struct {
			id    int64
			dirty bool
		}{{plain.ID, true}, {nosync.ID, false}, {off.ID, false}, {other.ID, false}} {
			row, gerr := tx.GetMirror(tc.id)
			if gerr != nil {
				return gerr
			}
			if got := row.DirtySince != 0; got != tc.dirty {
				t.Fatalf("mirror %d dirty = %v, want %v", tc.id, got, tc.dirty)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("dirty flags: %v", err)
	}
}

func TestMirrorDueGating(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	at := msOf(now)
	ids := map[string]int64{}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		mk := func(name string, enabled bool) int64 {
			row, ierr := tx.InsertMirror(now, MirrorInsert{
				RepoID: repoID, Name: name, TargetURL: "https://mirror.example.test/" + name + ".git",
				HostKind: "generic", Mode: "exact", RefSelection: "all",
				Enabled: enabled, SyncOnRefUpdate: true, State: "idle",
			})
			if ierr != nil {
				t.Fatalf("seed %s: %v", name, ierr)
			}
			return row.ID
		}
		ids["fresh"] = mk("fresh", true)
		ids["dirty"] = mk("dirty", true)
		ids["past"] = mk("past", true)
		ids["future"] = mk("future", true)
		ids["off"] = mk("off", false)
		if _, terr := tx.TouchMirrorDirty(now, repoID); terr != nil {
			return terr
		}
		if serr := tx.SetMirrorResult(now, ids["fresh"], MirrorResult{
			State: "idle", ClearDirty: true,
		}); serr != nil {
			return serr
		}
		if serr := tx.SetMirrorResult(now, ids["dirty"], MirrorResult{
			State: "idle", NextRunAt: at + 3600000, ClearDirty: false,
		}); serr != nil {
			return serr
		}
		if serr := tx.SetMirrorResult(now, ids["past"], MirrorResult{
			State: "idle", NextRunAt: at - 1, ClearDirty: true,
		}); serr != nil {
			return serr
		}
		if serr := tx.SetMirrorResult(now, ids["future"], MirrorResult{
			State: "idle", NextRunAt: at + 3600000, ClearDirty: true,
		}); serr != nil {
			return serr
		}
		return tx.SetMirrorResult(now, ids["off"], MirrorResult{
			State: "idle", NextRunAt: at - 1, ClearDirty: true,
		})
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		due, derr := tx.DueMirrors(at, 10)
		if derr != nil {
			return derr
		}
		want := map[int64]bool{ids["dirty"]: true, ids["past"]: true}
		if len(due) != len(want) {
			t.Fatalf("due = %v, want %v", due, want)
		}
		for _, row := range due {
			if !want[row.ID] {
				t.Fatalf("due = %v, want %v", due, want)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("due: %v", err)
	}
}

func TestMirrorRefStatesSwap(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	row := insertMirrorFixture(t, st, now, repoID, "edge")
	first := []RefStateRow{
		{MirrorID: row.ID, Ref: "refs/heads/a", LocalOid: "a", RemoteOid: "a", Status: "in_sync"},
		{MirrorID: row.ID, Ref: "refs/heads/b", Status: "diverged", Reason: "ff"},
	}
	second := []RefStateRow{
		{MirrorID: row.ID, Ref: "refs/heads/b", LocalOid: "b", RemoteOid: "c", Status: "failing"},
		{MirrorID: row.ID, Ref: "refs/heads/c", Status: "in_sync"},
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.ReplaceRefStates(row.ID, first)
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.ReplaceRefStates(row.ID, second)
	}); err != nil {
		t.Fatalf("second: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		got, _, lerr := tx.ListRefStates(row.ID, "", 10)
		if lerr != nil {
			return lerr
		}
		if len(got) != 2 || got[0].Ref != "refs/heads/b" || got[1].Ref != "refs/heads/c" {
			t.Fatalf("refstates = %+v, want b then c", got)
		}
		if got[0].Status != "failing" || got[0].LocalOid != "b" {
			t.Fatalf("b = %+v, want failing at b", got[0])
		}
		return nil
	}); err != nil {
		t.Fatalf("swap: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.ReplaceRefStates(row.ID, nil)
	}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		got, _, lerr := tx.ListRefStates(row.ID, "", 10)
		if lerr != nil {
			return lerr
		}
		if len(got) != 0 {
			t.Fatalf("cleared = %+v, want empty", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.ReplaceRefStates(row.ID+999, first)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing mirror err = %v, want ErrNotFound", err)
	}
}

func TestMirrorRunAttemptFlow(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	row := insertMirrorFixture(t, st, now, repoID, "edge")
	var run MirrorRunRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var rerr error
		run, rerr = tx.InsertRun(now, MirrorRunRow{
			MirrorID: row.ID, Kind: "sync", State: "queued", Trigger: "manual",
		})
		return rerr
	}); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	if run.PublicID == "" || run.Counts != "{}" {
		t.Fatalf("run = %+v, want minted public id and {} counts", run)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		got, gerr := tx.GetRunByPublicID(run.PublicID)
		if gerr != nil {
			return gerr
		}
		if got.ID != run.ID || got.DryRun || got.AttemptNo != 0 {
			t.Fatalf("run = %+v, want defaults", got)
		}
		if _, gerr := tx.GetRunByPublicID("ZZZZ"); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("missing run err = %v, want ErrNotFound", gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("get run: %v", err)
	}
	at := msOf(now)
	var attempt AttemptRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var aerr error
		attempt, aerr = tx.InsertAttempt(AttemptRow{
			RunID: run.ID, AttemptNo: 1, RukPlan: "plan", StartedAt: at,
		})
		return aerr
	}); err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	if attempt.ID == 0 {
		t.Fatalf("attempt = %+v, want id", attempt)
	}
	finished := at + 1000
	updated := run
	updated.State = "succeeded"
	updated.AttemptNo = 1
	updated.OutcomeClass = "ok"
	updated.Counts = `{"ok":1}`
	updated.StartedAt = at
	updated.FinishedAt = finished
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if uerr := tx.UpdateAttempt(attempt.ID, finished, "succeeded", "done"); uerr != nil {
			return uerr
		}
		if uerr := tx.UpdateAttempt(attempt.ID+999, finished, "x", "y"); !errors.Is(uerr, ErrNotFound) {
			t.Fatalf("missing attempt err = %v, want ErrNotFound", uerr)
		}
		return tx.UpdateRun(now, run.ID, updated)
	}); err != nil {
		t.Fatalf("updates: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.UpdateRun(now, run.ID+999, updated)
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing run update err = %v, want ErrNotFound", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		got, gerr := tx.GetRunByPublicID(run.PublicID)
		if gerr != nil {
			return gerr
		}
		if got.State != "succeeded" || got.AttemptNo != 1 || got.Counts != `{"ok":1}` {
			t.Fatalf("run = %+v, want updated", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("run after: %v", err)
	}
}

func TestMirrorCredentialRotate(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	row := insertMirrorFixture(t, st, now, repoID, "edge")
	created := msOf(now)
	first := CredentialRow{
		MirrorID: row.ID, KeyVersion: 1,
		Nonce: []byte("0123456789ab"), Ciphertext: []byte("first"),
		Last4: "CANARY-1111", CreatedAt: created,
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.PutCredential(first)
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	rotated := created + 1000
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		got, gerr := tx.GetCredential(row.ID)
		if gerr != nil {
			return gerr
		}
		if got.KeyVersion != 1 || string(got.Ciphertext) != "first" || got.CreatedAt != created {
			t.Fatalf("credential = %+v, want version 1", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("first seal: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.PutCredential(CredentialRow{
			MirrorID: row.ID, KeyVersion: 2,
			Nonce: []byte("abcdefghijkl"), Ciphertext: []byte("second"),
			Last4: "CANARY-2222", CreatedAt: created, RotatedAt: rotated,
		})
	}); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		got, gerr := tx.GetCredential(row.ID)
		if gerr != nil {
			return gerr
		}
		if got.KeyVersion != 2 || string(got.Ciphertext) != "second" || got.Last4 != "CANARY-2222" {
			t.Fatalf("credential = %+v, want version 2", got)
		}
		if got.CreatedAt != created || got.RotatedAt != rotated || got.LastUsedAt != 0 {
			t.Fatalf("credential = %+v, want kept created, new rotated, untouched last used", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("after rotate: %v", err)
	}
}

func TestMirrorSetResult(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	row := insertMirrorFixture(t, st, now, repoID, "edge")
	at := msOf(now)
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if _, terr := tx.TouchMirrorDirty(now, repoID); terr != nil {
			return terr
		}
		return tx.SetMirrorResult(now, row.ID, MirrorResult{
			State: "failing", ConsecutiveFailures: 2, LastErrorClass: "network",
			LastError: "timeout", LastHTTPStatus: 502, LastAttemptAt: at,
			NextRunAt: at + 60000, EventCursor: 7, Adopted: true, ClearDirty: true,
		})
	}); err != nil {
		t.Fatalf("set result: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		got, gerr := tx.GetMirror(row.ID)
		if gerr != nil {
			return gerr
		}
		if got.State != "failing" || got.ConsecutiveFailures != 2 || !got.Adopted {
			t.Fatalf("mirror = %+v, want failing/2/adopted", got)
		}
		if got.LastErrorClass != "network" || got.LastHTTPStatus != 502 {
			t.Fatalf("mirror = %+v, want error detail", got)
		}
		if got.DirtySince != 0 || got.NextRunAt != at+60000 || got.EventCursor != 7 {
			t.Fatalf("mirror = %+v, want cleared dirty with next run and cursor", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("after result: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.SetMirrorResult(now, row.ID+999, MirrorResult{State: "idle"})
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing result err = %v, want ErrNotFound", err)
	}
}

func TestMirrorListPagination(t *testing.T) {
	st, now := openMirrorTest(t)
	repoID, _ := seedMirrorRepo(t, st, now, "hello")
	row := insertMirrorFixture(t, st, now, repoID, "edge")
	var runIDs []int64
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		for _, state := range []string{"queued", "running", "succeeded"} {
			run, rerr := tx.InsertRun(now.Add(time.Duration(len(runIDs)+1)*time.Millisecond), MirrorRunRow{
				MirrorID: row.ID, Kind: "sync", State: state, Trigger: "manual",
			})
			if rerr != nil {
				return rerr
			}
			runIDs = append(runIDs, run.ID)
		}
		return nil
	}); err != nil {
		t.Fatalf("seed runs: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		page, next, lerr := tx.ListRuns(row.ID, "", "", 2)
		if lerr != nil {
			return lerr
		}
		if len(page) != 2 || next == "" {
			t.Fatalf("page = %v next = %q, want 2 rows and cursor", page, next)
		}
		if page[0].ID != runIDs[0] || page[1].ID != runIDs[1] {
			t.Fatalf("page = %v, want oldest first", page)
		}
		rest, _, lerr := tx.ListRuns(row.ID, "", next, 2)
		if lerr != nil {
			return lerr
		}
		if len(rest) != 1 || rest[0].ID != runIDs[2] {
			t.Fatalf("rest = %v, want last run", rest)
		}
		filtered, _, lerr := tx.ListRuns(row.ID, "succeeded", "", 10)
		if lerr != nil {
			return lerr
		}
		if len(filtered) != 1 || filtered[0].State != "succeeded" {
			t.Fatalf("filtered = %v, want one succeeded", filtered)
		}
		if n, cerr := tx.CountMirrors(repoID); cerr != nil || n != 1 {
			t.Fatalf("count = %d, %v; want 1", n, cerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("pagination: %v", err)
	}
}
