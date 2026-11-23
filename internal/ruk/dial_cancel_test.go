package ruk

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPendingDialPreservesCancellationCause(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		name := "cancel"
		if deadline {
			name = "deadline"
		}
		t.Run(name, func(t *testing.T) {
			cfg, entered, unblock := holdHandshakes(t)
			defer unblock()
			socket, _ := startFake(t, cfg)
			client := dialTest(t, socket, WithPool(2, 1))
			pc, stream, _, err := client.pool.acquire(t.Context())
			if err != nil {
				t.Fatalf("reserve first connection: %v", err)
			}
			defer pc.release(stream)
			cause := errors.New("dial canceled by its caller")
			var ctx context.Context
			var cancel context.CancelFunc
			want := context.Canceled
			if deadline {
				ctx, cancel = context.WithTimeoutCause(t.Context(), time.Second, cause)
				want = context.DeadlineExceeded
			} else {
				var cancelCause context.CancelCauseFunc
				ctx, cancelCause = context.WithCancelCause(t.Context())
				cancel = func() { cancelCause(cause) }
			}
			defer cancel()
			done := acquireAsync(ctx, client)
			awaitHandshake(t, entered)
			if !deadline {
				cancel()
			}
			got := awaitAcquisition(t, done)
			if got.s != nil {
				got.pc.release(got.s)
			}
			if !errors.Is(got.err, want) || !errors.Is(got.err, cause) {
				t.Fatalf("dial returned %v, want both %v and %v", got.err, want, cause)
			}
		})
	}
}
