package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"encoding/json/jsontext"
)

func jsontextValue(s string) jsontext.Value { return jsontext.Value(s) }

func TestWatchCRUD(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	ada := seedF8User(t, s, "watchada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "watches")
	other := seedF8Repo(ctx, t, s, ada.ID, "watches2")

	if err := s.Read(ctx, func(tx *ReadTx) error {
		if _, gerr := tx.GetWatch(ada.ID, repo); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("absent watch = %v, want ErrNotFound", gerr)
		}
		if got, gerr := tx.ListWatches(ada.ID); gerr != nil || len(got) != 0 {
			t.Fatalf("empty watches = %d, %v", len(got), gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := s.Write(ctx, func(tx *WriteTx) error {
		w, serr := tx.SetWatch(ada.ID, repo, "custom", []string{"tasks", "ci"}, 3000)
		if serr != nil {
			return serr
		}
		if w.Level != "custom" || len(w.CustomBits) != 2 || w.UpdatedAt != 3000 {
			t.Fatalf("watch = %+v", w)
		}
		if _, gerr := tx.GetWatch(ada.ID, repo); gerr != nil {
			return gerr
		}
		if _, serr = tx.SetWatch(ada.ID, other, "all", nil, 3000); serr != nil {
			return serr
		}
		// Invalid: unknown level, bad bit, empty custom, bits on plain level.
		for i, args := range [][3]any{
			{"everything", nil, nil},
			{"custom", []string{"nope"}, nil},
			{"custom", []string{}, nil},
			{"custom", []string{"tasks", "tasks"}, nil},
			{"all", []string{"tasks"}, nil},
		} {
			level := args[0].(string)
			var bits []string
			if args[1] != nil {
				bits = args[1].([]string)
			}
			if _, serr = tx.SetWatch(ada.ID, repo, level, bits, 3000); !errors.Is(serr, ErrInvalid) {
				t.Fatalf("case %d: err = %v, want ErrInvalid", i, serr)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := s.Read(ctx, func(tx *ReadTx) error {
		got, lerr := tx.ListWatches(ada.ID)
		if lerr != nil || len(got) != 2 {
			t.Fatalf("watches = %d, %v", len(got), lerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func seedInboxThread(ctx context.Context, t *testing.T, s *Store, now int64) (reporter, watcher AuthUser, repo int64, threadID string) {
	t.Helper()
	reporter = seedF8User(t, s, "markreporter")
	watcher = seedF8User(t, s, "markwatcher")
	repo = seedF8Repo(ctx, t, s, reporter.ID, "marks")
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, werr := tx.SetWatch(watcher.ID, repo, "all", nil, now); werr != nil {
			return werr
		}
		_, ierr := tx.InsertIntent(time.UnixMilli(now), IntentRow{
			RepoID: repo, Title: "mark me", Body: "b", CreatedBy: reporter.ID,
		})
		return ierr
	}); err != nil {
		t.Fatalf("seed thread: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		threads, _, lerr := tx.ListInboxThreads(watcher.ID, "", "", 0, "", 10)
		if lerr != nil || len(threads) != 1 {
			t.Fatalf("seed threads = %d, %v", len(threads), lerr)
		}
		threadID = threads[0].ID
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	return reporter, watcher, repo, threadID
}

func TestMarkTransitions(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	_, watcher, _, threadID := seedInboxThread(ctx, t, s, 40000)

	mark := func(op string, until int64, at int64) ([]string, map[string]string, error) {
		var updated []string
		var failed map[string]string
		err := s.Write(ctx, func(tx *WriteTx) error {
			var merr error
			updated, failed, merr = tx.MarkInboxThreads(watcher.ID, []string{threadID}, op, until, at)
			return merr
		})
		return updated, failed, err
	}
	get := func() InboxThreadRow {
		t.Helper()
		var th InboxThreadRow
		if err := s.Read(ctx, func(tx *ReadTx) error {
			var gerr error
			th, gerr = tx.GetInboxThread(watcher.ID, threadID)
			return gerr
		}); err != nil {
			t.Fatalf("get: %v", err)
		}
		return th
	}

	if up, fl, err := mark("read", 0, 40001); err != nil || len(up) != 1 || len(fl) != 0 {
		t.Fatalf("read = %v %v %v", up, fl, err)
	}
	if th := get(); th.State != "read" {
		t.Fatalf("state = %q", th.State)
	}
	if up, fl, err := mark("mark_done", 0, 40002); err != nil || len(up) != 1 || len(fl) != 0 {
		t.Fatalf("done = %v %v %v", up, fl, err)
	}
	if th := get(); th.State != "done" {
		t.Fatalf("state = %q", th.State)
	}
	if up, fl, err := mark("unread", 0, 40003); err != nil || len(up) != 1 {
		t.Fatalf("unread = %v %v %v", up, fl, err)
	}
	if up, fl, err := mark("mute", 0, 40004); err != nil || len(up) != 1 || len(fl) != 0 {
		t.Fatalf("mute = %v %v %v", up, fl, err)
	}
	if th := get(); !th.Muted || th.State != "unread" {
		t.Fatalf("muted = %+v", th)
	}
	if up, fl, err := mark("unmute", 0, 40005); err != nil || len(up) != 1 {
		t.Fatalf("unmute = %v %v %v", up, fl, err)
	}
	if th := get(); th.Muted {
		t.Fatalf("still muted = %+v", th)
	}
	if _, _, err := mark("bogus", 0, 40006); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bogus op = %v, want ErrInvalid", err)
	}

	// Wrong user and missing ids land in failed, not err.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		updated, failed, merr := tx.MarkInboxThreads(999999, []string{threadID, "nope"}, "read", 0, 40007)
		if merr != nil {
			return merr
		}
		if len(updated) != 0 || len(failed) != 2 {
			t.Fatalf("foreign mark = %v %v", updated, failed)
		}
		return nil
	}); err != nil {
		t.Fatalf("foreign: %v", err)
	}
}

func TestSnoozeWindow(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	_, watcher, _, threadID := seedInboxThread(ctx, t, s, 50000)
	now := int64(50000)

	snooze := func(until int64) error {
		return s.Write(ctx, func(tx *WriteTx) error {
			_, _, merr := tx.MarkInboxThreads(watcher.ID, []string{threadID}, "snooze", until, now)
			return merr
		})
	}
	// Boundaries: exactly 1m and exactly 90d are valid; outside is not.
	if err := snooze(now + 60*1000); err != nil {
		t.Fatalf("1m snooze: %v", err)
	}
	if err := snooze(now + 90*24*60*60*1000); err != nil {
		t.Fatalf("90d snooze: %v", err)
	}
	for _, until := range []int64{now - 1, now, now + 59*1000, now + 90*24*60*60*1000 + 1} {
		if err := snooze(until); !errors.Is(err, ErrInvalid) {
			t.Fatalf("until=%d accepted: %v", until, err)
		}
	}

	if err := s.Write(ctx, func(tx *WriteTx) error {
		until := now + 3600*1000
		_, _, merr := tx.MarkInboxThreads(watcher.ID, []string{threadID}, "snooze", until, now)
		return merr
	}); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		th, gerr := tx.GetInboxThread(watcher.ID, threadID)
		if gerr != nil {
			return gerr
		}
		if th.State != "snoozed" || th.SnoozeUntil != now+3600*1000 {
			t.Fatalf("snoozed = %+v", th)
		}
		if n, cerr := tx.CountSnoozedAwake(watcher.ID, now); cerr != nil || n != 0 {
			t.Fatalf("awake now = %d, %v", n, cerr)
		}
		if n, cerr := tx.CountSnoozedAwake(watcher.ID, now+3600*1000); cerr != nil || n != 1 {
			t.Fatalf("awake at deadline = %d, %v", n, cerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestSnoozedAndMutedFanout(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := int64(60000)
	_, watcher, repo, threadID := seedInboxThread(ctx, t, s, now)
	_ = repo

	// Snooze, then replay a new event on the same subject: the thread stays
	// snoozed with bumped_at frozen but counts the item.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		_, _, merr := tx.MarkInboxThreads(watcher.ID, []string{threadID}, "snooze", now+3600*1000, now)
		return merr
	}); err != nil {
		t.Fatalf("snooze: %v", err)
	}
	var before InboxThreadRow
	if err := s.Read(ctx, func(tx *ReadTx) error {
		var gerr error
		before, gerr = tx.GetInboxThread(watcher.ID, threadID)
		return gerr
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	// A distinct ref event (different new OID) reaches the snoozed thread.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		_, eerr := tx.Outbox().Emit(Event{OccurredAt: time.UnixMilli(now + 1000), Kind: "ref.updated",
			Body: jsontextValue(`{"repo":` + itoa(repo) + `,"name":"refs/heads/main","old":"000","new":"def","source":"t"}`)})
		return eerr
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		after, gerr := tx.GetInboxThread(watcher.ID, threadID)
		if gerr != nil {
			return gerr
		}
		// Snoozed branch refs collide with the seeded task thread? No: the
		// ref event makes a NEW (branch_view) thread. Find it instead.
		_ = after
		threads, _, lerr := tx.ListInboxThreads(watcher.ID, "", "", 0, "", 10)
		if lerr != nil {
			return lerr
		}
		if len(threads) != 2 {
			t.Fatalf("threads = %d, want task + branch_view", len(threads))
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	// A NEW event on the same subject reaches the snoozed thread: state and
	// bumped_at frozen, unread/total incremented, item recorded unbumped.
	var subjectID string
	if err := s.Read(ctx, func(tx *ReadTx) error {
		th, gerr := tx.GetInboxThread(watcher.ID, threadID)
		if gerr != nil {
			return gerr
		}
		subjectID = th.SubjectID
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	emitTask := func(kind string, at int64) {
		t.Helper()
		body := `{"repo_id":` + itoa(repo) + `,"intent_id":"` + subjectID + `","task_number":1}`
		if err := s.Write(ctx, func(tx *WriteTx) error {
			_, eerr := tx.Outbox().Emit(Event{OccurredAt: time.UnixMilli(at), Kind: kind, Body: jsontextValue(body)})
			return eerr
		}); err != nil {
			t.Fatalf("emit %s: %v", kind, err)
		}
	}
	emitTask("task.closed", now+1500)
	if err := s.Read(ctx, func(tx *ReadTx) error {
		after, gerr := tx.GetInboxThread(watcher.ID, threadID)
		if gerr != nil {
			return gerr
		}
		if after.State != "snoozed" || after.SnoozeUntil != now+3600*1000 {
			t.Fatalf("snooze broken: %+v", after)
		}
		if after.BumpedAt != before.BumpedAt {
			t.Fatalf("snoozed bump moved: %d -> %d", before.BumpedAt, after.BumpedAt)
		}
		if after.UnreadItems != before.UnreadItems+1 || after.TotalItems != before.TotalItems+1 {
			t.Fatalf("snoozed counters: %+v vs %+v", after, before)
		}
		items, lerr := tx.ListInboxItems(threadID, 10)
		if lerr != nil || len(items) != 2 || items[1].Bumped {
			t.Fatalf("snoozed items = %+v, %v", items, lerr)
		}
		before = after
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	// Mute the task thread, then drive another new emission on the same
	// subject: one silent item (total+1, bumped=false), every other counter
	// frozen. Distinct emissions append; only a same-row double fan-out
	// dedupes.
	if err := s.Write(ctx, func(tx *WriteTx) error {
		_, _, merr := tx.MarkInboxThreads(watcher.ID, []string{threadID}, "mute", 0, now+2000)
		return merr
	}); err != nil {
		t.Fatalf("mute: %v", err)
	}
	var claimedRow OutboxRow
	claimedBody := `{"repo_id":` + itoa(repo) + `,"intent_id":"` + subjectID + `","task_number":1}`
	if err := s.Write(ctx, func(tx *WriteTx) error {
		var eerr error
		claimedRow, eerr = tx.Outbox().Emit(Event{OccurredAt: time.UnixMilli(now + 2500), Kind: "task.claimed", Body: jsontextValue(claimedBody)})
		return eerr
	}); err != nil {
		t.Fatalf("emit claimed: %v", err)
	}
	// Same-row double fan-out changes nothing: the (event_id,user_id) and
	// (outbox_seq,endpoint_id) ignores absorb the replay.
	beforeMsgs := int64(0)
	if err := s.Read(ctx, func(tx *ReadTx) error {
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		return inner.QueryRowContext(tx.ctx, `SELECT count(*) FROM webhook_messages`).Scan(&beforeMsgs)
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	if err := s.Write(ctx, func(tx *WriteTx) error {
		return fanoutEvent(&tx.txCore, claimedRow)
	}); err != nil {
		t.Fatalf("refanout: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		after, gerr := tx.GetInboxThread(watcher.ID, threadID)
		if gerr != nil {
			return gerr
		}
		if !after.Muted || after.State != "snoozed" {
			t.Fatalf("muted thread changed state: %+v", after)
		}
		if after.UnreadItems != before.UnreadItems || after.TotalItems != before.TotalItems+1 {
			t.Fatalf("muted counters: %+v vs %+v", after, before)
		}
		items, lerr := tx.ListInboxItems(threadID, 10)
		if lerr != nil || len(items) != 3 {
			t.Fatalf("muted items = %d, %v", len(items), lerr)
		}
		if !items[0].Bumped || items[1].Bumped || items[2].Bumped {
			t.Fatalf("bump flags = %v %v %v, want true false false",
				items[0].Bumped, items[1].Bumped, items[2].Bumped)
		}
		if items[2].EventID != string(claimedRow.ID) || items[2].EventType != "task.claimed" {
			t.Fatalf("item identity = %+v, want event of row %s", items[2], claimedRow.ID)
		}
		var n int64
		inner, lerr := tx.live()
		if lerr != nil {
			return lerr
		}
		if serr := inner.QueryRowContext(tx.ctx, `SELECT count(*) FROM webhook_messages`).Scan(&n); serr != nil {
			return serr
		}
		if n != beforeMsgs {
			t.Fatalf("messages %d -> %d across same-row refanout", beforeMsgs, n)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestPusherExcludedFromFanout(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := int64(75000)
	pusher := seedF8User(t, s, "pushactor")
	other := seedF8User(t, s, "pushbystander")
	repo := seedF8Repo(ctx, t, s, other.ID, "pushrepo")
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, werr := tx.SetWatch(pusher.ID, repo, "all", nil, now); werr != nil {
			return werr
		}
		if _, werr := tx.SetWatch(other.ID, repo, "all", nil, now); werr != nil {
			return werr
		}
		body := `{"repo":` + itoa(repo) + `,"name":"refs/heads/main","old":"000","new":"abc","source":"t","pusher":"pushactor"}`
		_, eerr := tx.Outbox().Emit(Event{OccurredAt: time.UnixMilli(now), Kind: "ref.updated", Body: jsontextValue(body)})
		return eerr
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		mine, _, lerr := tx.ListInboxThreads(pusher.ID, "", "", 0, "", 10)
		if lerr != nil || len(mine) != 0 {
			t.Fatalf("pusher threads = %d, %v; want actor excluded", len(mine), lerr)
		}
		theirs, _, lerr := tx.ListInboxThreads(other.ID, "", "", 0, "", 10)
		if lerr != nil || len(theirs) != 1 {
			t.Fatalf("bystander threads = %d, %v", len(theirs), lerr)
		}
		items, lerr := tx.ListInboxItems(theirs[0].ID, 10)
		if lerr != nil || len(items) != 1 {
			t.Fatalf("bystander items = %d, %v", len(items), lerr)
		}
		if items[0].ActorID != pusher.ID || items[0].ActorName != "pushactor" {
			t.Fatalf("item actor = %d %q", items[0].ActorID, items[0].ActorName)
		}
		if items[0].EventType != "ref.updated" || items[0].EventID == "" {
			t.Fatalf("item identity = %+v", items[0])
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestIgnoreSuppressesAll(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := int64(70000)
	ignorer := seedF8User(t, s, "ignorer")
	other := seedF8User(t, s, "ignother")
	repo := seedF8Repo(ctx, t, s, ignorer.ID, "ignores")
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, werr := tx.SetWatch(ignorer.ID, repo, "ignore", nil, now); werr != nil {
			return werr
		}
		_, ierr := tx.InsertIntent(time.UnixMilli(now), IntentRow{
			RepoID: repo, Title: "ignored task", Body: "b", CreatedBy: other.ID,
			AssigneeUserID: sql.NullInt64{Int64: ignorer.ID, Valid: true},
		})
		return ierr
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		threads, _, lerr := tx.ListInboxThreads(ignorer.ID, "", "", 0, "", 10)
		if lerr != nil || len(threads) != 0 {
			t.Fatalf("ignored user threads = %d, %v", len(threads), lerr)
		}
		if n, cerr := tx.CountUnreadInbox(ignorer.ID); cerr != nil || n != 0 {
			t.Fatalf("ignored unread = %d, %v", n, cerr)
		}
		// The non-ignored participant still gets theirs.
		threads, _, lerr = tx.ListInboxThreads(other.ID, "", "", 0, "", 10)
		if lerr != nil || len(threads) != 1 {
			t.Fatalf("author threads = %d, %v", len(threads), lerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestInboxGetters(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	ada := seedF8User(t, s, "setada")
	bob := seedF8User(t, s, "setbob")

	if err := s.Read(ctx, func(tx *ReadTx) error {
		got, gerr := tx.GetInboxSettings(ada.ID)
		if gerr != nil {
			return gerr
		}
		if got != (InboxSettingsRow{}) {
			t.Fatalf("absent settings = %+v, want zero struct", got)
		}
		if _, gerr = tx.GetInboxThread(bob.ID, "msg_missing"); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("missing thread = %v", gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}

	if err := s.Write(ctx, func(tx *WriteTx) error {
		got, serr := tx.SetInboxSettings(ada.ID, "all", 0, 80000)
		if serr != nil {
			return serr
		}
		if got.DefaultLevel != "all" || got.ClaimWarnWindowSecs != 3600 || got.UpdatedAt != 80000 {
			t.Fatalf("settings = %+v", got)
		}
		if _, serr = tx.SetInboxSettings(ada.ID, "everything", 3600, 80000); !errors.Is(serr, ErrInvalid) {
			t.Fatalf("bad level = %v", serr)
		}
		for _, w := range []int64{299, 86401} {
			if _, serr = tx.SetInboxSettings(ada.ID, "all", w, 80000); !errors.Is(serr, ErrInvalid) {
				t.Fatalf("window %d = %v, want ErrInvalid", w, serr)
			}
		}
		for _, w := range []int64{300, 86400} {
			if _, serr = tx.SetInboxSettings(ada.ID, "all", w, 80000); serr != nil {
				t.Fatalf("window %d: %v", w, serr)
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestListInboxThreadsFilters(t *testing.T) {
	ctx := t.Context()
	s := openF8Store(t)
	now := int64(90000)
	ada := seedF8User(t, s, "listada")
	repo := seedF8Repo(ctx, t, s, ada.ID, "lists")
	repo2 := seedF8Repo(ctx, t, s, ada.ID, "lists2")
	if err := s.Write(ctx, func(tx *WriteTx) error {
		if _, werr := tx.SetWatch(ada.ID, repo, "all", nil, now); werr != nil {
			return werr
		}
		if _, werr := tx.SetWatch(ada.ID, repo2, "all", nil, now); werr != nil {
			return werr
		}
		if _, ierr := tx.InsertIntent(time.UnixMilli(now), IntentRow{
			RepoID: repo, Title: "t1", Body: "b", CreatedBy: ada.ID,
		}); ierr != nil {
			return ierr
		}
		if _, ierr := tx.InsertIntent(time.UnixMilli(now+1000), IntentRow{
			RepoID: repo2, Title: "t2", Body: "b", CreatedBy: ada.ID,
		}); ierr != nil {
			return ierr
		}
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.Read(ctx, func(tx *ReadTx) error {
		all, next, lerr := tx.ListInboxThreads(ada.ID, "", "", 0, "", 1)
		if lerr != nil || len(all) != 1 || next == "" {
			t.Fatalf("page1 = %d %q %v", len(all), next, lerr)
		}
		rest, next2, lerr := tx.ListInboxThreads(ada.ID, "", "", 0, "", 1)
		_ = rest
		_ = next2
		_ = lerr
		second, next3, lerr := tx.ListInboxThreads(ada.ID, "", "", 0, next, 1)
		if lerr != nil || len(second) != 1 || next3 != "" {
			t.Fatalf("page2 = %d %q %v", len(second), next3, lerr)
		}
		byRepo, _, lerr := tx.ListInboxThreads(ada.ID, "", "", repo, "", 10)
		if lerr != nil || len(byRepo) != 1 || byRepo[0].RepoID != repo {
			t.Fatalf("by repo = %+v, %v", byRepo, lerr)
		}
		byKind, _, lerr := tx.ListInboxThreads(ada.ID, "", "task", 0, "", 10)
		if lerr != nil || len(byKind) != 2 {
			t.Fatalf("by kind = %d, %v", len(byKind), lerr)
		}
		byState, _, lerr := tx.ListInboxThreads(ada.ID, "read", "", 0, "", 10)
		if lerr != nil || len(byState) != 0 {
			t.Fatalf("by read = %d, %v", len(byState), lerr)
		}
		if _, _, lerr = tx.ListInboxThreads(ada.ID, "bogus", "", 0, "", 10); lerr == nil {
			t.Fatalf("bad state accepted")
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	// Wrong-user GetInboxThread reports ErrNotFound, never the row.
	other := seedF8User(t, s, "liststranger")
	if err := s.Read(ctx, func(tx *ReadTx) error {
		threads, _, lerr := tx.ListInboxThreads(ada.ID, "", "", 0, "", 1)
		if lerr != nil || len(threads) != 1 {
			t.Fatalf("threads = %d", len(threads))
		}
		if _, gerr := tx.GetInboxThread(other.ID, threads[0].ID); !errors.Is(gerr, ErrNotFound) {
			t.Fatalf("foreign get = %v, want ErrNotFound", gerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}
