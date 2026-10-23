//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/mirror"
	"github.com/gosuda/rukie/internal/store"
)

const mirrorBase = "/ada/hello/settings/mirrors"
const mirrorCanary = "CANARY-mirror-token-never-persist-plaintext-7a93"

type mirrorGate struct {
	s     *stack
	h     *apiHarness
	token string
	work  string
	oid   string
}

func newMirrorGate(t *testing.T) *mirrorGate {
	t.Helper()
	s := newStackWithConfig(t, nil, "egress_allow = [\"127.0.0.0/8\", \"::1/128\"]\n", 0)
	h := mountAPI(t, s)
	s.addAdmin(t, "ada", "repo:read", "repo:write")
	session := apiLogin(t, h, "ada", "long-test-password-01")
	token := apiToken(t, h, session, "repo:create repo:read repo:write mirrors:read mirrors:trigger")
	seedF1Repo(t, s, "ada", "hello", "private")
	work, oid := s.localRepo(t, map[string]string{"README.md": "mirror fixture\n"})
	g := &mirrorGate{s: s, h: h, token: token, work: work, oid: oid}
	g.git(t, "push", s.repoURL("ada", "hello", "ada", s.users["ada"].token), "HEAD:refs/heads/main")
	return g
}

func (g *mirrorGate) git(t *testing.T, args ...string) string {
	t.Helper()
	out, err := gitRun(t.Context(), t, g.work, args...)
	mirrorNoError(t, err, "git: %s", out)
	return strings.TrimSpace(out)
}

func (g *mirrorGate) worker() mirror.Worker {
	return mirror.Worker{Store: g.s.st, Queue: g.h.queue, Ruk: g.s.client, SealKey: g.h.sealKey, Clock: time.Now, RepoRoot: g.s.roots}
}

func mirrorBody(t *testing.T, body api.MirrorCreateBody) string {
	t.Helper()
	b, err := jsonv2.Marshal(body, jsonv1.DefaultOptionsV1())
	mirrorNoError(t, err)
	return string(b)
}

func (g *mirrorGate) create(t *testing.T, f *mirrorFake, mode, selection string, globs []string) string {
	t.Helper()
	no := false
	body := api.MirrorCreateBody{Name: "gate", TargetURL: f.server.URL + "/repo.git", HostKind: "generic", Username: "mirror", Token: mirrorCanary, Mode: mode, RefSelection: selection, Globs: globs, AdoptExistingRefs: true, SyncOnRefUpdate: &no}
	status, out := apiCall(t, g.h, "POST", mirrorBase, g.token, mirrorBody(t, body))
	mirrorEqual(t, http.StatusCreated, status, "create: %v", out)
	encoded, err := jsonv2.Marshal(out, jsonv1.DefaultOptionsV1())
	mirrorNoError(t, err)
	mirrorNotContains(t, string(encoded), mirrorCanary)
	t.Logf("mirror create probe: %v", out["probe"])
	row, ok := out["mirror"].(map[string]any)
	mirrorTrue(t, ok)
	id, ok := row["id"].(string)
	mirrorTrue(t, ok)
	return id
}

