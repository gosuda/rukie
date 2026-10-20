package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
)

const (
	totpStepSeconds   = 30
	totpDigits        = 6
	totpWindow        = 1
	totpSecretBytes   = 20
	recoveryCodeCount = 10
	recoveryCodeBytes = 10
	recoveryCodeChars = 16
	totpFailLimit     = 10
)

const (
	pendingPasswordLevel = "password-pending"
	pendingOIDCLevel     = "oidc-pending"
	fullPasswordLevel    = "password"
	fullOIDCLevel        = "oidc"
)

var crockfordEncoding = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

// TOTPCounter maps a time to its RFC 6238 counter.
func TOTPCounter(t time.Time) int64 {
	return t.UTC().Unix() / totpStepSeconds
}

// TOTPCode renders the RFC 4226 dynamic-truncation code for a counter.
func TOTPCode(secret []byte, counter int64) string {
	if counter < 0 {
		counter = 0
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := (int64(sum[offset]&0x7f) << 24) |
		(int64(sum[offset+1]) << 16) |
		(int64(sum[offset+2]) << 8) |
		int64(sum[offset+3])
	mod := int64(1)
	for range totpDigits {
		mod *= 10
	}
	code := bin % mod
	s := make([]byte, 0, totpDigits)
	for mult := mod / 10; mult > 0; mult /= 10 {
		s = append(s, byte('0'+(code/mult)%10))
	}
	return string(s)
}

// TOTPCheck accepts counters T-1, T, T+1 above lastCounter and reports the winner.
func TOTPCheck(secret []byte, code string, now time.Time, lastCounter int64) (int64, bool) {
	if len(code) != totpDigits {
		return 0, false
	}
	for i := range code {
		if code[i] < '0' || code[i] > '9' {
			return 0, false
		}
	}
	current := TOTPCounter(now)
	for _, c := range []int64{current + totpWindow, current, current - totpWindow} {
		if c < 0 || c <= lastCounter {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(TOTPCode(secret, c)), []byte(code)) == 1 {
			return c, true
		}
	}
	return 0, false
}

// TOTPOTPAUTH builds the enrollment URI shown as copyable text.
func TOTPOTPAUTH(issuer, account, secretBase32 string) string {
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) +
		"?secret=" + secretBase32 + "&issuer=" + url.QueryEscape(issuer) +
		"&algorithm=SHA1&digits=6&period=30"
}

// TOTPEnrollment carries the one-time enrollment secret.
type TOTPEnrollment struct {
	SecretBase32 string
	URI          string
}

