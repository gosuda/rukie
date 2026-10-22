package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIntentDatabaseConstraintsRejectInvalidRows(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		db, err := tx.live()
		if err != nil {
			return err
		}
		_, err = db.ExecContext(tx.ctx, `INSERT INTO intents
(public_id, repo_id, number, title, body, state, contract, reporter_id, created_at, updated_at)
VALUES ('bad', ?, 1, 'bad', '', 'open', '{}', ?, ?, ?)`, repoID, userID, msOf(now), msOf(now))
		if err == nil {
			t.Fatal("invalid public id accepted by SQLite")
		}
		_, err = db.ExecContext(tx.ctx, `INSERT INTO intents
(public_id, repo_id, number, title, body, state, contract, reporter_id, created_at, updated_at)
VALUES (?, ?, 1, 'bad', ?, 'open', '{}', ?, ?, ?)`, string(NewID(now.Add(time.Second))), repoID, strings.Repeat("x", 65537), userID, msOf(now), msOf(now))
		if err == nil {
			t.Fatal("oversized body accepted by SQLite")
		}
		return nil
	}); err != nil {
		t.Fatalf("constraint probes: %v", err)
	}
}

func TestIntentListGetUpdateClose(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	var first, second IntentRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		first, ierr = tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "one", CreatedBy: userID})
		if ierr != nil {
			return ierr
		}
		second, ierr = tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "two", CreatedBy: userID})
		return ierr
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if first.Number != 1 || second.Number != 2 {
		t.Fatalf("numbers = %d, %d, want 1, 2", first.Number, second.Number)
	}
	if first.Version != 1 || first.State != "open" {
		t.Fatalf("first = %+v, want open version 1", first)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		byPublic, rerr := tx.ReadIntentByPublicID(repoID, first.PublicID)
		if rerr != nil {
			return rerr
		}
		if byPublic.ID != first.ID {
			t.Fatalf("public lookup id = %d, want %d", byPublic.ID, first.ID)
		}
		byNum, rerr := tx.ReadIntentByNumber(repoID, 2)
		if rerr != nil {
			return rerr
		}
		if byNum.ID != second.ID {
			t.Fatalf("number lookup id = %d, want %d", byNum.ID, second.ID)
		}
		listed, rerr := tx.ListIntents(repoID, "open", 10)
		if rerr != nil {
			return rerr
		}
		if len(listed) != 2 {
			t.Fatalf("listed = %d, want 2", len(listed))
		}
		if _, rerr := tx.ListIntents(repoID, "", 0); !errors.Is(rerr, ErrInvalid) {
			t.Fatalf("limit 0 err = %v, want invalid", rerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if _, uerr := tx.UpdateIntent(now, IntentRow{ID: first.ID, RepoID: repoID, Title: "x"}, first.Version-1); !errors.Is(uerr, ErrStale) {
			t.Fatalf("stale update err = %v, want stale", uerr)
		}
		updated, uerr := tx.UpdateIntent(now, IntentRow{
			ID: first.ID, RepoID: repoID, Title: "one edited", Body: "b",
			Contract: `{"requirements":[]}`, LeaseMode: "enforce", ClaimPaths: `["a"]`,
		}, first.Version)
		if uerr != nil {
			return uerr
		}
		if updated.Version != first.Version+1 || updated.LeaseMode != "enforce" {
			t.Fatalf("updated = %+v", updated)
		}
		first = updated
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if _, cerr := tx.SetIntentStateVersion(now, first.ID, first.Version, "open", "completed"); !errors.Is(cerr, ErrInvalid) {
			t.Fatalf("reason on open err = %v, want invalid", cerr)
		}
		if _, cerr := tx.SetIntentStateVersion(now, first.ID, first.Version, "closed", "bogus"); !errors.Is(cerr, ErrInvalid) {
			t.Fatalf("bogus reason err = %v, want invalid", cerr)
		}
		closed, cerr := tx.SetIntentStateVersion(now.Add(time.Second), first.ID, first.Version, "closed", "duplicate")
		if cerr != nil {
			return cerr
		}
		if closed.State != "closed" || !closed.CloseReason.Valid || closed.CloseReason.String != "duplicate" {
			t.Fatalf("closed = %+v, want duplicate", closed)
		}
		if !closed.ClosedAt.Valid {
			t.Fatalf("closed_at missing in %+v", closed)
		}
		events, oerr := tx.Outbox().After("intent-test-closed", 50)
		if oerr != nil {
			return oerr
		}
		seen := map[string]bool{}
		for _, ev := range events {
			seen[ev.Kind] = true
		}
		if !seen["task.created"] || !seen["task.closed"] {
			t.Fatalf("outbox kinds = %v, want task.created and task.closed", seen)
		}
		return nil
	}); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestIntentFacetsClosedSchema(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	var intent IntentRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		intent, ierr = tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "facets", CreatedBy: userID})
		return ierr
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		for _, key := range []string{"risk", "area", "size"} {
			if _, rerr := tx.ReadFacet(repoID, key); rerr != nil {
				t.Fatalf("seed facet %s: %v", key, rerr)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seeds: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if serr := tx.SetIntentFacets(now, intent.ID, map[string]string{"nope": `"x"`}); !errors.Is(serr, ErrNotFound) {
			t.Fatalf("unknown facet err = %v, want not found", serr)
		}
		return tx.SetIntentFacets(now, intent.ID, map[string]string{"risk": `"low"`, "area": `"docs"`})
	}); err != nil {
		t.Fatalf("set facets: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		got, rerr := tx.ListIntentFacets(intent.ID)
		if rerr != nil {
			return rerr
		}
		if got["risk"] != `"low"` || got["area"] != `"docs"` {
			t.Fatalf("facets = %v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("list facets: %v", err)
	}
}

func TestIntentChangeLinks(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	head := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var intent IntentRow
	var change ChangeRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		intent, ierr = tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "links", CreatedBy: userID})
		if ierr != nil {
			return ierr
		}
		change, ierr = tx.InsertChange(now, ChangeRow{RepoID: repoID, ChangeKey: "C1", TargetBranch: "main", AuthorUserID: userID, HeadOid: head})
		return ierr
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		link, ierr := tx.InsertIntentChange(now, IntentChangeRow{IntentID: intent.ID, ChangeID: "C1", Relation: "fixes"})
		if ierr != nil {
			return ierr
		}
		if link.RepoID != repoID || link.Relation != "fixes" {
			t.Fatalf("link = %+v", link)
		}
		if _, ierr := tx.InsertIntentChange(now, IntentChangeRow{IntentID: 999999, ChangeID: "C1"}); !errors.Is(ierr, ErrNotFound) {
			t.Fatalf("cross-intent err = %v, want not found", ierr)
		}
		return tx.LinkIntentChange(intent.ID, change.ID, head)
	}); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		links, rerr := tx.ListIntentChanges(intent.ID)
		if rerr != nil {
			return rerr
		}
		if len(links) != 1 || links[0].ChangeID != "C1" {
			t.Fatalf("links = %+v, want one C1", links)
		}
		return nil
	}); err != nil {
		t.Fatalf("list links: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.UnlinkIntentChange(intent.ID, change.ID)
	}); err != nil {
		t.Fatalf("unlink: %v", err)
	}
}

