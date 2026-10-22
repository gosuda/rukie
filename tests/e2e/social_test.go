//go:build e2e

package e2e

import (
	"context"
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/forks"
)

// The social and explore surface is bound through these helpers only. Each
// helper names one route or one JSON field so a schema change stays a single
// edit, and the tests below assert behavior rather than shape.

// JSON operation paths, per the settled usage surface. The HTML forms at
// /{owner}/{repo}/star, /unstar, /fork, /forks, /stargazers, and
// /-/explore/repos are page surfaces and are exercised by the page tests.
func forksPath(owner, repo string) string {
	return "/api/repos/" + owner + "/" + repo + "/forks"
}

func starPath(owner, repo string) string {
	return "/api/user/stars/" + owner + "/" + repo
}

func starsQuery(owner, repo string) string {
	return "/api/stars?repo=" + owner + "/" + repo
}

const exploreReposPath = "/api/explore/repos"

// socialDo issues one request and returns the status, the response headers,
// and the raw body. Mutation ops answer 204 with no body, so apiCall cannot
// read them, and fork creation is asserted through its Location header.
func socialDo(t *testing.T, h *apiHarness, method, target, auth, body string) (int, http.Header, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.server.URL+target, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.HasPrefix(auth, "Bearer ") {
		req.Header.Set("Authorization", auth)
	} else if auth != "" {
		req.Header.Set("Cookie", auth)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	const maxBody = 1 << 20
	var raw strings.Builder
	if _, cerr := io.Copy(&raw, io.LimitReader(resp.Body, maxBody+1)); cerr != nil {
		t.Fatalf("read body: %v", cerr)
	}
	if int64(raw.Len()) > maxBody {
		t.Fatalf("response body exceeds %d bytes", maxBody)
	}
	return resp.StatusCode, resp.Header, raw.String()
}

// socialJSON decodes a body the caller already knows is JSON.
func socialJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := jsonv2.Unmarshal([]byte(raw), &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return decoded
}

// createFork posts one repo.fork.create and requires the accepted reply: 202,
// a Location header naming the new repo, and a forking repo object.
func createFork(t *testing.T, h *apiHarness, owner, repo, auth, body string) map[string]any {
	t.Helper()
	status, header, raw := socialDo(t, h, "POST", forksPath(owner, repo), auth, body)
	if status != http.StatusAccepted {
		t.Fatalf("repo.fork.create %s/%s = %d, want 202: %s", owner, repo, status, raw)
	}
	created := repoOf(t, socialJSON(t, raw))
	destOwner, _ := created["owner"].(string)
	name, _ := created["name"].(string)
	if loc := header.Get("Location"); loc != "/"+destOwner+"/"+name {
		t.Fatalf("fork Location = %q, want /%s/%s", loc, destOwner, name)
	}
	if got := repoState(t, created); got != "forking" {
		t.Fatalf("fresh fork state = %q, want forking", got)
	}
	return created
}

func repoOf(t *testing.T, decoded map[string]any) map[string]any {
	t.Helper()
	repo, ok := decoded["repo"].(map[string]any)
	if !ok {
		t.Fatalf("response carries no repo object: %v", decoded)
	}
	return repo
}

func repoState(t *testing.T, repo map[string]any) string {
	t.Helper()
	state, _ := repo["state"].(string)
	if state == "" {
		t.Fatalf("repo carries no state: %v", repo)
	}
	return state
}

// starCount reads the star counter that repo.get carries beside the repo
// object. The field is omitzero, so an unstarred repo reports it as absent.
func starCount(t *testing.T, decoded map[string]any) int {
	t.Helper()
	raw, ok := decoded["stars"].(float64)
	if !ok {
		return 0
	}
	return int(raw)
}

func starEntries(t *testing.T, decoded map[string]any) []any {
	t.Helper()
	raw, ok := decoded["stars"].([]any)
	if !ok {
		t.Fatalf("response carries no stars list: %v", decoded)
	}
	return raw
}

func exploreRepos(t *testing.T, decoded map[string]any) []any {
	t.Helper()
	raw, ok := decoded["repos"].([]any)
	if !ok {
		t.Fatalf("explore response carries no repos list: %v", decoded)
	}
	return raw
}

func repoNames(t *testing.T, rows []any) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, item := range rows {
		repo, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("explore row is not an object: %v", item)
		}
		owner, _ := repo["owner"].(string)
		name, _ := repo["name"].(string)
		out[owner+"/"+name] = true
	}
	return out
}

