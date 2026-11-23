package auth

import (
	"errors"
	"testing"
)

func TestStepUpSessionRejectsRevokedSession(t *testing.T) {
	svc, _ := openTestService(t)
	ctx := t.Context()
	u := mustCreate(t, svc, "rotation", randomPassword(t, 16), false)
	secret, old, err := svc.MintSession(ctx, u.ID, pendingPasswordLevel, "127.0.0.1", "browser")
	if err != nil {
		t.Fatalf("mint session: %v", err)
	}
	if revokeErr := svc.RevokeSession(ctx, secret, "127.0.0.1", "browser"); revokeErr != nil {
		t.Fatalf("revoke session: %v", revokeErr)
	}
	next, _, err := svc.stepUpSession(ctx, old, "127.0.0.1", "browser")
	if !errors.Is(err, errInvalidCredentials) || next != "" {
		t.Fatalf("step up returned secret %q and error %v, want invalid credentials", next, err)
	}
	sessions, err := svc.ListSessions(ctx, u.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("step up recreated %d revoked sessions", len(sessions))
	}
}
