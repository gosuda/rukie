package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"
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
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND user_agent = ?`, userID, userAgent); txErr != nil {
			return txErr
		}
		_, txErr := tx.ExecContext(ctx, `INSERT INTO sessions
			(id_hash, user_id, csrf_token, auth_level, authed_at, totp_ok, created_at, last_seen_at, absolute_expires_at, created_ip, user_agent)
			VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, ?)`,
			sess.IDHash, userID, csrf, authLevel, now, now, now, sess.AbsoluteExpiresAt, ip, userAgent)
		return txErr
	})
	if err != nil {
		return "", Session{}, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "session.create", IP: ip, UserAgent: userAgent, SessionHash: sess.IDHash, Result: "ok"})
	return secret, sess, nil
}

var ErrSessionInvalid = errors.New("invalid session")

func (s *Service) LookupSession(ctx context.Context, secret string) (Session, User, error) {
	idHash := hashBearer(secret)
	now := millis(s.clock())
	var sess Session
	var totp int64
	err := s.db.QueryRowContext(ctx, `SELECT rowid, id_hash, user_id, auth_level, authed_at, totp_ok,
		created_at, last_seen_at, absolute_expires_at FROM sessions WHERE id_hash = ?`, idHash).Scan(
		&sess.RowID, &sess.IDHash, &sess.UserID, &sess.AuthLevel, &sess.AuthedAt, &totp,
		&sess.CreatedAt, &sess.LastSeenAt, &sess.AbsoluteExpiresAt)
	if err != nil {
		return Session{}, User{}, ErrSessionInvalid
	}
	sess.TOTPOk = totp != 0
	if now >= sess.AbsoluteExpiresAt || now-sess.LastSeenAt >= sessionIdleTTL.Milliseconds() {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
		return Session{}, User{}, ErrSessionInvalid
	}
	var u User
	err = s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, sess.UserID).Scan(
		&u.ID, &u.Name, &u.LowerName, &u.Email, &u.FullName,
		&u.PasswordAlgo, boolInt(&u.MustChangePassword), boolInt(&u.TOTPEnabled), boolInt(&u.OIDCOnly),
		boolInt(&u.IsAdmin), boolInt(&u.IsActive),
		&u.FailedLoginCount, nullInt(&u.LockedUntil), &u.PasswordChangedAt, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
		return Session{}, User{}, ErrSessionInvalid
	}
	if !u.IsActive {
		return Session{}, User{}, ErrSessionInvalid
	}
	if now-sess.LastSeenAt >= sessionRefreshAfter.Milliseconds() {
		_, _ = s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, now, idHash)
		sess.LastSeenAt = now
	}
	return sess, u, nil
}

func boolInt(b *bool) *boolIntScan {
	return (*boolIntScan)(b)
}

type boolIntScan bool

func (b *boolIntScan) Scan(src any) error {
	var v int64
	switch t := src.(type) {
	case int64:
		v = t
	case int:
		v = int64(t)
	default:
		return errors.New("unexpected boolean column type")
	}
	*b = boolIntScan(v != 0)
	return nil
}

func nullInt(dst *int64) *nullIntScan {
	return &nullIntScan{dst: dst}
}

type nullIntScan struct {
	dst *int64
}

func (n *nullIntScan) Scan(src any) error {
	if src == nil {
		*n.dst = 0
		return nil
	}
	var v sql.NullInt64
	if err := convertInt(src, &v); err != nil {
		return err
	}
	if v.Valid {
		*n.dst = v.Int64
	}
	return nil
}

func convertInt(src any, dst *sql.NullInt64) error {
	switch t := src.(type) {
	case int64:
		dst.Valid = true
		dst.Int64 = t
		return nil
	default:
		return errors.New("unexpected integer column type")
	}
}

func (s *Service) GetSessionByID(ctx context.Context, rowid int64) (Session, error) {
	var sess Session
	var totp int64
	err := s.db.QueryRowContext(ctx, `SELECT rowid, id_hash, user_id, auth_level, authed_at, totp_ok,
		created_at, last_seen_at, absolute_expires_at FROM sessions WHERE rowid = ?`, rowid).Scan(
		&sess.RowID, &sess.IDHash, &sess.UserID, &sess.AuthLevel, &sess.AuthedAt, &totp,
		&sess.CreatedAt, &sess.LastSeenAt, &sess.AbsoluteExpiresAt)
	if err != nil {
		return Session{}, ErrSessionInvalid
	}
	sess.TOTPOk = totp != 0
	return sess, nil
}

func (s *Service) RevokeSessionByID(ctx context.Context, rowid, userID int64, ip, userAgent string) error {
	var idHash string
	err := s.db.QueryRowContext(ctx, `SELECT id_hash FROM sessions WHERE rowid = ? AND user_id = ?`, rowid, userID).Scan(&idHash)
	if err != nil {
		return ErrSessionInvalid
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE rowid = ?`, rowid)
	if err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "session.destroy", IP: ip, UserAgent: userAgent, SessionHash: idHash, Result: "ok"})
	return nil
}

func (s *Service) RevokeSession(ctx context.Context, secret, ip, userAgent string) error {
	idHash := hashBearer(secret)
	var userID sql.NullInt64
	_ = s.db.QueryRowContext(ctx, `SELECT user_id FROM sessions WHERE id_hash = ?`, idHash).Scan(&userID)
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	if err != nil {
		return err
	}
	var actor int64
	if userID.Valid {
		actor = userID.Int64
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "session.destroy", IP: ip, UserAgent: userAgent, SessionHash: idHash, Result: "ok"})
	return nil
}

func (s *Service) RevokeUserSessions(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Service) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id_hash, user_id, auth_level, authed_at, totp_ok,
		created_at, last_seen_at, absolute_expires_at FROM sessions WHERE user_id = ? ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Session
	for rows.Next() {
		var sess Session
		var totp int64
		if err := rows.Scan(&sess.IDHash, &sess.UserID, &sess.AuthLevel, &sess.AuthedAt, &totp,
			&sess.CreatedAt, &sess.LastSeenAt, &sess.AbsoluteExpiresAt); err != nil {
			return nil, err
		}
		sess.TOTPOk = totp != 0
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Service) RevokeSessionByPrefix(ctx context.Context, userID int64, prefix string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id_hash FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return false, err
	}
	defer func() { _ = rows.Close() }()
	var match string
	for rows.Next() {
		var h string
		if scanErr := rows.Scan(&h); scanErr != nil {
			return false, scanErr
		}
		if len(h) >= len(prefix) && h[:len(prefix)] == prefix {
			if match != "" {
				return false, errors.New("session prefix is ambiguous")
			}
			match = h
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return false, rowsErr
	}
	if match == "" {
		return false, nil
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, match)
	if err != nil {
		return false, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "session.destroy", SessionHash: match, Result: "ok"})
	return true, nil
}

func SessionFresh(authedAtMillis, nowMillis int64) bool {
	return nowMillis-authedAtMillis <= freshAuthWindow.Milliseconds()
}
