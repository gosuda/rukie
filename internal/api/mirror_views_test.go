package api

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

const mirrorViewCanary = "CANARY-9f3a-mirror-token-secret"

var (
	mirrorScriptRe = regexp.MustCompile(`(?i)<script`)
	mirrorOnAttrRe = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	mirrorJSURLRe  = regexp.MustCompile(`(?i)javascript:`)
	mirrorFrameRe  = regexp.MustCompile(`(?i)<(iframe|object|embed)`)
)

func assertMirrorZeroJS(t *testing.T, body string) {
	t.Helper()
	if err := zerojs.Assert([]byte(body)); err != nil {
		t.Fatalf("zero-js: %v", err)
	}
	for name, re := range map[string]*regexp.Regexp{
		"script": mirrorScriptRe, "on-attr": mirrorOnAttrRe,
		"js-url": mirrorJSURLRe, "frame": mirrorFrameRe,
	} {
		if re.MatchString(body) {
			t.Fatalf("page contains %s construct", name)
		}
	}
	if strings.Contains(body, "srcdoc=") {
		t.Fatalf("page contains srcdoc")
	}
	// The token password field is always rendered empty, so a token fed
	// through the request inputs must never surface in the page.
	if strings.Contains(body, mirrorViewCanary) {
		t.Fatalf("page carries the canary token")
	}
}

func mirrorViewCtx(path string) op.ViewCtx {
	return op.ViewCtx{
		Path: path,
		CSRF: "csrf-token",
		Input: url.Values{
			"token": {mirrorViewCanary},
		},
		Errors: []op.FieldError{{Location: "body.name", Message: "boom <x>"}},
	}
}

func mirrorSample() MirrorJSON {
	return MirrorJSON{
		ID: "01MIRROR", Repo: 7, Name: "edge", TargetURL: "https://mirror.example.test/edge.git",
		HostKind: "github", Username: "ada", Mode: "exact", RefSelection: "all",
		Globs: []string{"refs/heads/*"},
		Flags: MirrorFlagsJSON{Enabled: true, SyncOnRefUpdate: true},
		State: "failing", ConsecutiveFailures: 2,
		LastError:     MirrorErrorJSON{Class: "network", Message: "timeout", HTTPStatus: 502},
		LastSuccessAt: 3, LastAttemptAt: 4, NextRunAt: 5,
		Credential: &MirrorCredentialJSON{
			Last4: "cret", KeyVersion: 2, CreatedAt: 6, RotatedAt: 7, LastUsedAt: 8,
		},
	}
}

func TestMirrorListView(t *testing.T) {
	ok := mirrorSample()
	ok.ID, ok.Name, ok.State = "01OK", "plain", "ok"
	ok.ConsecutiveFailures, ok.LastError, ok.Credential = 0, MirrorErrorJSON{}, nil
	var b strings.Builder
	if err := writeMirrorList(&b, mirrorViewCtx("/ada/hello/settings/mirrors"), MirrorListOut{
		Mirrors: []MirrorJSON{mirrorSample(), ok},
	}); err != nil {
		t.Fatalf("list: %v", err)
	}
	body := b.String()
	assertMirrorZeroJS(t, body)
	for _, want := range []string{
		"/ada/hello/settings/mirrors/01MIRROR",
		"/ada/hello/settings/mirrors/01OK",
		"Backlog",
		"/ada/hello/settings/mirrors/01MIRROR/runs",
		"cret",
		"&lt;x&gt;",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("list missing %q", want)
		}
	}
}

func TestMirrorDetailView(t *testing.T) {
	var b strings.Builder
	out := MirrorGetOut{
		Mirror: mirrorSample(),
		Refs: []RefStateJSON{
			{Ref: "refs/heads/main", LocalOid: "aaa", RemoteOid: "bbb", Status: "synced", ReasonClass: "ok", Reason: "in sync"},
			{Ref: "refs/heads/next", LocalOid: "ccc", RemoteOid: "ddd", Status: "behind", ReasonClass: "network", Reason: "timeout"},
		},
	}
	if err := writeMirrorDetail(&b, mirrorViewCtx("/ada/hello/settings/mirrors/01MIRROR"), out); err != nil {
		t.Fatalf("detail: %v", err)
	}
	body := b.String()
	assertMirrorZeroJS(t, body)
	for _, want := range []string{
		"/ada/hello/settings/mirrors/01MIRROR/runs",
		"/ada/hello/settings/mirrors",
		"cret",
		"Key version",
		"refs/heads/main",
		"refs/heads/next",
		`type="password"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail missing %q", want)
		}
	}
}

func TestMirrorRunListView(t *testing.T) {
	var b strings.Builder
	out := MirrorRunListOut{Runs: []RunJSON{
		{ID: "01RUN1", MirrorID: 9, Kind: "sync", State: "queued", Trigger: "manual", AttemptNo: 1, StartedAt: 10},
		{ID: "01RUN2", MirrorID: 9, Kind: "reconcile", State: "running", Trigger: "schedule", AttemptNo: 2, StartedAt: 11},
	}}
	if err := writeMirrorRunList(&b, mirrorViewCtx("/ada/hello/settings/mirrors/01MIRROR/runs"), out); err != nil {
		t.Fatalf("run list: %v", err)
	}
	body := b.String()
	assertMirrorZeroJS(t, body)
	for _, want := range []string{
		"/ada/hello/settings/mirrors/01MIRROR/runs/01RUN1",
		"/ada/hello/settings/mirrors/01MIRROR/runs/01RUN2",
		"reconcile",
		"schedule",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("run list missing %q", want)
		}
	}
}

func TestMirrorRunPageView(t *testing.T) {
	var b strings.Builder
	out := MirrorRunGetOut{Run: RunJSON{
		ID: "01RUN1", MirrorID: 9, Kind: "sync", State: "succeeded", Trigger: "manual",
		AttemptNo: 1, PlanDigest: "abc",
		Outcome:   RunOutcomeJSON{Class: "ok", Summary: "all refs synced", Counts: "{}"},
		StartedAt: 10, FinishedAt: 20,
	}}
	if err := writeMirrorRunPage(&b, mirrorViewCtx("/ada/hello/settings/mirrors/01MIRROR/runs/01RUN1"), out); err != nil {
		t.Fatalf("run page: %v", err)
	}
	body := b.String()
	assertMirrorZeroJS(t, body)
	for _, want := range []string{
		"succeeded",
		"manual",
		"abc",
		"all refs synced",
		"/ada/hello/settings/mirrors/01MIRROR/runs",
		"/ada/hello/settings/mirrors/01MIRROR",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("run page missing %q", want)
		}
	}
}
