package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

type stubCode struct {
	challenge     string
	sub           string
	username      string
	email         string
	nonce         string
	emailVerified bool
}

type stubOP struct {
	t      *testing.T
	server *httptest.Server
	priv   *rsa.PrivateKey
	codes  map[string]stubCode
	issuer string
	kid    string
	mu     sync.Mutex
}

func b64uint(n *big.Int) string {
	return base64.RawURLEncoding.EncodeToString(n.Bytes())
}

func newStubOP(t *testing.T) *stubOP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	op := &stubOP{t: t, priv: priv, kid: "stub-1", codes: map[string]stubCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/op/.well-known/openid-configuration", op.discovery)
	mux.HandleFunc("/op/jwks", op.jwks)
	mux.HandleFunc("/op/token", op.token)
	op.server = httptest.NewServer(mux)
	t.Cleanup(op.server.Close)
	op.issuer = op.server.URL + "/op"
	return op
}

func (o *stubOP) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		o.t.Errorf("encode: %v", err)
	}
}

func (o *stubOP) discovery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		o.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	o.writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                o.issuer,
		"authorization_endpoint":                o.issuer + "/auth",
		"token_endpoint":                        o.issuer + "/token",
		"jwks_uri":                              o.issuer + "/jwks",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (o *stubOP) jwks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		o.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	pub := o.priv.Public().(*rsa.PublicKey)
	o.writeJSON(w, http.StatusOK, map[string]any{"keys": []any{map[string]any{
		"kty": "RSA", "kid": o.kid, "use": "sig", "alg": "RS256",
		"n": b64uint(pub.N), "e": b64uint(big.NewInt(int64(pub.E))),
	}}})
}

func (o *stubOP) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		o.writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if err := r.ParseForm(); err != nil {
		o.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if r.Form.Get("grant_type") != "authorization_code" {
		o.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	code := r.Form.Get("code")
	username, password, hasBasic := r.BasicAuth()
	clientID := r.Form.Get("client_id")
	if hasBasic {
		clientID = username
		_ = password
	}
	o.mu.Lock()
	binding, ok := o.codes[code]
	o.mu.Unlock()
	if !ok {
		o.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	if clientID != "rukie-test" || r.Form.Get("redirect_uri") != "http://localhost/cb" {
		o.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_client"})
		return
	}
	sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != binding.challenge {
		o.writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	o.mu.Lock()
	delete(o.codes, code)
	o.mu.Unlock()
	o.writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "stub-access",
		"token_type":   "Bearer",
		"expires_in":   300,
		"id_token":     o.mintIDToken(binding),
	})
}

func (o *stubOP) mintIDToken(b stubCode) string {
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": o.kid, "typ": "JWT"})
	now := time.Now()
	payload, _ := json.Marshal(map[string]any{
		"iss": o.issuer, "sub": b.sub, "aud": "rukie-test",
		"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(),
		"nonce": b.nonce, "preferred_username": b.username,
		"email": b.email, "email_verified": b.emailVerified, "name": "Stub User",
	})
	segments := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(segments))
	sig, err := rsa.SignPKCS1v15(rand.Reader, o.priv, crypto.SHA256, digest[:])
	if err != nil {
		o.t.Fatalf("sign: %v", err)
	}
	return segments + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (o *stubOP) issueCode(challenge, nonce, sub, username, email string, verified bool) string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		o.t.Fatalf("rand: %v", err)
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	o.mu.Lock()
	o.codes[code] = stubCode{challenge: challenge, sub: sub, username: username, email: email, nonce: nonce, emailVerified: verified}
	o.mu.Unlock()
	return code
}

func startStubLogin(t *testing.T, svc *Service, linkUser int64) (state, challenge, nonce string) {
	t.Helper()
	redirect, err := svc.StartOIDCLogin(t.Context(), StartOIDCLoginInput{ProviderID: "stub", ReturnTo: "/", IP: "127.0.0.1", LinkUserID: linkUser})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	parsed, err := url.Parse(redirect)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	q := parsed.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("redirect params = %v", q)
	}
	if q.Get("code_challenge") == "" || q.Get("nonce") == "" || q.Get("state") == "" {
		t.Fatalf("redirect missing PKCE/nonce/state: %v", q)
	}
	if _, err := base64.RawURLEncoding.DecodeString(q.Get("nonce")); err != nil {
		t.Fatalf("nonce encoding: %v", err)
	}
	return q.Get("state"), q.Get("code_challenge"), q.Get("nonce")
}

