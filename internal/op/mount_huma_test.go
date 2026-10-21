package op_test

import (
	"context"
	"net/http"
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
