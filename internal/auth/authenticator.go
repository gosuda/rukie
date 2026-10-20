package auth

import (
	"context"
	"errors"
	"time"

	"github.com/gosuda/rukie/internal/op"
)

type Authenticator struct {
	svc *Service
}

func NewAuthenticator(svc *Service) Authenticator {
	return Authenticator{svc: svc}
}

func (a Authenticator) Bearer(ctx context.Context, secret string) (op.Principal, error) {
	tok, u, err := a.svc.LookupToken(ctx, secret, "")
	if err != nil {
		return op.Principal{}, err
	}
	var rowid int64
	if err := a.svc.db.QueryRowContext(ctx, `SELECT rowid FROM scoped_tokens WHERE token_hash = ?`, tok.Hash).Scan(&rowid); err != nil || rowid <= 0 {
		return op.Principal{}, ErrTokenInvalid
	}
	return op.Principal{
		UserID:  u.ID,
		Kind:    op.PrincipalHuman,
		TokenID: rowid,
		Grants:  tok.Scopes,
	}, nil
}

func (a Authenticator) Session(ctx context.Context, secret string) (op.Principal, error) {
	sess, u, err := a.svc.LookupSession(ctx, secret)
	if err != nil {
		return op.Principal{}, err
	}
	if sess.RowID <= 0 {
		return op.Principal{}, ErrSessionInvalid
	}
	return op.Principal{
		UserID:  u.ID,
		Kind:    op.PrincipalHuman,
		TokenID: -sess.RowID,
		Grants:  ScopesAll(),
	}, nil
}

func (a Authenticator) Service() *Service {
	return a.svc
}

func RequireSession(p op.Principal) (int64, int64, error) {
	if p.Kind == op.PrincipalAnonymous || p.UserID == 0 {
		return 0, 0, errors.New("authentication required")
	}
	if p.TokenID >= 0 {
		return 0, 0, errors.New("session authentication required")
	}
	return p.UserID, -p.TokenID, nil
}

func RequireFresh(ctx context.Context, svc *Service, p op.Principal, now time.Time) (int64, error) {
	userID, rowid, err := RequireSession(p)
	if err != nil {
		return 0, err
	}
	sess, err := svc.GetSessionByID(ctx, rowid)
	if err != nil {
		return 0, errors.New("session authentication required")
	}
	if sess.UserID != userID {
		return 0, errors.New("session authentication required")
	}
	if now.UTC().UnixMilli()-sess.AuthedAt > freshAuthWindow.Milliseconds() {
		return 0, errors.New("fresh authentication required")
	}
	return userID, nil
}
