package op_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
)

type tokenIDAuth struct{}

func (tokenIDAuth) Bearer(_ context.Context, secret string) (op.Principal, error) {
	switch secret {
	case "fresh-session":
		return op.Principal{
			UserID:  7,
			Kind:    op.PrincipalHuman,
			TokenID: -11,
			Grants:  op.Scopes(0).Add(op.ScopeRepoRead),
		}, nil
	case "read-token":
		return op.Principal{
			UserID:  7,
			Kind:    op.PrincipalHuman,
			TokenID: 22,
			Grants:  op.Scopes(0).Add(op.ScopeRepoRead),
		}, nil
	default:
		return op.Principal{}, op.ErrUnauthenticated
	}
}

func (tokenIDAuth) Session(context.Context, string) (op.Principal, error) {
	return op.AnonymousPrincipal(), nil
}

func TestIdempotencyReplayIsScopedToCredential(t *testing.T) {
	table := op.NewTable()
	calls := 0
	mustRegister(t, table, op.Op[jsonCreateIn, jsonCreateOut]{
		Name: "test.fresh.secret", Kind: op.KindMutation, Method: http.MethodPost, Path: "/test/fresh-secret",
		Scope: op.ScopeRepoRead, Summary: "Create fresh secret",
		Handler: func(_ context.Context, p op.Principal, _ jsonCreateIn) (jsonCreateOut, error) {
			calls++
			if p.TokenID >= 0 {
				return jsonCreateOut{}, op.Errorf(op.CodeUnauthorized, "fresh authentication required")
			}
			return jsonCreateOut{Note: "fresh-secret"}, nil
		},
	})
	srv, err := op.Mount(http.NewServeMux(), table, op.Config{
		Authenticator: tokenIDAuth{},
		Idempotency:   op.NewMemoryIdempotency(),
		CSRFKey:       testCSRFKey,
		Title:         "test",
		Version:       "0",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}

	headers := func(secret string) map[string]string {
		return map[string]string{
			"Accept":          "application/json",
			"Authorization":   "Bearer " + secret,
			"Content-Type":    "application/json",
			"Idempotency-Key": "same-key",
		}
	}
	first := doRequest(srv, http.MethodPost, "/test/fresh-secret", headers("fresh-session"), `{"note":"secret"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status %d, want 201: %s", first.Code, first.Body.String())
	}
	replay := doRequest(srv, http.MethodPost, "/test/fresh-secret", headers("fresh-session"), `{"note":"secret"}`)
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("same-credential replay = %d %s, want 201 %s", replay.Code, replay.Body.String(), first.Body.String())
	}
	other := doRequest(srv, http.MethodPost, "/test/fresh-secret", headers("read-token"), `{"note":"secret"}`)
	if other.Code != http.StatusUnauthorized {
		t.Fatalf("other-credential status %d, want 401: %s", other.Code, other.Body.String())
	}
	if calls != 2 {
		t.Fatalf("handler calls %d, want 2", calls)
	}
}

func TestIdempotencyReplayIncludesSetCookie(t *testing.T) {
	table := op.NewTable()
	calls := 0
	mustRegister(t, table, op.Op[jsonCreateIn, jsonCreateOut]{
		Name: "test.cookie.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/test/cookie",
		Scope: op.ScopeRepoRead, Summary: "Create cookie",
		Handler: func(ctx context.Context, _ op.Principal, _ jsonCreateIn) (jsonCreateOut, error) {
			calls++
			op.SetCookie(ctx, &http.Cookie{
				Name:     "__Host-rukie_session",
				Value:    "session-secret",
				Path:     "/",
				Secure:   true,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			return jsonCreateOut{Note: "cookie-created"}, nil
		},
	})
	srv, err := op.Mount(http.NewServeMux(), table, op.Config{
		Authenticator: tokenIDAuth{},
		Idempotency:   op.NewMemoryIdempotency(),
		CSRFKey:       testCSRFKey,
		Title:         "test",
		Version:       "0",
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	headers := map[string]string{
		"Accept":          "application/json",
		"Authorization":   "Bearer fresh-session",
		"Content-Type":    "application/json",
		"Idempotency-Key": "cookie-key",
	}
	first := doRequest(srv, http.MethodPost, "/test/cookie", headers, `{"note":"cookie"}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first status %d, want 201: %s", first.Code, first.Body.String())
	}
	firstCookies := first.Header().Values("Set-Cookie")
	if len(firstCookies) != 1 || !strings.Contains(firstCookies[0], "session-secret") {
		t.Fatalf("first Set-Cookie = %v", firstCookies)
	}
	replay := doRequest(srv, http.MethodPost, "/test/cookie", headers, `{"note":"cookie"}`)
	if replay.Code != http.StatusCreated {
		t.Fatalf("replay status %d, want 201: %s", replay.Code, replay.Body.String())
	}
	replayCookies := replay.Header().Values("Set-Cookie")
	if len(replayCookies) != 1 || replayCookies[0] != firstCookies[0] {
		t.Fatalf("replay Set-Cookie = %v, want %v", replayCookies, firstCookies)
	}
	if calls != 1 {
		t.Fatalf("handler calls %d, want 1", calls)
	}
}

func TestIdempotencyReplayUsesRecordedRepresentation(t *testing.T) {
	newServer := func(t *testing.T) *op.Server {
		t.Helper()
		table := op.NewTable()
		mustRegister(t, table, op.Op[jsonCreateIn, jsonCreateOut]{
			Name: "test.replay.create", Kind: op.KindMutation, Method: http.MethodPost, Path: "/test/replay",
			Scope: op.ScopeRepoRead, Summary: "Replay representation",
			Location: func(jsonCreateOut) string { return "/test/replay-result" },
			Handler: func(context.Context, op.Principal, jsonCreateIn) (jsonCreateOut, error) {
				return jsonCreateOut{Note: "recorded-secret"}, nil
			},
		})
		views := op.NewViews()
		views.Form("test.replay.create", func(io.Writer, op.ViewCtx) error {
			return nil
		})
		srv, err := op.Mount(http.NewServeMux(), table, op.Config{
			Authenticator: tokenIDAuth{},
			Idempotency:   op.NewMemoryIdempotency(),
			CSRFKey:       testCSRFKey,
			Title:         "test",
			Version:       "0",
			Views:         views,
		})
		if err != nil {
			t.Fatalf("mount: %v", err)
		}
		return srv
	}
	request := func(srv *op.Server, accept, key string) *httptest.ResponseRecorder {
		return doRequest(srv, http.MethodPost, "/test/replay", map[string]string{
			"Accept":          accept,
			"Authorization":   "Bearer fresh-session",
			"Content-Type":    "application/json",
			"Idempotency-Key": key,
		}, `{"note":"request"}`)
	}

	t.Run("markdown body is not rerendered", func(t *testing.T) {
		srv := newServer(t)
		first := request(srv, "text/markdown", "markdown-key")
		if first.Code != http.StatusCreated {
			t.Fatalf("first status %d, want 201: %s", first.Code, first.Body.String())
		}
		replay := request(srv, "text/markdown", "markdown-key")
		if replay.Code != first.Code || replay.Body.String() != first.Body.String() {
			t.Fatalf("markdown replay = %d %q, want %d %q", replay.Code, replay.Body.String(), first.Code, first.Body.String())
		}
		if !strings.HasPrefix(replay.Header().Get("Content-Type"), "text/markdown") {
			t.Fatalf("markdown replay content type %q", replay.Header().Get("Content-Type"))
		}
	})

	t.Run("json replay ignores later accept", func(t *testing.T) {
		srv := newServer(t)
		first := request(srv, "application/json", "json-key")
		if first.Code != http.StatusCreated {
			t.Fatalf("first status %d, want 201: %s", first.Code, first.Body.String())
		}
		replay := request(srv, "text/markdown", "json-key")
		if replay.Code != first.Code || replay.Body.String() != first.Body.String() {
			t.Fatalf("json replay = %d %q, want %d %q", replay.Code, replay.Body.String(), first.Code, first.Body.String())
		}
		if !strings.HasPrefix(replay.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("json replay content type %q", replay.Header().Get("Content-Type"))
		}
	})

	t.Run("html mutation replay preserves 303 headers", func(t *testing.T) {
		srv := newServer(t)
		first := request(srv, "text/html", "html-key")
		if first.Code != http.StatusSeeOther {
			t.Fatalf("first status %d, want 303: %s", first.Code, first.Body.String())
		}
		replay := request(srv, "application/json", "html-key")
		if replay.Code != http.StatusSeeOther || replay.Body.Len() != 0 {
			t.Fatalf("html replay = %d %q, want 303 empty", replay.Code, replay.Body.String())
		}
		if replay.Header().Get("Content-Type") != first.Header().Get("Content-Type") {
			t.Fatalf("html replay content type %q, want %q", replay.Header().Get("Content-Type"), first.Header().Get("Content-Type"))
		}
		if replay.Header().Get("Location") != first.Header().Get("Location") {
			t.Fatalf("html replay location %q, want %q", replay.Header().Get("Location"), first.Header().Get("Location"))
		}
	})
}
