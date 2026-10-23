package store

import (
	"fmt"
)

func (tx *ReadTx) CIState() (int64, int64, []byte, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, 0, nil, fmt.Errorf("CIState: %w", err)
	}
	var epoch, fence int64
	var state []byte
	err = inner.QueryRowContext(tx.ctx, "SELECT epoch, fence, state FROM ci_controller WHERE id=1").Scan(&epoch, &fence, &state)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("CIState: %w", err)
	}
	return epoch, fence, state, nil
}

func (tx *WriteTx) CIState() (int64, int64, []byte, error) {
	inner, err := tx.live()
	if err != nil {
		return 0, 0, nil, fmt.Errorf("CIState: %w", err)
	}
	var epoch, fence int64
	var state []byte
	err = inner.QueryRowContext(tx.ctx, "SELECT epoch, fence, state FROM ci_controller WHERE id=1").Scan(&epoch, &fence, &state)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("CIState: %w", err)
	}
	return epoch, fence, state, nil
}

func (tx *WriteTx) SetCIState(expected, epoch, fence int64, state []byte) error {
	inner, err := tx.live()
	if err != nil {
		return fmt.Errorf("SetCIState: %w", err)
	}
	result, err := inner.ExecContext(tx.ctx, "UPDATE ci_controller SET epoch=?,fence=?,state=? WHERE id=1 AND epoch=?", epoch, fence, state, expected)
	if err != nil {
		return fmt.Errorf("SetCIState: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("SetCIState: %w", err)
	}
	if count != 1 {
		return fmt.Errorf("SetCIState: %w", ErrStale)
	}
	return nil
}