// TOTPStatus reports whether second factor is enforced and whether a rotation is staged.
func (s *Service) TOTPStatus(ctx context.Context, userID int64) (enabled, pending bool, err error) {
	var enabledInt int64
	if err := s.db.QueryRowContext(ctx, `SELECT totp_enabled FROM users WHERE id = ?`, userID).Scan(&enabledInt); err != nil {
		return false, false, err
	}
	var pendingRaw sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT pending_secret_sealed FROM user_totp WHERE user_id = ?`, userID).Scan(&pendingRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return enabledInt != 0, false, nil
		}
		return false, false, err
	}
	return enabledInt != 0, pendingRaw.Valid && pendingRaw.String != "", nil
}

// EnrollTOTP stages a fresh secret. A verified row keeps its current secret
// until the staged one verifies, so abandoning rotation never locks the user out.
func (s *Service) EnrollTOTP(ctx context.Context, userID int64, ip string) (TOTPEnrollment, error) {
	u, _, err := s.userByNameID(ctx, userID)
	if err != nil {
		return TOTPEnrollment{}, err
	}
	raw := make([]byte, totpSecretBytes)
	if _, rerr := rand.Read(raw); rerr != nil {
		return TOTPEnrollment{}, rerr
	}
	sealed, serr := s.seal(raw)
	if serr != nil {
		return TOTPEnrollment{}, serr
	}
	now := millis(s.clock())
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		var verified sql.NullInt64
		verr := tx.QueryRowContext(ctx, `SELECT verified_at FROM user_totp WHERE user_id = ?`, userID).Scan(&verified)
		if verr != nil {
			if !errors.Is(verr, sql.ErrNoRows) {
				return verr
			}
			_, verr = tx.ExecContext(ctx, `INSERT INTO user_totp
				(user_id, secret_sealed, last_counter, created_at, verified_at)
				VALUES (?, ?, -1, ?, NULL)`, userID, sealed, now)
			return verr
		}
		if !verified.Valid {
			_, verr = tx.ExecContext(ctx, `UPDATE user_totp SET secret_sealed = ?, last_counter = -1,
				created_at = ? WHERE user_id = ?`, sealed, now, userID)
			return verr
		}
		_, verr = tx.ExecContext(ctx, `UPDATE user_totp SET pending_secret_sealed = ? WHERE user_id = ?`, sealed, userID)
		return verr
	})
	if err != nil {
		return TOTPEnrollment{}, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "totp.enroll", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	encoded := base32NoPad.EncodeToString(raw)
	return TOTPEnrollment{SecretBase32: encoded, URI: TOTPOTPAUTH("rukie", u.Name, encoded)}, nil
}

type totpRow struct {
	secretSealed  string
	pendingSealed sql.NullString
	lastCounter   int64
	verifiedAt    sql.NullInt64
}

// VerifyTOTPInput carries one second-factor attempt on a session.
type VerifyTOTPInput struct {
	SessionSecret string
	Code          string
	IP            string
	UserAgent     string
}

// VerifyTOTPResult carries the rotated session and any freshly issued codes.
type VerifyTOTPResult struct {
	Secret        string
	RecoveryCodes []string
	Session       Session
	RecoveryUsed  bool
}

var errTOTPNotEnrolled = errors.New("totp is not enrolled")

func (s *Service) VerifyTOTP(ctx context.Context, in VerifyTOTPInput) (VerifyTOTPResult, error) {
	sess, u, err := s.LookupSession(ctx, in.SessionSecret)
	if err != nil {
		return VerifyTOTPResult{}, errInvalidCredentials
	}
	return s.verifyTOTPOnSession(ctx, sess, u, normalizeTOTPInput(in.Code), in.IP, in.UserAgent)
}

// VerifyTOTPByRowID verifies a code against the session row named by a principal.
func (s *Service) VerifyTOTPByRowID(ctx context.Context, userID, rowid int64, code, ip, userAgent string) (VerifyTOTPResult, error) {
	sess, err := s.GetSessionByID(ctx, rowid)
	if err != nil {
		return VerifyTOTPResult{}, errInvalidCredentials
	}
	if sess.UserID != userID {
		return VerifyTOTPResult{}, errInvalidCredentials
	}
	u, _, err := s.userByNameID(ctx, userID)
	if err != nil {
		return VerifyTOTPResult{}, errInvalidCredentials
	}
	return s.verifyTOTPOnSession(ctx, sess, u, normalizeTOTPInput(code), ip, userAgent)
}

func (s *Service) verifyTOTPOnSession(ctx context.Context, sess Session, u User, code, ip, userAgent string) (VerifyTOTPResult, error) {
	var row totpRow
	derr := s.db.QueryRowContext(ctx, `SELECT secret_sealed, pending_secret_sealed, last_counter, verified_at
		FROM user_totp WHERE user_id = ?`, u.ID).Scan(
		&row.secretSealed, &row.pendingSealed, &row.lastCounter, &row.verifiedAt)
	if derr != nil {
		return VerifyTOTPResult{}, errTOTPNotEnrolled
	}
	now := s.clock()
	nowMillis := millis(now)
	matched, counter, promoted, recovery := s.matchSecondFactor(row, code, now)
	locked := u.LockedUntil > nowMillis
	switch {
	case !matched:
		if ferr := s.recordFailureReason(ctx, u.LowerName, u, ip, nowMillis, "totp"); ferr != nil {
			return VerifyTOTPResult{}, ferr
		}
		_, _ = s.totpFailBudget(ctx, u, sess)
		return VerifyTOTPResult{}, errInvalidCredentials
	case locked:
		_ = s.recordFailureReason(ctx, u.LowerName, u, ip, nowMillis, "totp")
		return VerifyTOTPResult{}, errInvalidCredentials
	default:
		return s.applyTOTPMatch(ctx, totpMatch{
			user: u, sess: sess, row: row, counter: counter, code: code,
			promoted: promoted, recovery: recovery,
			ip: ip, userAgent: userAgent, nowMillis: nowMillis,
		})
	}
}

type totpMatch struct {
	code      string
	ip        string
	userAgent string
	row       totpRow
	user      User
	sess      Session
	counter   int64
	nowMillis int64
	promoted  bool
	recovery  bool
}

func normalizeTOTPInput(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
}

func (s *Service) matchSecondFactor(row totpRow, code string, now time.Time) (matched bool, counter int64, promoted, hasRecovery bool) {
	if len(code) == totpDigits {
		if row.pendingSealed.Valid && row.pendingSealed.String != "" {
			if pending, perr := s.unseal(row.pendingSealed.String); perr == nil {
				if c, ok := TOTPCheck(pending, code, now, row.lastCounter); ok {
					return true, c, true, false
				}
			}
		}
		if current, cerr := s.unseal(row.secretSealed); cerr == nil {
			if c, ok := TOTPCheck(current, code, now, row.lastCounter); ok {
				return true, c, false, false
			}
		}
		return false, 0, false, false
	}
	if !row.verifiedAt.Valid {
		return false, 0, false, false
	}
	if len(code) != recoveryCodeChars {
		return false, 0, false, false
	}
	for i := range code {
		if strings.IndexByte("0123456789ABCDEFGHJKMNPQRSTVWXYZ", code[i]) < 0 {
			return false, 0, false, false
		}
	}
	return true, 0, false, true
}

func (s *Service) applyTOTPMatch(ctx context.Context, m totpMatch) (VerifyTOTPResult, error) {
	var out VerifyTOTPResult
	err := s.execTx(ctx, func(tx *sql.Tx) error {
		switch {
		case m.recovery:
			digest := sha256.Sum256([]byte(m.code))
			res, rerr := tx.ExecContext(ctx, `UPDATE recovery_codes SET used_at = ?
				WHERE user_id = ? AND code_sha256 = ? AND used_at IS NULL`,
				m.nowMillis, m.user.ID, hex.EncodeToString(digest[:]))
			if rerr != nil {
				return rerr
			}
			affected, rerr := res.RowsAffected()
			if rerr != nil {
				return rerr
			}
			if affected != 1 {
				return errInvalidCredentials
			}
			out.RecoveryUsed = true
		case m.promoted:
			var pending string
			if qerr := tx.QueryRowContext(ctx, `SELECT pending_secret_sealed FROM user_totp WHERE user_id = ?`,
				m.user.ID).Scan(&pending); qerr != nil {
				return qerr
			}
			pres, perr := tx.ExecContext(ctx, `UPDATE user_totp SET secret_sealed = ?, pending_secret_sealed = NULL,
				last_counter = ?, verified_at = ? WHERE user_id = ? AND last_counter < ?`,
				pending, m.counter, m.nowMillis, m.user.ID, m.counter)
			if perr != nil {
				return perr
			}
			if affected, perr := pres.RowsAffected(); perr != nil || affected != 1 {
				return errInvalidCredentials
			}
			if _, rerr := tx.ExecContext(ctx, `UPDATE users SET totp_enabled = 1, updated_at = ? WHERE id = ?`,
				m.nowMillis, m.user.ID); rerr != nil {
				return rerr
			}
			codes, rerr := s.issueRecoveryCodesTx(ctx, tx, m.user.ID, m.nowMillis)
			if rerr != nil {
				return rerr
			}
			out.RecoveryCodes = codes
		default:
			cres, cerr := tx.ExecContext(ctx, `UPDATE user_totp SET last_counter = ? WHERE user_id = ? AND last_counter < ?`,
				m.counter, m.user.ID, m.counter)
			if cerr != nil {
				return cerr
			}
			if affected, cerr := cres.RowsAffected(); cerr != nil || affected != 1 {
				return errInvalidCredentials
			}
			if !m.row.verifiedAt.Valid {
				if _, rerr := tx.ExecContext(ctx, `UPDATE user_totp SET verified_at = ? WHERE user_id = ?`,
					m.nowMillis, m.user.ID); rerr != nil {
					return rerr
				}
				if _, rerr := tx.ExecContext(ctx, `UPDATE users SET totp_enabled = 1, updated_at = ? WHERE id = ?`,
					m.nowMillis, m.user.ID); rerr != nil {
					return rerr
				}
				codes, rerr := s.issueRecoveryCodesTx(ctx, tx, m.user.ID, m.nowMillis)
				if rerr != nil {
					return rerr
				}
				out.RecoveryCodes = codes
			}
		}
		if _, rerr := tx.ExecContext(ctx, `INSERT INTO login_attempts (username_lower, ip, at, success, reason)
			VALUES (?, ?, ?, 1, 'totp')`, m.user.LowerName, m.ip, m.nowMillis); rerr != nil {
			return rerr
		}
		return nil
	})
	if err != nil {
		return VerifyTOTPResult{}, err
	}
	action := "totp.verify"
	if m.recovery {
		action = "totp.recovery_used"
	}
	if m.sess.TOTPOk {
		out.Session = m.sess
		s.recordAudit(ctx, AuditEvent{Actor: m.user.ID, Action: action, Target: "user:" + m.user.Name,
			IP: m.ip, UserAgent: m.userAgent, SessionHash: m.sess.IDHash, Result: "ok"})
		return out, nil
	}
	secret, stepped, serr := s.stepUpSession(ctx, m.sess, m.ip, m.userAgent)
	if serr != nil {
		return VerifyTOTPResult{}, serr
	}
	out.Secret = secret
	out.Session = stepped
	s.recordAudit(ctx, AuditEvent{Actor: m.user.ID, Action: action, Target: "user:" + m.user.Name,
		IP: m.ip, UserAgent: m.userAgent, SessionHash: stepped.IDHash, Result: "ok"})
	return out, nil
}

// DisableTOTP clears the second factor under fresh full auth.
func (s *Service) DisableTOTP(ctx context.Context, userID int64, ip string) error {
	u, _, err := s.userByNameID(ctx, userID)
	if err != nil {
		return err
	}
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM user_totp WHERE user_id = ?`, userID); txErr != nil {
			return txErr
		}
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); txErr != nil {
			return txErr
		}
		_, txErr := tx.ExecContext(ctx, `UPDATE users SET totp_enabled = 0, updated_at = ? WHERE id = ?`,
			millis(s.clock()), userID)
		return txErr
	})
	if err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "totp.disable", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}

