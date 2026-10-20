package events

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	jsonv1 "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

const (
	FollowerBatchLimit = 100
	FollowerWaitMs     = 1000
	sourceEvents       = "events"
	sourceResnapshot   = "resnapshot"
)

type Tailer func(ctx context.Context, after uint64) (ruk.EventsTailReply, error)

type Lister func(ctx context.Context, prefix string) (ruk.RefsListReply, error)

type Follower struct {
	client *ruk.Client
	store  *store.Store
	clock  func() time.Time
	tail   Tailer
	list   Lister
	repo   ruk.Repo
	repoID int64
}

func eventConsumer(repoID int64) string {
	return "events:" + strconv.FormatInt(repoID, 10)
}

func FollowerConsumer(repoID int64) string {
	return eventConsumer(repoID)
}

func OpenFollower(st *store.Store, repoID int64, repo ruk.Repo, client *ruk.Client, clock func() time.Time) *Follower {
	if clock == nil {
		clock = time.Now
	}
	f := &Follower{store: st, repoID: repoID, repo: repo, client: client, clock: clock}
	f.tail = f.daemonTail
	f.list = f.daemonList
	return f
}

func OpenFollowerWith(st *store.Store, repoID int64, tail Tailer, list Lister, clock func() time.Time) *Follower {
	if clock == nil {
		clock = time.Now
	}
	return &Follower{store: st, repoID: repoID, clock: clock, tail: tail, list: list}
}

func (f *Follower) daemonTail(ctx context.Context, after uint64) (ruk.EventsTailReply, error) {
	return ruk.EventsTail(ctx, f.client, f.repo, ruk.EventsTailArgs{
		After:  &after,
		Limit:  FollowerBatchLimit,
		WaitMs: FollowerWaitMs,
	})
}

func (f *Follower) daemonList(ctx context.Context, prefix string) (ruk.RefsListReply, error) {
	return ruk.RefsList(ctx, f.client, f.repo, ruk.RefsListArgs{Prefix: prefix})
}

func (f *Follower) Cursor(ctx context.Context) (uint64, error) {
	var cursor int64
	err := f.store.Read(ctx, func(tx *store.ReadTx) error {
		got, rerr := tx.Outbox().Cursor(eventConsumer(f.repoID))
		if rerr != nil {
			return rerr
		}
		cursor = got
		return nil
	})
	if err != nil {
		return 0, err
	}
	if cursor < 0 {
		return 0, fmt.Errorf("Cursor: stored cursor %d is negative", cursor)
	}
	return uint64(cursor), nil
}

func (f *Follower) Ingest(ctx context.Context, events []ruk.RefEvent, nextAfter uint64) error {
	if len(events) == 0 && nextAfter == 0 {
		return nil
	}
	ordered := make([]ruk.RefEvent, len(events))
	copy(ordered, events)
	slices.SortFunc(ordered, func(a, b ruk.RefEvent) int { return cmp.Compare(a.Seq, b.Seq) })
	now := f.clock()
	if now.UnixMilli() <= 0 {
		return fmt.Errorf("Ingest: clock must be after the Unix epoch")
	}
	return f.store.Write(ctx, func(tx *store.WriteTx) error {
		cursor, cerr := tx.Outbox().Cursor(eventConsumer(f.repoID))
		if cerr != nil {
			return cerr
		}
		if cursor < 0 {
			return fmt.Errorf("Ingest: stored cursor %d is negative", cursor)
		}
		if nextAfter > math.MaxInt64 {
			return fmt.Errorf("Ingest: %w: next_after overflows int64", store.ErrInvalid)
		}
		front := cursor
		if int64(nextAfter) > front {
			front = int64(nextAfter)
		}
		for _, event := range ordered {
			if event.Seq > math.MaxInt64 {
				return fmt.Errorf("Ingest: %w: event seq %d overflows int64", store.ErrInvalid, event.Seq)
			}
			if int64(event.Seq) <= cursor {
				continue
			}
			if event.Name == "" {
				return fmt.Errorf("Ingest: %w: event seq %d misses its name", store.ErrInvalid, event.Seq)
			}
			current := event.New.Hex()
			inserted, serr := tx.InsertSeen(f.repoID, event.Seq, event.Name, current, now)
			if serr != nil {
				return serr
			}
			if !inserted {
				continue
			}
			if derr := f.deliver(tx, event, now); derr != nil {
				return derr
			}
			if int64(event.Seq) > front {
				front = int64(event.Seq)
			}
		}
		if front == cursor {
			return nil
		}
		if aerr := tx.Outbox().Advance(eventConsumer(f.repoID), cursor, front); aerr != nil {
			return aerr
		}
		if front < 0 {
			return fmt.Errorf("Ingest: front %d is negative", front)
		}
		return tx.SweepSeen(f.repoID, uint64(front))
	})
}

