package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postSettings(t *testing.T, e *testEnv, cookie, etag, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/settings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Cookie", cookie)
	req.Header.Set("If-Match", etag)
	rec := httptest.NewRecorder()
	e.server.ServeHTTP(rec, req)
	return rec
}

func TestUserSelfUpdateReturnsUsableETag(t *testing.T) {
	e := buildEnv(t)
	pw := randomPW(t)
	createAdmin(t, e, "ada", pw)
	cookie := loginCookie(t, e, "ada", pw)

	page := doJSON(t, e, http.MethodGet, "/settings", cookie, "")
	if page.Code != http.StatusOK {
		t.Fatalf("settings page = %d: %s", page.Code, page.Body.String())
	}
	etag, _ := decodeBody(t, page)["etag"].(string)
	if etag == "" {
		t.Fatal("settings page carries no entity tag")
	}

	first := postSettings(t, e, cookie, etag, `{"full_name":"Ada Lovelace"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first update = %d: %s", first.Code, first.Body.String())
	}
	fresh, _ := decodeBody(t, first)["etag"].(string)
	if fresh == "" {
		t.Fatal("update response carries no entity tag")
	}
	if fresh == "user-ada" {
		t.Fatal("update returned the display tag instead of the concurrency tag")
	}

	second := postSettings(t, e, cookie, fresh, `{"full_name":"Ada L"}`)
	if second.Code != http.StatusCreated {
		t.Fatalf("second update with the returned tag = %d, want 201: %s", second.Code, second.Body.String())
	}
}
