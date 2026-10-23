package runner

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeContainer struct {
	code   int
	delay  time.Duration
	killed atomic.Bool
}

func (c *fakeContainer) Wait() int {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	return c.code
}

func (c *fakeContainer) Kill() error {
	c.killed.Store(true)
	return nil
}

func (c *fakeContainer) Name() string { return "fake" }

type fakeExec struct {
	container *fakeContainer
}

func (e *fakeExec) Start(_ []string, _ time.Duration) (Container, error) {
	return e.container, nil
}

type flakyHeartbeat struct {
	base  http.RoundTripper
	calls atomic.Int64
	fails int64
}

func (f *flakyHeartbeat) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.HasSuffix(req.URL.Path, "/heartbeat") {
		n := f.calls.Add(1)
		if n <= f.fails {
			return &http.Response{
				Status: "500 Internal Server Error", StatusCode: 500, Body: http.NoBody,
				Header: make(http.Header), Request: req,
			}, nil
		}
	}
	return f.base.RoundTrip(req)
}

func TestLoopRidesOutHeartbeatErrors(t *testing.T) {
	clk := &crashClock{now: time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)}
	c, svc := openCrashCI(t, clk)
	run := crashRun(t, c, "change:loop:tip")
	srv, client, _, _ := crashServer(t, c, svc)
	_ = srv
	_ = run
	grant := mustHTTPLease(t, client)
	src := t.TempDir() + "/src"
	mustGit(t, src, "init")
	mustGitFile(t, src, "main.go", "package main\n")
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-m", "one")
	grant.Fetch.URL = src
	grant.Fetch.Want = mustGitOut(t, src, "rev-parse", "HEAD")
	grant.Fetch.ExpectTree = mustGitOut(t, src, "rev-parse", "HEAD^{tree}")
	grant.Job.Caches = []Cache{{Name: "go", Path: "/cache/go", Mode: "exclusive"}}
	flaky := &flakyHeartbeat{base: http.DefaultTransport, fails: 1}
	client.HTTP = &http.Client{Timeout: 40 * time.Second, Transport: flaky}
	state := t.TempDir()
	loop := &Loop{
		Config: DefaultConfig(state),
		Client: client,
		Exec:   &fakeExec{container: &fakeContainer{delay: 150 * time.Millisecond}},
		Tick:   20 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if got := loop.execute(ctx, grant); got != ExecuteDone {
		t.Fatalf("execute = %v, want done despite one failed heartbeat", got)
	}
	if flaky.calls.Load() < 2 {
		t.Fatalf("heartbeats seen = %d, want at least 2", flaky.calls.Load())
	}
	left, lerr := os.ReadDir(state + "/attempts")
	if lerr != nil || len(left) != 0 {
		t.Fatalf("attempt dir remains: %v %v", left, lerr)
	}
	remains := []string{}
	_ = filepath.WalkDir(state, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.Name() == ".inflight" {
			remains = append(remains, path)
		}
		return nil
	})
	if len(remains) != 0 {
		t.Fatalf("inflight markers remain: %v", remains)
	}
	_, jobs, err := c.Run(t.Context(), run.ID)
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
		t.Fatalf("jobs = %+v, want passed", jobs)
	}
}

func TestFinishDowngradesOversizedArtifact(t *testing.T) {
	seen := map[string]string{}
	server := stubServer(t, stubGrant{attempt: "att_1", fence: 7, token: "tok"}, &seen)
	defer server.Close()
	client := NewClient(server.URL, "rnr_1", "runner-token")
	work := t.TempDir()
	big, err := os.Create(filepath.Join(work, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(ArtifactMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	grant := &Grant{
		Attempt: AttemptRef{ID: "att_1", Fence: 7, Token: "tok"},
		Job:     JobPayload{Artifacts: []string{"big.bin"}},
	}
	stream := &logStreamer{client: client, attempt: grant.Attempt, logDir: t.TempDir(), seq: -1}
	l := &Loop{Client: client}
	l.finish(context.Background(), AttemptDone{
		Grant: grant, Stream: stream, Outcome: "passed", Class: "", Exit: 0,
		Work: AttemptWork{Dir: t.TempDir(), Work: work, Log: t.TempDir()},
	})
	if !strings.Contains(seen["finish-body"], `"outcome":"failed"`) {
		t.Fatalf("finish body = %s, want failed outcome", seen["finish-body"])
	}
	if !strings.Contains(seen["finish-body"], `"class":"script_failure"`) {
		t.Fatalf("finish body = %s, want script_failure class", seen["finish-body"])
	}
}
