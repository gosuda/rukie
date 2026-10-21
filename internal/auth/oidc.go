package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gosuda/rukie/internal/store"
	"golang.org/x/oauth2"
)

const (
	oidcStateBytes     = 32
	oidcVerifierChars  = 64
	oidcStateTTL       = 10 * time.Minute
	oidcBadStateLimit  = 20
	oidcBadStateWindow = 10 * time.Minute
	oidcBlockTTL       = time.Hour
	oidcBrowserBytes   = 32
	oidcBrowserCookie  = "__Host-rukie_oidc"
)

var oidcVerifierAlphabet = []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")

type OIDCProviderConfig struct {
	ID           string
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
	AllowSignup  bool
}

type oidcRuntime struct {
	provider *oidc.Provider
	oauth    *oauth2.Config
	config   OIDCProviderConfig
}

type StartOIDCLoginInput struct {
	ProviderID string
	ReturnTo   string
	IP         string
	LinkUserID int64
}

type OIDCCallbackInput struct {
	ProviderID    string
	Code          string
	State         string
	IP            string
	UserAgent     string
	BrowserSecret string
	SessionSecret string
	SessionUserID int64
	SessionFresh  bool
}

type OIDCCallbackResult struct {
	Secret   string
	ReturnTo string
	User     User
	Session  Session
	IsNew    bool
	NeedTOTP bool
	Linked   bool
}

var (
	errOIDCUnknownProvider = errors.New("unknown oidc provider")
	errOIDCBadState        = errors.New("invalid or expired oidc state")
	errOIDCIPBlocked       = errors.New("oidc logins from this address are temporarily blocked")
	errOIDCBadToken        = errors.New("invalid identity token")
	errOIDCEmailLink       = errors.New("an account with this email exists: sign in, then link")
	errOIDCSignupDisabled  = errors.New("signup is disabled: ask an admin to link your account")
	errOIDCLinkConflict    = errors.New("this identity is linked to a different account")
)

var ErrTOTPNotEnrolled = errTOTPNotEnrolled

var ErrOIDCUnknownProvider = errOIDCUnknownProvider

var ErrOIDCBadState = errOIDCBadState

var ErrOIDCIPBlocked = errOIDCIPBlocked

var ErrOIDCBadToken = errOIDCBadToken

var ErrOIDCEmailLink = errOIDCEmailLink

var ErrOIDCSignupDisabled = errOIDCSignupDisabled

var ErrOIDCLinkConflict = errOIDCLinkConflict

func (s *Service) SetOIDCProviders(ctx context.Context, cfgs []OIDCProviderConfig) error {
	runtime := make(map[string]*oidcRuntime, len(cfgs))
	for _, c := range cfgs {
		if c.ID == "" || c.Issuer == "" || c.ClientID == "" || c.RedirectURL == "" {
			return errors.New("oidc provider needs id, issuer, client_id, and redirect_url")
		}
		if !strings.HasPrefix(c.Issuer, "https://") && !strings.HasPrefix(c.Issuer, "http://127.0.0.1") &&
			!strings.HasPrefix(c.Issuer, "http://localhost") {
			return errors.New("oidc issuer must be https")
		}
		provider, err := oidc.NewProvider(ctx, c.Issuer)
		if err != nil {
			return err
		}
		scopes := append([]string{"openid"}, c.Scopes...)
		if uerr := s.store.UpsertOIDCProvider(ctx, store.OIDCProviderUpsert{
			ID:          c.ID,
			Issuer:      c.Issuer,
			ClientID:    c.ClientID,
			Scopes:      strings.Join(scopes, " "),
			AllowSignup: c.AllowSignup,
		}); uerr != nil {
			return uerr
		}
		runtime[c.ID] = &oidcRuntime{
			provider: provider,
			oauth: &oauth2.Config{
				ClientID:     c.ClientID,
				ClientSecret: c.ClientSecret,
				Endpoint:     provider.Endpoint(),
				RedirectURL:  c.RedirectURL,
				Scopes:       scopes,
			},
			config: c,
		}
	}
	s.oidcMu.Lock()
	s.oidcProviders = runtime
	s.oidcMu.Unlock()
	return nil
}

