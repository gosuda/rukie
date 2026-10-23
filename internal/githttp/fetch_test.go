package githttp

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/ci"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

func seedFetchStore(t *testing.T) (*store.Store, *auth.Service, int64, int64) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, store.Options{Path: t.TempDir() + "/fetch.sqlite"})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(ctx) })
	ada, err := st.InsertAuthUser(ctx, store.AuthUserCreate{
		Name: "ada", Lower: "ada", Email: "ada@example.test",
		Hash: "test-hash", Algo: "argon2id-v19", Now: 1,
	})
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	var hello, world int64
	if werr := st.Write(ctx, func(tx *store.WriteTx) error {
		helloID, ierr := tx.InsertRepo(time.Unix(1, 0), store.RepoInsert{
			Name: "hello", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ada.ID, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		worldID, ierr := tx.InsertRepo(time.Unix(1, 0), store.RepoInsert{
			Name: "world", Visibility: "private", DefaultBranch: "main",
			OwnerUserID: sql.NullInt64{Int64: ada.ID, Valid: true},
		})
		if ierr != nil {
			return ierr
		}
		hello, world = helloID, worldID
		return nil
	}); werr != nil {
		t.Fatalf("seed repos: %v", werr)
	}
	return st, auth.New(st), hello, world
}

func mintFetchToken(t *testing.T, svc *auth.Service, userID, repoID int64) string {
	t.Helper()
	minted, err := svc.CreateToken(t.Context(), auth.CreateTokenInput{
		UserID: userID, Name: ci.FetchCredentialPrefix + "att_test",
		Scopes: op.Scopes(0).Add(op.ScopeRepoRead),
		TTL:    time.Hour, Repos: []int64{repoID}, IP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("mint fetch token: %v", err)
	}
	return minted.Secret
}

func openFetchServer(t *testing.T, st *store.Store, svc *auth.Service) *Server {
	t.Helper()
	srv, err := New(Config{
		Auth: svc, Limiter: access.NewLimiter(), Store: st,
		RepoRoot: t.TempDir(), Limits: wire.DefaultGitLimits(),
		SocketPath: filepath.Join(t.TempDir(), "ruk.sock"),
	})
	if err != nil {
		t.Fatalf("git server: %v", err)
	}
	return srv
}

func TestFetchCredentialAuthenticate(t *testing.T) {
	st, svc, hello, _ := seedFetchStore(t)
	ada, err := svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	fetch := mintFetchToken(t, svc, ada.ID, hello)
	plain, err := svc.CreateToken(t.Context(), auth.CreateTokenInput{
		UserID: ada.ID, Name: "plain", Scopes: op.Scopes(0).Add(op.ScopeRepoRead),
		TTL: time.Hour, Repos: []int64{hello}, IP: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("mint plain token: %v", err)
	}
	srv := openFetchServer(t, st, svc)
	authed := func(secret string) (identity, bool) {
		r := httptest.NewRequest("GET", "/ada/hello.git/info/refs?service=git-upload-pack", nil)
		r.Header.Set("Authorization", "Bearer "+secret)
		id, ok, _ := srv.authenticate(r)
		return id, ok
	}
	id, ok := authed(fetch)
	if !ok || !id.fetchOnly {
		t.Fatalf("fetch credential authed=%v fetchOnly=%v, want true true", ok, id.fetchOnly)
	}
	id, ok = authed(plain.Secret)
	if !ok || id.fetchOnly {
		t.Fatalf("plain token authed=%v fetchOnly=%v, want true false", ok, id.fetchOnly)
	}
	if _, ok := authed("0123456789abcdef0123456789abcdef"); ok {
		t.Fatal("attempt-shaped token authenticated against the git path")
	}
}

func TestFetchCredentialPushDenied(t *testing.T) {
	st, svc, hello, _ := seedFetchStore(t)
	ada, err := svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	fetch := mintFetchToken(t, svc, ada.ID, hello)
	srv := openFetchServer(t, st, svc)
	live := httptest.NewServer(srv)
	defer live.Close()
	get := func(path string) (int, string) {
		req, rerr := http.NewRequest("GET", live.URL+path, nil)
		if rerr != nil {
			t.Fatal(rerr)
		}
		req.Header.Set("Authorization", "Bearer "+fetch)
		resp, rerr := http.DefaultClient.Do(req)
		if rerr != nil {
			t.Fatal(rerr)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, body := get("/ada/hello.git/info/refs?service=git-receive-pack"); code != http.StatusForbidden || !strings.Contains(body, "fetch credentials cannot push") {
		t.Fatalf("receive-pack advertisement = %d: %s, want 403", code, body)
	}
	post, err := http.NewRequest("POST", live.URL+"/ada/hello.git/git-receive-pack", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	post.Header.Set("Authorization", "Bearer "+fetch)
	post.Header.Set("Content-Type", "application/x-git-receive-pack-request")
	resp, err := http.DefaultClient.Do(post)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("receive-pack push = %d, want 403", resp.StatusCode)
	}
}

func TestFetchCredentialScopedToRepo(t *testing.T) {
	st, svc, hello, _ := seedFetchStore(t)
	ada, err := svc.GetUser(t.Context(), "ada")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	fetch := mintFetchToken(t, svc, ada.ID, hello)
	srv := openFetchServer(t, st, svc)
	live := httptest.NewServer(srv)
	defer live.Close()
	get := func(path string) (int, string) {
		req, rerr := http.NewRequest("GET", live.URL+path, nil)
		if rerr != nil {
			t.Fatal(rerr)
		}
		req.Header.Set("Authorization", "Bearer "+fetch)
		resp, rerr := http.DefaultClient.Do(req)
		if rerr != nil {
			t.Fatal(rerr)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if code, body := get("/ada/hello.git/info/refs?service=git-upload-pack"); code == http.StatusUnauthorized || code == http.StatusForbidden {
		t.Fatalf("bound clone = %d: %s, want past auth", code, body)
	}
	if code, _ := get("/ada/world.git/info/refs?service=git-upload-pack"); code != http.StatusForbidden {
		t.Fatalf("other-repo clone = %d, want 403", code)
	}
}
