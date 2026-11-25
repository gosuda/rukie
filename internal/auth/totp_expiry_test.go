package auth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

func TestVerifyTOTPChecksExpiryWhenWriterExecutes(t *testing.T) {
	for _, boundary := range []string{"absolute", "idle"} {
		t.Run(boundary, func(t *testing.T) {
			svc, setNow := openTestService(t)
			svc.SetSealKey(testSealKey())
			now := time.Unix(1754000000, 0).UTC()
			setNow(now)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			u := mustCreate(t, svc, "expiry", randomPassword(t, 16), false)
			enrollment, enrollErr := svc.EnrollTOTP(ctx, u.ID, "127.0.0.1")
			if enrollErr != nil {
				t.Fatal(enrollErr)
			}
			raw, decodeErr := base32NoPad.DecodeString(enrollment.SecretBase32)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			before, beforeErr := svc.store.GetTOTPCredentials(ctx, u.ID)
			if beforeErr != nil {
				t.Fatal(beforeErr)
			}
			session := store.SessionCreate{
				IDHash: strings.Repeat("a", 64), UserID: u.ID, CSRF: "csrf",
				AuthLevel: pendingPasswordLevel, AuthedAt: millis(now),
				CreatedAt: millis(now), LastSeenAt: millis(now),
				AbsoluteExpiresAt: millis(now.Add(sessionAbsoluteTTL)),
			}
			if boundary == "absolute" {
				session.AbsoluteExpiresAt = millis(now) + 1
			} else {
				session.LastSeenAt = millis(now) - sessionIdleTTL.Milliseconds() + 1
				session.CreatedAt = session.LastSeenAt
				session.AuthedAt = session.LastSeenAt
			}
			if createErr := svc.store.CreateSession(ctx, session); createErr != nil {
				t.Fatal(createErr)
			}
			row, rowErr := svc.store.GetSessionByHash(ctx, session.IDHash)
			if rowErr != nil {
				t.Fatal(rowErr)
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			writerDone := make(chan error, 1)
			go func() {
				writerDone <- svc.store.Write(ctx, func(*store.WriteTx) error {
					close(entered)
					select {
					case <-release:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("writer did not reach the barrier")
			}
			var clock atomic.Int64
			clock.Store(millis(now))
			matched := make(chan struct{})
			var matchOnce sync.Once
			svc.now = func() time.Time {
				at := time.UnixMilli(clock.Load())
				matchOnce.Do(func() { close(matched) })
				return at
			}
			results := make(chan VerifyTOTPResult, 1)
			verified := make(chan error, 1)
			go func() {
				out, err := svc.VerifyTOTPByRowID(ctx, u.ID, row.RowID, TOTPCode(raw, TOTPCounter(now)), "127.0.0.1", "browser")
				results <- out
				verified <- err
			}()
			select {
			case <-matched:
			case <-ctx.Done():
				t.Fatal("verification did not read the original time")
			}
			clock.Store(millis(now) + 2)
			unblock()
			select {
			case writerErr := <-writerDone:
				if writerErr != nil {
					t.Fatal(writerErr)
				}
			case <-ctx.Done():
				t.Fatal("writer did not leave the barrier")
			}
			select {
			case verifyErr := <-verified:
				if !errors.Is(verifyErr, errInvalidCredentials) {
					t.Fatalf("expired queued verification returned %v", verifyErr)
				}
			case <-ctx.Done():
				t.Fatal("verification did not finish")
			}
			out := <-results
			if out.Secret != "" || len(out.RecoveryCodes) != 0 || out.Session != (Session{}) {
				t.Fatalf("expired verification returned credentials: %+v", out)
			}
			after, afterErr := svc.store.GetTOTPCredentials(ctx, u.ID)
			if afterErr != nil {
				t.Fatal(afterErr)
			}
			if after != before {
				t.Fatalf("expired verification changed credentials: before %+v, after %+v", before, after)
			}
			user, userErr := svc.store.GetAuthUserByID(ctx, u.ID)
			if userErr != nil {
				t.Fatal(userErr)
			}
			if user.TOTPEnabled {
				t.Fatal("expired verification enabled enrollment")
			}
		})
	}
}