// forkWorker is the production repo_fork worker, built the way cmd/rukie wires
// it. Driving it here exercises the real queue and the real state transition;
// the harness runs no background workers of its own.
func forkWorker(s *stack, h *apiHarness) forks.Worker {
	return forks.Worker{Store: s.st, Queue: h.queue, Clock: time.Now}
}

// waitRepoActive drives the fork worker and polls repo.get until the fork
// reports state active. Both the worker ticks and the reads are bounded, with
// exponential backoff and jitter, so a stuck fork fails fast.
func waitRepoActive(t *testing.T, s *stack, h *apiHarness, owner, repo, auth string) map[string]any {
	t.Helper()
	const (
		maxAttempts = 20
		startWait   = 25 * time.Millisecond
		maxWait     = 400 * time.Millisecond
		budget      = 20 * time.Second
	)
	deadline := time.Now().Add(budget)
	state := "unknown"
	status := 0
	wait := startWait
	worker := forkWorker(s, h)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		// One tick gets its own deadline, so a blocked queue call cannot
		// outlive the readiness budget.
		tickCtx, cancel := context.WithTimeout(t.Context(), min(time.Until(deadline), 10*time.Second))
		_, werr := worker.DriveOnce(tickCtx, "e2e-fork")
		cancel()
		if werr != nil {
			t.Fatalf("fork worker tick %d: %v", attempt, werr)
		}
		var decoded map[string]any
		var raw string
		status, _, raw = socialDo(t, h, "GET", "/"+owner+"/"+repo, auth, "")
		if status == http.StatusOK {
			decoded = socialJSON(t, raw)
			state = repoState(t, repoOf(t, decoded))
			if state == "active" {
				return decoded
			}
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(wait + time.Duration(rand.Int64N(int64(wait/2)+1)))
		if wait < maxWait {
			wait *= 2
		}
	}
	t.Fatalf("fork %s/%s never became active after %d polls; last status %d state %q", owner, repo, maxAttempts, status, state)
	return nil
}

// seedSocialRepo creates a metadata row and an empty Ruk store for one owner.
func seedSocialRepo(t *testing.T, s *stack, owner, name, visibility string) int64 {
	t.Helper()
	return seedF1Repo(t, s, owner, name, visibility)
}

