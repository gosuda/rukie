package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/gosuda/rukie/internal/store"
)

const sessionCookieName = "__Host-rukie_session"

func SessionCookieName() string {
	return sessionCookieName
}

func hashBearer(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func newSecret() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func SessionCookie(secret string, absolute time.Time, now time.Time) *http.Cookie {
	maxAge := int(absolute.Sub(now).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    secret,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func ClearSessionCookie() *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

type Session struct {
	IDHash            string
	AuthLevel         string
	UserID            int64
	AuthedAt          int64
	CreatedAt         int64
	LastSeenAt        int64
	AbsoluteExpiresAt int64
	RowID             int64
	TOTPOk            bool
}

func sessionFromStore(rec store.SessionRecord) Session {
	return Session{
		IDHash:            rec.IDHash,
		AuthLevel:         rec.AuthLevel,
		UserID:            rec.UserID,
		AuthedAt:          rec.AuthedAt,
		CreatedAt:         rec.CreatedAt,
		LastSeenAt:        rec.LastSeenAt,
		AbsoluteExpiresAt: rec.AbsoluteExpiresAt,
		RowID:             rec.RowID,
		TOTPOk:            rec.TOTPOk,
	}
}

func (s *Service) MintSession(ctx context.Context, userID int64, authLevel, ip, userAgent string) (secret string, sess Session, err error) {
	secret, err = newSecret()
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := newSecret()
	if err != nil {
		return "", Session{}, err
	}
	now := millis(s.clock())
	sess = Session{
		IDHash:            hashBearer(secret),
		UserID:            userID,
		AuthLevel:         authLevel,
		AuthedAt:          now,
		CreatedAt:         now,
		LastSeenAt:        now,
		AbsoluteExpiresAt: now + sessionAbsoluteTTL.Milliseconds(),
	}
	serr := s.store.CreateSession(ctx, store.SessionCreate{
		IDHash:            sess.IDHash,
		AuthLevel:         authLevel,
		CSRF:              csrf,
		IP:                ip,
		UserAgent:         userAgent,
		UserID:            userID,
		AuthedAt:          now,
		CreatedAt:         now,
		LastSeenAt:        now,
		AbsoluteExpiresAt: sess.AbsoluteExpiresAt,
	})
	if serr != nil {
		return "", Session{}, serr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "session.create", IP: ip, UserAgent: userAgent, SessionHash: sess.IDHash, Result: "ok"})
	return secret, sess, nil
}

var ErrSessionInvalid = errors.New("invalid session")

func (s *Service) LookupSession(ctx context.Context, secret string) (Session, User, error) {
	idHash := hashBearer(secret)
	now := millis(s.clock())
	rec, err := s.store.GetSessionByHash(ctx, idHash)
	if err != nil {
		return Session{}, User{}, ErrSessionInvalid
	}
	sess := sessionFromStore(rec)
	if now >= sess.AbsoluteExpiresAt || now-sess.LastSeenAt >= sessionIdleTTL.Milliseconds() {
		_, _ = s.store.DeleteSessionByHash(ctx, idHash)
		return Session{}, User{}, ErrSessionInvalid
	}
	au, uerr := s.store.GetAuthUserByID(ctx, sess.UserID)
	if uerr != nil {
		_, _ = s.store.DeleteSessionByHash(ctx, idHash)
		return Session{}, User{}, ErrSessionInvalid
	}
	u, _ := authUserFromStore(au)
	if !u.IsActive {
		return Session{}, User{}, ErrSessionInvalid
	}
	if now-sess.LastSeenAt >= sessionRefreshAfter.Milliseconds() {
		_ = s.store.TouchSession(ctx, idHash, now)
		sess.LastSeenAt = now
	}
	return sess, u, nil
}

func (s *Service) GetSessionByID(ctx context.Context, rowid int64) (Session, error) {
	rec, err := s.store.GetSessionByID(ctx, rowid)
	if err != nil {
		return Session{}, ErrSessionInvalid
	}
	return sessionFromStore(rec), nil
}

func (s *Service) RevokeSessionByID(ctx context.Context, rowid, userID int64, ip, userAgent string) error {
	idHash, err := s.store.DeleteSessionByRowID(ctx, userID, rowid)
	if err != nil {
		return ErrSessionInvalid
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "session.destroy", IP: ip, UserAgent: userAgent, SessionHash: idHash, Result: "ok"})
	return nil
}

func (s *Service) RevokeSession(ctx context.Context, secret, ip, userAgent string) error {
	idHash := hashBearer(secret)
	userID, err := s.store.DeleteSessionByHash(ctx, idHash)
	if err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "session.destroy", IP: ip, UserAgent: userAgent, SessionHash: idHash, Result: "ok"})
	return nil
}

func (s *Service) RevokeUserSessions(ctx context.Context, userID int64) error {
	return s.store.DeleteUserSessions(ctx, userID)
}

func (s *Service) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	recs, err := s.store.ListSessionsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Session, 0, len(recs))
	for _, rec := range recs {
		sess := sessionFromStore(rec)
		sess.RowID = 0
		out = append(out, sess)
	}
	return out, nil
}

func (s *Service) RevokeSessionByPrefix(ctx context.Context, userID int64, prefix string) (bool, error) {
	hashes, err := s.store.ListSessionHashesByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	match := ""
	for _, h := range hashes {
		if len(h) >= len(prefix) && h[:len(prefix)] == prefix {
			if match != "" {
				return false, errors.New("session prefix is ambiguous")
			}
			match = h
		}
	}
	if match == "" {
		return false, nil
	}
	if _, derr := s.store.DeleteSessionByHash(ctx, match); derr != nil {
		return false, derr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "session.destroy", SessionHash: match, Result: "ok"})
	return true, nil
}

func SessionFresh(authedAtMillis, nowMillis int64) bool {
	return nowMillis-authedAtMillis <= freshAuthWindow.Milliseconds()
}