// RegenerateRecoveryCodes invalidates every code and issues ten fresh ones.
func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID int64, ip string) ([]string, error) {
	u, _, err := s.userByNameID(ctx, userID)
	if err != nil {
		return nil, err
	}
	var enabled int64
	if serr := s.db.QueryRowContext(ctx, `SELECT totp_enabled FROM users WHERE id = ?`, userID).Scan(&enabled); serr != nil {
		return nil, serr
	}
	if enabled == 0 {
		return nil, errTOTPNotEnrolled
	}
	now := millis(s.clock())
	var codes []string
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); txErr != nil {
			return txErr
		}
		fresh, txErr := s.issueRecoveryCodesTx(ctx, tx, userID, now)
		if txErr != nil {
			return txErr
		}
		codes = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "totp.recovery_regenerate", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return codes, nil
}

// TOTPReset clears the second factor for an admin target.
func (s *Service) TOTPReset(ctx context.Context, actor int64, target string, revokeSessions bool, ip string) error {
	u, _, err := s.userByName(ctx, s.db, strings.ToLower(target))
	if err != nil {
		return errors.New("unknown user")
	}
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM user_totp WHERE user_id = ?`, u.ID); txErr != nil {
			return txErr
		}
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, u.ID); txErr != nil {
			return txErr
		}
		if _, txErr := tx.ExecContext(ctx, `UPDATE users SET totp_enabled = 0, updated_at = ? WHERE id = ?`,
			millis(s.clock()), u.ID); txErr != nil {
			return txErr
		}
		if revokeSessions {
			_, txErr := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, u.ID)
			return txErr
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.totp_reset", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}

func (s *Service) issueRecoveryCodesTx(ctx context.Context, tx *sql.Tx, userID, now int64) ([]string, error) {
	codes := make([]string, 0, recoveryCodeCount)
	for len(codes) < recoveryCodeCount {
		raw := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		display := formatRecoveryCode(crockfordEncoding.EncodeToString(raw))
		digest := sha256.Sum256([]byte(strings.ReplaceAll(display, "-", "")))
		_, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, code_sha256, created_at)
			VALUES (?, ?, ?)`, userID, hex.EncodeToString(digest[:]), now)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") {
				continue
			}
			return nil, err
		}
		codes = append(codes, display)
	}
	return codes, nil
}

