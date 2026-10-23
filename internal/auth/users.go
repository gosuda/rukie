package auth

import (
	"context"
	"strings"

	"github.com/gosuda/rukie/internal/store"
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
	targetType, targetID, _ := strings.Cut(e.Target, ":")
	_ = s.store.RecordAudit(ctx, store.AuditRecord{
		Action:     e.Action,
		TargetType: targetType,
		TargetID:   targetID,
		IP:         e.IP,
		UserAgent:  e.UserAgent,
		Prefix:     e.SessionHash,
		Result:     e.Result,
		At:         millis(s.clock()),
		Actor:      e.Actor,
	})
}

func (s *Service) recordAttempt(ctx context.Context, usernameLower, ip string, success bool, reason string) {
	_ = s.store.RecordLoginAttempt(ctx, usernameLower, ip, millis(s.clock()), success, reason)
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
	u, hash, err := s.authUserByName(ctx, lower)
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
	newHash := ""
	if NeedsRehash(hash) {
		fresh, herr := HashPassword(in.Password)
		if herr != nil {
			return LoginResult{}, herr
		}
		newHash = fresh
		res.Rehashed = true
	}
	if serr := s.store.RecordAuthLoginSuccess(ctx, u.ID, lower, in.IP, now, newHash); serr != nil {
		return LoginResult{}, serr
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
	escalated, err := s.store.RecordAuthLoginFailure(ctx, store.AuthFailureRecord{
		Lower:       lower,
		IP:          ip,
		Reason:      reason,
		Policy:      lockoutPolicy(),
		UserID:      u.ID,
		Now:         now,
		LockedUntil: u.LockedUntil,
	})
	if err != nil {
		return err
	}
	if escalated {
		s.recordAudit(ctx, AuditEvent{Actor: u.ID, Action: "lockout.lock", Target: "user:" + u.Name, Result: "ok"})
	}
	return nil
}

func (s *Service) applyLockout(ctx context.Context, u User, now int64) {
	_ = s.recordFailure(ctx, u.LowerName, u, "", now)
}

func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next, ip string) error {
	u, hash, err := s.authUserByID(ctx, userID)
	if err != nil {
		return errInvalidCredentials
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
	if serr := s.store.ApplyPasswordChange(ctx, userID, fresh, now); serr != nil {
		return serr
	}
	s.recordAudit(ctx, AuditEvent{Actor: userID, Action: "password.change", Target: "user:" + u.Name, IP: ip, Result: "ok"})
	return nil
}

func (s *Service) ETagFor(ctx context.Context, userID int64) (string, error) {
	updated, err := s.store.AuthUserUpdatedAt(ctx, userID)
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
