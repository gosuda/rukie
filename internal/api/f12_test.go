package api_test

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

func f12SealKey() []byte {
	return []byte("f12-test-seal-key-0123456789abcd")
}

func f12Enroll(t *testing.T, e *testEnv, cookie string) (secret string) {
	t.Helper()
	e.svc.SetSealKey(f12SealKey())
	rec := postForm(t, e, "/settings/security/totp", "/settings/security", cookie, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("enroll = %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "/settings/security/totp?reveal=") {
		t.Fatalf("enroll location = %q", loc)
	}
	page := doForm(t, e, "GET", loc, cookie, "text/html,application/xhtml+xml", "")
	if page.Code != http.StatusOK {
		t.Fatalf("enroll page = %d", page.Code)
	}
	body := page.Body.String()
	if strings.Contains(strings.ToLower(body), "qr") || strings.Contains(body, "<img") {
		t.Fatal("enrollment page carries a QR image")
	}
	idx := strings.Index(body, "Manual key: <code>")
	if idx < 0 {
		t.Fatalf("no manual key on page: %s", body)
	}
	rest := body[idx+len("Manual key: <code>"):]
	end := strings.Index(rest, "</code>")
	if end < 0 {
		t.Fatal("unterminated manual key")
	}
	if !strings.Contains(body, "otpauth://totp/") {
		t.Fatal("no otpauth URI on enrollment page")
	}
	return rest[:end]
}

func f12Code(t *testing.T, secretBase32 string, at time.Time) string {
	t.Helper()
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretBase32)
	if err != nil {
		t.Fatalf("decode secret: %v", err)
	}
	return auth.TOTPCode(raw, auth.TOTPCounter(at))
}

func TestF12EnrollVerifyDisableFlow(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	secret := f12Enroll(t, e, cookie)
	rec := postForm(t, e, "/login/totp", "/login/totp", cookie, "code="+f12Code(t, secret, time.Now()))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("verify = %d: %s", rec.Code, rec.Body.String())
	}
	pendingLogin := doJSON(t, e, "POST", "/login", "", `{"username":"ada","password":"`+pw+`"}`)
	if pendingLogin.Code != http.StatusCreated && pendingLogin.Code != http.StatusOK {
		t.Fatalf("login = %d", pendingLogin.Code)
	}
	setCookie := pendingLogin.Header().Get("Set-Cookie")
	pending := strings.TrimSpace(strings.Split(setCookie, ";")[0])
	if secRec := doJSON(t, e, "GET", "/settings/security", pending, ""); secRec.Code != http.StatusForbidden {
		t.Fatalf("pending session reads security = %d, want 403", secRec.Code)
	}
	stepRec := postForm(t, e, "/login/totp", "/login/totp", pending, "code="+f12Code(t, secret, time.Now().Add(30*time.Second)))
	if stepRec.Code != http.StatusSeeOther {
		t.Fatalf("step-up = %d: %s", stepRec.Code, stepRec.Body.String())
	}
	full := strings.TrimSpace(strings.Split(stepRec.Header().Get("Set-Cookie"), ";")[0])
	if fullRec := doJSON(t, e, "GET", "/settings/security", full, ""); fullRec.Code != http.StatusOK {
		t.Fatalf("full session reads security = %d", fullRec.Code)
	}
	rec = postForm(t, e, "/settings/security/totp/disable", "/settings/security", full, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("disable = %d: %s", rec.Code, rec.Body.String())
	}
	if enabled, _, err := e.svc.TOTPStatus(t.Context(), 1); err != nil || enabled {
		t.Fatalf("enabled = %v, err = %v", enabled, err)
	}
}

