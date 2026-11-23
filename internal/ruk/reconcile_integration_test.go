//go:build integration

package ruk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

func TestDaemonPushReconcilePagination(t *testing.T) {
	const count = 501
	const tip = "1111111111111111111111111111111111111111"
	packet := func(payload string) string {
		return fmt.Sprintf("%04x%s", len(payload)+4, payload)
	}
	advertisement := []string{packet("# service=git-receive-pack\n"), "0000"}
	report := []string{packet("unpack ok\n")}
	updates := make([]PushUpdate, count)
	for i := range count {
		requested := fmt.Sprintf("refs/heads/request-%03d", i)
		reported := fmt.Sprintf("refs/heads/reported-%03d", i)
		line := tip + " " + requested
		if i == 0 {
			line += "\x00report-status-v2 delete-refs object-format=sha1"
		}
		advertisement = append(advertisement, packet(line+"\n"))
		report = append(report, packet("ok "+requested+"\n"), packet("option refname "+reported+"\n"))
		expected := PresentExpect(tip)
		updates[i] = PushUpdate{
			RemoteRef: requested, Source: DeleteSource(), ExpectedOld: &expected,
		}
	}
	advertisement = append(advertisement, "0000")
	report = append(report, "0000")
	advertisedBody := strings.Join(advertisement, "")
	reportedBody := strings.Join(report, "")
	var discoveries, sends atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repo.git/info/refs" &&
			r.URL.Query().Get("service") == "git-receive-pack":
			discoveries.Add(1)
			w.Header().Set("Content-Type", "application/x-git-receive-pack-advertisement")
			if _, err := io.WriteString(w, advertisedBody); err != nil {
				t.Errorf("advertise: %v", err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/repo.git/git-receive-pack":
			sends.Add(1)
			body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
			if err != nil || len(body) > 1<<20 {
				t.Errorf("read bounded push: %v, bytes %d", err, len(body))
				http.Error(w, "invalid push", http.StatusBadRequest)
				return
			}
			if got := strings.Count(string(body), " refs/heads/request-"); got != count {
				t.Errorf("push contains %d commands, want %d", got, count)
				http.Error(w, "wrong commands", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/x-git-receive-pack-result")
			if _, err := io.WriteString(w, reportedBody); err != nil {
				t.Errorf("push report: %v", err)
			}
		default:
			t.Errorf("unexpected remote request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(remote.Close)
	d := spawnDaemon(t, currentUID(t), "127.0.0.1/32")
	repoDir := filepath.Join(filepath.Dir(d.socket), "roots", "paged")
	seedStore(t, repoDir)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var pageCalls atomic.Int64
	client, err := Dial(ctx, d.socket, WithHooks(Hooks{OnCall: func(event CallEvent) {
		if event.Method == string(wire.MethodPushReconcile) {
			pageCalls.Add(1)
		}
	}}))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("close client: %v", closeErr)
		}
	})
	repo, err := client.OpenRepo(ctx, filepath.Join(repoDir, ".ruk"))
	if err != nil {
		t.Fatalf("open repository: %v", err)
	}
	args := PushArgs{RemoteURL: remote.URL + "/repo.git", Updates: updates}
	pushed, err := Push(ctx, client, repo, args, wire.TransportAuth{})
	if err != nil {
		t.Fatalf("rewritten push: %v\n%s", err, d.logs.String())
	}
	if pushed.Plan == "" || pushed.State != "uncertain" || len(pushed.Refs) != count {
		t.Fatalf("rewritten push returned plan %q, state %q, refs %d", pushed.Plan, pushed.State, len(pushed.Refs))
	}
	if _, guardErr := Push(ctx, client, repo, args, wire.TransportAuth{}); !errors.Is(guardErr, ErrPushUncertain) {
		t.Fatalf("unreconciled push guard: %v", guardErr)
	}
	before := discoveries.Load()
	reconcileArgs := PushReconcileArgs{Plan: pushed.Plan}
	firstPage, err := CallAuth[PushReconcileArgs, PushReconcileReply](ctx, client, repo, wire.MethodPushReconcile,
		reconcileArgs, wire.TransportAuth{})
	if err != nil {
		t.Fatalf("first actual daemon page: %v\n%s", err, d.logs.String())
	}
	if firstPage.Next == nil || firstPage.Next.Page != 1 {
		t.Fatalf("first page lacks its continuation: %+v", firstPage.Next)
	}
	if _, guardErr := Push(ctx, client, repo, args, wire.TransportAuth{}); !errors.Is(guardErr, ErrPushUncertain) {
		t.Fatalf("push guard between recovery pages: %v", guardErr)
	}
	lastPage, err := CallAuth[PushReconcileArgs, PushReconcileReply](ctx, client, repo, wire.MethodPushReconcile,
		PushReconcileArgs{Plan: pushed.Plan, Cursor: firstPage.Next}, wire.TransportAuth{})
	if err != nil {
		t.Fatalf("last actual daemon page: %v\n%s", err, d.logs.String())
	}
	if lastPage.Next != nil || pageCalls.Load() != 2 || discoveries.Load()-before != 1 || sends.Load() != 1 {
		t.Fatalf("individual pages returned next %+v, calls %d, discoveries %d, sends %d",
			lastPage.Next, pageCalls.Load(), discoveries.Load()-before, sends.Load())
	}
	before = discoveries.Load()
	beforePages := pageCalls.Load()
	reply, err := PushReconcile(ctx, client, repo, reconcileArgs, wire.TransportAuth{})
	if err != nil {
		t.Fatalf("collect actual daemon pages: %v\n%s", err, d.logs.String())
	}
	if reply.Plan != pushed.Plan || reply.State != "uncertain" || !reply.Rewritten || reply.Next != nil {
		t.Fatalf("reconciled plan %q, state %q, rewritten %v, next %+v", reply.Plan, reply.State, reply.Rewritten, reply.Next)
	}
	if len(reply.Observations) != 2*count || len(reply.ReportedObservations) != count {
		t.Fatalf("collected %d original and %d reported rows, want %d and %d",
			len(reply.Observations), len(reply.ReportedObservations), 2*count, count)
	}
	if pageCalls.Load()-beforePages != 2 || discoveries.Load()-before != 1 || sends.Load() != 1 {
		t.Fatalf("recovery used %d pages, %d discoveries, %d sends",
			pageCalls.Load()-beforePages, discoveries.Load()-before, sends.Load())
	}
	observed := make(map[string]ReconcileObservation, len(reply.Observations))
	for _, observation := range reply.Observations {
		observed[observation.RemoteRef] = observation
	}
	for i := range count {
		requested := fmt.Sprintf("refs/heads/request-%03d", i)
		reported := fmt.Sprintf("refs/heads/reported-%03d", i)
		original := observed[requested]
		if original.Verdict != "equal_old" || original.Observed == nil || *original.Observed != tip {
			t.Fatalf("requested comparison %q: %+v", requested, original)
		}
		alternate := observed[reported]
		if alternate.Verdict != "equal_new" || alternate.Observed != nil {
			t.Fatalf("reported destination %q: %+v", reported, alternate)
		}
		comparison := reply.ReportedObservations[i]
		if comparison.RemoteRef != reported || comparison.Old == nil || *comparison.Old != tip ||
			comparison.New != nil || comparison.Observed != nil || comparison.Verdict != "equal_new" {
			t.Fatalf("reported comparison %d: %+v", i, comparison)
		}
	}
	retried, err := Push(ctx, client, repo, args, wire.TransportAuth{})
	if err != nil {
		t.Fatalf("push after complete recovery: %v\n%s", err, d.logs.String())
	}
	if retried.Plan == "" || retried.Plan == pushed.Plan || sends.Load() != 2 {
		t.Fatalf("completed recovery kept the old guard: plan %q, sends %d", retried.Plan, sends.Load())
	}
}