func (f *Follower) deliver(tx *store.WriteTx, event ruk.RefEvent, now time.Time) error {
	current := event.New.Hex()
	body, err := marshalRefUpdated(f.repoID, event.Name, event.Old.Hex(), current, sourceEvents, event.Seq)
	if err != nil {
		return err
	}
	if _, err = tx.Outbox().Emit(store.Event{
		OccurredAt: now,
		Kind:       kindRefUpdated,
		Body:       jsontext.Value(body),
	}); err != nil {
		return err
	}
	if event.New.Null() || isZeroID(current) {
		return tx.DeleteRefTip(f.repoID, event.Name)
	}
	return tx.PutRefTip(f.repoID, event.Name, current, now)
}

func marshalRefUpdated(repoID int64, name, old, current, source string, seq uint64) ([]byte, error) {
	return marshalEventMap(map[string]any{
		"repo":   repoID,
		"name":   name,
		"old":    old,
		"new":    current,
		"source": source,
		"seq":    seq,
	})
}

func marshalEventMap(body map[string]any) ([]byte, error) {
	raw, err := jsonv2.Marshal(body, jsonv1.DefaultOptionsV1())
	if err != nil {
		return nil, err
	}
	if !jsontext.Value(raw).IsValid() {
		return nil, fmt.Errorf("event body is not valid JSON")
	}
	return raw, nil
}

func (f *Follower) PollOnce(ctx context.Context) error {
	cursor, err := f.Cursor(ctx)
	if err != nil {
		return err
	}
	reply, terr := f.tail(ctx, cursor)
	if terr != nil {
		return terr
	}
	if reply.Expired() {
		return f.resnapshot(ctx)
	}
	return f.Ingest(ctx, reply.Events, reply.NextAfter)
}

func (f *Follower) resnapshot(ctx context.Context) error {
	reply, err := f.list(ctx, "")
	if err != nil {
		return err
	}
	now := f.clock()
	tips := make(map[string]string, len(reply.Refs.Items))
	for _, ref := range reply.Refs.Items {
		target := ref.Target.Hex()
		if ref.Name == "" || target == "" {
			return fmt.Errorf("resnapshot: %w: ref entry misses name or target", store.ErrInvalid)
		}
		tips[ref.Name] = target
	}
	return f.store.Write(ctx, func(tx *store.WriteTx) error {
		before, berr := tx.WriteTips(f.repoID)
		if berr != nil {
			return berr
		}
		if rerr := tx.ReplaceRefTips(f.repoID, tips, now); rerr != nil {
			return rerr
		}
		for name, target := range tips {
			if before[name] == target {
				continue
			}
			if serr := f.emitSynthetic(tx, name, before[name], target, now); serr != nil {
				return serr
			}
		}
		for name, target := range before {
			if _, kept := tips[name]; kept {
				continue
			}
			if serr := f.emitSynthetic(tx, name, target, "", now); serr != nil {
				return serr
			}
		}
		cursor, cerr := tx.Outbox().Cursor(eventConsumer(f.repoID))
		if cerr != nil {
			return cerr
		}
		if reply.AsOf > math.MaxInt64 {
			return fmt.Errorf("resnapshot: %w: as_of overflows int64", store.ErrInvalid)
		}
		if int64(reply.AsOf) <= cursor {
			return nil
		}
		return tx.Outbox().Advance(eventConsumer(f.repoID), cursor, int64(reply.AsOf))
	})
}

func (f *Follower) emitSynthetic(tx *store.WriteTx, name, old, current string, now time.Time) error {
	body, err := marshalEventMap(map[string]any{
		"repo":   f.repoID,
		"name":   name,
		"old":    old,
		"new":    current,
		"source": sourceResnapshot,
	})
	if err != nil {
		return err
	}
	_, err = tx.Outbox().Emit(store.Event{
		OccurredAt: now,
		Kind:       kindRefUpdated,
		Body:       jsontext.Value(body),
	})
	return err
}

func (f *Follower) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := f.PollOnce(ctx); err != nil {
			return err
		}
	}
}
