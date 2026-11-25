package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSecondFactorRollsBackWithSessionRotation(t *testing.T) {
	for _, factor := range []string{"recovery", "enrollment", "promotion", "counter"} {
		t.Run(factor, func(t *testing.T) {
			ctx := t.Context()
			s := openWith(t, testPath(t), embeddedMigrations, Options{})
			u, userErr := s.InsertAuthUser(ctx, AuthUserCreate{
				Name: "ada", Lower: "ada", Email: "ada@example.test",
				Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
			})
			if userErr != nil {
				t.Fatal(userErr)
			}
			old := SessionCreate{
				IDHash:            strings.Repeat("a", 64),
				UserID:            u.ID,
				CSRF:              "old-csrf",
				AuthLevel:         "password-pending",
				UserAgent:         "old",
				AuthedAt:          1,
				CreatedAt:         1,
				LastSeenAt:        1,
				AbsoluteExpiresAt: 1000,
			}
			if createErr := s.CreateSession(ctx, old); createErr != nil {
				t.Fatal(createErr)
			}
			next := old
			next.IDHash, next.CSRF, next.UserAgent = strings.Repeat("b", 64), "next-csrf", "other"
			next.AuthLevel, next.TOTPOk = "password", true
			if conflictErr := s.CreateSession(ctx, next); conflictErr != nil {
				t.Fatal(conflictErr)
			}
			if stageErr := s.StageTOTPSecret(ctx, u.ID, "sealed-current", 1); stageErr != nil {
				t.Fatal(stageErr)
			}
			digest := strings.Repeat("c", 64)
			if factor != "enrollment" {
				applied, confirmErr := s.ConfirmTOTPSecret(ctx, u.ID, 1, 2, u.Lower, "127.0.0.1", []string{digest})
				if confirmErr != nil || !applied {
					t.Fatalf("initial confirmation = %v, %v", applied, confirmErr)
				}
			}
			if factor == "promotion" {
				if stageErr := s.StageTOTPSecret(ctx, u.ID, "sealed-pending", 3); stageErr != nil {
					t.Fatal(stageErr)
				}
			}
			before, beforeErr := s.GetTOTPCredentials(ctx, u.ID)
			if beforeErr != nil {
				t.Fatal(beforeErr)
			}
			attemptsBefore := readInt(t, s, "SELECT COUNT(*) FROM login_attempts")
			codesBefore := readInt(t, s, "SELECT COUNT(*) FROM recovery_codes")
			enabledBefore := readInt(t, s, "SELECT totp_enabled FROM users WHERE id = ?", u.ID)
			err := s.Write(ctx, func(tx *WriteTx) error {
				var applied bool
				var applyErr error
				switch factor {
				case "recovery":
					applied, applyErr = tx.VerifyRecoveryCode(u.ID, digest, 4, u.Lower, "127.0.0.1")
				case "enrollment":
					applied, applyErr = tx.ConfirmTOTPSecret(u.ID, 2, 4, u.Lower, "127.0.0.1", []string{digest})
				case "promotion":
					applied, applyErr = tx.PromoteTOTPSecret(u.ID, 2, 4, u.Lower, "127.0.0.1", []string{strings.Repeat("d", 64)})
				case "counter":
					applied, applyErr = tx.AdvanceTOTPCounter(u.ID, 2, 4, u.Lower, "127.0.0.1")
				}
				if applyErr != nil {
					return fmt.Errorf("TestSecondFactorRollsBackWithSessionRotation: %w", applyErr)
				}
				if !applied {
					return fmt.Errorf("TestSecondFactorRollsBackWithSessionRotation: %s was not applied", factor)
				}
				return tx.RotateSession(old.IDHash, next)
			})
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("rotation returned %v, want insertion conflict", err)
			}
			after, afterErr := s.GetTOTPCredentials(ctx, u.ID)
			if afterErr != nil {
				t.Fatal(afterErr)
			}
			if after != before {
				t.Fatalf("failed rotation committed factor state: before %+v, after %+v", before, after)
			}
			if got := readInt(t, s, "SELECT COUNT(*) FROM login_attempts"); got != attemptsBefore {
				t.Fatalf("failed rotation committed %d authentication attempts", got-attemptsBefore)
			}
			if got := readInt(t, s, "SELECT COUNT(*) FROM recovery_codes"); got != codesBefore {
				t.Fatalf("failed rotation changed recovery-code count from %d to %d", codesBefore, got)
			}
			if got := readInt(t, s, "SELECT COUNT(*) FROM recovery_codes WHERE used_at IS NOT NULL"); got != 0 {
				t.Fatalf("failed rotation spent %d recovery codes", got)
			}
			if got := readInt(t, s, "SELECT totp_enabled FROM users WHERE id = ?", u.ID); got != enabledBefore {
				t.Fatal("failed rotation committed enrollment")
			}
			if _, oldErr := s.GetSessionByHash(ctx, old.IDHash); oldErr != nil {
				t.Fatalf("failed rotation removed original session: %v", oldErr)
			}
		})
	}
}