func TestF12RecoveryRegenerateFlow(t *testing.T) {
	e := buildEnv(t)
	e.svc.SetSealKey(f12SealKey())
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	secret := f12Enroll(t, e, cookie)
	rec := postForm(t, e, "/login/totp", "/login/totp", cookie, "code="+f12Code(t, secret, time.Now()))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("verify = %d", rec.Code)
	}
	full := strings.TrimSpace(strings.Split(rec.Header().Get("Set-Cookie"), ";")[0])
	rec = postForm(t, e, "/settings/security/recovery", "/settings/security", full, "")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("regen = %d: %s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	page := doForm(t, e, "GET", loc, full, "text/html,application/xhtml+xml", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "<code>") {
		t.Fatalf("regen page = %d", page.Code)
	}
}

func TestF12AdminResetFlow(t *testing.T) {
	e := buildEnv(t)
	e.svc.SetSealKey(f12SealKey())
	adminPW := randomPW(t)
	createAdmin(t, e, "ada", adminPW)
	bobPW := randomPW(t)
	if _, err := e.svc.CreateUser(t.Context(), 1, auth.CreateUserInput{Name: "bob", Password: bobPW}, "127.0.0.1"); err != nil {
		t.Fatalf("create bob: %v", err)
	}
	bobCookie := loginCookie(t, e, "bob", bobPW)
	secret := f12Enroll(t, e, bobCookie)
	if rec := postForm(t, e, "/login/totp", "/login/totp", bobCookie, "code="+f12Code(t, secret, time.Now())); rec.Code != http.StatusSeeOther {
		t.Fatalf("bob verify = %d", rec.Code)
	}
	adminCookie := loginCookie(t, e, "ada", adminPW)
	rec := postForm(t, e, "/admin/users/bob/totp-reset", "/admin/users", adminCookie, "revoke_sessions=1")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("reset = %d: %s", rec.Code, rec.Body.String())
	}
	bob, err := e.svc.GetUserByID(t.Context(), 2)
	if err != nil {
		t.Fatalf("bob: %v", err)
	}
	if enabled, _, serr := e.svc.TOTPStatus(t.Context(), bob.ID); serr != nil || enabled {
		t.Fatalf("bob enabled = %v, err = %v", enabled, serr)
	}
}

type f12StubCode struct {
	challenge string
	nonce     string
	sub       string
	username  string
}

type f12Stub struct {
	t      *testing.T
	server *httptest.Server
	priv   *rsa.PrivateKey
	codes  map[string]f12StubCode
	issuer string
	mu     sync.Mutex
}

