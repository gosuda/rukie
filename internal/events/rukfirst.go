package events

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"encoding/json/jsontext"

	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/store"
)

func StableRequestID(operation, repo string, digest []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(operation))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(repo))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(digest)
	sum := h.Sum(nil)
	var raw [16]byte
	copy(raw[:], sum[:16])
	raw[6] = raw[6]&0x0f | 0x50
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func WriteAfterRuk(ctx context.Context, st *store.Store, requestID string, call, replay func(string) (jsontext.Value, error), commit func(*store.WriteTx, jsontext.Value) error) error {
	if requestID == "" {
		return fmt.Errorf("WriteAfterRuk: request id must not be empty")
	}
	if call == nil {
		return fmt.Errorf("WriteAfterRuk: call must not be nil")
	}
	if commit == nil {
		return fmt.Errorf("WriteAfterRuk: commit must not be nil")
	}
	receipt, err := call(requestID)
	if err != nil {
		if !errors.Is(err, ruk.ErrOutcomeUnknown) {
			return err
		}
		if replay == nil {
			return err
		}
		receipt, err = replay(requestID)
		if err != nil {
			return err
		}
	}
	return st.Write(ctx, func(tx *store.WriteTx) error {
		return commit(tx, receipt)
	})
}
