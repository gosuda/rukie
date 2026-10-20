//go:build render

package api_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/auth"
)

func writeRenderPage(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile %s = %v", name, err)
	}
	t.Logf("wrote %s (%d bytes)", name, len(body))
}

func TestDumpF12Pages(t *testing.T) {
	dir := os.Getenv("RUKIE_RENDER_DIR")
	if dir == "" {
		t.Fatal("RUKIE_RENDER_DIR is empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll = %v", err)
	}
	e := buildEnv(t)
	e.svc.SetSealKey(f12SealKey())
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)
	secret := f12Enroll(t, e, cookie)
	rec := doForm(t, e, "GET", "/login/totp", cookie, "text/html,application/xhtml+xml", "")
	writeRenderPage(t, dir, "f12-login-totp.html", rec.Body.String())
	rec = doForm(t, e, "GET", "/settings/security/totp", cookie, "text/html,application/xhtml+xml", "")
	writeRenderPage(t, dir, "f12-security-totp.html", rec.Body.String())
	rec = doForm(t, e, "GET", "/settings/security", cookie, "text/html,application/xhtml+xml", "")
	writeRenderPage(t, dir, "f12-security.html", rec.Body.String())
	verify := postForm(t, e, "/login/totp", "/login/totp", cookie, "code="+f12Code(t, secret, time.Now()))
	if verify.Code != http.StatusSeeOther {
		t.Fatalf("verify = %d", verify.Code)
	}
	stub := newF12Stub(t)
	if err := e.svc.SetOIDCProviders(t.Context(), []auth.OIDCProviderConfig{{
		ID: "stub", Issuer: stub.issuer, ClientID: "rukie-test",
		RedirectURL: "http://localhost/cb", AllowSignup: true,
	}}); err != nil {
		t.Fatalf("configure: %v", err)
	}
	rec = doJSON(t, e, "GET", "/login/oidc/stub", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("start = %d", rec.Code)
	}
	startPage := doForm(t, e, "GET", "/login/oidc/stub", "", "text/html,application/xhtml+xml", "")
	writeRenderPage(t, dir, "f12-oidc-start.html", startPage.Body.String())
	started := decodeBody(t, rec)
	parsed, _ := url.Parse(started["auth_url"].(string))
	q := parsed.Query()
	code := stub.issue(q.Get("code_challenge"), q.Get("nonce"), "render-1", "renderuser")
	cb := doForm(t, e, "GET", "/login/oidc/stub/callback?code="+code+"&state="+q.Get("state"), "", "text/html,application/xhtml+xml", "")
	if cb.Code != http.StatusOK {
		t.Fatalf("callback = %d: %s", cb.Code, cb.Body.String())
	}
	writeRenderPage(t, dir, "f12-oidc-callback.html", cb.Body.String())
}
