package events

import (
	"context"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

func TestFollowerResnapshotKeepsNewerIngest(t *testing.T) {
	st := openFeedStore(t)
	ctx := feedCtx(t)
	seed := OpenFollowerWith(st, 31, nil, nil, feedTime)
	old := strings.Repeat("b", 40)
	latest := strings.Repeat("d", 40)
	if err := seed.Ingest(ctx, []ruk.RefEvent{tailEvent(4, "refs/heads/main", "", old)}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tail := func(_ context.Context, _ uint64) (ruk.EventsTailReply, error) {
		return expiredReply(12, 9), nil
	}
	list := func(ctx context.Context, _, _ string) (ruk.RefsListReply, error) {
		if err := seed.Ingest(ctx, []ruk.RefEvent{
			tailEvent(13, "refs/heads/main", old, latest),
			tailEvent(14, "refs/heads/new", "", latest),
		}); err != nil {
			return ruk.RefsListReply{}, err
		}
		return listReply(12, map[string]string{
			"refs/heads/main":  strings.Repeat("c", 40),
			"refs/heads/stale": old,
		}), nil
	}
	f := OpenFollowerWith(st, 31, tail, list, feedTime)
	if err := f.PollOnce(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	}
	cursor, err := f.Cursor(ctx)
	if err != nil {
		t.Fatalf("cursor: %v", err)
	}
	if cursor != 14 {
		t.Fatalf("cursor = %d, want 14", cursor)
	}
	err = st.Read(ctx, func(tx *store.ReadTx) error {
		tips, rerr := tx.RefTips(31)
		if rerr != nil {
			return rerr
		}
		if len(tips) != 2 || tips["refs/heads/main"] != latest || tips["refs/heads/new"] != latest {
			t.Errorf("tips = %+v, want the newer main and new refs", tips)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("tips: %v", err)
	}
	if n := countOutboxKind(t, st, kindRefUpdated); n != 3 {
		t.Fatalf("ref.updated rows = %d, want only the 3 ingested events", n)
	}
}
