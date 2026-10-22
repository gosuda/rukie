package api_test

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/web/htmlcheck"
)

type releaseEnv struct {
	server *httptest.Server
	front  http.Handler
	svc    *auth.Service
	st     *store.Store
	deps   api.Deps
}

func buildReleaseEnv(t *testing.T) *releaseEnv {
	t.Helper()
	path := t.TempDir() + "/releases.sqlite"
	st, err := store.Open(t.Context(), store.Options{Path: path})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	svc := auth.New(st)
	t.Cleanup(func() {
		_ = st.Close(context.Background())
	})
	deps := api.Deps{
		Service: svc, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(),
		Store: st, DataDir: t.TempDir(),
	}
	table := op.NewTable()
	if rerr := api.RegisterReleases(table, deps); rerr != nil {
		t.Fatalf("register releases: %v", rerr)
	}
	if verr := table.Validate(); verr != nil {
		t.Fatalf("validate: %v", verr)
	}
	views := op.NewViews()
	api.RegisterReleasesViews(views)
	mux := http.NewServeMux()
	server, err := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(svc),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		Views:         views,
		Title:         "test",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	front := api.ReleaseBytes{Deps: deps}.Wrap(server)
	ts := httptest.NewServer(front)
	t.Cleanup(ts.Close)
	return &releaseEnv{server: ts, front: front, svc: svc, st: st, deps: deps}
}

type relcall struct {
	method  string
	target  string
	auth    string
	accept  string
	headers map[string]string
	body    string
}

func dorel(t *testing.T, e *releaseEnv, call relcall) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if call.body != "" {
		reader = strings.NewReader(call.body)
	}
	req := httptest.NewRequest(call.method, e.server.URL+call.target, reader)
	if _, ok := call.headers["Content-Type"]; !ok {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", call.accept)
	for key, value := range call.headers {
		req.Header.Set(key, value)
	}
	if call.auth != "" {
		if strings.HasPrefix(call.auth, "Bearer ") {
			req.Header.Set("Authorization", call.auth)
		} else {
			req.Header.Set("Cookie", call.auth)
		}
	}
	rec := httptest.NewRecorder()
	e.front.ServeHTTP(rec, req)
	return rec
}

func relget(t *testing.T, e *releaseEnv, target, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	return dorel(t, e, relcall{method: http.MethodGet, target: target, auth: authHeader, accept: "application/json"})
}

func relpost(t *testing.T, e *releaseEnv, target, authHeader, body string) *httptest.ResponseRecorder {
	t.Helper()
	return dorel(t, e, relcall{method: http.MethodPost, target: target, auth: authHeader, accept: "application/json", body: body})
}

func reldecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal(rec.Body.Bytes(), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %d: %v\n%s", rec.Code, err, rec.Body.String())
	}
	return decoded
}

func rreleaseAdmin(t *testing.T, e *releaseEnv, name string) auth.User {
	t.Helper()
	user, err := e.svc.BootstrapAdmin(t.Context(), name, "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	return user
}

func rreleaseRepo(t *testing.T, e *releaseEnv, ownerID int64, name, visibility string) {
	t.Helper()
	err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		_, ierr := tx.InsertRepo(time.Now(), store.RepoInsert{
			OwnerUserID:   sql.NullInt64{Int64: ownerID, Valid: true},
			Name:          name,
			Visibility:    visibility,
			DefaultBranch: "main",
			RukPath:       "seed/" + name + ".git",
		})
		return ierr
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}
}

func rreleaseToken(t *testing.T, e *releaseEnv, user auth.User, scopes ...string) string {
	t.Helper()
	parsed, err := auth.ParseScopeSet(scopes)
	if err != nil {
		t.Fatalf("scopes: %v", err)
	}
	secret, err := e.svc.CreateToken(t.Context(), auth.CreateTokenInput{
		Name: "rel", Scopes: parsed, UserID: user.ID,
	})
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	return "Bearer " + secret.Secret
}

func rseedRelease(t *testing.T, e *releaseEnv, owner, repo, tag, title string, draft bool) string {
	t.Helper()
	user, err := e.svc.GetUser(t.Context(), owner)
	if err != nil {
		t.Fatal(err)
	}
	token := rreleaseToken(t, e, user, "repo:read", "repo:write", "releases:write")
	body := `{"tag":"` + tag + `","title":"` + title + `","draft":` + map[bool]string{true: "true", false: "false"}[draft] + `}`
	rec := relpost(t, e, "/"+owner+"/"+repo+"/releases/new", token, body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed release = %d: %s", rec.Code, rec.Body.String())
	}
	decoded := reldecode(t, rec)
	release, _ := decoded["release"].(map[string]any)
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("seed release missing ETag: %v", decoded)
	}
	_ = release
	return etag
}

