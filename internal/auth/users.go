package auth

import (
	"context"
	"database/sql"
	"strings"
)

type AuditEvent struct {
	Action      string
	Target      string
	IP          string
	UserAgent   string
	SessionHash string
	Result      string
	Actor       int64
}

func (s *Service) recordAudit(ctx context.Context, e AuditEvent) {
	var actorArg any
	if e.Actor != 0 {
		actorArg = e.Actor
	}
	targetType, targetID, _ := strings.Cut(e.Target, ":")
	var prefixArg any
	if len(e.SessionHash) >= 12 {
		prefixArg = e.SessionHash[:12]
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO audit_events
		(at, actor_user_id, action, target_type, target_id, ip, user_agent, session_hash_prefix, result, details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		millis(s.clock()), actorArg, e.Action, targetType, targetID, e.IP, e.UserAgent, prefixArg, e.Result, "{}")
}

func (s *Service) recordAttempt(ctx context.Context, usernameLower, ip string, success bool, reason string) {
	flag := 0
	if success {
		flag = 1
	}
	_, _ = s.db.ExecContext(ctx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
		VALUES (?, ?, ?, ?, ?)`, usernameLower, ip, millis(s.clock()), flag, reason)
}

type LoginInput struct {
	Username  string
	Password  string
	IP        string
	UserAgent string
}

type LoginResult struct {
	Secret       string
	User         User
	Session      Session
	Rehashed     bool
	TOTPRequired bool
}

var errInvalidCredentials = loginError{}

type loginError struct{}

func (loginError) Error() string {
	return "Invalid username or password."
}

func (s *Service) Login(ctx context.Context, in LoginInput) (LoginResult, error) {
	lower := strings.ToLower(in.Username)
	u, hash, err := s.userByName(ctx, s.db, lower)
	if err != nil {
		DummyVerify(in.Password)
		s.recordAttempt(ctx, lower, in.IP, false, "password")
		return LoginResult{}, errInvalidCredentials
	}
	now := millis(s.clock())
	locked := u.LockedUntil > now
	if locked {
		ok, _ := VerifyPassword(in.Password, hash)
		if !ok {
			if ferr := s.recordFailure(ctx, lower, u, in.IP, now); ferr != nil {
				return LoginResult{}, ferr
			}
		} else {
			s.recordAttempt(ctx, lower, in.IP, false, "password")
		}
		return LoginResult{}, errInvalidCredentials
	}
	if !u.IsActive || u.OIDCOnly {
		DummyVerify(in.Password)
		s.recordAttempt(ctx, lower, in.IP, false, "password")
		return LoginResult{}, errInvalidCredentials
	}
	ok, verr := VerifyPassword(in.Password, hash)
	if verr != nil || !ok {
		if ferr := s.recordFailure(ctx, lower, u, in.IP, now); ferr != nil {
			return LoginResult{}, ferr
		}
		return LoginResult{}, errInvalidCredentials
	}
	var res LoginResult
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `UPDATE users SET failed_login_count = 0, locked_until = NULL,
			updated_at = ? WHERE id = ?`, now, u.ID); txErr != nil {
			return txErr
		}
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM login_attempts WHERE username_lower = ? AND success = 0`, lower); txErr != nil {
			return txErr
		}
		if _, txErr := tx.ExecContext(ctx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
			VALUES (?, ?, ?, 1, 'password')`, lower, in.IP, now); txErr != nil {
			return txErr
		}
		if NeedsRehash(hash) {
			fresh, herr := HashPassword(in.Password)
			if herr != nil {
				return herr
			}
			if _, txErr := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`,
				fresh, now, u.ID); txErr != nil {
				return txErr
			}
			res.Rehashed = true
		}
		return nil
	})
	if err != nil {
		return LoginResult{}, err
	}
	level := fullPasswordLevel
	if u.TOTPEnabled {
		level = pendingPasswordLevel
	}
	secret, sess, err := s.MintSession(ctx, u.ID, level, in.IP, in.UserAgent)
	if err != nil {
		return LoginResult{}, err
	}
	u.FailedLoginCount = 0
	u.LockedUntil = 0
	res.Session = sess
	res.User = u
	res.Secret = secret
	res.TOTPRequired = u.TOTPEnabled
	s.recordAudit(ctx, AuditEvent{Actor: u.ID, Action: "password.login", Target: "user:" + u.Name, IP: in.IP, UserAgent: in.UserAgent, SessionHash: sess.IDHash, Result: "ok"})
	return res, nil
}

func (s *Service) recordFailure(ctx context.Context, lower string, u User, ip string, now int64) error {
	return s.recordFailureReason(ctx, lower, u, ip, now, "password")
}

