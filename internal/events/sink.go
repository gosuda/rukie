package events

import (
	"context"
	"time"

	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

type HookSink struct {
	store *store.Store
	spool *Spool
	clock func() time.Time
}

func OpenHookSink(st *store.Store, sp *Spool, clock func() time.Time) *HookSink {
	if clock == nil {
		clock = time.Now
	}
	return &HookSink{store: st, spool: sp, clock: clock}
}

func (snk *HookSink) Handle(ctx context.Context, meta PushMeta, call wire.HookCall) wire.HookReply {
	return HandlePostReceive(ctx, snk.store, snk.spool, meta, call, snk.clock())
}

func (snk *HookSink) Record(ctx context.Context, rec PushRecord) (bool, error) {
	var inserted bool
	err := snk.store.Write(ctx, func(tx *store.WriteTx) error {
		ok, werr := RecordPush(tx, rec)
		if werr != nil {
			return werr
		}
		inserted = ok
		return nil
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
}