// pushSocialContent fills a repo through Smart HTTP with two branches and one
// annotated tag, so a fork has heads and tags to copy.
func pushSocialContent(t *testing.T, s *stack, owner, repo, user, token string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	work, mainOID := s.localRepo(t, map[string]string{"README.md": "social fixture\n"})
	if out, err := gitRun(ctx, t, work, "tag", "-a", "v1.0.0", "-m", "first"); err != nil {
		t.Fatalf("tag: %v\n%s", err, out)
	}
	if out, err := gitRun(ctx, t, work, "checkout", "-b", "side"); err != nil {
		t.Fatalf("checkout side: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(work, "SIDE.md"), []byte("side\n"), 0o644); err != nil {
		t.Fatalf("write side: %v", err)
	}
	if out, err := gitRun(ctx, t, work, "add", "-A"); err != nil {
		t.Fatalf("add side: %v\n%s", err, out)
	}
	if out, err := gitRun(ctx, t, work, "commit", "-m", "side"); err != nil {
		t.Fatalf("commit side: %v\n%s", err, out)
	}
	out, err := gitRun(ctx, t, work, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse side: %v\n%s", err, out)
	}
	sideOID := strings.TrimSpace(out)
	if out, err := gitRun(ctx, t, work, "checkout", "main"); err != nil {
		t.Fatalf("checkout main: %v\n%s", err, out)
	}
	pushURL := s.repoURL(owner, repo, user, token)
	for _, refspec := range []string{"refs/heads/main:refs/heads/main", "refs/heads/side:refs/heads/side", "refs/tags/v1.0.0:refs/tags/v1.0.0"} {
		if out, err := gitRun(ctx, t, work, "push", pushURL, refspec); err != nil {
			t.Fatalf("push %s: %v\n%s", refspec, err, out)
		}
	}
	return map[string]string{"refs/heads/main": mainOID, "refs/heads/side": sideOID}
}

func TestForkAndStarE2E(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	ada := s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addUser(t, "bob", "repo:read", "repo:create")
	bobSession := apiLogin(t, h, "bob", "long-test-password-01")

	seedSocialRepo(t, s, "ada", "hello", "public")
	want := pushSocialContent(t, s, "ada", "hello", "ada", ada.token)
	tagOID := s.lsRemote(t, s.repoURL("ada", "hello", "ada", ada.token))["refs/tags/v1.0.0"]
	if tagOID == "" {
		t.Fatal("source tag refs/tags/v1.0.0 missing after push")
	}

	forkRow := createFork(t, h, "ada", "hello", bobSession, `{"name":"hello-fork","default_branch_only":false}`)
	if got := forkRow["owner"]; got != "bob" {
		t.Fatalf("fork owner = %v, want bob", got)
	}

	// The fork is not ready until the repo_fork job completes, so repo.get must
	// still report it as forking before the worker runs.
	if status, decoded := apiCall(t, h, "GET", "/bob/hello-fork", bobSession, ""); status != http.StatusOK {
		t.Fatalf("repo.get of the new fork = %d: %v", status, decoded)
	} else if got := repoState(t, repoOf(t, decoded)); got != "forking" {
		t.Fatalf("fork state before the worker ran = %q, want forking", got)
	}

	active := waitRepoActive(t, s, h, "bob", "hello-fork", bobSession)
	if got := starCount(t, active); got != 0 {
		t.Fatalf("fresh fork star count = %d, want 0", got)
	}

	forkRefs := s.lsRemote(t, s.repoURL("bob", "hello-fork", "bob", s.users["bob"].token))
	for ref, oid := range want {
		if forkRefs[ref] != oid {
			t.Fatalf("fork %s = %q, want %q", ref, forkRefs[ref], oid)
		}
	}
	if forkRefs["refs/tags/v1.0.0"] != tagOID {
		t.Fatalf("fork tag = %q, want %q", forkRefs["refs/tags/v1.0.0"], tagOID)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if status, _, body := socialDo(t, h, "PUT", starPath("ada", "hello"), bobSession, ""); status != http.StatusNoContent {
			t.Fatalf("star.add attempt %d = %d, want 204: %s", attempt, status, body)
		}
		_, listed := apiCall(t, h, "GET", starsQuery("ada", "hello"), bobSession, "")
		entries := starEntries(t, listed)
		if got := len(entries); got != 1 {
			t.Fatalf("star.add attempt %d left %d star rows, want 1", attempt, got)
		}
		entry, ok := entries[0].(map[string]any)
		if !ok || entry["user"] != "bob" {
			t.Fatalf("star.add attempt %d recorded %v, want user bob", attempt, entries[0])
		}
		_, detail := apiCall(t, h, "GET", "/ada/hello", bobSession, "")
		if got := starCount(t, detail); got != 1 {
			t.Fatalf("star.add attempt %d set count %d, want 1", attempt, got)
		}
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if status, _, body := socialDo(t, h, "DELETE", starPath("ada", "hello"), bobSession, ""); status != http.StatusNoContent {
			t.Fatalf("star.remove attempt %d = %d, want 204: %s", attempt, status, body)
		}
		_, listed := apiCall(t, h, "GET", starsQuery("ada", "hello"), bobSession, "")
		if got := len(starEntries(t, listed)); got != 0 {
			t.Fatalf("star.remove attempt %d left %d star rows, want 0", attempt, got)
		}
		_, detail := apiCall(t, h, "GET", "/ada/hello", bobSession, "")
		if got := starCount(t, detail); got != 0 {
			t.Fatalf("star.remove attempt %d set count %d, want 0", attempt, got)
		}
	}

	_, forks := apiCall(t, h, "GET", forksPath("ada", "hello"), bobSession, "")
	rows, ok := forks["forks"].([]any)
	if !ok {
		t.Fatalf("fork list carries no forks list: %v", forks)
	}
	if len(rows) != 1 {
		t.Fatalf("fork list holds %d rows, want 1: %v", len(rows), rows)
	}
	if entry, ok := rows[0].(map[string]any); !ok || entry["name"] != "hello-fork" || entry["owner"] != "bob" {
		t.Fatalf("fork list entry = %v", rows[0])
	}

	_, anonForks := apiCall(t, h, "GET", forksPath("bob", "hello-fork"), "", "")
	if anonRows, ok := anonForks["forks"].([]any); !ok || len(anonRows) != 0 {
		t.Fatalf("a fresh fork lists children, want none: %v", anonForks)
	}
}

func TestExplorePermissionMatrix(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addUser(t, "bob", "repo:read")
	bobSession := apiLogin(t, h, "bob", "long-test-password-01")

	seedSocialRepo(t, s, "ada", "open", "public")
	seedSocialRepo(t, s, "ada", "inner", "internal")
	seedSocialRepo(t, s, "ada", "secret", "private")

	status, anonExplore := apiCall(t, h, "GET", exploreReposPath, "", "")
	if status != http.StatusOK {
		t.Fatalf("anonymous explore.repos = %d: %v", status, anonExplore)
	}
	anon := repoNames(t, exploreRepos(t, anonExplore))
	if !anon["ada/open"] {
		t.Fatalf("anonymous explore hides the public repo: %v", anon)
	}
	if anon["ada/secret"] {
		t.Fatalf("anonymous explore lists a private repo: %v", anon)
	}
	if anon["ada/inner"] {
		t.Fatalf("anonymous explore lists an internal repo: %v", anon)
	}

	var detail map[string]any
	if status, detail = apiCall(t, h, "GET", "/ada/secret", "", ""); status != http.StatusNotFound {
		t.Fatalf("anonymous private repo.get = %d, want 404: %v", status, detail)
	}
	if status, detail = apiCall(t, h, "GET", "/ada/inner", "", ""); status != http.StatusNotFound {
		t.Fatalf("anonymous internal repo.get = %d, want 404: %v", status, detail)
	}
	if status, detail = apiCall(t, h, "GET", "/ada/open", "", ""); status != http.StatusOK {
		t.Fatalf("anonymous public repo.get = %d: %v", status, detail)
	}

	if status, detail = apiCall(t, h, "GET", "/ada/secret", bobSession, ""); status != http.StatusNotFound {
		t.Fatalf("non-member private repo.get = %d, want 404: %v", status, detail)
	}
	if status, detail = apiCall(t, h, "GET", "/ada/inner", bobSession, ""); status != http.StatusOK {
		t.Fatalf("authenticated internal repo.get = %d: %v", status, detail)
	}

	status, bobExplore := apiCall(t, h, "GET", exploreReposPath, bobSession, "")
	if status != http.StatusOK {
		t.Fatalf("authenticated explore.repos = %d: %v", status, bobExplore)
	}
	authed := repoNames(t, exploreRepos(t, bobExplore))
	if !authed["ada/open"] || !authed["ada/inner"] {
		t.Fatalf("authenticated explore misses visible repos: %v", authed)
	}
	if authed["ada/secret"] {
		t.Fatalf("authenticated explore lists a private repo for a non-member: %v", authed)
	}

	adaSession := apiLogin(t, h, "ada", "long-test-password-01")
	if status, decoded := apiCall(t, h, "GET", "/ada/secret", adaSession, ""); status != http.StatusOK {
		t.Fatalf("owner private repo.get = %d: %v", status, decoded)
	}
	_, ownerExplore := apiCall(t, h, "GET", exploreReposPath, adaSession, "")
	if !repoNames(t, exploreRepos(t, ownerExplore))["ada/secret"] {
		t.Fatal("owner explore misses their own private repo")
	}
}

func TestForkVisibilityCap(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addUser(t, "bob", "repo:read", "repo:create")
	bobSession := apiLogin(t, h, "bob", "long-test-password-01")

	adaSession := apiLogin(t, h, "ada", "long-test-password-01")

	seedSocialRepo(t, s, "ada", "open", "public")
	seedSocialRepo(t, s, "ada", "inner", "internal")
	seedSocialRepo(t, s, "ada", "secret", "private")

	privateFork := createFork(t, h, "ada", "open", bobSession, `{"name":"open-private","visibility":"private"}`)
	if got := privateFork["visibility"]; got != "private" {
		t.Fatalf("fork visibility = %v, want private", got)
	}
	waitRepoActive(t, s, h, "bob", "open-private", bobSession)

	refusals := []struct {
		source     string
		name       string
		visibility string
		why        string
	}{
		{"inner", "inner-public", "public", "an internal source must not fork public"},
		{"secret", "secret-public", "public", "a private source must not fork public"},
		{"secret", "secret-internal", "internal", "a private source must not fork internal"},
	}
	for _, tc := range refusals {
		// The owner forks into her own namespace, so the only possible refusal
		// is the visibility cap: she holds the destination create right and can
		// read every source here.
		body := `{"name":"` + tc.name + `","visibility":"` + tc.visibility + `"}`
		status, _, raw := socialDo(t, h, "POST", forksPath("ada", tc.source), adaSession, body)
		if status != http.StatusForbidden {
			t.Fatalf("%s: status = %d, want 403: %s", tc.why, status, raw)
		}
		if code, _ := socialJSON(t, raw)["code"].(string); code == "" {
			t.Fatalf("%s: refusal carries no problem code: %s", tc.why, raw)
		}
		if status, decoded := apiCall(t, h, "GET", "/ada/"+tc.name, adaSession, ""); status != http.StatusNotFound {
			t.Fatalf("%s: refused fork left a visible repo (%d): %v", tc.why, status, decoded)
		}
	}

	internalFork := createFork(t, h, "ada", "inner", bobSession, `{"name":"inner-internal","visibility":"internal"}`)
	if got := internalFork["visibility"]; got != "internal" {
		t.Fatalf("internal fork visibility = %v, want internal", got)
	}
}

func TestForkTokenNamespaceOnly(t *testing.T) {
	s := newStack(t, nil)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	s.addUser(t, "bob", "repo:read", "repo:create")
	s.addUser(t, "cyd", "repo:read")
	bobSession := apiLogin(t, h, "bob", "long-test-password-01")
	bobToken := apiToken(t, h, bobSession, "repo:read repo:create")
	adaSession := apiLogin(t, h, "ada", "long-test-password-01")

	seedSocialRepo(t, s, "ada", "open", "public")

	ownFork := createFork(t, h, "ada", "open", bobToken, `{"name":"own-fork"}`)
	if got := ownFork["owner"]; got != "bob" {
		t.Fatalf("token fork owner = %v, want bob", got)
	}
	waitRepoActive(t, s, h, "bob", "own-fork", bobToken)

	status, _, raw := socialDo(t, h, "POST", forksPath("ada", "open"), bobToken, `{"name":"stolen","target_owner":"cyd"}`)
	if status != http.StatusForbidden {
		t.Fatalf("token fork into another user namespace = %d, want 403: %s", status, raw)
	}
	if code, _ := socialJSON(t, raw)["code"].(string); code == "" {
		t.Fatalf("namespace refusal carries no problem code: %s", raw)
	}
	var refused map[string]any
	if status, refused = apiCall(t, h, "GET", "/cyd/stolen", adaSession, ""); status != http.StatusNotFound {
		t.Fatalf("refused cross-user fork left a repo (%d): %v", status, refused)
	}

	if status, refused = apiCall(t, h, "POST", "/new/org", adaSession, `{"name":"acme"}`); status != http.StatusCreated {
		t.Fatalf("org.create = %d: %v", status, refused)
	}
	status, _, raw = socialDo(t, h, "POST", forksPath("ada", "open"), bobToken, `{"name":"org-fork","target_owner":"acme"}`)
	if status != http.StatusForbidden {
		t.Fatalf("token fork into an org = %d, want 403: %s", status, raw)
	}
	if code, _ := socialJSON(t, raw)["code"].(string); code == "" {
		t.Fatalf("org namespace refusal carries no problem code: %s", raw)
	}

	adaToken := apiToken(t, h, adaSession, "repo:read repo:create")
	status, _, raw = socialDo(t, h, "POST", forksPath("ada", "open"), adaToken, `{"name":"owner-org-fork","target_owner":"acme"}`)
	if status != http.StatusForbidden {
		t.Fatalf("org-owner token fork into own org = %d, want 403 (only the token-namespace guard refuses this): %s", status, raw)
	}
	if leftStatus, leftBody := apiCall(t, h, "GET", "/acme/owner-org-fork", adaSession, ""); leftStatus != http.StatusNotFound {
		t.Fatalf("refused org-owner token fork left a repo behind (%d): %v", leftStatus, leftBody)
	}

	orgFork := createFork(t, h, "ada", "open", adaSession, `{"name":"session-org","target_owner":"acme"}`)
	if got := orgFork["owner"]; got != "acme" {
		t.Fatalf("session org fork owner = %v, want acme", got)
	}
	settled := repoOf(t, waitRepoActive(t, s, h, "acme", "session-org", adaSession))
	if got := settled["owner"]; got != "acme" {
		t.Fatalf("settled org fork owner = %v, want acme", got)
	}
	if status, refused = apiCall(t, h, "GET", "/bob/session-org", adaSession, ""); status != http.StatusNotFound {
		t.Fatalf("org fork also appeared in a user namespace (%d): %v", status, refused)
	}
}
