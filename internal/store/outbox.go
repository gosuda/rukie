package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"encoding/json/jsontext"
)

const maxKindLen = 64

// Event is written in the same WriteTx as the mutation it describes. Kind is 1 to 64 bytes.
// Body must be a JSON object.
type Event struct {
	OccurredAt time.Time
	Kind       string
	Body       jsontext.Value
}

// OutboxRow is one committed event. Seq is store-assigned, strictly increasing, and never reused.
type OutboxRow struct {
	OccurredAt time.Time
	ID         ID
	Kind       string
	Body       jsontext.Value
	Seq        int64
}

// OutboxReader reads committed events for named consumers.
type OutboxReader interface {
	// Cursor returns 0 for a consumer that has never advanced.
	Cursor(consumer string) (int64, error)
	// After returns up to limit rows with seq above the consumer's cursor, ascending.
	After(consumer string, limit int) ([]OutboxRow, error)
}

// OutboxWriter appends events and advances consumer cursors inside a WriteTx.
type OutboxWriter interface {
	OutboxReader
	// Emit appends an event in the caller's transaction.
	Emit(ev Event) (OutboxRow, error)
	// Advance moves the cursor from from to to. It returns ErrStale when the stored cursor is not from.
	Advance(consumer string, from, to int64) error
}

type outboxReader struct {
	core *txCore
}

type outboxWriter struct {
	outboxReader
}

func (o outboxReader) Cursor(consumer string) (int64, error) {
	if err := checkConsumer(consumer); err != nil {
		return 0, err
	}
	tx, err := o.core.live()
	if err != nil {
		return 0, err
	}
	var cursor int64
	err = tx.QueryRowContext(o.core.ctx, sqlConsumerCursor, consumer).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("Cursor: %w", err)
	}
	return cursor, nil
}

func (o outboxReader) After(consumer string, limit int) ([]OutboxRow, error) {
	if err := checkConsumer(consumer); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, fmt.Errorf("After: %w: limit must be positive", ErrInvalid)
	}
	tx, err := o.core.live()
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(o.core.ctx, sqlOutboxAfter, consumer, limit)
	if err != nil {
		return nil, fmt.Errorf("After: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []OutboxRow
	for rows.Next() {
		var row OutboxRow
		var pid, body string
		var occurred int64
		if err = rows.Scan(&row.Seq, &pid, &row.Kind, &body, &occurred); err != nil {
			return nil, fmt.Errorf("After: scan: %w", err)
		}
		row.ID = ID(pid)
		row.Body = jsontext.Value(body)
		row.OccurredAt = timeOf(occurred)
		out = append(out, row)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("After: %w", err)
	}
	return out, nil
}

func (o outboxWriter) Emit(ev Event) (OutboxRow, error) {
	if err := validateEvent(ev); err != nil {
		return OutboxRow{}, err
	}
	tx, err := o.core.live()
	if err != nil {
		return OutboxRow{}, err
	}
	id := NewID(time.Now())
	occurred := msOf(ev.OccurredAt)
	res, err := tx.ExecContext(o.core.ctx, sqlOutboxInsert, string(id), ev.Kind, string(ev.Body), occurred)
	if err != nil {
		return OutboxRow{}, fmt.Errorf("Emit: %w", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return OutboxRow{}, fmt.Errorf("Emit: %w", err)
	}
	row := OutboxRow{Seq: seq, ID: id, Kind: ev.Kind, Body: ev.Body, OccurredAt: timeOf(occurred)}
	if ferr := fanoutEvent(o.core, row); ferr != nil {
		return OutboxRow{}, fmt.Errorf("Emit: %w", ferr)
	}
	return row, nil
}

func (o outboxWriter) Advance(consumer string, from, to int64) error {
	if err := checkConsumer(consumer); err != nil {
		return err
	}
	if from < 0 || to < from {
		return fmt.Errorf("Advance: %w: cursor must satisfy 0 <= from <= to", ErrInvalid)
	}
	tx, err := o.core.live()
	if err != nil {
		return err
	}
	now := msOf(time.Now())
	moved, err := execAffected(o.core.ctx, tx, sqlConsumerAdvance, to, now, consumer, from)
	if err != nil {
		return fmt.Errorf("Advance: %w", err)
	}
	if moved == 1 {
		return nil
	}
	if from != 0 {
		return fmt.Errorf("Advance: %w", ErrStale)
	}
	created, err := execAffected(o.core.ctx, tx, sqlConsumerCreate, consumer, to, now)
	if err != nil {
		return fmt.Errorf("Advance: %w", err)
	}
	if created == 0 {
		return fmt.Errorf("Advance: %w", ErrStale)
	}
	return nil
}

func execAffected(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func checkConsumer(name string) error {
	if len(name) < 1 || len(name) > maxKindLen {
		return fmt.Errorf("consumer: %w: name must be 1 to 64 bytes", ErrInvalid)
	}
	return nil
}

func validateEvent(ev Event) error {
	if len(ev.Kind) < 1 || len(ev.Kind) > maxKindLen {
		return fmt.Errorf("Emit: %w: kind must be 1 to 64 bytes", ErrInvalid)
	}
	body := bytes.TrimSpace(ev.Body)
	if len(body) == 0 || body[0] != '{' || !jsontext.Value(body).IsValid() {
		return fmt.Errorf("Emit: %w: body must be a JSON object", ErrInvalid)
	}
	if msOf(ev.OccurredAt) <= 0 {
		return fmt.Errorf("Emit: %w: occurred_at must be after the Unix epoch", ErrInvalid)
	}
	return nil
}