func newF12Stub(t *testing.T) *f12Stub {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa: %v", err)
	}
	stub := &f12Stub{t: t, priv: priv, codes: map[string]f12StubCode{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/op/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		raw, _ := jsonv2.Marshal(map[string]any{
			"issuer": stub.issuer, "authorization_endpoint": stub.issuer + "/auth",
			"token_endpoint": stub.issuer + "/token", "jwks_uri": stub.issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}, jsonv1.DefaultOptionsV1())
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("/op/jwks", func(w http.ResponseWriter, _ *http.Request) {
		pub := stub.priv.Public().(*rsa.PublicKey)
		w.Header().Set("Content-Type", "application/json")
		raw, _ := jsonv2.Marshal(map[string]any{"keys": []any{map[string]any{
			"kty": "RSA", "kid": "k", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString([]byte{1, 0, 1}),
		}}}, jsonv1.DefaultOptionsV1())
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("/op/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		user, _, basic := r.BasicAuth()
		clientID := r.Form.Get("client_id")
		if basic {
			clientID = user
		}
		code := r.Form.Get("code")
		stub.mu.Lock()
		binding, ok := stub.codes[code]
		stub.mu.Unlock()
		if !ok || clientID != "rukie-test" {
			w.WriteHeader(http.StatusBadRequest)
			raw, _ := jsonv2.Marshal(map[string]string{"error": "invalid_grant"}, jsonv1.DefaultOptionsV1())
			_, _ = w.Write(raw)
			return
		}
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != binding.challenge {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		stub.mu.Lock()
		delete(stub.codes, code)
		stub.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		header, _ := jsonv2.Marshal(map[string]string{"alg": "RS256", "kid": "k", "typ": "JWT"}, jsonv1.DefaultOptionsV1())
		payload, _ := jsonv2.Marshal(map[string]any{
			"iss": stub.issuer, "sub": binding.sub, "aud": "rukie-test",
			"exp": time.Now().Add(5 * time.Minute).Unix(), "iat": time.Now().Unix(),
			"nonce": binding.nonce, "preferred_username": binding.username,
			"email": binding.username + "@example.com", "email_verified": true,
		}, jsonv1.DefaultOptionsV1())
		segments := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
		digest := sha256.Sum256([]byte(segments))
		sig, serr := rsa.SignPKCS1v15(rand.Reader, stub.priv, crypto.SHA256, digest[:])
		if serr != nil {
			t.Errorf("sign: %v", serr)
			return
		}
		raw, _ := jsonv2.Marshal(map[string]any{
			"access_token": "x", "token_type": "Bearer",
			"id_token": segments + "." + base64.RawURLEncoding.EncodeToString(sig),
		}, jsonv1.DefaultOptionsV1())
		_, _ = w.Write(raw)
	})
	stub.server = httptest.NewServer(mux)
	t.Cleanup(stub.server.Close)
	stub.issuer = stub.server.URL + "/op"
	return stub
}

func (s *f12Stub) issue(challenge, nonce, sub, username string) string {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw)
	code := base64.RawURLEncoding.EncodeToString(raw)
	s.mu.Lock()
	s.codes[code] = f12StubCode{challenge: challenge, nonce: nonce, sub: sub, username: username}
	s.mu.Unlock()
	return code
}

func TestF12OIDCFlowHTTP(t *testing.T) {
	e := buildEnv(t)
	stub := newF12Stub(t)
	if err := e.svc.SetOIDCProviders(t.Context(), []auth.OIDCProviderConfig{{
		ID: "stub", Issuer: stub.issuer, ClientID: "rukie-test",
		RedirectURL: "http://localhost/cb", AllowSignup: true,
	}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	rec := doJSON(t, e, "GET", "/login/oidc/stub", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d: %s", rec.Code, rec.Body.String())
	}
	started := decodeBody(t, rec)
	authURL, _ := started["auth_url"].(string)
	parsed, _ := url.Parse(authURL)
	q := parsed.Query()
	code := stub.issue(q.Get("code_challenge"), q.Get("nonce"), "web-1", "webuser")
	cb := doJSON(t, e, "GET", "/login/oidc/stub/callback?code="+code+"&state="+q.Get("state"), "", "")
	if cb.Code != http.StatusOK {
		t.Fatalf("callback = %d: %s", cb.Code, cb.Body.String())
	}
	done := decodeBody(t, cb)
	if done["username"] != "webuser" {
		t.Fatalf("callback body = %s", cb.Body.String())
	}
	setCookie := cb.Header().Get("Set-Cookie")
	cookie := strings.TrimSpace(strings.Split(setCookie, ";")[0])
	if rec := doJSON(t, e, "GET", "/settings/security", cookie, ""); rec.Code != http.StatusOK {
		t.Fatalf("oidc session reads security = %d", rec.Code)
	}
}

func TestF12PagesZeroJS(t *testing.T) {
	e := buildEnv(t)
	e.svc.SetSealKey(f12SealKey())
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	saved := map[string]string{}
	for _, target := range []string{"/login/totp", "/settings/security/totp", "/settings/security"} {
		rec := doForm(t, e, "GET", target, cookie, "text/html,application/xhtml+xml", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", target, rec.Code)
		}
		if err := zerojs.Assert(rec.Body.Bytes()); err != nil {
			t.Fatalf("GET %s zero-JS: %v", target, err)
		}
		saved[target] = rec.Body.String()
	}
	for target, body := range saved {
		if !strings.Contains(body, "fonts.bunny.net") {
			t.Fatalf("%s missing font link", target)
		}
	}
}
