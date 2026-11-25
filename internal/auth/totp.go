package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gosuda/rukie/internal/store"
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

func TOTPCounter(t time.Time) int64 {
	return t.UTC().Unix() / totpStepSeconds
}

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

func TOTPOTPAUTH(issuer, account, secretBase32 string) string {
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) +
		"?secret=" + secretBase32 + "&issuer=" + url.QueryEscape(issuer) +
		"&algorithm=SHA1&digits=6&period=30"
}

type TOTPEnrollment struct {
	SecretBase32 string
	URI          string
}

func (s *Service) TOTPStatus(ctx context.Context, userID int64) (enabled, pending bool, err error) {
	au, uerr := s.store.GetAuthUserByID(ctx, userID)
	if uerr != nil {
		return false, false, uerr
	}
	creds, cerr := s.store.GetTOTPCredentials(ctx, userID)
	if cerr != nil {
		if errors.Is(cerr, store.ErrNotFound) {
			return au.TOTPEnabled, false, nil
		}
		return false, false, cerr
	}
	return au.TOTPEnabled, creds.HasPending, nil
}

func (s *Service) EnrollTOTP(ctx context.Context, userID int64, ip string) (TOTPEnrollment, error) {
	u, _, err := s.authUserByID(ctx, userID)
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
	if serr := s.store.StageTOTPSecret(ctx, userID, sealed, now); serr != nil {
		return TOTPEnrollment{}, serr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "totp.enroll", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	encoded := base32NoPad.EncodeToString(raw)
	return TOTPEnrollment{SecretBase32: encoded, URI: TOTPOTPAUTH("rukie", u.Name, encoded)}, nil
}

type VerifyTOTPInput struct {
	SessionSecret string
	Code          string
	IP            string
	UserAgent     string
}

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

func (s *Service) VerifyTOTPByRowID(ctx context.Context, userID, rowid int64, code, ip, userAgent string) (VerifyTOTPResult, error) {
	sess, err := s.GetSessionByID(ctx, rowid)
	if err != nil {
		return VerifyTOTPResult{}, errInvalidCredentials
	}
	if sess.UserID != userID {
		return VerifyTOTPResult{}, errInvalidCredentials
	}
	u, _, err := s.authUserByID(ctx, userID)
	if err != nil {
		return VerifyTOTPResult{}, errInvalidCredentials
	}
	return s.verifyTOTPOnSession(ctx, sess, u, normalizeTOTPInput(code), ip, userAgent)
}

func (s *Service) verifyTOTPOnSession(ctx context.Context, sess Session, u User, code, ip, userAgent string) (VerifyTOTPResult, error) {
	creds, derr := s.store.GetTOTPCredentials(ctx, u.ID)
	if derr != nil {
		return VerifyTOTPResult{}, errTOTPNotEnrolled
	}
	now := s.clock()
	nowMillis := millis(now)
	matched, counter, promoted, recovery := s.matchSecondFactor(creds, code, now)
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
			user: u, sess: sess, creds: creds, counter: counter, code: code,
			promoted: promoted, recovery: recovery,
			ip: ip, userAgent: userAgent, nowMillis: nowMillis,
		})
	}
}