func (g *mirrorGate) run(t *testing.T, id string) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		_, err := g.worker().DriveOnce(ctx, "e2e-mirror")
		mirrorNoError(t, err)
		status, out := apiCall(t, g.h, "GET", mirrorBase+"/"+id+"/runs", g.token, "")
		mirrorEqual(t, http.StatusOK, status, "runs: %v", out)
		runs, ok := out["runs"].([]any)
		mirrorTrue(t, ok)
		if len(runs) > 0 {
			run, ok := runs[len(runs)-1].(map[string]any)
			mirrorTrue(t, ok)
			pub, ok := run["id"].(string)
			mirrorTrue(t, ok)
			status, detail := apiCall(t, g.h, "GET", mirrorBase+"/"+id+"/runs/"+pub, g.token, "")
			mirrorEqual(t, http.StatusOK, status)
			got, ok := detail["run"].(map[string]any)
			mirrorTrue(t, ok)
			switch got["state"] {
			case "ok", "succeeded", "failed", "partial", "cancelled":
				t.Logf("mirror run: %v", got)
				return got
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("mirror run did not terminate: %v", ctx.Err())
	return nil
}

func (g *mirrorGate) get(t *testing.T, id string) map[string]any {
	t.Helper()
	status, out := apiCall(t, g.h, "GET", mirrorBase+"/"+id+"?include_refs=true", g.token, "")
	mirrorEqual(t, http.StatusOK, status)
	return out
}

func (g *mirrorGate) sync(t *testing.T, id, mode string) {
	t.Helper()
	status, out := apiCall(t, g.h, "POST", mirrorBase+"/"+id+"/sync", g.token, fmt.Sprintf(`{"mode":%q}`, mode))
	mirrorEqual(t, http.StatusAccepted, status, "sync: %v", out)
}

func mirrorAuthURL(f *mirrorFake) string {
	u, _ := url.Parse(f.server.URL + "/repo.git")
	u.User = url.UserPassword("mirror", mirrorCanary)
	return u.String()
}

func TestMirrorHonorsRetryAfter(t *testing.T) {
	g := newMirrorGate(t)
	f := newMirrorFake(t, "authed", "mirror", mirrorCanary)
	f.rateLimit = 2
	f.retryAfter = 120
	id := g.create(t, f, "fast-forward-only", "all", nil)
	before := time.Now()
	run := g.run(t, id)
	mirrorEqual(t, "failed", run["state"], "%v", run)
	outcome, ok := run["outcome"].(map[string]any)
	mirrorTrue(t, ok)
	mirrorEqual(t, "rate_limited", outcome["class"], "%v", run)
	out := g.get(t, id)
	row, ok := out["mirror"].(map[string]any)
	mirrorTrue(t, ok)
	mirrorEqual(t, "failing", row["state"])
	next, ok := row["next_run_at"].(float64)
	mirrorTrue(t, ok)
	mirrorGreaterOrEqual(t, int64(next), before.Add(120*time.Second).UnixMilli())
	mirrorGreaterOrEqual(t, f.posts.Load(), int64(1))
	mirrorLessOrEqual(t, f.posts.Load(), int64(2))
}

func TestMirrorCredentialCanary(t *testing.T) {
	g := newMirrorGate(t)
	f := newMirrorFake(t, "authed", "mirror", mirrorCanary)
	id := g.create(t, f, "fast-forward-only", "all", nil)
	run := g.run(t, id)
	for _, path := range []string{mirrorBase, mirrorBase + "/" + id, mirrorBase + "/" + id + "/runs"} {
		status, out := apiCall(t, g.h, "GET", path, g.token, "")
		mirrorEqual(t, http.StatusOK, status)
		b, err := jsonv2.Marshal(out, jsonv1.DefaultOptionsV1())
		mirrorNoError(t, err)
		mirrorNotContains(t, string(b), mirrorCanary)
	}
	mirrorNoError(t, filepath.WalkDir(g.s.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk credential files: %w", err)
		}
		if d.IsDir() || !strings.Contains(d.Name(), "sqlite") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return fmt.Errorf("read credential file: %w", rerr)
		}
		mirrorNotContains(t, string(b), mirrorCanary, "plaintext in %s", path)
		return nil
	}))
	var row store.MirrorRow
	var cred store.CredentialRow
	mirrorNoError(t, g.s.st.Read(t.Context(), func(tx *store.ReadTx) error {
		var err error
		row, err = tx.GetMirrorByPublicID(id)
		if err != nil {
			return err
		}
		cred, err = tx.GetCredential(row.ID)
		if err != nil {
			return err
		}
		rows, err := tx.Outbox().After("mirror-canary", 1000)
		if err != nil {
			return err
		}
		b, err := jsonv2.Marshal(rows, jsonv1.DefaultOptionsV1())
		mirrorNoError(t, err)
		mirrorNotContains(t, string(b), mirrorCanary)
		return nil
	}))
	plain, err := mirror.Open(g.h.sealKey, mirror.SealedCredential{KeyVersion: uint32(cred.KeyVersion), Nonce: cred.Nonce, Ciphertext: cred.Ciphertext}, "mirror:"+id)
	mirrorNoError(t, err)
	mirrorEqual(t, mirrorCanary, string(plain))
	mirror.Zero(plain)
	encodedRun, err := jsonv2.Marshal(run, jsonv1.DefaultOptionsV1())
	mirrorNoError(t, err)
	mirrorNotContains(t, string(encodedRun), mirrorCanary)
}

func TestMirrorCodebergNeedsPolicyAck(t *testing.T) {
	g := newMirrorGate(t)
	no := false
	body := api.MirrorCreateBody{Name: "codeberg", TargetURL: "https://codeberg.org/mirror/repo.git", HostKind: "forgejo", Username: "mirror", Token: mirrorCanary, SyncOnRefUpdate: &no}
	status, out := apiCall(t, g.h, "POST", mirrorBase, g.token, mirrorBody(t, body))
	mirrorEqual(t, http.StatusUnprocessableEntity, status, "without acknowledgement: %v", out)
	mirrorContains(t, fmt.Sprint(out), "mirror configuration is not valid")
	body.PolicyAck = true
	status, out = apiCall(t, g.h, "POST", mirrorBase, g.token, mirrorBody(t, body))
	mirrorEqual(t, http.StatusCreated, status, "with acknowledgement: %v", out)
}

