package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

// Audit records one domain audit event for admin handlers outside this
// package. Actor is the admin user id; Target uses the "type:id" form.
func (s *Service) Audit(ctx context.Context, actor int64, action, target, ip string) {
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: action, Target: target, IP: ip, Result: "ok"})
}

// DeleteUserInput carries the admin user deletion contract.
type DeleteUserInput struct {
	Target  string
	Confirm string
	IfMatch string
}

// userPurgeDelay is the tombstone horizon for deleted users and the
// user-namespace repos soft-deleted alongside them. It mirrors the
// repository purge delay: one horizon for the whole deletion.
const userPurgeDelay = 30 * 24 * time.Hour

// DeleteUser soft-deletes any user account and returns the purge horizon in
// Unix millis. It mirrors DeleteSelf without the self-only fresh-auth rule:
// only site admins reach it (the op handler enforces that first). Sessions
// are revoked, scoped tokens are suspended, and live user-namespace repos
// are soft-deleted with the same horizon. The last active admin and sole org
// owners are refused exactly like self deletion.
func (s *Service) DeleteUser(ctx context.Context, actor int64, in DeleteUserInput, ip string) (int64, error) {
	u, _, err := s.authUserByName(ctx, strings.ToLower(in.Target))
	if err != nil {
		return 0, errors.New("unknown user")
	}
	if in.IfMatch == "" {
		return 0, errors.New("if-match is required")
	}
	if !etagMatch(in.IfMatch, quoteETag(u.ID, u.UpdatedAt)) {
		return 0, errStale
	}
	if in.Confirm == "" || !strings.EqualFold(in.Confirm, u.Name) {
		return 0, errors.New("confirmation name does not match")
	}
	if actor == u.ID && u.IsAdmin && u.IsActive && s.CountActiveAdmins(ctx) <= 1 {
		return 0, errors.New("cannot delete the last active admin")
	}
	now := s.clock()
	purgeAfter := now.Add(userPurgeDelay)
	if serr := s.store.SoftDeleteUserCascade(ctx, u.ID, now, purgeAfter); serr != nil {
		if errors.Is(serr, store.ErrSoleOwner) {
			return 0, errors.New("cannot delete the last org owner")
		}
		if errors.Is(serr, store.ErrNotFound) {
			return 0, errors.New("unknown user")
		}
		if errors.Is(serr, store.ErrLastAdmin) {
			return 0, errors.New("cannot delete the last active admin")
		}
		return 0, serr
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.user_delete", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return purgeAfter.UnixMilli(), nil
}