func (s *Service) recordFailureReason(ctx context.Context, lower string, u User, ip string, now int64, reason string) error {
	return s.execTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
			VALUES (?, ?, ?, 0, ?)`, lower, ip, now, reason); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM login_attempts WHERE at < ?`, now-lockoutRetain.Milliseconds()); err != nil {
			return err
		}
		var recent int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM login_attempts
			WHERE username_lower = ? AND at >= ? AND success = 0`, lower, now-lockoutWindow.Milliseconds()).Scan(&recent); err != nil {
			return err
		}
		if u.LockedUntil > now {
			remaining := u.LockedUntil - now
			doubled := min(remaining*2, lockoutMax.Milliseconds())
			if _, err := tx.ExecContext(ctx, `UPDATE users SET locked_until = ?, updated_at = ? WHERE id = ?`,
				now+doubled, now, u.ID); err != nil {
				return err
			}
			return killPendingSessions(ctx, tx, u.ID)
		}
		if recent >= lockoutThreshold {
			if _, err := tx.ExecContext(ctx, `UPDATE users SET failed_login_count = ?, locked_until = ?, updated_at = ? WHERE id = ?`,
				recent, now+lockoutBase.Milliseconds(), now, u.ID); err != nil {
				return err
			}
			if err := killPendingSessions(ctx, tx, u.ID); err != nil {
				return err
			}
			s.recordAudit(ctx, AuditEvent{Actor: u.ID, Action: "lockout.lock", Target: "user:" + u.Name, Result: "ok"})
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE users SET failed_login_count = ?, updated_at = ? WHERE id = ?`, recent, now, u.ID)
		return err
	})
}

func killPendingSessions(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?
		AND auth_level IN ('password-pending', 'oidc-pending')`, userID)
	return err
}

func (s *Service) applyLockout(ctx context.Context, u User, now int64) {
	_ = s.recordFailure(ctx, u.LowerName, u, "", now)
}

func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next, ip string) error {
	var hash string
	var u User
	var err error
	u, hash, err = s.userByNameID(ctx, userID)
	if err != nil {
		return err
	}
	ok, verr := VerifyPassword(current, hash)
	if verr != nil || !ok {
		return errInvalidCredentials
	}
	if policyErr := CheckPasswordPolicy(next, u.Name); policyErr != nil {
		return policyErr
	}
	fresh, err := HashPassword(next)
	if err != nil {
		return err
	}
	now := millis(s.clock())
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ?,
			must_change_password = 0, failed_login_count = 0, locked_until = NULL, updated_at = ? WHERE id = ?`,
			fresh, now, now, userID); txErr != nil {
			return txErr
		}
		_, txErr := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		return txErr
	})
	if err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "password.change", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}

func (s *Service) userByNameID(ctx context.Context, userID int64) (User, string, error) {
	var u User
	var hash string
	var mustChange, totp, oidc, admin, active int64
	var locked sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT `+userColumns+`, password_hash FROM users WHERE id = ?`, userID).Scan(
		&u.ID, &u.Name, &u.LowerName, &u.Email, &u.FullName,
		&u.PasswordAlgo, &mustChange, &totp, &oidc, &admin, &active,
		&u.FailedLoginCount, &locked, &u.PasswordChangedAt, &u.CreatedAt, &u.UpdatedAt, &hash)
	if err != nil {
		return User{}, "", err
	}
	u.MustChangePassword = mustChange != 0
	u.TOTPEnabled = totp != 0
	u.OIDCOnly = oidc != 0
	u.IsAdmin = admin != 0
	u.IsActive = active != 0
	if locked.Valid {
		u.LockedUntil = locked.Int64
	}
	return u, hash, nil
}

func (s *Service) ETagFor(userID int64) (string, error) {
	var updated int64
	err := s.db.QueryRowContext(context.Background(), `SELECT updated_at FROM users WHERE id = ?`, userID).Scan(&updated)
	if err != nil {
		return "", err
	}
	return quoteETag(userID, updated), nil
}

func quoteETag(userID, updated int64) string {
	return itoa64(userID) + `-` + itoa64(updated)
}

func stripETag(tag string) string {
	trimmed := tag
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\t') {
		trimmed = trimmed[1:]
	}
	for len(trimmed) > 0 && (trimmed[len(trimmed)-1] == ' ' || trimmed[len(trimmed)-1] == '\t') {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		trimmed = trimmed[1 : len(trimmed)-1]
	}
	return trimmed
}

func etagMatch(header, expected string) bool {
	return stripETag(header) == expected
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

var errStale = staleError{}

type staleError struct{}

func (staleError) Error() string {
	return "representation changed"
}