func mirrorKillReconcile(t *testing.T) {
	g := newMirrorGate(t)
	f := newMirrorFake(t, "authed", "mirror", mirrorCanary)
	f.hold = true
	id := g.create(t, f, "fast-forward-only", "all", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	driven := make(chan error, 1)
	go func() {
		for ctx.Err() == nil {
			_, err := g.worker().DriveOnce(ctx, "e2e-mirror")
			if err != nil {
				driven <- err
				return
			}
			var terminal string
			err = g.s.st.Read(ctx, func(tx *store.ReadTx) error {
				row, rerr := tx.GetMirrorByPublicID(id)
				if rerr != nil {
					return rerr
				}
				runs, _, lerr := tx.ListRuns(row.ID, "", "", 100)
				if lerr != nil {
					return lerr
				}
				for _, run := range runs {
					if run.State == "failed" || run.State == "succeeded" || run.State == "partial" {
						terminal = run.OutcomeSummary
					}
				}
				return nil
			})
			if err != nil {
				driven <- err
				return
			}
			if terminal != "" {
				driven <- fmt.Errorf("mirror terminated before kill: %s", terminal)
				return
			}
			select {
			case <-ctx.Done():
			case <-time.After(20 * time.Millisecond):
			}
		}
		driven <- ctx.Err()
	}()
	select {
	case <-f.entered:
	case err := <-driven:
		t.Fatalf("mirror never reached receive-pack POST: worker=%v; mirror=%v", err, g.get(t, id))
	case <-ctx.Done():
		t.Fatalf("waiting for receive-pack POST: %v", ctx.Err())
	}
	_ = g.s.child.Kill()
	close(f.release)
	select {
	case derr := <-driven:
		t.Logf("mirror worker stopped: %v", derr)
	case <-ctx.Done():
		t.Fatalf("worker did not stop after kill: %v", ctx.Err())
	}
	restartDaemon(t, g.s)
	before := f.posts.Load()
	out := g.get(t, id)
	mirrorContains(t, fmt.Sprint(out), "uncertain", "after kill: %v", out)
	g.sync(t, id, "reconcile")
	run := g.run(t, id)
	mirrorEqual(t, "succeeded", run["state"], "reconcile: %v", run)
	mirrorEqual(t, g.oid, f.refs(t)["refs/heads/main"])
	mirrorLessOrEqual(t, f.posts.Load()-before, int64(1), "reconcile double-applied a push")
}

func mirrorAgentFlow(t *testing.T) {
	t.Run("exact_prune", func(t *testing.T) {
		g := newMirrorGate(t)
		f := newMirrorFake(t, "authed", "mirror", mirrorCanary)
		remote, _ := g.s.localRepo(t, map[string]string{"remote": "diverged\n"})
		out, err := gitRun(t.Context(), t, remote, "push", mirrorAuthURL(f), "HEAD:refs/heads/main", "HEAD:refs/heads/deleted")
		mirrorNoError(t, err, "%s", out)
		out, err = gitRun(t.Context(), t, f.root, "--git-dir="+filepath.Join(f.root, "repo.git"), "update-ref", "refs/pull/1/head", strings.TrimSpace(gitOutput(t, remote, "rev-parse", "HEAD")))
		mirrorNoError(t, err, "%s", out)
		hidden := f.refs(t)["refs/pull/1/head"]
		g.git(t, "branch", "feature")
		g.git(t, "tag", "-a", "v1", "-m", "annotated")
		g.git(t, "push", g.s.repoURL("ada", "hello", "ada", g.s.users["ada"].token), "refs/heads/feature", "refs/tags/v1")
		g.git(t, "branch", "temporary")
		g.git(t, "push", g.s.repoURL("ada", "hello", "ada", g.s.users["ada"].token), "refs/heads/temporary")
		g.git(t, "push", g.s.repoURL("ada", "hello", "ada", g.s.users["ada"].token), ":refs/heads/temporary")
		id := g.create(t, f, "exact", "all", nil)
		initial := g.run(t, id)
		mirrorEqual(t, "partial", initial["state"], "confirmation run: %v", initial)
		digest, ok := initial["plan_digest"].(string)
		mirrorTrue(t, ok)
		mirrorNotEmpty(t, digest)
		status, detail := apiCall(t, g.h, "POST", mirrorBase+"/"+id+"/sync", g.token, fmt.Sprintf(`{"confirm_digest":%q}`, digest))
		mirrorEqual(t, http.StatusAccepted, status, "confirmed sync: %v", detail)
		run := g.run(t, id)
		mirrorEqual(t, "succeeded", run["state"], "%v", run)
		mirrorEqual(t, map[string]string{"refs/heads/main": g.oid, "refs/heads/feature": g.oid, "refs/tags/v1": g.git(t, "rev-parse", "refs/tags/v1"), "refs/pull/1/head": hidden}, f.refs(t))
	})
	t.Run("ff_only_refusal", func(t *testing.T) {
		g := newMirrorGate(t)
		f := newMirrorFake(t, "authed", "mirror", mirrorCanary)
		work, oid := g.s.localRepo(t, map[string]string{"remote": "unrelated\n"})
		out, err := gitRun(t.Context(), t, work, "push", mirrorAuthURL(f), "HEAD:refs/heads/main")
		mirrorNoError(t, err, "%s", out)
		id := g.create(t, f, "fast-forward-only", "all", nil)
		run := g.run(t, id)
		mirrorContains(t, fmt.Sprint(g.get(t, id)), "diverged", "%v", run)
		mirrorEqual(t, oid, f.refs(t)["refs/heads/main"])
	})
	t.Run("globs", func(t *testing.T) {
		g := newMirrorGate(t)
		f := newMirrorFake(t, "authed", "mirror", mirrorCanary)
		g.git(t, "push", g.s.repoURL("ada", "hello", "ada", g.s.users["ada"].token), "HEAD:refs/heads/release-one", "HEAD:refs/heads/private")
		id := g.create(t, f, "fast-forward-only", "globs", []string{"refs/heads/release-*"})
		run := g.run(t, id)
		mirrorEqual(t, "succeeded", run["state"], "%v", run)
		mirrorEqual(t, map[string]string{"refs/heads/release-one": g.oid}, f.refs(t))
	})
	t.Run("reconcile_restore", mirrorKillReconcile)
	t.Run("stops_on_401", func(t *testing.T) {
		g := newMirrorGate(t)
		f := newMirrorFake(t, "always401", "mirror", mirrorCanary)
		id := g.create(t, f, "fast-forward-only", "all", nil)
		run := g.run(t, id)
		row := g.get(t, id)
		got, ok := row["mirror"].(map[string]any)
		mirrorTrue(t, ok, "%v", row)
		mirrorEqual(t, "auth_failed", got["state"], "%v", run)
		mirrorEqual(t, "auth", got["last_error"].(map[string]any)["class"], "%v", run)
		mirrorEqual(t, float64(0), got["next_run_at"], "%v", run)
		for range 3 {
			_, err := g.worker().DriveOnce(t.Context(), "e2e-mirror")
			mirrorNoError(t, err)
		}
		mirrorZero(t, f.posts.Load())
	})
}

func mirrorNoError(t *testing.T, err error, detail ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v; detail: %v", err, detail)
	}
}