type totpMatch struct {
	code      string
	ip        string
	userAgent string
	creds     store.TOTPCredentials
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

func (s *Service) matchSecondFactor(creds store.TOTPCredentials, code string, now time.Time) (matched bool, counter int64, promoted, hasRecovery bool) {
	if len(code) == totpDigits {
		if creds.HasPending {
			if pending, perr := s.unseal(creds.Pending); perr == nil {
				if c, ok := TOTPCheck(pending, code, now, creds.LastCounter); ok {
					return true, c, true, false
				}
			}
		}
		if current, cerr := s.unseal(creds.Secret); cerr == nil {
			if c, ok := TOTPCheck(current, code, now, creds.LastCounter); ok {
				return true, c, false, false
			}
		}
		return false, 0, false, false
	}
	if !creds.HasVerified {
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
	for range 5 {
		out := VerifyTOTPResult{Session: m.sess, RecoveryUsed: m.recovery}
		var next store.SessionCreate
		if !m.sess.TOTPOk {
			secret, secretErr := newSecret()
			if secretErr != nil {
				return VerifyTOTPResult{}, fmt.Errorf("applyTOTPMatch: %w", secretErr)
			}
			csrf, csrfErr := newSecret()
			if csrfErr != nil {
				return VerifyTOTPResult{}, fmt.Errorf("applyTOTPMatch: %w", csrfErr)
			}
			out.Secret = secret
			out.Session = Session{
				IDHash:            hashBearer(secret),
				UserID:            m.sess.UserID,
				AuthLevel:         fullLevelFor(m.sess.AuthLevel),
				AuthedAt:          m.nowMillis,
				CreatedAt:         m.nowMillis,
				LastSeenAt:        m.nowMillis,
				AbsoluteExpiresAt: m.sess.AbsoluteExpiresAt,
				TOTPOk:            true,
			}
			next = store.SessionCreate{
				IDHash:            out.Session.IDHash,
				AuthLevel:         out.Session.AuthLevel,
				CSRF:              csrf,
				IP:                m.ip,
				UserAgent:         m.userAgent,
				UserID:            out.Session.UserID,
				AuthedAt:          m.nowMillis,
				CreatedAt:         m.nowMillis,
				LastSeenAt:        m.nowMillis,
				AbsoluteExpiresAt: out.Session.AbsoluteExpiresAt,
				TOTPOk:            true,
			}
		}
		var digests []string
		if !m.recovery && (m.promoted || !m.creds.HasVerified) {
			displays, hashes, codesErr := newRecoveryCodes()
			if codesErr != nil {
				return VerifyTOTPResult{}, fmt.Errorf("applyTOTPMatch: %w", codesErr)
			}
			out.RecoveryCodes, digests = displays, hashes
		}
		err := s.store.Write(ctx, func(tx *store.WriteTx) error {
			return commitTOTPMatch(tx, m, next, digests, millis(s.clock()))
		})
		if errors.Is(err, store.ErrConflict) {
			continue
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				err = errInvalidCredentials
			}
			return VerifyTOTPResult{}, fmt.Errorf("applyTOTPMatch: %w", err)
		}
		if !m.sess.TOTPOk {
			s.recordAudit(ctx, AuditEvent{Actor: m.user.ID, Action: "session.rotate", IP: m.ip, UserAgent: m.userAgent,
				SessionHash: out.Session.IDHash, Result: "ok"})
		}
		action := "totp.verify"
		if m.recovery {
			action = "totp.recovery_used"
		}
		s.recordAudit(ctx, AuditEvent{Actor: m.user.ID, Action: action, Target: "user:" + m.user.Name,
			IP: m.ip, UserAgent: m.userAgent, SessionHash: out.Session.IDHash, Result: "ok"})
		return out, nil
	}
	return VerifyTOTPResult{}, fmt.Errorf("applyTOTPMatch: %w", errInvalidCredentials)
}

func commitTOTPMatch(tx *store.WriteTx, m totpMatch, next store.SessionCreate, digests []string, nowMillis int64) error {
	current, err := tx.GetSessionByHash(m.sess.IDHash)
	if err != nil {
		return fmt.Errorf("commitTOTPMatch: %w", err)
	}
	if current.UserID != m.user.ID || nowMillis >= current.AbsoluteExpiresAt ||
		nowMillis-current.LastSeenAt >= sessionIdleTTL.Milliseconds() {
		return fmt.Errorf("commitTOTPMatch: %w", errInvalidCredentials)
	}
	creds, err := tx.GetTOTPCredentials(m.user.ID)
	if err != nil {
		return fmt.Errorf("commitTOTPMatch: %w", err)
	}
	if creds != m.creds {
		return fmt.Errorf("commitTOTPMatch: %w", errInvalidCredentials)
	}
	var applied bool
	switch {
	case m.recovery:
		digest := sha256.Sum256([]byte(m.code))
		applied, err = tx.VerifyRecoveryCode(m.user.ID, hex.EncodeToString(digest[:]), m.nowMillis, m.user.LowerName, m.ip)
	case m.promoted:
		applied, err = tx.PromoteTOTPSecret(m.user.ID, m.counter, m.nowMillis, m.user.LowerName, m.ip, digests)
	case !m.creds.HasVerified:
		applied, err = tx.ConfirmTOTPSecret(m.user.ID, m.counter, m.nowMillis, m.user.LowerName, m.ip, digests)
	default:
		applied, err = tx.AdvanceTOTPCounter(m.user.ID, m.counter, m.nowMillis, m.user.LowerName, m.ip)
	}
	if err != nil {
		return fmt.Errorf("commitTOTPMatch: %w", err)
	}
	if !applied {
		return fmt.Errorf("commitTOTPMatch: %w", errInvalidCredentials)
	}
	if !m.sess.TOTPOk {
		if rotateErr := tx.RotateSession(m.sess.IDHash, next); rotateErr != nil {
			return fmt.Errorf("commitTOTPMatch: %w", rotateErr)
		}
	}
	return nil
}

func (s *Service) DisableTOTP(ctx context.Context, userID int64, ip string) error {
	u, _, err := s.authUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if cerr := s.store.ClearTOTP(ctx, userID, millis(s.clock())); cerr != nil {
		return cerr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "totp.disable", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}

func (s *Service) RegenerateRecoveryCodes(ctx context.Context, userID int64, ip string) ([]string, error) {
	u, _, err := s.authUserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !u.TOTPEnabled {
		return nil, errTOTPNotEnrolled
	}
	now := millis(s.clock())
	var codes []string
	for range 5 {
		displays, digests, gerr := newRecoveryCodes()
		if gerr != nil {
			return nil, gerr
		}
		if rerr := s.store.ReplaceRecoveryCodes(ctx, userID, now, digests); rerr != nil {
			if errors.Is(rerr, store.ErrConflict) {
				continue
			}
			return nil, rerr
		}
		codes = displays
		break
	}
	if codes == nil {
		return nil, errInvalidCredentials
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "totp.recovery_regenerate", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return codes, nil
}

func (s *Service) TOTPReset(ctx context.Context, actor int64, target string, revokeSessions bool, ip string) error {
	u, _, err := s.authUserByName(ctx, strings.ToLower(target))
	if err != nil {
		return errors.New("unknown user")
	}
	now := millis(s.clock())
	var cerr error
	if revokeSessions {
		cerr = s.store.ClearTOTPRevokingSessions(ctx, u.ID, now)
	} else {
		cerr = s.store.ClearTOTP(ctx, u.ID, now)
	}
	if cerr != nil {
		return cerr
	}
	s.recordAudit(ctx, AuditEvent{Actor: actor, Action: "admin.totp_reset", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}

func newRecoveryCodes() ([]string, []string, error) {
	displays := make([]string, 0, recoveryCodeCount)
	digests := make([]string, 0, recoveryCodeCount)
	seen := map[string]bool{}
	for len(displays) < recoveryCodeCount {
		raw := make([]byte, recoveryCodeBytes)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		display := formatRecoveryCode(crockfordEncoding.EncodeToString(raw))
		plain := strings.ReplaceAll(display, "-", "")
		digest := sha256.Sum256([]byte(plain))
		encoded := hex.EncodeToString(digest[:])
		if seen[encoded] {
			continue
		}
		seen[encoded] = true
		displays = append(displays, display)
		digests = append(digests, encoded)
	}
	return displays, digests, nil
}

func formatRecoveryCode(encoded string) string {
	return encoded[0:4] + "-" + encoded[4:8] + "-" + encoded[8:12] + "-" + encoded[12:16]
}

func (s *Service) SessionRestricted(sess Session, u User) bool {
	if pendingLevel(sess.AuthLevel) {
		return true
	}
	return u.TOTPEnabled && !sess.TOTPOk
}

func pendingLevel(level string) bool {
	return level == pendingPasswordLevel || level == pendingOIDCLevel
}

func fullLevelFor(pending string) string {
	if pending == pendingOIDCLevel {
		return fullOIDCLevel
	}
	return fullPasswordLevel
}

func (s *Service) totpFailBudget(ctx context.Context, u User, sess Session) (destroyed bool, err error) {
	recent, err := s.store.CountRecentTOTPFailures(ctx, u.LowerName, sess.CreatedAt)
	if err != nil {
		return false, err
	}
	if recent < totpFailLimit {
		return false, nil
	}
	if _, derr := s.store.DeleteSessionByHash(ctx, sess.IDHash); derr != nil {
		return false, derr
	}
	s.recordAudit(ctx, AuditEvent{Actor: u.ID, Action: "totp.lockout", Target: "user:" + u.Name, Result: "ok"})
	return true, nil
}
