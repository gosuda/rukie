package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"encoding/json/jsontext"
)

func emitEvent(kind, body string) Event {
	return Event{Kind: kind, Body: jsontext.Value(body), OccurredAt: time.Now()}
}

func TestOutboxEmitIsTransactional(t *testing.T) {
	s := openTest(t, Options{})
	errRollback := errors.New("planned rollback")
	err := s.Write(t.Context(), func(tx *WriteTx) error {
		if _, emitErr := tx.Outbox().Emit(emitEvent("probe.gone", `{"n":1}`)); emitErr != nil {
			return emitErr
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatalf("write returned %v", err)
	}
	if got := readInt(t, s, `SELECT count(*) FROM outbox`); got != 0 {
		t.Fatalf("rolled-back event persisted: %d rows", got)
	}
	var row OutboxRow
	if err = s.Write(t.Context(), func(tx *WriteTx) error {
		var emitErr error
		row, emitErr = tx.Outbox().Emit(emitEvent("probe.kept", `{"n":2}`))
		return emitErr
	}); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if row.Seq <= 0 || row.ID == "" || row.Kind != "probe.kept" {
		t.Fatalf("emitted row = %+v", row)
	}
}

func TestOutboxAdvanceAndAfter(t *testing.T) {
	s := openTest(t, Options{})
	var seqs []int64
	for i := range 3 {
		var row OutboxRow
		if err := s.Write(t.Context(), func(tx *WriteTx) error {
			var emitErr error
			row, emitErr = tx.Outbox().Emit(emitEvent("probe.n", fmt.Sprintf(`{"n":%d}`, i)))
			return emitErr
		}); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
		seqs = append(seqs, row.Seq)
	}
	err := s.Write(t.Context(), func(tx *WriteTx) error {
		o := tx.Outbox()
		if err := o.Advance("indexer", 0, seqs[0]); err != nil {
			return err
		}
		if err := o.Advance("indexer", 0, seqs[2]); !errors.Is(err, ErrStale) {
			return errors.New("stale-from advance was accepted")
		}
		if err := o.Advance("ghost", 4, 5); !errors.Is(err, ErrStale) {
			return errors.New("new consumer with nonzero from was accepted")
		}
		return o.Advance("indexer", seqs[0], seqs[1])
	})
	if err != nil {
		t.Fatalf("advance sequence: %v", err)
	}
	err = s.Read(t.Context(), func(tx *ReadTx) error {
		cursor, cerr := tx.Outbox().Cursor("indexer")
		if cerr != nil || cursor != seqs[1] {
			t.Errorf("cursor = %d, %v; want %d", cursor, cerr, seqs[1])
		}
		rows, rerr := tx.Outbox().After("indexer", 10)
		if rerr != nil || len(rows) != 1 || rows[0].Seq != seqs[2] {
			t.Errorf("After = %+v, %v; want only seq %d", rows, rerr, seqs[2])
		}
		fresh, ferr := tx.Outbox().After("fresh", 10)
		if ferr != nil || len(fresh) != 3 || fresh[0].Seq != seqs[0] {
			t.Errorf("fresh consumer After = %+v, %v", fresh, ferr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestOutboxRejectsBadEvents(t *testing.T) {
	s := openTest(t, Options{})
	bad := []Event{
		{Kind: "", Body: jsontext.Value(`{}`), OccurredAt: time.Now()},
		{Kind: "probe.x", Body: jsontext.Value(`[1]`), OccurredAt: time.Now()},
		{Kind: "probe.x", Body: jsontext.Value(`{"a":1,"a":2}`), OccurredAt: time.Now()},
		{Kind: "probe.x", Body: jsontext.Value(`{"a":`), OccurredAt: time.Now()},
		{Kind: "probe.x", Body: jsontext.Value(`{}`), OccurredAt: time.Time{}},
	}
	for i, ev := range bad {
		err := s.Write(t.Context(), func(tx *WriteTx) error {
			_, emitErr := tx.Outbox().Emit(ev)
			return emitErr
		})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("event %d: got %v, want ErrInvalid", i, err)
		}
	}
	if got := readInt(t, s, `SELECT count(*) FROM outbox`); got != 0 {
		t.Fatalf("invalid events persisted: %d rows", got)
	}
}

func TestWriteTxClosedAfterCallback(t *testing.T) {
	s := openTest(t, Options{})
	var escaped *WriteTx
	if err := s.Write(t.Context(), func(tx *WriteTx) error {
		escaped = tx
		return nil
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := escaped.Outbox().Emit(emitEvent("probe.late", `{}`)); !errors.Is(err, ErrTxClosed) {
		t.Fatalf("Emit after callback returned %v, want ErrTxClosed", err)
	}
}