func mirrorTrue(t *testing.T, ok bool, detail ...any) {
	t.Helper()
	if !ok {
		t.Fatalf("assertion failed: %v", detail)
	}
}

func mirrorEqual(t *testing.T, want, got any, detail ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("got %#v, want %#v; detail: %v", got, want, detail)
	}
}

func mirrorNotContains(t *testing.T, text, secret string, detail ...any) {
	t.Helper()
	if strings.Contains(text, secret) {
		t.Fatalf("unexpected secret occurrence; detail: %v", detail)
	}
}

func mirrorContains(t *testing.T, text, want string, detail ...any) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("missing %q in %s; detail: %v", want, text, detail)
	}
}

func mirrorNotEmpty(t *testing.T, text string) {
	t.Helper()
	if text == "" {
		t.Fatal("expected nonempty string")
	}
}

func mirrorGreaterOrEqual(t *testing.T, got, want int64) {
	t.Helper()
	if got < want {
		t.Fatalf("got %d, want >= %d", got, want)
	}
}

func mirrorLessOrEqual(t *testing.T, got, want int64, detail ...any) {
	t.Helper()
	if got > want {
		t.Fatalf("got %d, want <= %d; detail: %v", got, want, detail)
	}
}

func mirrorZero(t *testing.T, got int64) {
	t.Helper()
	if got != 0 {
		t.Fatalf("got %d, want zero", got)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitRun(t.Context(), t, dir, args...)
	mirrorNoError(t, err, "%s", out)
	return out
}