func TestAskBlocksAndAnswerUnblocks(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	var intent IntentRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		intent, ierr = tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "ask", CreatedBy: userID})
		return ierr
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var ask AskRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		ask, ierr = tx.InsertAsk(now, AskRow{
			IntentID: intent.ID, Question: "go?", Options: `[{"id":"a","label":"A"}]`,
			BlocksClaim: true, CreatedBy: userID, DeadlineMS: msOf(now.Add(time.Hour)),
		})
		return ierr
	}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		n, rerr := tx.CountOpenBlockingAsks(intent.ID)
		if rerr != nil {
			return rerr
		}
		if n != 1 {
			t.Fatalf("blocking = %d, want 1", n)
		}
		got, rerr := tx.ReadIntentByPublicID(repoID, intent.PublicID)
		if rerr != nil {
			return rerr
		}
		if got.State != "blocked" {
			t.Fatalf("intent state = %q, want blocked", got.State)
		}
		return nil
	}); err != nil {
		t.Fatalf("blocked: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		return tx.AnswerAsk(intent.ID, ask.ID, userID, "a")
	}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		n, rerr := tx.CountOpenBlockingAsks(intent.ID)
		if rerr != nil {
			return rerr
		}
		if n != 0 {
			t.Fatalf("blocking = %d, want 0", n)
		}
		got, rerr := tx.ReadIntentByPublicID(repoID, intent.PublicID)
		if rerr != nil {
			return rerr
		}
		if got.State != "open" {
			t.Fatalf("intent state = %q, want open", got.State)
		}
		asked, rerr := tx.ListAsks(repoID, intent.ID, "asked", 10)
		if rerr != nil {
			return rerr
		}
		if len(asked) != 0 {
			t.Fatalf("asked = %d, want 0", len(asked))
		}
		byPublic, rerr := tx.ReadAskByPublicID(repoID, ask.PublicID)
		if rerr != nil {
			return rerr
		}
		if byPublic.State != "answered" || byPublic.Answer != "a" {
			t.Fatalf("ask = %+v, want answered a", byPublic)
		}
		return nil
	}); err != nil {
		t.Fatalf("unblocked: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if aerr := tx.AnswerAskAt(now, intent.ID, ask.ID, userID, "again"); !errors.Is(aerr, ErrStale) {
			t.Fatalf("re-answer err = %v, want stale", aerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("re-answer: %v", err)
	}
}

func TestAskExpiryNeverAnswers(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	var intent IntentRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		intent, ierr = tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "expiry", CreatedBy: userID})
		return ierr
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var ask AskRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		ask, ierr = tx.InsertAsk(now, AskRow{
			IntentID: intent.ID, Question: "late?", BlocksClaim: true,
			CreatedBy: userID, DeadlineMS: msOf(now.Add(time.Hour)),
		})
		return ierr
	}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	late := now.Add(2 * time.Hour)
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if aerr := tx.AnswerAskAt(late, intent.ID, ask.ID, userID, "too late"); !errors.Is(aerr, ErrStale) {
			t.Fatalf("late answer err = %v, want stale", aerr)
		}
		return tx.ExpireIntentAsks(late, intent.ID)
	}); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		n, rerr := tx.CountOpenBlockingAsksAt(late, intent.ID)
		if rerr != nil {
			return rerr
		}
		if n != 0 {
			t.Fatalf("blocking = %d, want 0", n)
		}
		got, rerr := tx.ReadIntentByPublicID(repoID, intent.PublicID)
		if rerr != nil {
			return rerr
		}
		if got.State != "open" {
			t.Fatalf("intent state = %q, want open", got.State)
		}
		return nil
	}); err != nil {
		t.Fatalf("expired: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if cerr := tx.CancelAsk(late, ask.ID, ask.Version); !errors.Is(cerr, ErrStale) {
			t.Fatalf("cancel expired err = %v, want stale", cerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

func TestAskCancelVersioned(t *testing.T) {
	st, now := openReviewTest(t)
	repoID, userID := seedReviewRepo(t, st, now)
	var intent IntentRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		intent, ierr = tx.InsertIntent(now, IntentRow{RepoID: repoID, Title: "cancel", CreatedBy: userID})
		return ierr
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var ask AskRow
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		var ierr error
		ask, ierr = tx.InsertAsk(now, AskRow{
			IntentID: intent.ID, Question: "stop?", BlocksClaim: true,
			CreatedBy: userID, DeadlineMS: msOf(now.Add(time.Hour)),
		})
		return ierr
	}); err != nil {
		t.Fatalf("ask: %v", err)
	}
	if err := st.Write(t.Context(), func(tx *WriteTx) error {
		if cerr := tx.CancelAsk(now, ask.ID, ask.Version+1); !errors.Is(cerr, ErrStale) {
			t.Fatalf("wrong version err = %v, want stale", cerr)
		}
		return tx.CancelAsk(now, ask.ID, ask.Version)
	}); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := st.Read(t.Context(), func(tx *ReadTx) error {
		n, rerr := tx.CountOpenBlockingAsks(intent.ID)
		if rerr != nil {
			return rerr
		}
		if n != 0 {
			t.Fatalf("blocking = %d, want 0", n)
		}
		return nil
	}); err != nil {
		t.Fatalf("unblocked: %v", err)
	}
}