func configureStub(t *testing.T, svc *Service, op *stubOP, allowSignup bool) {
	t.Helper()
	if err := svc.SetOIDCProviders(t.Context(), []OIDCProviderConfig{{
		ID: "stub", Issuer: op.issuer, ClientID: "rukie-test", ClientSecret: "stub-secret",
		RedirectURL: "http://localhost/cb", AllowSignup: allowSignup,
	}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
}

func TestOIDCStubProviderLogin(t *testing.T) {
	svc, _ := openTestService(t)
	svc.SetSealKey(testSealKey())
	op := newStubOP(t)
	configureStub(t, svc, op, true)
	state, challenge, nonce := startStubLogin(t, svc, 0)
	code := op.issueCode(challenge, nonce, "user-1", "oidcuser", "oidcuser@example.com", true)
	res, err := svc.HandleOIDCCallback(t.Context(), OIDCCallbackInput{
		ProviderID: "stub", Code: code, State: state, IP: "127.0.0.1", UserAgent: "test",
	})
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	if !res.IsNew || res.NeedTOTP || res.Secret == "" {
		t.Fatalf("login result = %+v", res)
	}
	if res.User.Name != "oidcuser" || !res.User.OIDCOnly || !res.User.IsActive {
		t.Fatalf("user = %+v", res.User)
	}
	if _, _, serr := svc.LookupSession(t.Context(), res.Secret); serr != nil {
		t.Fatalf("session missing: %v", serr)
	}
	linked, err := svc.OIDCLinkedProviders(t.Context(), res.User.ID)
	if err != nil || len(linked) != 1 || linked[0] != op.issuer {
		t.Fatalf("links = %v, err = %v", linked, err)
	}
	state2, challenge2, nonce2 := startStubLogin(t, svc, 0)
	code2 := op.issueCode(challenge2, nonce2, "user-1", "oidcuser", "oidcuser@example.com", true)
	res2, err := svc.HandleOIDCCallback(t.Context(), OIDCCallbackInput{
		ProviderID: "stub", Code: code2, State: state2, IP: "127.0.0.1", UserAgent: "test",
	})
	if err != nil {
		t.Fatalf("second callback: %v", err)
	}
	if res2.IsNew || res2.User.ID != res.User.ID {
		t.Fatalf("returning login = %+v", res2)
	}
	bob := mustCreateWithPassword(t, svc, "bob", false)
	bobLogin, err := svc.Login(t.Context(), LoginInput{Username: "bob", Password: testPasswords["bob"], IP: "127.0.0.1"})
	if err != nil {
		t.Fatalf("bob login: %v", err)
	}
	state3, challenge3, nonce3 := startStubLogin(t, svc, bob.ID)
	code3 := op.issueCode(challenge3, nonce3, "user-9", "boboidc", "bob@example.com", true)
	linkRes, err := svc.HandleOIDCCallback(t.Context(), OIDCCallbackInput{
		ProviderID: "stub", Code: code3, State: state3, IP: "127.0.0.1",
		UserAgent: "test", SessionSecret: bobLogin.Secret,
	})
	if err != nil {
		t.Fatalf("link callback: %v", err)
	}
	if !linkRes.Linked || linkRes.Secret != "" {
		t.Fatalf("link result = %+v", linkRes)
	}
	linked, err = svc.OIDCLinkedProviders(t.Context(), bob.ID)
	if err != nil || len(linked) != 1 {
		t.Fatalf("bob links = %v, err = %v", linked, err)
	}
}

func TestOIDCPKCEAndNonce(t *testing.T) {
	svc, _ := openTestService(t)
	svc.SetSealKey(testSealKey())
	op := newStubOP(t)
	configureStub(t, svc, op, true)
	state, _, _ := startStubLogin(t, svc, 0)
	foreign := oauth2.S256ChallengeFromVerifier(strings.Repeat("z", 64))
	badCode := op.issueCode(foreign, "irrelevant", "user-x", "mallory", "m@example.com", true)
	if _, err := svc.HandleOIDCCallback(t.Context(), OIDCCallbackInput{
		ProviderID: "stub", Code: badCode, State: state, IP: "127.0.0.1", UserAgent: "test",
	}); !errors.Is(err, errOIDCBadToken) {
		t.Fatalf("wrong verifier err = %v", err)
	}
	if _, err := svc.userByOIDCLink(t.Context(), op.issuer, "user-x"); err == nil {
		t.Fatal("user row created from a failed exchange")
	}
	state2, challenge2, _ := startStubLogin(t, svc, 0)
	tampered := op.issueCode(challenge2, "tampered-nonce", "user-y", "mallory2", "m2@example.com", true)
	if _, err := svc.HandleOIDCCallback(t.Context(), OIDCCallbackInput{
		ProviderID: "stub", Code: tampered, State: state2, IP: "127.0.0.1", UserAgent: "test",
	}); !errors.Is(err, errOIDCBadToken) {
		t.Fatalf("wrong nonce err = %v", err)
	}
	if _, err := svc.userByOIDCLink(t.Context(), op.issuer, "user-y"); err == nil {
		t.Fatal("user row created from a forged nonce")
	}
}

func TestOIDCStateAbuseBlock(t *testing.T) {
	svc, setNow := openTestService(t)
	svc.SetSealKey(testSealKey())
	op := newStubOP(t)
	configureStub(t, svc, op, true)
	now := time.Now()
	setNow(now)
	bad := OIDCCallbackInput{ProviderID: "stub", Code: "nope", State: "bogus-state", IP: "10.9.8.7", UserAgent: "test"}
	for i := range oidcBadStateLimit - 1 {
		if _, err := svc.HandleOIDCCallback(t.Context(), bad); !errors.Is(err, errOIDCBadState) {
			t.Fatalf("attempt %d err = %v", i+1, err)
		}
	}
	if _, err := svc.StartOIDCLogin(t.Context(), StartOIDCLoginInput{ProviderID: "stub", IP: "10.9.8.7"}); err != nil {
		t.Fatalf("start at N-1 blocked: %v", err)
	}
	if _, err := svc.HandleOIDCCallback(t.Context(), bad); !errors.Is(err, errOIDCBadState) {
		t.Fatalf("attempt N err = %v", err)
	}
	if _, err := svc.HandleOIDCCallback(t.Context(), bad); !errors.Is(err, errOIDCIPBlocked) {
		t.Fatalf("attempt N+1 err = %v, want blocked", err)
	}
	if _, err := svc.StartOIDCLogin(t.Context(), StartOIDCLoginInput{ProviderID: "stub", IP: "10.9.8.7"}); !errors.Is(err, errOIDCIPBlocked) {
		t.Fatalf("start while blocked err = %v", err)
	}
	setNow(now.Add(oidcBlockTTL + time.Minute))
	if _, err := svc.HandleOIDCCallback(t.Context(), bad); !errors.Is(err, errOIDCBadState) {
		t.Fatalf("after block expiry err = %v, want bad-state", err)
	}
	if _, err := svc.StartOIDCLogin(t.Context(), StartOIDCLoginInput{ProviderID: "stub", IP: "10.9.8.7"}); err != nil {
		t.Fatalf("start after block expiry: %v", err)
	}
	windowBad := OIDCCallbackInput{ProviderID: "stub", Code: "nope", State: "bogus-state", IP: "10.9.8.9", UserAgent: "test"}
	setNow(now.Add(2 * time.Hour))
	for i := range oidcBadStateLimit - 1 {
		if _, err := svc.HandleOIDCCallback(t.Context(), windowBad); !errors.Is(err, errOIDCBadState) {
			t.Fatalf("window attempt %d err = %v", i+1, err)
		}
	}
	setNow(now.Add(2*time.Hour + oidcBadStateWindow + time.Minute))
	if _, err := svc.HandleOIDCCallback(t.Context(), windowBad); !errors.Is(err, errOIDCBadState) {
		t.Fatalf("after window slide err = %v, want bad-state", err)
	}
	for i := range oidcBadStateLimit - 2 {
		if _, err := svc.HandleOIDCCallback(t.Context(), windowBad); !errors.Is(err, errOIDCBadState) {
			t.Fatalf("fresh window attempt %d err = %v", i+1, err)
		}
	}
	if _, err := svc.StartOIDCLogin(t.Context(), StartOIDCLoginInput{ProviderID: "stub", IP: "10.9.8.9"}); err != nil {
		t.Fatalf("start at fresh N-1 blocked: %v", err)
	}
	if _, err := svc.HandleOIDCCallback(t.Context(), windowBad); !errors.Is(err, errOIDCBadState) {
		t.Fatalf("fresh attempt N err = %v", err)
	}
	if _, err := svc.StartOIDCLogin(t.Context(), StartOIDCLoginInput{ProviderID: "stub", IP: "10.9.8.9"}); !errors.Is(err, errOIDCIPBlocked) {
		t.Fatalf("start at fresh N+1 err = %v, want blocked", err)
	}
}
