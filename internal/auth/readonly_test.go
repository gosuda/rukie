package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
)

func TestReadOnlyAuthenticationPreservesVerdictsAndRecords(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "auth.sqlite")
	initial, err := store.Open(ctx, store.Options{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = initial.Close(context.Background()) })
	start := time.Now().Truncate(time.Millisecond)
	mintUser := func(name string, admin bool) store.AuthUser {
		u, userErr := initial.InsertAuthUser(ctx, store.AuthUserCreate{
			Name: name, Lower: name, Email: name + "@example.test", Hash: "unused",
			Algo: "argon2id-v19", Admin: admin, Now: start.UnixMilli(),
		})
		if userErr != nil {
			t.Fatal(userErr)
		}
		return u
	}
	active := mintUser("active", true)
	inactive := mintUser("inactive", false)
	seed := New(initial)
	seed.now = func() time.Time { return start }
	grants := op.Scopes(0).Add(op.ScopeAdminWrite)
	tokens := make(map[string]TokenSecret)
	for _, name := range []string{"valid", "expired", "revoked", "inactive"} {
		userID, ttl := active.ID, time.Hour
		if name == "inactive" {
			userID = inactive.ID
		}
		if name == "expired" {
			ttl = time.Minute
		}
		tok, tokenErr := seed.CreateToken(ctx, CreateTokenInput{UserID: userID, Name: name, TTL: ttl, Scopes: grants})
		if tokenErr != nil {
			t.Fatal(tokenErr)
		}
		tokens[name] = tok
	}
	if err = initial.RevokeTokenByHash(ctx, tokens["revoked"].Token.Hash, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	tokenRecords := make(map[string]store.TokenRecord)
	for name, tok := range tokens {
		rec, tokenErr := initial.GetTokenByHash(ctx, tok.Token.Hash)
		if tokenErr != nil {
			t.Fatal(tokenErr)
		}
		tokenRecords[name] = rec
	}
	now := start.Add(10 * time.Minute)
	sessions := make(map[string]store.SessionRecord)
	for _, name := range []string{"valid", "idle expired", "absolute expired", "inactive"} {
		userID, lastSeen, expires := active.ID, start.UnixMilli(), now.Add(time.Hour).UnixMilli()
		if name == "inactive" {
			userID = inactive.ID
		}
		if name == "idle expired" {
			lastSeen = now.Add(-sessionIdleTTL).UnixMilli()
		}
		if name == "absolute expired" {
			expires = now.UnixMilli()
		}
		hash := hashBearer("read-only session " + name)
		if err = initial.CreateSession(ctx, store.SessionCreate{
			IDHash: hash, CSRF: "csrf", AuthLevel: "password", UserAgent: name, UserID: userID,
			AuthedAt: start.UnixMilli(), CreatedAt: lastSeen, LastSeenAt: lastSeen, AbsoluteExpiresAt: expires,
		}); err != nil {
			t.Fatal(err)
		}
		rec, sessionErr := initial.GetSessionByHash(ctx, hash)
		if sessionErr != nil {
			t.Fatal(sessionErr)
		}
		sessions[name] = rec
	}
	if err = initial.UpdateAuthUserAdmin(ctx, inactive.ID, "", inactive.Email, false, start.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err = initial.Close(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, store.Options{Path: path, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	svc := New(st)
	svc.now = func() time.Time { return now }
	auther := NewAuthenticator(svc)
	for name, tok := range tokens {
		principal, authErr := auther.Bearer(ctx, tok.Secret)
		if name == "valid" {
			if authErr != nil || principal.UserID != active.ID || principal.Kind != op.PrincipalHuman || principal.Grants != ExpandScopes(grants) {
				t.Fatalf("valid bearer changed: %+v, %v", principal, authErr)
			}
		} else if !errors.Is(authErr, ErrTokenInvalid) {
			t.Fatalf("%s bearer = %+v, %v; want ErrTokenInvalid", name, principal, authErr)
		}
		after, tokenErr := st.GetTokenByHash(ctx, tok.Token.Hash)
		if tokenErr != nil || after != tokenRecords[name] {
			t.Fatalf("%s token bookkeeping changed: %+v, %v", name, after, tokenErr)
		}
	}
	for name, beforeSession := range sessions {
		principal, authErr := auther.Session(ctx, "read-only session "+name)
		if name == "valid" {
			if authErr != nil || principal.UserID != active.ID || principal.Kind != op.PrincipalHuman || principal.Grants != ScopesAll() {
				t.Fatalf("valid session changed: %+v, %v", principal, authErr)
			}
		} else if !errors.Is(authErr, ErrSessionInvalid) {
			t.Fatalf("%s session = %+v, %v; want ErrSessionInvalid", name, principal, authErr)
		}
		after, sessionErr := st.GetSessionByHash(ctx, beforeSession.IDHash)
		if sessionErr != nil || after != beforeSession {
			t.Fatalf("%s session bookkeeping or cleanup changed: %+v, %v", name, after, sessionErr)
		}
	}
	expired := tokens["expired"]
	if count, auditErr := st.CountAuditEvents(ctx, "token.expired", "token", expired.Token.PublicPrefix); auditErr != nil || count != 0 {
		t.Fatalf("read-only authentication wrote an audit event: %d, %v", count, auditErr)
	}
	if err = st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("read-only authentication changed database bytes")
	}
}
