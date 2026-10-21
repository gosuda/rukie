package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func loginCookieWithUA(t *testing.T, e *testEnv, username, password, ua string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", ua)
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body.String())
	}
	return strings.TrimSpace(strings.Split(rec.Header().Get("Set-Cookie"), ";")[0])
}

func TestLoginKeepsBothBrowserSessions(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookieA := loginCookieWithUA(t, e, "ada", pw, "browser-a/1.0")
	cookieB := loginCookieWithUA(t, e, "ada", pw, "browser-b/1.0")
	for _, cookie := range []string{cookieA, cookieB} {
		if rec := doJSON(t, e, "GET", "/settings/tokens", cookie, ""); rec.Code != http.StatusOK {
			t.Fatalf("session probe = %d: %s", rec.Code, rec.Body.String())
		}
	}
}
