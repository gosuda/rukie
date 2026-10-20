package events

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

func tailEvent(seq uint64, name, old, current string) ruk.RefEvent {
	return ruk.RefEvent{
		At:     1729468800000,
		Name:   name,
		Old:    ruk.DirectTarget(old),
		New:    ruk.DirectTarget(current),
		Source: "ReceivePack",
		Seq:    seq,
	}
}

func expiredReply(head, oldest uint64) ruk.EventsTailReply {
	return ruk.EventsTailReply{Head: head, Oldest: oldest, Expired: true}
}

func liveReply(head, oldest uint64, events []ruk.RefEvent) ruk.EventsTailReply {
	return ruk.EventsTailReply{Events: events, Head: head, Oldest: oldest}
}

func listReply(asOf uint64, tips map[string]string) ruk.RefsListReply {
	items := make([]ruk.RefEntry, 0, len(tips))
	for name, target := range tips {
		items = append(items, ruk.RefEntry{Name: name, Target: ruk.DirectTarget(target)})
	}
	return ruk.RefsListReply{Refs: ruk.RefPage{Items: items}, AsOf: asOf}
}

func TestFollowerIngestDedupesReplay(t *testing.T) {
	st := openFeedStore(t)
	f := OpenFollowerWith(st, 11, nil, nil, feedTime)
	ctx := feedCtx(t)
	batch := []ruk.RefEvent{
		tailEvent(5, "refs/heads/main", strings.Repeat("a", 40), strings.Repeat("b", 40)),
		tailEvent(6, "refs/heads/main", strings.Repeat("b", 40), strings.Repeat("c", 40)),
	}
	if err := f.Ingest(ctx, batch); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := f.Ingest(ctx, batch); err != nil {
		t.Fatalf("replay ingest: %v", err)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 2 {
		t.Fatalf("ref.updated rows = %d, want 2", n)
	}
	cursor, err := f.Cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != 6 {
		t.Fatalf("cursor = %d, want 6", cursor)
	}
}

func TestFollowerIngestSkipsBelowCursor(t *testing.T) {
	st := openFeedStore(t)
	f := OpenFollowerWith(st, 11, nil, nil, feedTime)
	ctx := feedCtx(t)
	batch := []ruk.RefEvent{tailEvent(6, "refs/heads/main", strings.Repeat("a", 40), strings.Repeat("b", 40))}
	if err := f.Ingest(ctx, batch); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	old := []ruk.RefEvent{tailEvent(5, "refs/heads/main", strings.Repeat("0", 40), strings.Repeat("a", 40))}
	if err := f.Ingest(ctx, old); err != nil {
		t.Fatalf("old ingest: %v", err)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 1 {
		t.Fatalf("ref.updated rows = %d, want 1", n)
	}
}

func TestFollowerIngestOrdersOutOfOrder(t *testing.T) {
	st := openFeedStore(t)
	f := OpenFollowerWith(st, 11, nil, nil, feedTime)
	ctx := feedCtx(t)
	batch := []ruk.RefEvent{
		tailEvent(7, "refs/heads/main", strings.Repeat("b", 40), strings.Repeat("c", 40)),
		tailEvent(5, "refs/heads/main", strings.Repeat("a", 40), strings.Repeat("b", 40)),
	}
	if err := f.Ingest(ctx, batch); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	cursor, err := f.Cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != 7 {
		t.Fatalf("cursor = %d, want 7", cursor)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 2 {
		t.Fatalf("ref.updated rows = %d, want 2", n)
	}
}

func TestFollowerIngestRejectsMalformed(t *testing.T) {
	st := openFeedStore(t)
	f := OpenFollowerWith(st, 11, nil, nil, feedTime)
	ctx := feedCtx(t)
	batch := []ruk.RefEvent{tailEvent(5, "", strings.Repeat("a", 40), strings.Repeat("b", 40))}
	if err := f.Ingest(ctx, batch); err == nil {
		t.Fatal("nameless event ingested")
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 0 {
		t.Fatalf("malformed event emitted %d rows", n)
	}
	cursor, err := f.Cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != 0 {
		t.Fatalf("cursor = %d, want 0", cursor)
	}
}

func TestFollowerIngestDeletesNullNew(t *testing.T) {
	st := openFeedStore(t)
	f := OpenFollowerWith(st, 21, nil, nil, feedTime)
	ctx := feedCtx(t)
	create := []ruk.RefEvent{tailEvent(5, "refs/heads/gone", strings.Repeat("0", 40), strings.Repeat("b", 40))}
	if err := f.Ingest(ctx, create); err != nil {
		t.Fatalf("create: %v", err)
	}
	remove := ruk.RefEvent{At: 1729468800000, Name: "refs/heads/gone", Old: ruk.DirectTarget(strings.Repeat("b", 40)), Source: "ReceivePack", Seq: 6}
	if err := f.Ingest(ctx, []ruk.RefEvent{remove}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var tips map[string]string
	err := st.Read(ctx, func(tx *store.ReadTx) error {
		got, rerr := tx.RefTips(21)
		if rerr != nil {
			return rerr
		}
		tips = got
		return nil
	})
	if err != nil {
		t.Fatalf("tips: %v", err)
	}
	if _, ok := tips["refs/heads/gone"]; ok {
		t.Fatalf("deleted tip kept: %+v", tips)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 2 {
		t.Fatalf("ref.updated rows = %d, want 2", n)
	}
}

func TestListAllPagesThroughNextAfter(t *testing.T) {
	next := "refs/heads/b"
	calls := 0
	list := func(_ context.Context, prefix, after string) (ruk.RefsListReply, error) {
		calls++
		if prefix != "" {
			t.Fatalf("prefix = %q, want empty", prefix)
		}
		if after == "" {
			return ruk.RefsListReply{
				Refs: ruk.RefPage{Items: []ruk.RefEntry{{Name: "refs/heads/a", Target: ruk.DirectTarget("a")}}, NextAfter: &next},
				AsOf: 8,
			}, nil
		}
		if after != "refs/heads/b" {
			t.Fatalf("after = %q, want the page cursor", after)
		}
		return ruk.RefsListReply{
			Refs: ruk.RefPage{Items: []ruk.RefEntry{{Name: "refs/heads/b", Target: ruk.DirectTarget("b")}}},
			AsOf: 9,
		}, nil
	}
	items, asOf, err := ListAll(feedCtx(t), list, "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(items) != 2 || asOf != 8 {
		t.Fatalf("items = %+v, as_of = %d, want the first page head", items, asOf)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestListAllRefusesEndlessPages(t *testing.T) {
	after := "refs/heads/a"
	list := func(_ context.Context, _, _ string) (ruk.RefsListReply, error) {
		return ruk.RefsListReply{
			Refs: ruk.RefPage{NextAfter: &after},
			AsOf: 1,
		}, nil
	}
	if _, _, err := ListAll(feedCtx(t), list, ""); err == nil {
		t.Fatal("endless paging accepted")
	}
}

func TestFollowerPollOnceExpiredResnapshots(t *testing.T) {
	st := openFeedStore(t)
	ctx := feedCtx(t)
	seed := OpenFollowerWith(st, 31, nil, nil, feedTime)
	if err := seed.Ingest(ctx, []ruk.RefEvent{
		tailEvent(4, "refs/heads/main", strings.Repeat("a", 40), strings.Repeat("b", 40)),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	calls := 0
	tail := func(_ context.Context, after uint64) (ruk.EventsTailReply, error) {
		calls++
		if after == 4 {
			return expiredReply(12, 9), nil
		}
		if after == 12 {
			return liveReply(12, 9, nil), nil
		}
		return ruk.EventsTailReply{}, errors.New("tail resumed from the wrong cursor")
	}
	list := func(_ context.Context, _, _ string) (ruk.RefsListReply, error) {
		return listReply(12, map[string]string{
			"refs/heads/main": strings.Repeat("c", 40),
			"refs/heads/keep": strings.Repeat("k", 40),
		}), nil
	}
	f := OpenFollowerWith(st, 31, tail, list, feedTime)
	if err := f.PollOnce(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if calls != 1 {
		t.Fatalf("tail calls = %d, want 1", calls)
	}
	cursor, err := f.Cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != 12 {
		t.Fatalf("cursor = %d, want 12", cursor)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 3 {
		t.Fatalf("ref.updated rows = %d, want 3 (seed plus two resnapshot synthetics)", n)
	}
	before := countOutboxKind(t, st, kindRefUpdated)
	if err := f.PollOnce(ctx); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != before {
		t.Fatalf("stable resnapshot emitted %d rows, want %d", n, before)
	}
}

func TestFollowerIngestSkipsAlreadyAppliedTip(t *testing.T) {
	st := openFeedStore(t)
	f := OpenFollowerWith(st, 11, nil, nil, feedTime)
	ctx := feedCtx(t)
	applied := strings.Repeat("b", 40)
	err := st.Write(ctx, func(tx *store.WriteTx) error {
		return tx.PutRefTip(11, "refs/heads/main", applied, feedTime())
	})
	if err != nil {
		t.Fatalf("seed tip: %v", err)
	}
	// The tip is already at b but the cursor is behind: a resnapshot or
	// a multi-page listing covered this ref past the stored cursor.
	event := tailEvent(5, "refs/heads/main", strings.Repeat("a", 40), applied)
	if err := f.Ingest(ctx, []ruk.RefEvent{event}); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 0 {
		t.Fatalf("ref.updated rows = %d, want 0: the transition is already applied", n)
	}
	cursor, err := f.Cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != 5 {
		t.Fatalf("cursor = %d, want 5: the covered seq still advances", cursor)
	}
	var tips map[string]string
	rerr := st.Read(ctx, func(tx *store.ReadTx) error {
		got, gerr := tx.RefTips(11)
		if gerr != nil {
			return gerr
		}
		tips = got
		return nil
	})
	if rerr != nil {
		t.Fatalf("tips: %v", rerr)
	}
	if tips["refs/heads/main"] != applied {
		t.Fatalf("tip = %q, want the stored %q", tips["refs/heads/main"], applied)
	}
}

func TestListAllCarriesMinimumAsOf(t *testing.T) {
	ctx := feedCtx(t)
	list := func(_ context.Context, prefix, after string) (ruk.RefsListReply, error) {
		if after == "" {
			reply := listReply(10, map[string]string{"refs/heads/main": strings.Repeat("a", 40)})
			next := "cursor-1"
			reply.Refs.NextAfter = &next
			return reply, nil
		}
		if after != "cursor-1" {
			t.Fatalf("second page after = %q, want cursor-1", after)
		}
		// The second page observed a newer state than the first page's
		// snapshot. The resnapshot must advance only to the minimum,
		// and Ingest drops the overlap by tip equality.
		return listReply(12, map[string]string{"refs/heads/main": strings.Repeat("b", 40)}), nil
	}
	items, asOf, err := ListAll(ctx, list, "")
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if asOf != 10 {
		t.Fatalf("asOf = %d, want the minimum 10, not the newer page", asOf)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want both pages", len(items))
	}
}
