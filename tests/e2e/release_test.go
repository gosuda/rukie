//go:build e2e

package e2e

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/queue"
	"github.com/gosuda/rukie/internal/repowatch"
	"github.com/gosuda/rukie/internal/web/htmlcheck"
)

func mountReleaseAPI(t *testing.T, s *stack) *apiHarness {
	t.Helper()
	var sealKey [32]byte
	_, _ = rand.Read(sealKey[:])
	mirrorQueue := queue.Open(s.st, time.Now)
	deps := api.Deps{
		Service: s.auth, Limiter: access.NewLimiter(), Reveals: auth.NewRevealCache(),
		Store: s.st, Ruk: s.client, Queue: mirrorQueue,
		RepoRoot: s.roots, RukBin: rukBin(t), DataDir: s.dir,
		SealKey: sealKey[:],
	}
	watcher := repowatch.New(repowatch.Config{
		Store: s.st, Queue: queue.Open(s.st, time.Now), Client: s.client,
		RepoRoot: s.roots, Clock: time.Now,
	})
	deps.OnRepoCreated = func(repoID int64) { watcher.Start(t.Context(), repoID) }
	deps.OnRepoDeleted = watcher.Stop
	table := op.NewTable()
	docs := &api.DocStore{}
	for _, step := range []func() error{
		func() error { return api.RegisterAll(table, docs) },
		func() error { return api.RegisterAuth(table, deps) },
		func() error { return api.RegisterUser(table, deps) },
		func() error { return api.RegisterAdmin(table, deps) },
		func() error { return api.RegisterRepos(table, deps) },
		func() error { return api.RegisterCode(table, deps) },
		func() error { return api.RegisterRefs(table, deps) },
		func() error { return api.RegisterReview(table, deps) },
		func() error { return api.RegisterMirror(table, deps) },
		func() error { return api.RegisterReleases(table, deps) },
	} {
		if err := step(); err != nil {
			t.Fatalf("register: %v", err)
		}
	}
	if err := table.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	views := op.NewViews()
	api.RegisterViews(views)
	api.RegisterAuthViews(views)
	api.RegisterUserViews(views)
	api.RegisterAdminViews(views)
	api.RegisterReposViews(views)
	api.RegisterCodeViews(views)
	api.RegisterRefsViews(views)
	api.RegisterReviewViews(views)
	api.RegisterMirrorViews(views)
	api.RegisterReleasesViews(views)
	var key [32]byte
	_, _ = rand.Read(key[:])
	mux := http.NewServeMux()
	mounted, err := op.Mount(mux, table, op.Config{
		Authenticator: auth.NewAuthenticator(s.auth),
		Idempotency:   op.NewMemoryIdempotency(),
		Audit:         op.FuncAuditor(func(context.Context, op.AuditRow) error { return nil }),
		Limiter:       deps.Limiter,
		CSRFKey:       key[:],
		Views:         views,
		Title:         "e2e",
		Version:       "0",
		IsSiteSegment: api.SiteSegment,
	})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	front := api.ReleaseBytes{Deps: deps}.Wrap(api.RawBytes{Deps: deps}.Wrap(mounted))
	ts := httptest.NewServer(front)
	t.Cleanup(ts.Close)
	return &apiHarness{server: ts, watcher: watcher, queue: mirrorQueue, sealKey: sealKey[:]}
}

type relCall struct {
	method  string
	target  string
	auth    string
	accept  string
	headers map[string]string
	body    []byte
}