func (s *Service) oidcLookup(id string) (*oidcRuntime, bool) {
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	rt, ok := s.oidcProviders[id]
	return rt, ok
}

func (s *Service) StartOIDCLogin(ctx context.Context, in StartOIDCLoginInput) (string, string, error) {
	rt, ok := s.oidcLookup(in.ProviderID)
	if !ok {
		return "", "", errOIDCUnknownProvider
	}
	now := millis(s.clock())
	if blocked, _ := s.oidcIPBlocked(ctx, in.IP, now); blocked {
		return "", "", errOIDCIPBlocked
	}
	stateRaw := make([]byte, oidcStateBytes)
	if _, err := rand.Read(stateRaw); err != nil {
		return "", "", err
	}
	nonceRaw := make([]byte, oidcStateBytes)
	if _, err := rand.Read(nonceRaw); err != nil {
		return "", "", err
	}
	verifier := make([]byte, oidcVerifierChars)
	if _, err := rand.Read(verifier); err != nil {
		return "", "", err
	}
	for i := range verifier {
		verifier[i] = oidcVerifierAlphabet[int(verifier[i])%len(oidcVerifierAlphabet)]
	}
	browserRaw := make([]byte, oidcBrowserBytes)
	if _, err := rand.Read(browserRaw); err != nil {
		return "", "", err
	}
	state := base64.RawURLEncoding.EncodeToString(stateRaw)
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	sum := sha256.Sum256([]byte(state))
	browser := base64.RawURLEncoding.EncodeToString(browserRaw)
	browserSum := sha256.Sum256([]byte(browser))
	returnTo := in.ReturnTo
	if returnTo == "" {
		returnTo = "/"
	}
	serr := s.store.CreateOIDCState(ctx, store.OIDCStateCreate{
		StateHash:   hexEncode(sum[:]),
		ProviderID:  in.ProviderID,
		Nonce:       nonce,
		Verifier:    string(verifier),
		ReturnTo:    returnTo,
		BrowserHash: hexEncode(browserSum[:]),
		LinkUserID:  in.LinkUserID,
		HasLink:     in.LinkUserID != 0,
		Now:         now,
		ExpiresAt:   now + oidcStateTTL.Milliseconds(),
	})
	if serr != nil {
		return "", "", serr
	}
	s.recordAudit(ctx, AuditEvent{Action: "oidc.start", IP: in.IP, Result: "ok"})
	return rt.oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(string(verifier)), oidc.Nonce(nonce)), browser, nil
}

func OIDCBrowserCookieName() string {
	return oidcBrowserCookie
}

