package store

import (
	"context"
	"database/sql"
)

type txCore struct {
	ctx  context.Context
	tx   *sql.Tx
	done bool
}

func (c *txCore) live() (*sql.Tx, error) {
	if c.done {
		return nil, ErrTxClosed
	}
	return c.tx, nil
}

// ReadTx is valid only inside the Read callback that receives it.
type ReadTx struct {
	txCore
}

// WriteTx is valid only inside the Write callback that receives it. It must not call Store.Write.
type WriteTx struct {
	txCore
}

// Outbox returns the outbox view of this read snapshot.
func (tx *ReadTx) Outbox() OutboxReader {
	return outboxReader{core: &tx.txCore}
}

// Outbox returns the outbox view of this write transaction.
func (tx *WriteTx) Outbox() OutboxWriter {
	return outboxWriter{outboxReader{core: &tx.txCore}}
}
