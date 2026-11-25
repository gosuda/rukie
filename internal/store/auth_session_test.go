package store

import (
	"errors"
	"strings"
	"testing"
)

func TestRotateSessionConsumesMatchingSession(t *testing.T) {
	oldHash := strings.Repeat("a", 64)
	nextHash := strings.Repeat("b", 64)
	firstHash := strings.Repeat("c", 64)
	for _, scenario := range []string{"live", "revoked", "rotated", "wrong_owner", "conflict"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			s := openWith(t, testPath(t), embeddedMigrations, Options{})
			u, err := s.InsertAuthUser(ctx, AuthUserCreate{
				Name: "ada", Lower: "ada", Email: "ada@example.test",
				Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
			})
			if err != nil {
				t.Fatalf("insert user: %v", err)
			}
			old := SessionCreate{
				IDHash:            oldHash,
				UserID:            u.ID,
				AuthLevel:         "password-pending",
				CSRF:              "old-csrf",
				UserAgent:         "browser",
				AuthedAt:          1,
				CreatedAt:         1,
				LastSeenAt:        1,
				AbsoluteExpiresAt: 1000,
			}
			if createErr := s.CreateSession(ctx, old); createErr != nil {
				t.Fatalf("create session: %v", createErr)
			}
			next := old
			next.IDHash, next.CSRF, next.AuthLevel, next.TOTPOk = nextHash, "next-csrf", "password", true
			var wantErr error
			switch scenario {
			case "revoked":
				if _, revokeErr := s.DeleteSessionByHash(ctx, old.IDHash); revokeErr != nil {
					t.Fatalf("revoke session: %v", revokeErr)
				}
				wantErr = ErrNotFound
			case "rotated":
				first := next
				first.IDHash = firstHash
				if rotateErr := s.RotateSession(ctx, old.IDHash, first); rotateErr != nil {
					t.Fatalf("first rotation: %v", rotateErr)
				}
				wantErr = ErrNotFound
			case "wrong_owner":
				other, otherErr := s.InsertAuthUser(ctx, AuthUserCreate{
					Name: "grace", Lower: "grace", Email: "grace@example.test",
					Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
				})
				if otherErr != nil {
					t.Fatalf("insert other user: %v", otherErr)
				}
				next.UserID = other.ID
				wantErr = ErrNotFound
			case "conflict":
				existing := next
				existing.UserAgent = "other-browser"
				if conflictErr := s.CreateSession(ctx, existing); conflictErr != nil {
					t.Fatalf("create conflicting session: %v", conflictErr)
				}
				wantErr = ErrConflict
			}
			err = s.RotateSession(ctx, old.IDHash, next)
			if !errors.Is(err, wantErr) {
				t.Fatalf("rotation error = %v, want %v", err, wantErr)
			}
			_, oldErr := s.GetSessionByHash(ctx, old.IDHash)
			if scenario == "wrong_owner" || scenario == "conflict" {
				if oldErr != nil {
					t.Fatalf("failed rotation removed old session: %v", oldErr)
				}
			} else if !errors.Is(oldErr, ErrNotFound) {
				t.Fatalf("old session error = %v, want not found", oldErr)
			}
			got, nextErr := s.GetSessionByHash(ctx, next.IDHash)
			if scenario == "live" || scenario == "conflict" {
				if nextErr != nil || got.UserID != next.UserID || !got.TOTPOk {
					t.Fatalf("replacement = %+v, error = %v", got, nextErr)
				}
			} else if !errors.Is(nextErr, ErrNotFound) {
				t.Fatalf("rejected replacement exists: %+v, error = %v", got, nextErr)
			}
			if scenario == "rotated" {
				if _, firstErr := s.GetSessionByHash(ctx, firstHash); firstErr != nil {
					t.Fatalf("repeated rotation removed the first replacement: %v", firstErr)
				}
			}
		})
	}
}