func TestReleaseDraftLifecycle(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	token := rreleaseToken(t, e, ada, "repo:read", "repo:write", "releases:write")

	rec := relpost(t, e, "/ada/ships/releases/new", token, `{"tag":"v1","title":"First","notes":"hello","draft":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create draft = %d: %s", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("create draft missing ETag")
	}

	got := relget(t, e, "/ada/ships/releases/tag/v1", token)
	if got.Code != http.StatusOK {
		t.Fatalf("writer get draft = %d: %s", got.Code, got.Body.String())
	}
	if anon := relget(t, e, "/ada/ships/releases/tag/v1", ""); anon.Code != http.StatusNotFound {
		t.Fatalf("anon get draft = %d, want 404: %s", anon.Code, anon.Body.String())
	}

	dup := relpost(t, e, "/ada/ships/releases/new", token, `{"tag":"V1","title":"Dupe","draft":true}`)
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate tag = %d, want 409: %s", dup.Code, dup.Body.String())
	}

	upd := dorel(t, e, relcall{method: http.MethodPost, target: "/ada/ships/releases/edit/v1", auth: token, accept: "application/json", headers: map[string]string{"If-Match": etag}, body: `{"title":"First rev"}`})
	if upd.Code != http.StatusOK {
		t.Fatalf("update draft = %d: %s", upd.Code, upd.Body.String())
	}
	stale := dorel(t, e, relcall{method: http.MethodPost, target: "/ada/ships/releases/edit/v1", auth: token, accept: "application/json", headers: map[string]string{"If-Match": etag}, body: `{"title":"Lost race"}`})
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale draft update = %d, want 412: %s", stale.Code, stale.Body.String())
	}

	plain := relpost(t, e, "/ada/ships/releases/new", token, `{"tag":"v2","title":"Second","draft":true}`)
	if plain.Code != http.StatusCreated {
		t.Fatalf("create second = %d: %s", plain.Code, plain.Body.String())
	}
	anonList := relget(t, e, "/ada/ships/releases?include_drafts=true", "")
	decoded := reldecode(t, anonList)
	items, _ := decoded["releases"].([]any)
	for _, item := range items {
		row, _ := item.(map[string]any)
		if draft, _ := row["draft"].(bool); draft {
			t.Fatalf("anonymous list exposed a draft: %v", row)
		}
	}

	del := dorel(t, e, relcall{method: http.MethodPost, target: "/ada/ships/releases/delete/v1", auth: token, accept: "application/json", headers: map[string]string{"If-Match": upd.Header().Get("ETag")}})
	if del.Code != http.StatusOK {
		t.Fatalf("discard draft = %d: %s", del.Code, del.Body.String())
	}
	if gone := relget(t, e, "/ada/ships/releases/tag/v1", token); gone.Code != http.StatusNotFound {
		t.Fatalf("discarded get = %d, want 404: %s", gone.Code, gone.Body.String())
	}
}

func TestReleasePreconditions(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	token := rreleaseToken(t, e, ada, "repo:read", "repo:write", "releases:write")
	etag := rseedRelease(t, e, "ada", "ships", "v9", "Nine", true)

	if rec := dorel(t, e, relcall{method: http.MethodPost, target: "/ada/ships/releases/edit/v9", auth: token, accept: "application/json", body: `{"title":"x"}`}); rec.Code != 428 {
		t.Fatalf("missing If-Match = %d, want 428: %s", rec.Code, rec.Body.String())
	}
	if rec := dorel(t, e, relcall{method: http.MethodPost, target: "/ada/ships/releases/edit/v9", auth: token, accept: "application/json", headers: map[string]string{"If-Match": "\"rel-nope-0\""}, body: `{"title":"x"}`}); rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match = %d, want 412: %s", rec.Code, rec.Body.String())
	}
	_ = etag
}

func TestReleasePublishedImmutableSeeded(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	token := rreleaseToken(t, e, ada, "repo:read", "repo:write", "releases:write")
	rseedRelease(t, e, "ada", "ships", "v3", "Three", true)

	now := time.Now()
	var row store.ReleaseRow
	if err := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetRelease(1, "v3")
		return gerr
	}); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		return tx.PublishRelease(row.ID, "abc", "lightweight", "<p>Three</p>", now)
	}); err != nil {
		t.Fatalf("seed publish: %v", err)
	}

	freshGet := relget(t, e, "/ada/ships/releases/tag/v3", token)
	if freshGet.Code != http.StatusOK {
		t.Fatalf("seeded get = %d: %s", freshGet.Code, freshGet.Body.String())
	}
	freshETag := freshGet.Header().Get("ETag")
	mut := dorel(t, e, relcall{method: http.MethodPost, target: "/ada/ships/releases/edit/v3", auth: token, accept: "application/json", headers: map[string]string{"If-Match": freshETag}, body: `{"title":"Changed"}`})
	if mut.Code != http.StatusConflict {
		t.Fatalf("published update = %d, want 409: %s", mut.Code, mut.Body.String())
	}
	pub := dorel(t, e, relcall{method: http.MethodPost, target: "/ada/ships/releases/edit/v3", auth: token, accept: "application/json", headers: map[string]string{"If-Match": freshETag}, body: `{"draft":false}`})
	if pub.Code != http.StatusConflict {
		t.Fatalf("published republish = %d, want 409: %s", pub.Code, pub.Body.String())
	}

	adel := dorel(t, e, relcall{method: http.MethodPost, target: "/ada/ships/releases/delete/v3", auth: token, accept: "application/json", headers: map[string]string{"If-Match": freshETag}})
	if adel.Code != http.StatusOK {
		t.Fatalf("published delete = %d: %s", adel.Code, adel.Body.String())
	}
	if gone := relget(t, e, "/ada/ships/releases/tag/v3", token); gone.Code != http.StatusNotFound {
		t.Fatalf("deleted get = %d, want 404: %s", gone.Code, gone.Body.String())
	}
}

func TestReleaseAssetUploadDownload(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	token := rreleaseToken(t, e, ada, "repo:read", "repo:write", "releases:write")
	rseedRelease(t, e, "ada", "ships", "v4", "Four", true)

	payload := "asset-bytes-fixture-0123456789"
	put := dorel(t, e, relcall{method: http.MethodPut, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/json",
		headers: map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream"}, body: payload})
	if put.Code != http.StatusCreated {
		t.Fatalf("upload = %d: %s", put.Code, put.Body.String())
	}
	etag := put.Header().Get("ETag")
	if etag == "" {
		t.Fatal("upload missing ETag")
	}
	if digest := put.Header().Get("Repr-Digest"); digest == "" {
		t.Fatal("upload missing Repr-Digest")
	}

	dup := dorel(t, e, relcall{method: http.MethodPut, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/json",
		headers: map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream"}, body: payload})
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate upload = %d, want 409: %s", dup.Code, dup.Body.String())
	}

	get := dorel(t, e, relcall{method: http.MethodGet, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/octet-stream"})
	if get.Code != http.StatusOK {
		t.Fatalf("download = %d: %s", get.Code, get.Body.String())
	}
	if get.Body.String() != payload {
		t.Fatalf("download = %q, want %q", get.Body.String(), payload)
	}

	partial := dorel(t, e, relcall{method: http.MethodGet, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/octet-stream", headers: map[string]string{"Range": "bytes=0-4"}})
	if partial.Code != http.StatusPartialContent {
		t.Fatalf("range = %d, want 206: %s", partial.Code, partial.Body.String())
	}
	if partial.Body.String() != payload[:5] {
		t.Fatalf("range body = %q, want %q", partial.Body.String(), payload[:5])
	}
	suffix := dorel(t, e, relcall{method: http.MethodGet, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/octet-stream", headers: map[string]string{"Range": "bytes=-5"}})
	if suffix.Code != http.StatusPartialContent {
		t.Fatalf("suffix range = %d, want 206: %s", suffix.Code, suffix.Body.String())
	}
	if suffix.Body.String() != payload[len(payload)-5:] {
		t.Fatalf("suffix body = %q, want tail", suffix.Body.String())
	}
	badRange := dorel(t, e, relcall{method: http.MethodGet, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/octet-stream", headers: map[string]string{"Range": "bytes=9-1"}})
	if badRange.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("bad range = %d, want 416: %s", badRange.Code, badRange.Body.String())
	}
	whole := dorel(t, e, relcall{method: http.MethodGet, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/octet-stream", headers: map[string]string{"Range": "bytes2-"}})
	if whole.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("hyphenless range = %d, want 416: %s", whole.Code, whole.Body.String())
	}

	cond := dorel(t, e, relcall{method: http.MethodGet, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/octet-stream", headers: map[string]string{"If-None-Match": etag}})
	if cond.Code != http.StatusNotModified {
		t.Fatalf("cond get = %d, want 304: %s", cond.Code, cond.Body.String())
	}

	bad := dorel(t, e, relcall{method: http.MethodPut, target: "/ada/ships/releases/assets/v4/other.bin", auth: token, accept: "application/json",
		headers: map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream", "Repr-Digest": "sha-256=:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=:"}, body: payload})
	if bad.Code != 422 {
		t.Fatalf("digest mismatch = %d, want 422: %s", bad.Code, bad.Body.String())
	}

	meta := relget(t, e, "/ada/ships/releases/assets/v4/blob.bin", token)
	if meta.Code != http.StatusOK {
		t.Fatalf("asset meta = %d: %s", meta.Code, meta.Body.String())
	}

	del := dorel(t, e, relcall{method: http.MethodDelete, target: "/ada/ships/releases/assets/v4/blob.bin", auth: token, accept: "application/json", headers: map[string]string{"If-Match": etag}})
	if del.Code != http.StatusNoContent {
		t.Fatalf("asset delete = %d: %s", del.Code, del.Body.String())
	}
}

func TestReleaseMutationsRequireReleaseScope(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	writerOnly := rreleaseToken(t, e, ada, "repo:read", "repo:write")
	if rec := relpost(t, e, "/ada/ships/releases/new", writerOnly, `{"tag":"v1","title":"First","draft":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("repo:write create = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	relOnly := rreleaseToken(t, e, ada, "releases:write")
	if rec := relpost(t, e, "/ada/ships/releases/new", relOnly, `{"tag":"v1","title":"First","draft":true}`); rec.Code != http.StatusCreated {
		t.Fatalf("releases:write create = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	mallory, merr := e.svc.CreateUser(t.Context(), ada.ID, auth.CreateUserInput{
		Name: "mallory", Email: "mallory@example.test", Password: "long-test-password-01",
	}, "127.0.0.1")
	if merr != nil {
		t.Fatalf("create writer: %v", merr)
	}
	var repoID int64
	if rerr := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		row, gerr := tx.FindRepo(ada.ID, 0, "ships")
		if gerr != nil {
			return gerr
		}
		repoID = row.ID
		return nil
	}); rerr != nil {
		t.Fatalf("find repo: %v", rerr)
	}
	if gerr := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		return tx.PutGrant(repoID, mallory.ID, "write")
	}); gerr != nil {
		t.Fatalf("grant write: %v", gerr)
	}
	writerToken := rreleaseToken(t, e, mallory, "repo:read", "repo:write", "releases:write")
	if rec := relpost(t, e, "/ada/ships/releases/new", writerToken, `{"tag":"v2","title":"Second","draft":true}`); rec.Code != http.StatusForbidden {
		t.Fatalf("writer create = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	upload := func(token, name string) *httptest.ResponseRecorder {
		return dorel(t, e, relcall{method: http.MethodPut, target: "/ada/ships/releases/assets/v1/" + name, auth: token, accept: "application/json",
			headers: map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream"}, body: "bytes-" + name})
	}
	if rec := upload(writerToken, "w.bin"); rec.Code != http.StatusForbidden {
		t.Fatalf("writer byte upload = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if rec := upload(relOnly, "r.bin"); rec.Code != http.StatusCreated {
		t.Fatalf("releases:write byte upload = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

func TestReleaseListCursorPaginatesAll(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	token := rreleaseToken(t, e, ada, "repo:read", "repo:write", "releases:write")
	for _, tag := range []string{"v1", "v2", "v3"} {
		rseedRelease(t, e, "ada", "ships", tag, "Title "+tag, true)
	}
	seen := map[string]bool{}
	target := "/ada/ships/releases?include_drafts=true&limit=2"
	for range 10 {
		got := relget(t, e, target, token)
		if got.Code != http.StatusOK {
			t.Fatalf("page %s = %d: %s", target, got.Code, got.Body.String())
		}
		decoded := reldecode(t, got)
		items, _ := decoded["releases"].([]any)
		for _, item := range items {
			row, _ := item.(map[string]any)
			tag, _ := row["tag"].(string)
			if seen[tag] {
				t.Fatalf("tag %q returned twice across pages", tag)
			}
			seen[tag] = true
		}
		link := got.Header().Get("Link")
		if !strings.Contains(link, `rel="next"`) {
			break
		}
		target = nextLinkTarget(t, link)
	}
	for _, tag := range []string{"v1", "v2", "v3"} {
		if !seen[tag] {
			t.Fatalf("tag %q skipped by pagination, saw %v", tag, seen)
		}
	}
}

func nextLinkTarget(t *testing.T, link string) string {
	t.Helper()
	start := strings.Index(link, "<")
	end := strings.Index(link, ">")
	if start < 0 || end < 0 || end <= start+1 {
		t.Fatalf("malformed next link: %q", link)
	}
	target := link[start+1 : end]
	if strings.HasPrefix(target, "http") {
		if idx := strings.Index(target[len("https://"):], "/"); idx >= 0 {
			target = target[len("https://")+idx:]
		}
	}
	return target
}

func TestReleaseAssetListCursorPaginatesAll(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	token := rreleaseToken(t, e, ada, "repo:read", "repo:write", "releases:write")
	rseedRelease(t, e, "ada", "ships", "v4", "Four", true)
	for _, name := range []string{"a.bin", "b.bin", "c.bin"} {
		put := dorel(t, e, relcall{method: http.MethodPut, target: "/ada/ships/releases/assets/v4/" + name, auth: token, accept: "application/json",
			headers: map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream"}, body: "payload-" + name})
		if put.Code != http.StatusCreated {
			t.Fatalf("upload %s = %d: %s", name, put.Code, put.Body.String())
		}
	}
	seen := map[string]bool{}
	target := "/ada/ships/releases/assets/v4?limit=2"
	for range 10 {
		got := relget(t, e, target, token)
		if got.Code != http.StatusOK {
			t.Fatalf("page %s = %d: %s", target, got.Code, got.Body.String())
		}
		decoded := reldecode(t, got)
		items, _ := decoded["assets"].([]any)
		for _, item := range items {
			row, _ := item.(map[string]any)
			name, _ := row["name"].(string)
			if seen[name] {
				t.Fatalf("asset %q returned twice across pages", name)
			}
			seen[name] = true
		}
		link := got.Header().Get("Link")
		if !strings.Contains(link, `rel="next"`) {
			break
		}
		target = nextLinkTarget(t, link)
	}
	for _, name := range []string{"a.bin", "b.bin", "c.bin"} {
		if !seen[name] {
			t.Fatalf("asset %q skipped by pagination, saw %v", name, seen)
		}
	}
}

func TestReleaseAssetUploadToPublishedRefused(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	token := rreleaseToken(t, e, ada, "repo:read", "repo:write", "releases:write")
	rseedRelease(t, e, "ada", "ships", "v6", "Six", true)
	now := time.Now()
	var row store.ReleaseRow
	if err := e.st.Read(t.Context(), func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetRelease(1, "v6")
		return gerr
	}); err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if err := e.st.Write(t.Context(), func(tx *store.WriteTx) error {
		if perr := tx.PublishRelease(row.ID, "abc", "lightweight", "<p>Six</p>", now); perr != nil {
			return perr
		}
		return tx.PublishAssets(row.ID, now)
	}); err != nil {
		t.Fatalf("seed publish: %v", err)
	}
	put := dorel(t, e, relcall{method: http.MethodPut, target: "/ada/ships/releases/assets/v6/late.bin", auth: token, accept: "application/json",
		headers: map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream"}, body: "late-bytes"})
	if put.Code != http.StatusConflict {
		t.Fatalf("upload to published = %d, want 409: %s", put.Code, put.Body.String())
	}
}

func TestReleasePagesZeroJS(t *testing.T) {
	e := buildReleaseEnv(t)
	ada := rreleaseAdmin(t, e, "ada")
	rreleaseRepo(t, e, ada.ID, "ships", "public")
	token := rreleaseToken(t, e, ada, "repo:read", "repo:write", "releases:write")
	rseedRelease(t, e, "ada", "ships", "v5", "Five", true)
	meta := reldecode(t, relget(t, e, "/ada/ships/releases/tag/v5", token))
	release, _ := meta["release"].(map[string]any)
	bodyETag, _ := release["etag"].(string)
	if bodyETag == "" {
		t.Fatal("release body carries no entity tag")
	}

	for _, target := range []string{"/ada/ships/releases", "/ada/ships/releases/tag/v5"} {
		rec := dorel(t, e, relcall{method: http.MethodGet, target: target, auth: token, accept: "text/html"})
		if rec.Code != http.StatusOK {
			t.Fatalf("page %s = %d: %s", target, rec.Code, rec.Body.String())
		}
		if err := htmlcheck.Check(rec.Body.Bytes()); err != nil {
			t.Fatalf("page %s fails zero-JS: %v", target, err)
		}
	}
	page := dorel(t, e, relcall{method: http.MethodGet, target: "/ada/ships/releases/tag/v5", auth: token, accept: "text/html"})
	want := `name="_etag" value="` + strings.ReplaceAll(bodyETag, `"`, "&#34;") + `"`
	if !strings.Contains(page.Body.String(), want) {
		t.Fatalf("release page lacks the entity tag in its forms: want %s", want)
	}
}
