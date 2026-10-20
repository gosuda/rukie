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
		At:     "2026-10-20T00:00:00Z",
		Name:   name,
		Old:    ruk.DirectTarget(old),
		New:    ruk.DirectTarget(current),
		Source: "receive_pack",
		Seq:    seq,
	}
}

func tailReply(status string, head, oldest, nextAfter uint64, events []ruk.RefEvent) ruk.EventsTailReply {
	return ruk.EventsTailReply{Events: events, Status: status, Head: head, Oldest: oldest, NextAfter: nextAfter}
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
	if err := f.Ingest(ctx, batch, 6); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if err := f.Ingest(ctx, batch, 6); err != nil {
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
	if err := f.Ingest(ctx, batch, 6); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	old := []ruk.RefEvent{tailEvent(5, "refs/heads/main", strings.Repeat("0", 40), strings.Repeat("a", 40))}
	if err := f.Ingest(ctx, old, 5); err != nil {
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
	if err := f.Ingest(ctx, batch, 7); err != nil {
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
	if err := f.Ingest(ctx, batch, 5); err == nil {
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
	if err := f.Ingest(ctx, create, 5); err != nil {
		t.Fatalf("create: %v", err)
	}
	remove := ruk.RefEvent{At: "t", Name: "refs/heads/gone", Old: ruk.DirectTarget(strings.Repeat("b", 40)), Source: "receive_pack", Seq: 6}
	if err := f.Ingest(ctx, []ruk.RefEvent{remove}, 6); err != nil {
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

func TestFollowerPollOnceExpiredResnapshots(t *testing.T) {
	st := openFeedStore(t)
	ctx := feedCtx(t)
	seed := OpenFollowerWith(st, 31, nil, nil, feedTime)
	if err := seed.Ingest(ctx, []ruk.RefEvent{
		tailEvent(4, "refs/heads/main", strings.Repeat("a", 40), strings.Repeat("b", 40)),
	}, 4); err != nil {
		t.Fatalf("seed: %v", err)
	}
	calls := 0
	tail := func(_ context.Context, after uint64) (ruk.EventsTailReply, error) {
		calls++
		if after == 4 {
			return tailReply(ruk.TailStatusExpired, 12, 9, 12, nil), nil
		}
		if after == 12 {
			return tailReply(ruk.TailStatusOK, 12, 9, 12, nil), nil
		}
		return ruk.EventsTailReply{}, errors.New("tail resumed from the wrong cursor")
	}
	list := func(_ context.Context, _ string) (ruk.RefsListReply, error) {
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
