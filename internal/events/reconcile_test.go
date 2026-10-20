package events

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

func seedTips(t *testing.T, st *store.Store, repo int64, tips map[string]string) {
	t.Helper()
	err := st.Write(feedCtx(t), func(tx *store.WriteTx) error {
		return tx.ReplaceRefTips(repo, tips, feedTime())
	})
	if err != nil {
		t.Fatalf("seed tips: %v", err)
	}
}

func setCursor(t *testing.T, st *store.Store, repo int64, to int64) {
	t.Helper()
	err := st.Write(feedCtx(t), func(tx *store.WriteTx) error {
		return tx.Outbox().Advance(eventConsumer(repo), 0, to)
	})
	if err != nil {
		t.Fatalf("seed cursor: %v", err)
	}
}

func outboxSources(t *testing.T, st *store.Store) []string {
	t.Helper()
	var rows []store.OutboxRow
	err := st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.Outbox().After("probe", 1000)
		if rerr != nil {
			return rerr
		}
		rows = got
		return nil
	})
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Kind)
	}
	return out
}

func TestReconcilerSynthesizesMissedUpdate(t *testing.T) {
	st := openFeedStore(t)
	seedTips(t, st, 41, map[string]string{"refs/heads/main": strings.Repeat("a", 40)})
	setCursor(t, st, 41, 9)
	list := func(_ context.Context, _, _ string) (ruk.RefsListReply, error) {
		return listReply(9, map[string]string{"refs/heads/main": strings.Repeat("b", 40)}), nil
	}
	r := OpenReconciler(st, 41, list, feedTime)
	n, err := r.ReconcileRepo(feedCtx(t))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("synthesized = %d, want 1", n)
	}
	if kinds := outboxSources(t, st); len(kinds) != 1 || kinds[0] != kindRefUpdated {
		t.Fatalf("outbox = %v", kinds)
	}
	again, err := r.ReconcileRepo(feedCtx(t))
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if again != 0 {
		t.Fatalf("second synthesized = %d, want 0", again)
	}
}

func TestReconcilerSkipsEventsInFlight(t *testing.T) {
	st := openFeedStore(t)
	seedTips(t, st, 41, map[string]string{"refs/heads/main": strings.Repeat("a", 40)})
	setCursor(t, st, 41, 5)
	list := func(_ context.Context, _, _ string) (ruk.RefsListReply, error) {
		return listReply(9, map[string]string{"refs/heads/main": strings.Repeat("b", 40)}), nil
	}
	r := OpenReconciler(st, 41, list, feedTime)
	n, err := r.ReconcileRepo(feedCtx(t))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Fatalf("synthesized = %d while events are in flight, want 0", n)
	}
	if kinds := outboxSources(t, st); len(kinds) != 0 {
		t.Fatalf("outbox = %v, want empty", kinds)
	}
}

func TestReconcilerHandlesDeletion(t *testing.T) {
	st := openFeedStore(t)
	seedTips(t, st, 41, map[string]string{
		"refs/heads/main": strings.Repeat("a", 40),
		"refs/heads/gone": strings.Repeat("g", 40),
	})
	setCursor(t, st, 41, 10)
	list := func(_ context.Context, _, _ string) (ruk.RefsListReply, error) {
		return listReply(10, map[string]string{"refs/heads/main": strings.Repeat("a", 40)}), nil
	}
	r := OpenReconciler(st, 41, list, feedTime)
	n, err := r.ReconcileRepo(feedCtx(t))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("synthesized = %d, want 1", n)
	}
	var tips map[string]string
	err = st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.RefTips(41)
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
}

func TestReconcilerRejectsMalformedEntry(t *testing.T) {
	st := openFeedStore(t)
	seedTips(t, st, 41, map[string]string{})
	setCursor(t, st, 41, 10)
	list := func(_ context.Context, _, _ string) (ruk.RefsListReply, error) {
		return ruk.RefsListReply{
			Refs: ruk.RefPage{Items: []ruk.RefEntry{{Name: "", Target: ruk.DirectTarget("a")}}},
			AsOf: 10,
		}, nil
	}
	r := OpenReconciler(st, 41, list, feedTime)
	if _, err := r.ReconcileRepo(feedCtx(t)); err == nil {
		t.Fatal("nameless entry reconciled")
	}
	if kinds := outboxSources(t, st); len(kinds) != 0 {
		t.Fatalf("outbox = %v, want empty", kinds)
	}
}

func TestReconcilerRunProcessesOnce(t *testing.T) {
	st := openFeedStore(t)
	seedTips(t, st, 41, map[string]string{"refs/heads/main": strings.Repeat("a", 40)})
	setCursor(t, st, 41, 10)
	list := func(_ context.Context, _, _ string) (ruk.RefsListReply, error) {
		return listReply(10, map[string]string{"refs/heads/main": strings.Repeat("b", 40)}), nil
	}
	r := OpenReconciler(st, 41, list, feedTime)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if kinds := outboxSources(t, st); len(kinds) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reconcile loop never synthesized the row")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("run err = %v, want canceled", err)
	}
}

func refTipOf(t *testing.T, st *store.Store, repo int64, name string) string {
	t.Helper()
	var tips map[string]string
	err := st.Read(feedCtx(t), func(tx *store.ReadTx) error {
		got, rerr := tx.RefTips(repo)
		if rerr != nil {
			return rerr
		}
		tips = got
		return nil
	})
	if err != nil {
		t.Fatalf("tips: %v", err)
	}
	return tips[name]
}

func TestReconcilerSkipsWhenFollowerIsAhead(t *testing.T) {
	st := openFeedStore(t)
	newer := strings.Repeat("c", 40)
	seedTips(t, st, 41, map[string]string{"refs/heads/main": newer})
	setCursor(t, st, 41, 11)
	list := func(_ context.Context, _, _ string) (ruk.RefsListReply, error) {
		return listReply(10, map[string]string{"refs/heads/main": strings.Repeat("b", 40)}), nil
	}
	r := OpenReconciler(st, 41, list, feedTime)
	n, err := r.ReconcileRepo(feedCtx(t))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 0 {
		t.Fatalf("synthesized = %d against a stale listing, want 0", n)
	}
	if kinds := outboxSources(t, st); len(kinds) != 0 {
		t.Fatalf("outbox = %v, want empty: no backwards ref.updated", kinds)
	}
	if got := refTipOf(t, st, 41, "refs/heads/main"); got != newer {
		t.Fatalf("tip = %s, want the follower's newer %s", got, newer)
	}
}
