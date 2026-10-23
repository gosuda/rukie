package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/api"
	"github.com/gosuda/rukie/internal/ci"
	"github.com/gosuda/rukie/internal/e2estack"
)

func TestRunnerFetchCredentialEndToEnd(t *testing.T) {
	if os.Getenv("RUK_BIN") == "" {
		t.Skip("RUK_BIN is not set")
	}
	t.Setenv("RUK_COORDINATOR", "off")
	stack := e2estack.New(t, nil)
	admin := stack.AddAdmin(t, "ada", "repo:read", "repo:write", "ci:read", "ci:trigger")
	repoID := stack.AddRepo(t, "ada", "hello", "private")

	local := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := gitCmd(local, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-b", "main", ".")
	if err := os.WriteFile(local+"/main.go", []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "-A")
	runGit("commit", "-m", "seed")
	head := runGit("rev-parse", "HEAD")
	tree := runGit("rev-parse", "HEAD^{tree}")

	pushURL := stack.RepoURL("ada", "hello", "ada", admin.Token)
	push := gitCmd(local, "push", pushURL, "HEAD:refs/heads/main")
	if out, err := push.CombinedOutput(); err != nil {
		t.Fatalf("seed push: %v\n%s", err, out)
	}
	if _, err := e2estack.GitRun(t.Context(), t, t.TempDir(), "ls-remote", pushURL); err != nil {
		t.Skipf("daemon upload-pack advertisement unavailable, skipping live fetch proof: %v", err)
	}
	refs := stack.LsRemote(t, pushURL)
	if refs["refs/heads/main"] != head {
		t.Fatalf("daemon refs = %v, want main at %s", refs, head)
	}

	seed := sha256.Sum256([]byte("e2e-fetch-test"))
	seal := sha256.Sum256([]byte("e2e-fetch-seal"))
	c := ci.NewController(stack.Store, ci.Config{
		Now: time.Now, SignKey: ed25519.NewKeyFromSeed(seed[:]), SealKey: seal[:],
		Deliver: func(context.Context, ci.Outbox, string) (string, error) { return "live", nil },
	})
	if err := c.Recover(t.Context()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	spec, err := ci.DecodeSpec([]byte(crashSpecTOML))
	if err != nil {
		t.Fatalf("decode spec: %v", err)
	}
	run, err := c.CreateRun(t.Context(), ci.CreateRunRequest{
		RepoID: repoID, Owner: "ada", Repo: "hello", Principal: "ada",
		Subject: head, Tree: tree, BaseCommit: head, SpecFrom: "base",
		Trust: "trusted", TriggerKey: "change:e2e:fetch", Class: "interactive", Spec: spec,
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	srv := httptest.NewServer(api.Deps{CI: c, Service: stack.Auth}.CIRunnerV1(http.NotFoundHandler()))
	defer srv.Close()
	regTok, err := c.MintRegToken(t.Context(), "trusted", []string{"linux-amd64"}, 2)
	if err != nil {
		t.Fatalf("mint reg token: %v", err)
	}
	_, sealPub, serr := GenerateSealKey()
	if serr != nil {
		t.Fatalf("seal key: %v", serr)
	}
	client := NewClient(srv.URL, "", "")
	runnerID, runnerToken, rerr := client.Register(regTok, sealPub)
	if rerr != nil {
		t.Fatalf("register: %v", rerr)
	}
	client = NewClient(srv.URL, runnerID, runnerToken)
	grant, err := client.Lease(10)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if grant == nil {
		t.Fatal("lease returned no work")
	}
	if !strings.HasPrefix(grant.Fetch.Token, "rk_") {
		t.Fatalf("grant fetch token = %q, want rk_ shape", grant.Fetch.Token)
	}
	if grant.Fetch.URL != "/ada/hello.git" {
		t.Fatalf("grant fetch url = %q, want the daemon path", grant.Fetch.URL)
	}

	fetchURL := "http://" + "ada:" + grant.Fetch.Token + "@" + strings.TrimPrefix(stack.URL, "http://") + "/ada/hello.git"
	if refs := stack.LsRemote(t, fetchURL); refs["refs/heads/main"] != head {
		t.Fatalf("fetch-credential ls-remote = %v, want main at %s", refs, head)
	}
	denied := gitCmd(local, "push", fetchURL, "HEAD:refs/heads/probe")
	if out, derr := denied.CombinedOutput(); derr == nil {
		t.Fatalf("push with fetch credential succeeded: %s", out)
	}
	stack.AddRepo(t, "ada", "other", "private")
	otherURL := "http://" + "ada:" + grant.Fetch.Token + "@" + strings.TrimPrefix(stack.URL, "http://") + "/ada/other.git"
	bad := gitCmd(t.TempDir(), "ls-remote", otherURL)
	if out, berr := bad.CombinedOutput(); berr == nil {
		t.Fatalf("wrong-repo fetch with credential succeeded: %s", out)
	}

	cfg := DefaultConfig(t.TempDir())
	cfg.Server = stack.URL
	loop := &Loop{
		Config: cfg, Client: client, Exec: &fakeExec{container: &fakeContainer{}},
		Tick: 200 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	if got := loop.execute(ctx, grant); got != ExecuteDone {
		t.Fatalf("execute = %v, want done", got)
	}
	_, jobs, err := c.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("run get: %v", err)
	}
	passed := false
	for _, job := range jobs {
		if job.State == "passed" {
			passed = true
		}
	}
	if !passed {
		t.Fatalf("jobs = %+v, want a pass", jobs)
	}
}
