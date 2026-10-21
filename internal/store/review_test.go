package store

import (
	"database/sql"
	"testing"
	"time"
)

func openReviewTest(t *testing.T) (*Store, time.Time) {
	t.Helper()
	now := time.Now()
	st, err := Open(t.Context(), Options{Path: t.TempDir() + "/review.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(t.Context()) })
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		_, uerr := inner.ExecContext(tx.ctx, `INSERT INTO users
(name, lower_name, email, password_hash, password_algo, password_changed_at, is_admin, created_at, updated_at)
VALUES ('ada', 'ada', 'ada@example.test', 'x', 'argon2id-v19', 1, 1, 1, 1)`)
		return uerr
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return st, now
}

func seedReviewRepo(t *testing.T, st *Store, now time.Time) (int64, int64) {
	t.Helper()
	var repoID, userID int64
	err := st.Write(t.Context(), func(tx *WriteTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if qerr := inner.QueryRowContext(tx.ctx,
			`SELECT id FROM users WHERE lower_name = 'ada'`).Scan(&userID); qerr != nil {
			return qerr
		}
		id, ierr := tx.InsertRepo(now, RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: userID, Valid: true},
			Name:          "hello",
			Visibility:    "private",
			DefaultBranch: "main",
			RukPath:       "ada/hello.git",
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

func TestReviewChangePatchsetFlow(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	headA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	var changeID int64
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		row, ierr := tx.InsertChange(now, ChangeRow{
			RepoID: repoID, ChangeKey: "main~7", TargetBranch: "main",
			AuthorUserID: userID, HeadOid: headA,
		})
		if ierr != nil {
			return ierr
		}
		changeID = row.ID
		if row.PatchsetCount != 1 {
			t.Fatalf("patchsets = %d, want 1", row.PatchsetCount)
		}
		updated, aerr := tx.AppendPatchset(now, row.ID, headB)
		if aerr != nil {
			return aerr
		}
		if updated.PatchsetCount != 2 || updated.HeadOid != headB {
			t.Fatalf("updated = %+v, want 2 patchsets at head B", updated)
		}
		return tx.SetChangeState(now, row.ID, "abandoned")
	}); err != nil {
		t.Fatalf("flow: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		sets, lerr := tx.ListPatchsets(changeID)
		if lerr != nil {
			return lerr
		}
		if len(sets) != 2 || sets[0].HeadOid != headA || sets[1].HeadOid != headB {
			t.Fatalf("patchsets = %+v, want A then B", sets)
		}
		return nil
	}); err != nil {
		t.Fatalf("patchsets: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		rows, lerr := tx.ListChanges(repoID, "abandoned", 10)
		if lerr != nil {
			return lerr
		}
		if len(rows) != 1 || rows[0].ID != changeID {
			t.Fatalf("abandoned changes = %+v", rows)
		}
		return nil
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
}

func TestReviewCommentRemap(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	headA := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		row, ierr := tx.InsertChange(now, ChangeRow{
			RepoID: repoID, ChangeKey: "main~t", TargetBranch: "main",
			AuthorUserID: userID, HeadOid: headA,
		})
		if ierr != nil {
			return ierr
		}
		live := CommentRow{ChangeID: row.ID, HeadOid: headA, Body: "nit",
			Path: sql.NullString{String: "a.txt", Valid: true},
			Line: sql.NullInt64{Int64: 3, Valid: true}, AuthorUserID: userID}
		if _, ierr := tx.InsertComment(now, live); ierr != nil {
			return ierr
		}
		if _, ierr := tx.InsertComment(now, CommentRow{
			ChangeID: row.ID, HeadOid: headA, Body: "overall", AuthorUserID: userID,
		}); ierr != nil {
			return ierr
		}
		comments, lerr := tx.ListCommentsAtHeadIncludingUnmatched(row.ID, headA)
		if lerr != nil {
			return lerr
		}
		if len(comments) != 2 {
			t.Fatalf("comments = %d, want 2", len(comments))
		}
		line := sql.NullInt64{Int64: 5, Valid: true}
		if rerr := tx.RemapComment(row.ID, comments[0].ID, headA, headB, line, false); rerr != nil {
			return rerr
		}
		if rerr := tx.RemapComment(row.ID, comments[1].ID, headA, headB, sql.NullInt64{}, true); rerr != nil {
			return rerr
		}
		moved, merr := tx.ListCommentsAtHeadIncludingUnmatched(row.ID, headB)
		if merr != nil {
			return merr
		}
		var current []CommentRow
		for _, comment := range moved {
			if !comment.Unmatched {
				current = append(current, comment)
			}
		}
		if len(current) != 1 || !current[0].Line.Valid || current[0].Line.Int64 != 5 {
			t.Fatalf("remapped = %+v", moved)
		}
		return nil
	}); err != nil {
		t.Fatalf("remap: %v", err)
	}
}

func TestLandIntentMachine(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var planID int64
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		plan, ierr := tx.InsertLandIntent(now, LandIntentRow{
			RepoID: repoID, TargetRef: "refs/heads/main",
			CandidateOid: head, BaseOid: head, PolicyGeneration: 1,
			Gap: "[]", CreatedBy: userID,
		})
		if ierr != nil {
			return ierr
		}
		planID = plan.ID
		if plan.State != "awaiting" {
			t.Fatalf("plan state = %q, want awaiting", plan.State)
		}
		return nil
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	illegal := []string{"landed", "repreparing"}
	for _, state := range illegal {
		if err := st.Write(t.Context(), func(tx *WriteTx) error {
			return tx.SetLandIntentState(now, planID, state)
		}); err == nil {
			t.Fatalf("awaiting to %s succeeded", state)
		}
	}
	flow := []string{"executing", "failed", "repreparing", "awaiting", "executing", "landed"}
	for _, state := range flow {
		if err := st.Write(t.Context(), func(tx *WriteTx) error {
			return tx.SetLandIntentState(now, planID, state)
		}); err != nil {
			t.Fatalf("move to %s: %v", state, err)
		}
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.SetLandIntentState(now, planID, "awaiting")
	}); err == nil {
		t.Fatal("landed plan moved")
	}
}

func TestLandIntentReprepareCap(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var planID int64
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		plan, ierr := tx.InsertLandIntent(now, LandIntentRow{
			RepoID: repoID, TargetRef: "refs/heads/main",
			CandidateOid: head, BaseOid: head, PolicyGeneration: 1,
			Gap: "[]", CreatedBy: userID,
		})
		if ierr != nil {
			return ierr
		}
		planID = plan.ID
		return tx.SetLandIntentState(now, plan.ID, "failed")
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := range MaxLandReprepares {
		if err := st.Write(t.Context(), func(tx *WriteTx) error {
			if serr := tx.SetLandIntentState(now, planID, "repreparing"); serr != nil {
				return serr
			}
			return tx.SetLandIntentState(now, planID, "failed")
		}); err != nil {
			t.Fatalf("reprepare %d: %v", i, err)
		}
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.SetLandIntentState(now, planID, "repreparing")
	}); err == nil {
		t.Fatal("fourth reprepare succeeded past the cap")
	}
}

func TestIntentAndViewFlow(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		first, ierr := tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "one", CreatedBy: userID})
		if ierr != nil {
			return ierr
		}
		second, ierr := tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "two", CreatedBy: userID})
		if ierr != nil {
			return ierr
		}
		if first.Number != 1 || second.Number != 2 {
			t.Fatalf("numbers = %d, %d", first.Number, second.Number)
		}
		ask, aerr := tx.InsertAsk(now, AskRow{
			IntentID: first.ID, Question: "which?", Options: `[{"id":"a","label":"A"}]`,
			BlocksClaim: true, CreatedBy: userID,
		})
		if aerr != nil {
			return aerr
		}
		if aerr := tx.AnswerAsk(first.ID, ask.ID, userID, "a"); aerr != nil {
			return aerr
		}
		view, verr := tx.InsertView(now, ViewRow{RepoID: repoID, Name: "gaps", Shared: true})
		if verr != nil {
			return verr
		}
		if _, verr := tx.AppendViewVersion(now, view.ID, "tip", `{"gap:*":true}`); verr != nil {
			return verr
		}
		return tx.SetViewShared(now, view.ID, false)
	}); err != nil {
		t.Fatalf("flow: %v", err)
	}
}