func OIDCBrowserCookie(secret string) *http.Cookie {
	return &http.Cookie{
		Name:     oidcBrowserCookie,
		Value:    secret,
		Path:     "/",
		MaxAge:   int(oidcStateTTL.Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func ClearOIDCBrowserCookie() *http.Cookie {
	return &http.Cookie{
		Name:     oidcBrowserCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func hexEncode(raw []byte) string {
	return hex.EncodeToString(raw)
}

type oidcTokenClaims struct {
	Email             string `json:"email"`
	PreferredUsername string `json:"preferred_username"`
	FullName          string `json:"name"`
	AZP               string `json:"azp"`
	EmailVerified     bool   `json:"email_verified"`
}

func (s *Service) HandleOIDCCallback(ctx context.Context, in OIDCCallbackInput) (OIDCCallbackResult, error) {
	now := millis(s.clock())
	if blocked, _ := s.oidcIPBlocked(ctx, in.IP, now); blocked {
		return OIDCCallbackResult{}, errOIDCIPBlocked
	}
	rt, ok := s.oidcLookup(in.ProviderID)
	if !ok {
		return OIDCCallbackResult{}, errOIDCUnknownProvider
	}
	sum := sha256.Sum256([]byte(in.State))
	st, cerr := s.store.ConsumeOIDCState(ctx, hexEncode(sum[:]), in.ProviderID, now)
	if cerr != nil {
		_ = s.recordOIDCBadState(ctx, in.IP, now)
		s.recordAudit(ctx, AuditEvent{Action: "oidc.callback", IP: in.IP, Result: "bad-state"})
		return OIDCCallbackResult{}, errOIDCBadState
	}
	presented := sha256.Sum256([]byte(in.BrowserSecret))
	if subtle.ConstantTimeCompare([]byte(hexEncode(presented[:])), []byte(st.BrowserHash)) != 1 {
		_ = s.recordOIDCBadState(ctx, in.IP, now)
		s.recordAudit(ctx, AuditEvent{Action: "oidc.callback", IP: in.IP, Result: "bad-browser"})
		return OIDCCallbackResult{}, errOIDCBadState
	}
	exctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, terr := rt.oauth.Exchange(exctx, in.Code, oauth2.VerifierOption(st.Verifier))
	if terr != nil {
		s.recordAudit(ctx, AuditEvent{Action: "oidc.callback", IP: in.IP, Result: "exchange-failed"})
		return OIDCCallbackResult{}, errOIDCBadToken
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return OIDCCallbackResult{}, errOIDCBadToken
	}
	idToken, verr := rt.provider.Verifier(&oidc.Config{ClientID: rt.config.ClientID, SupportedSigningAlgs: []string{"RS256"}}).Verify(ctx, raw)
	if verr != nil {
		s.recordAudit(ctx, AuditEvent{Action: "oidc.callback", IP: in.IP, Result: "bad-token"})
		return OIDCCallbackResult{}, errOIDCBadToken
	}
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(st.Nonce)) != 1 {
		s.recordAudit(ctx, AuditEvent{Action: "oidc.callback", IP: in.IP, Result: "bad-nonce"})
		return OIDCCallbackResult{}, errOIDCBadToken
	}
	var claims oidcTokenClaims
	if clerr := idToken.Claims(&claims); clerr != nil {
		return OIDCCallbackResult{}, errOIDCBadToken
	}
	if claims.AZP != "" && claims.AZP != rt.config.ClientID {
		return OIDCCallbackResult{}, errOIDCBadToken
	}
	var current User
	var haveSession bool
	var sess Session
	var serr error
	if in.SessionSecret != "" {
		sess, current, serr = s.LookupSession(ctx, in.SessionSecret)
		if serr != nil {
			return OIDCCallbackResult{}, errInvalidCredentials
		}
		haveSession = true
	} else if in.SessionUserID != 0 {
		current, _, serr = s.authUserByID(ctx, in.SessionUserID)
		if serr != nil {
			return OIDCCallbackResult{}, errInvalidCredentials
		}
		haveSession = true
	}
	if st.HasLink {
		if !haveSession || current.ID != st.LinkUserID {
			return OIDCCallbackResult{}, errInvalidCredentials
		}
		if lerr := s.linkIdentity(ctx, rt.config.Issuer, idToken.Subject, current.ID, now); lerr != nil {
			return OIDCCallbackResult{}, lerr
		}
		s.recordAudit(ctx, AuditEvent{Actor: current.ID, Action: "oidc.link",
			Target: "user:" + current.Name, IP: in.IP, UserAgent: in.UserAgent, Result: "ok"})
		return OIDCCallbackResult{User: current, Linked: true, ReturnTo: safeReturnTo(st.ReturnTo)}, nil
	}
	linked, err := s.userByOIDCLink(ctx, rt.config.Issuer, idToken.Subject)
	if err == nil {
		if haveSession && current.ID != linked.ID {
			return OIDCCallbackResult{}, errOIDCLinkConflict
		}
		res, lerr := s.oidcLogin(ctx, linked, in.IP, in.UserAgent, false)
		if lerr != nil {
			return OIDCCallbackResult{}, lerr
		}
		res.ReturnTo = safeReturnTo(st.ReturnTo)
		return res, nil
	}
	if haveSession {
		fresh := in.SessionFresh
		if in.SessionSecret != "" {
			fresh = SessionFresh(sess.AuthedAt, now)
		}
		if !fresh {
			return OIDCCallbackResult{}, errInvalidCredentials
		}
		if lerr := s.linkIdentity(ctx, rt.config.Issuer, idToken.Subject, current.ID, now); lerr != nil {
			return OIDCCallbackResult{}, lerr
		}
		s.recordAudit(ctx, AuditEvent{Actor: current.ID, Action: "oidc.link",
			Target: "user:" + current.Name, IP: in.IP, UserAgent: in.UserAgent, Result: "ok"})
		return OIDCCallbackResult{User: current, Linked: true, ReturnTo: safeReturnTo(st.ReturnTo)}, nil
	}
	if claims.EmailVerified && claims.Email != "" {
		if _, _, uerr := s.authUserByEmail(ctx, claims.Email); uerr == nil {
			return OIDCCallbackResult{}, errOIDCEmailLink
		}
	}
	if !rt.config.AllowSignup {
		return OIDCCallbackResult{}, errOIDCSignupDisabled
	}
	created, cerr := s.createOIDCUser(ctx, rt.config.Issuer, idToken.Subject, claims, now)
	if cerr != nil {
		return OIDCCallbackResult{}, cerr
	}
	res, lerr := s.oidcLogin(ctx, created, in.IP, in.UserAgent, true)
	if lerr != nil {
		return OIDCCallbackResult{}, lerr
	}
	res.ReturnTo = safeReturnTo(st.ReturnTo)
	return res, nil
}

func (s *Service) oidcLogin(ctx context.Context, u User, ip, userAgent string, isNew bool) (OIDCCallbackResult, error) {
	level := fullOIDCLevel
	needTOTP := false
	if u.TOTPEnabled {
		level = pendingOIDCLevel
		needTOTP = true
	}
	secret, sess, err := s.MintSession(ctx, u.ID, level, ip, userAgent)
	if err != nil {
		return OIDCCallbackResult{}, err
	}
	s.recordAudit(ctx, AuditEvent{Actor: u.ID, Action: "oidc.callback",
		Target: "user:" + u.Name, IP: ip, UserAgent: userAgent, SessionHash: sess.IDHash, Result: "ok"})
	return OIDCCallbackResult{Secret: secret, Session: sess, User: u, IsNew: isNew, NeedTOTP: needTOTP}, nil
}

func safeReturnTo(target string) string {
	if len(target) > 1 && target[0] == '/' && target[1] != '/' && !strings.Contains(target, "\\") {
		return target
	}
	return "/"
}

func (s *Service) linkIdentity(ctx context.Context, issuer, subject string, userID, now int64) error {
	owner, err := s.store.LinkOIDCIdentity(ctx, issuer, subject, userID, now)
	if err != nil {
		return err
	}
	if owner != userID {
		return errOIDCLinkConflict
	}
	return nil
}

func (s *Service) userByOIDCLink(ctx context.Context, issuer, subject string) (User, error) {
	userID, err := s.store.LookupOIDCLink(ctx, issuer, subject)
	if err != nil {
		return User{}, err
	}
	u, _, err := s.authUserByID(ctx, userID)
	return u, err
}

func (s *Service) createOIDCUser(ctx context.Context, issuer, subject string, claims oidcTokenClaims, now int64) (User, error) {
	name, err := s.freeOIDCUsername(ctx, issuer, subject, claims)
	if err != nil {
		return User{}, err
	}
	password, err := randomUnusablePassword()
	if err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	email := ""
	if claims.EmailVerified {
		email = claims.Email
	}
	fullName := claims.FullName
	if len(fullName) > 255 {
		fullName = fullName[:255]
	}
	au, cerr := s.store.CreateOIDCUser(ctx, store.OIDCUserCreate{
		Name:     name,
		Lower:    strings.ToLower(name),
		Email:    email,
		FullName: fullName,
		Hash:     hash,
		Algo:     argonAlgo,
		Issuer:   issuer,
		Subject:  subject,
		Now:      now,
	})
	if cerr != nil {
		return User{}, cerr
	}
	u, _ := authUserFromStore(au)
	s.recordAudit(ctx, AuditEvent{Actor: u.ID, Action: "oidc.callback", Target: "user:" + u.Name, Result: "ok"})
	return u, nil
}

func randomUnusablePassword() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "unusable-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

func sanitizeOIDCName(candidate string) string {
	lowered := strings.ToLower(candidate)
	var b strings.Builder
	for _, r := range lowered {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 39 {
		out = out[:39]
	}
	out = strings.Trim(out, ".-_")
	if out == "" || (out[0] >= '0' && out[0] <= '9') {
		return ""
	}
	return out
}

func fallbackOIDCName(issuer, subject string) string {
	sum := sha256.Sum256([]byte(issuer + "\x00" + subject))
	n := new(big.Int).SetBytes(sum[:])
	text := n.Text(36)
	if len(text) < 8 {
		text += strings.Repeat("0", 8-len(text))
	}
	return "oidc-" + text[:8]
}

func (s *Service) freeOIDCUsername(ctx context.Context, issuer, subject string, claims oidcTokenClaims) (string, error) {
	candidates := []string{}
	if cleaned := sanitizeOIDCName(claims.PreferredUsername); cleaned != "" {
		candidates = append(candidates, cleaned)
	}
	if at := strings.Index(claims.Email, "@"); claims.EmailVerified && at > 0 {
		if cleaned := sanitizeOIDCName(claims.Email[:at]); cleaned != "" {
			candidates = append(candidates, cleaned)
		}
	}
	candidates = append(candidates, fallbackOIDCName(issuer, subject))
	for _, base := range candidates {
		name := base
		for suffix := range 8 {
			if suffix > 0 {
				name = base + "-" + strings.Repeat("x", suffix)
				if len(name) > 39 {
					name = base[:39-suffix-1] + "-" + strings.Repeat("x", suffix)
				}
			}
			if ReservedName(name) {
				continue
			}
			if _, uerr := s.store.GetAuthUserByName(ctx, strings.ToLower(name)); uerr != nil {
				if errors.Is(uerr, store.ErrNotFound) {
					return name, nil
				}
				return "", uerr
			}
		}
	}
	return fallbackOIDCName(issuer, subject) + "-z", nil
}

func (s *Service) recordOIDCBadState(ctx context.Context, ip string, now int64) error {
	return s.store.RecordOIDCBadState(ctx, store.OIDCBadStateRecord{
		IP:           ip,
		Now:          now,
		WindowStart:  now - oidcBadStateWindow.Milliseconds(),
		PruneBefore:  now - oidcBlockTTL.Milliseconds(),
		Limit:        oidcBadStateLimit,
		BlockedUntil: now + oidcBlockTTL.Milliseconds(),
	})
}

func (s *Service) oidcIPBlocked(ctx context.Context, ip string, now int64) (bool, error) {
	until, err := s.store.GetOIDCIPBlock(ctx, ip)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if until <= now {
		_ = s.store.ClearOIDCIPBlock(ctx, ip)
		return false, nil
	}
	return true, nil
}

func (s *Service) OIDCProviders() []string {
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	out := make([]string, 0, len(s.oidcProviders))
	for id := range s.oidcProviders {
		out = append(out, id)
	}
	return out
}

func (s *Service) OIDCLinkedProviders(ctx context.Context, userID int64) ([]string, error) {
	return s.store.ListOIDCIssuers(ctx, userID)
}