func formatRecoveryCode(encoded string) string {
	return encoded[0:4] + "-" + encoded[4:8] + "-" + encoded[8:12] + "-" + encoded[12:16]
}

// SessionRestricted reports whether a session is parked on a second-factor challenge.
func (s *Service) SessionRestricted(sess Session, u User) bool {
	return u.TOTPEnabled && !sess.TOTPOk
}

func fullLevelFor(pending string) string {
	if pending == pendingOIDCLevel {
		return fullOIDCLevel
	}
	return fullPasswordLevel
}

func (s *Service) stepUpSession(ctx context.Context, old Session, ip, userAgent string) (string, Session, error) {
	secret, err := newSecret()
	if err != nil {
		return "", Session{}, err
	}
	csrf, err := newSecret()
	if err != nil {
		return "", Session{}, err
	}
	now := millis(s.clock())
	next := Session{
		IDHash:            hashBearer(secret),
		UserID:            old.UserID,
		AuthLevel:         fullLevelFor(old.AuthLevel),
		AuthedAt:          now,
		CreatedAt:         now,
		LastSeenAt:        now,
		AbsoluteExpiresAt: old.AbsoluteExpiresAt,
		TOTPOk:            true,
	}
	err = s.execTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, old.IDHash); txErr != nil {
			return txErr
		}
		_, txErr := tx.ExecContext(ctx, `INSERT INTO sessions
			(id_hash, user_id, csrf_token, auth_level, authed_at, totp_ok, created_at, last_seen_at, absolute_expires_at, created_ip, user_agent)
			VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
			next.IDHash, next.UserID, csrf, next.AuthLevel, now, now, now, next.AbsoluteExpiresAt, ip, userAgent)
		return txErr
	})
	if err != nil {
		return "", Session{}, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: old.UserID, Action: "session.rotate", IP: ip, UserAgent: userAgent,
		SessionHash: next.IDHash, Result: "ok"})
	return secret, next, nil
}

func (s *Service) totpFailBudget(ctx context.Context, u User, sess Session) (destroyed bool, err error) {
	var recent int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM login_attempts
		WHERE username_lower = ? AND reason = 'totp' AND success = 0 AND at >= ?`,
		u.LowerName, sess.CreatedAt).Scan(&recent); err != nil {
		return false, err
	}
	if recent < totpFailLimit {
		return false, nil
	}
	_, derr := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, sess.IDHash)
	if derr != nil {
		return false, derr
	}
	s.recordAudit(ctx, AuditEvent{Actor: u.ID, Action: "totp.lockout", Target: "user:" + u.Name, Result: "ok"})
	return true, nil
}