func relDo(t *testing.T, h *apiHarness, call relCall) (int, http.Header, []byte) {
	t.Helper()
	var reader io.Reader
	if call.body != nil {
		reader = bytes.NewReader(call.body)
	}
	req, err := http.NewRequest(call.method, h.server.URL+call.target, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if _, ok := call.headers["Content-Type"]; !ok && call.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", call.accept)
	for k, v := range call.headers {
		req.Header.Set(k, v)
	}
	if call.auth != "" {
		req.Header.Set("Authorization", call.auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode, resp.Header, readBody(t, resp)
}

func relJSON(t *testing.T, h *apiHarness, call relCall) (int, map[string]any) {
	t.Helper()
	call.accept = "application/json"
	status, _, raw := relDo(t, h, call)
	var decoded map[string]any
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %d: %v", status, err)
	}
	return status, decoded
}

func relBytes(t *testing.T, h *apiHarness, method, target, authHeader, accept string, headers map[string]string, body []byte) (int, http.Header, []byte) {
	t.Helper()
	return relDo(t, h, relCall{method: method, target: target, auth: authHeader, accept: accept, headers: headers, body: body})
}

func relSetup(t *testing.T, name string) (*stack, *apiHarness, string) {
	t.Helper()
	s := newStack(t, nil)
	h := mountReleaseAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:read repo:write releases:write")
	seedF1Repo(t, s, "ada", name, "private")
	return s, h, token
}

func relPush(t *testing.T, s *stack, name, work string, refs ...string) {
	t.Helper()
	pushCtx, pushCancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer pushCancel()
	args := append([]string{"push", s.repoURL("ada", name, "ada", s.users["ada"].token)}, refs...)
	if out, err := gitRun(pushCtx, t, work, args...); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
}

func relPublish(t *testing.T, h *apiHarness, token, fullName, tag, title string) map[string]any {
	t.Helper()
	owner, repo, _ := strings.Cut(fullName, "/")
	status, decoded := relJSON(t, h, relCall{method: "POST", target: "/" + owner + "/" + repo + "/releases/new", auth: token, body: []byte(`{"tag":"` + tag + `","title":"` + title + `","draft":false}`)})
	if status != http.StatusCreated {
		t.Fatalf("publish = %d: %v", status, decoded)
	}
	release, _ := decoded["release"].(map[string]any)
	if release == nil {
		t.Fatalf("publish missing release: %v", decoded)
	}
	if draft, _ := release["draft"].(bool); draft {
		t.Fatalf("published release still draft: %v", release)
	}
	return release
}

func TestReleaseTarballDownloadE2E(t *testing.T) {
	s, h, token := relSetup(t, "hello")
	work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n", "cmd/main.go": "package main\n"})
	relPush(t, s, "hello", work, "HEAD:refs/heads/main", "HEAD:refs/tags/v1")

	release := relPublish(t, h, token, "ada/hello", "v1", "First")
	if commit, _ := release["commit"].(string); commit == "" {
		t.Fatalf("published release missing commit: %v", release)
	}
	commit := release["commit"].(string)

	for _, format := range []struct {
		query       string
		contentType string
		etag        string
	}{
		{"tar", "application/x-tar", commit + ":tar"},
		{"tar.gz", "application/gzip", commit + ":tar.gz"},
		{"zip", "application/zip", commit + ":zip"},
	} {
		status, header, rawBody := relBytes(t, h, "GET", "/ada/hello/archive/"+commit+"?format="+format.query, token, "application/octet-stream", nil, nil)
		if status != http.StatusOK {
			t.Fatalf("archive %s = %d", format.query, status)
		}
		if got := header.Get("Content-Type"); got != format.contentType {
			t.Fatalf("archive %s content-type = %q, want %q", format.query, got, format.contentType)
		}
		if got := header.Get("ETag"); got != `"`+format.etag+`"` {
			t.Fatalf("archive %s etag = %q, want %q", format.query, got, `"`+format.etag+`"`)
		}
		body := rawBody
		if format.query == "tar.gz" {
			gzr, gerr := gzip.NewReader(bytes.NewReader(rawBody))
			if gerr != nil {
				t.Fatalf("gzip open: %v", gerr)
			}
			unzipped, rerr := io.ReadAll(gzr)
			if rerr != nil {
				t.Fatalf("gzip read: %v", rerr)
			}
			_ = gzr.Close()
			body = unzipped
		}
		if len(body) == 0 {
			t.Fatalf("archive %s is empty", format.query)
		}
		if format.query == "tar" || format.query == "tar.gz" {
			names := map[string]bool{}
			tr := tar.NewReader(bytes.NewReader(body))
			for {
				hdr, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("tar read: %v", err)
				}
				names[hdr.Name] = true
			}
			if !names["README.md"] && !names["./README.md"] {
				t.Fatalf("tar missing README.md: %v", names)
			}
		} else {
			zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
			if err != nil {
				t.Fatalf("zip read: %v", err)
			}
			found := false
			for _, f := range zr.File {
				if strings.HasSuffix(f.Name, "README.md") {
					found = true
				}
			}
			if !found {
				t.Fatalf("zip missing README.md")
			}
		}
		condStatus, _, _ := relBytes(t, h, "GET", "/ada/hello/archive/"+commit+"?format="+format.query, token, "application/octet-stream", map[string]string{"If-None-Match": `"` + format.etag + `"`}, nil)
		if condStatus != http.StatusNotModified {
			t.Fatalf("archive %s cond = %d, want 304", format.query, condStatus)
		}
		if format.query == "tar" {
			pageStatus, _, pageBody := relBytes(t, h, "GET", "/ada/hello/archive/"+commit, token, "text/html", nil, nil)
			if pageStatus != http.StatusOK {
				t.Fatalf("archive page = %d", pageStatus)
			}
			if err := htmlcheck.Check(pageBody); err != nil {
				t.Fatalf("archive page zero-JS: %v", err)
			}
		}
	}

	pattern := []byte("0123456789ABCDEF")
	big := make([]byte, 0, 17<<20)
	for len(big) < 17<<20 {
		big = append(big, pattern...)
	}
	if err := os.WriteFile(work+"/big.bin", big, 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}
	commitCtx, commitCancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer commitCancel()
	if out, err := gitRun(commitCtx, t, work, "add", "-A"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	if out, err := gitRun(commitCtx, t, work, "commit", "-m", "big"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	relPush(t, s, "hello", work, "HEAD:refs/heads/main")
	tagCtx, tagCancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer tagCancel()
	if out, err := gitRun(tagCtx, t, work, "tag", "v2"); err != nil {
		t.Fatalf("tag: %v\n%s", err, out)
	}
	relPush(t, s, "hello", work, "refs/tags/v2:refs/tags/v2")
	relPublish(t, h, token, "ada/hello", "v2", "Second")
	status, header, body := relBytes(t, h, "GET", "/ada/hello/archive/v2?format=tar", token, "application/octet-stream", nil, nil)
	if status != http.StatusOK {
		t.Fatalf("large archive = %d", status)
	}
	if int64(len(body)) <= 16<<20 {
		t.Fatalf("large archive = %d bytes, want over 16 MiB", len(body))
	}
	if got := header.Get("ETag"); !strings.HasSuffix(got, ":tar\"") {
		t.Fatalf("large archive etag = %q", got)
	}
	count := 0
	tr := tar.NewReader(bytes.NewReader(body))
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("large tar read: %v", err)
		}
		count++
		if count > 3 {
			break
		}
	}
	_ = oid
}

func TestReleaseAssetRangeAndDigest(t *testing.T) {
	s, h, token := relSetup(t, "assets")
	work, _ := s.localRepo(t, map[string]string{"README.md": "assets\n"})
	relPush(t, s, "assets", work, "HEAD:refs/heads/main", "HEAD:refs/tags/av1")

	status, _ := relJSON(t, h, relCall{method: "POST", target: "/ada/assets/releases/new", auth: token, body: []byte(`{"tag":"av1","title":"A","draft":true}`)})
	if status != http.StatusCreated {
		t.Fatalf("draft = %d", status)
	}

	payload := []byte("asset-range-digest-fixture-0123456789")
	sum := sha256.Sum256(payload)
	digest := "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
	putStatus, putHeader, _ := relBytes(t, h, "PUT", "/ada/assets/releases/assets/av1/blob.bin", token, "application/json",
		map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream", "Repr-Digest": digest}, payload)
	if putStatus != http.StatusCreated {
		t.Fatalf("upload = %d", putStatus)
	}
	if got := putHeader.Get("Repr-Digest"); got != digest {
		t.Fatalf("upload digest = %q, want %q", got, digest)
	}
	etag := putHeader.Get("ETag")
	if etag == "" {
		t.Fatal("upload missing ETag")
	}

	rangeStatus, rangeHeader, rangeBody := relBytes(t, h, "GET", "/ada/assets/releases/assets/av1/blob.bin", token, "application/octet-stream", map[string]string{"Range": "bytes=0-9"}, nil)
	if rangeStatus != http.StatusPartialContent {
		t.Fatalf("range = %d", rangeStatus)
	}
	if string(rangeBody) != string(payload[:10]) {
		t.Fatalf("range body = %q", rangeBody)
	}
	if got := rangeHeader.Get("Content-Range"); got != fmt.Sprintf("bytes 0-9/%d", len(payload)) {
		t.Fatalf("content-range = %q", got)
	}

	fullStatus, fullHeader, fullBody := relBytes(t, h, "GET", "/ada/assets/releases/assets/av1/blob.bin", token, "application/octet-stream", nil, nil)
	if fullStatus != http.StatusOK {
		t.Fatalf("download = %d", fullStatus)
	}
	if !bytes.Equal(fullBody, payload) {
		t.Fatal("download bytes differ")
	}
	gotSum := sha256.Sum256(fullBody)
	if "sha-256=:"+base64.StdEncoding.EncodeToString(gotSum[:])+":" != fullHeader.Get("Repr-Digest") {
		t.Fatalf("download digest = %q", fullHeader.Get("Repr-Digest"))
	}

	condStatus, _, _ := relBytes(t, h, "GET", "/ada/assets/releases/assets/av1/blob.bin", token, "application/octet-stream", map[string]string{"If-None-Match": etag}, nil)
	if condStatus != http.StatusNotModified {
		t.Fatalf("cond = %d, want 304", condStatus)
	}
}

func TestReleaseImmutableOncePublished(t *testing.T) {
	s, h, token := relSetup(t, "locked")
	work, _ := s.localRepo(t, map[string]string{"README.md": "locked\n"})
	relPush(t, s, "locked", work, "HEAD:refs/heads/main", "HEAD:refs/tags/lv1")

	status, decoded := relJSON(t, h, relCall{method: "POST", target: "/ada/locked/releases/new", auth: token, body: []byte(`{"tag":"lv1","title":"L","draft":true}`)})
	if status != http.StatusCreated {
		t.Fatalf("draft = %d: %v", status, decoded)
	}
	payload := []byte("immutable-fixture")
	putStatus, putHeader, _ := relBytes(t, h, "PUT", "/ada/locked/releases/assets/lv1/f.bin", token, "application/json",
		map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream"}, payload)
	if putStatus != http.StatusCreated {
		t.Fatalf("upload = %d", putStatus)
	}
	assetETag := putHeader.Get("ETag")

	etagStatus, etagDecoded := relJSON(t, h, relCall{method: "GET", target: "/ada/locked/releases/tag/lv1", auth: token})
	if etagStatus != http.StatusOK {
		t.Fatalf("get = %d: %v", etagStatus, etagDecoded)
	}
	release, _ := etagDecoded["release"].(map[string]any)
	draftETag, _ := release["etag"].(string)
	pubStatus, pubDecoded := relJSON(t, h, relCall{method: "POST", target: "/ada/locked/releases/edit/lv1", auth: token,
		headers: map[string]string{"If-Match": draftETag}, body: []byte(`{"draft":false}`)})
	if pubStatus != http.StatusOK {
		t.Fatalf("publish = %d: %v", pubStatus, pubDecoded)
	}

	againStatus, _, _ := relBytes(t, h, "PUT", "/ada/locked/releases/assets/lv1/f.bin", token, "application/json",
		map[string]string{"If-Match": assetETag, "Content-Type": "application/octet-stream"}, payload)
	if againStatus != http.StatusConflict {
		t.Fatalf("published asset replace = %d, want 409", againStatus)
	}
	delStatus, _, _ := relBytes(t, h, "DELETE", "/ada/locked/releases/assets/lv1/f.bin", token, "application/json",
		map[string]string{"If-Match": assetETag}, nil)
	if delStatus != http.StatusConflict {
		t.Fatalf("published asset delete = %d, want 409", delStatus)
	}
	pubRelease, _ := pubDecoded["release"].(map[string]any)
	pubETag, _ := pubRelease["etag"].(string)
	mutStatus, _ := relJSON(t, h, relCall{method: "POST", target: "/ada/locked/releases/edit/lv1", auth: token,
		headers: map[string]string{"If-Match": pubETag}, body: []byte(`{"title":"Changed"}`)})
	if mutStatus != http.StatusConflict {
		t.Fatalf("published update = %d, want 409", mutStatus)
	}
}

func TestReleasedTagFreezeRace(t *testing.T) {
	s, h, token := relSetup(t, "froze")
	work, oid := s.localRepo(t, map[string]string{"README.md": "froze\n"})
	relPush(t, s, "froze", work, "HEAD:refs/heads/main", "HEAD:refs/tags/fv1")

	release := relPublish(t, h, token, "ada/froze", "fv1", "Frozen")
	if commit, _ := release["commit"].(string); commit == "" {
		t.Fatalf("published release missing commit: %v", release)
	}

	secondCtx, secondCancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer secondCancel()
	if out, err := gitRun(secondCtx, t, work, "commit", "--allow-empty", "-m", "second"); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	out, err := gitRun(secondCtx, t, work, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse: %v\n%s", err, out)
	}
	second := strings.TrimSpace(out)
	if out, err := gitRun(secondCtx, t, work, "tag", "-f", "fv1", second); err != nil {
		t.Fatalf("tag: %v\n%s", err, out)
	}
	pushCtx, pushCancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer pushCancel()
	pushOut, pushErr := gitRun(pushCtx, t, work, "push", s.repoURL("ada", "froze", "ada", s.users["ada"].token), "+refs/tags/fv1:refs/tags/fv1")
	if pushErr == nil {
		t.Fatalf("frozen tag push succeeded: %s", pushOut)
	}

	refs := s.lsRemote(t, s.repoURL("ada", "froze", "ada", s.users["ada"].token))
	if refs["refs/tags/fv1"] != oid {
		t.Fatalf("frozen tag moved to %s, want %s", refs["refs/tags/fv1"], oid)
	}
	gotStatus, gotDecoded := relJSON(t, h, relCall{method: "GET", target: "/ada/froze/releases/tag/fv1", auth: token})
	if gotStatus != http.StatusOK {
		t.Fatalf("release get = %d: %v", gotStatus, gotDecoded)
	}
	got, _ := gotDecoded["release"].(map[string]any)
	if commit, _ := got["commit"].(string); commit != oid {
		t.Fatalf("release target = %s, want pinned %s", commit, oid)
	}

	tagStatus, tagDecoded := relJSON(t, h, relCall{method: "GET", target: "/ada/froze/tags/fv1", auth: token})
	if tagStatus != http.StatusOK {
		t.Fatalf("tag get = %d: %v", tagStatus, tagDecoded)
	}
	tagRow, _ := tagDecoded["tag"].(map[string]any)
	tagTarget, _ := tagRow["target"].(string)
	tagDelStatus, tagDelDecoded := relJSON(t, h, relCall{method: "POST", target: "/ada/froze/tags/delete/fv1", auth: token,
		headers: map[string]string{"If-Match": `"` + tagTarget + `"`}})
	if tagDelStatus == http.StatusNoContent {
		t.Fatalf("frozen tag delete succeeded: %v", tagDelDecoded)
	}
	after := s.lsRemote(t, s.repoURL("ada", "froze", "ada", s.users["ada"].token))
	if after["refs/tags/fv1"] != oid {
		t.Fatalf("frozen tag moved after delete attempt: %s", after["refs/tags/fv1"])
	}
}
