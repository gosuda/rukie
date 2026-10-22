package api

import (
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/web/zerojs"
)

var ciMetaRefRe = regexp.MustCompile(`<meta http-equiv="refresh" content="(\d+)">`)

func assertCIZeroJS(t *testing.T, body string) {
	t.Helper()
	if err := zerojs.Assert([]byte(body)); err != nil {
		t.Fatalf("zero-js: %v", err)
	}
}

func TestMatchCIPage(t *testing.T) {
	cases := []struct {
		path  string
		owner string
		repo  string
		run   string
		job   string
		kind  ciPageKind
	}{
		{path: "/ada/hello/ci", kind: ciPageRunsTab, owner: "ada", repo: "hello"},
		{path: "/ada/hello/ci/badge.svg", kind: ciPageBadge, owner: "ada", repo: "hello"},
		{path: "/ada/hello/ci/runs/42/jobs/build", kind: ciPageRunJob, owner: "ada", repo: "hello", run: "42", job: "build"},
		{path: "/ada/hello/ci/runs/42/jobs/build/log", kind: ciPageJobLog, owner: "ada", repo: "hello", run: "42", job: "build"},
	}
	for _, tc := range cases {
		kind, owner, repo, run, job, ok := matchCIPage(tc.path)
		if !ok || kind != tc.kind || owner != tc.owner || repo != tc.repo || run != tc.run || job != tc.job {
			t.Fatalf("matchCIPage(%q) = %d, %q, %q, %q, %q, %v; want %d, %q, %q, %q, %q, true",
				tc.path, kind, owner, repo, run, job, ok, tc.kind, tc.owner, tc.repo, tc.run, tc.job)
		}
	}
}

func TestMatchCIPageRejects(t *testing.T) {
	for _, path := range []string{
		"",
		"/",
		"/ci",
		"/ada",
		"/ada/hello",
		"/ada/hello/ci/runs",
		"/ada/hello/ci/runs/42",
		"/ada/hello/ci/runs/42/jobs",
		"/ada/hello/ci/runs//jobs/build",
		"/ada/hello/ci/runs/42/jobs/",
		"/ada/hello/ci/runs/42/jobs/build/log/tail",
		"/ada/hello/cix",
		"/ada/hello/releases",
		"/ada/hello/ci/blobs/abc123",
		"/ada/hello/ci/badge.svg/extra",
	} {
		if kind, _, _, _, _, ok := matchCIPage(path); ok {
			t.Fatalf("matchCIPage(%q) matched kind %d; want no match", path, kind)
		}
	}
}

func TestCILogPageZeroJS(t *testing.T) {
	body := ciLogPage("/ada/hello/ci/runs/42/jobs/build/log",
		"step 1 ok\n<script>alert(1)</script> <img src=x onerror=alert(1)> javascript:void(0)",
		"started", 15)
	assertCIZeroJS(t, body)
	for _, want := range []string{
		"step 1 ok",
		"&lt;script&gt;alert(1)&lt;/script&gt;",
		"&lt;img src=x onerror=alert(1)&gt;",
		"Refresh every 15 seconds is on",
		"refresh_seconds=0",
		`<meta http-equiv="refresh" content="15">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("log page misses %q", want)
		}
	}
	for _, raw := range []string{"<script>", "<img", "javascript:void(0)\""} {
		if strings.Contains(body, raw) {
			t.Fatalf("log page leaks raw markup %q", raw)
		}
	}
	if strings.Contains(body, "<form") {
		t.Fatalf("log page must not carry an editable form")
	}
}

func TestCILogPageRefreshMatrix(t *testing.T) {
	path := "/ada/hello/ci/runs/42/jobs/build/log"
	cases := []struct {
		name         string
		state        string
		refresh      int
		wantInterval int
		wantMeta     bool
		wantOffer    bool
	}{
		{name: "running on", state: "started", refresh: 15, wantMeta: true, wantInterval: 15},
		{name: "running on long", state: "suspect", refresh: 30, wantMeta: true, wantInterval: 30},
		{name: "running off", state: "granted", wantOffer: true},
		{name: "running below min", state: "started", refresh: 14, wantOffer: true},
		{name: "finished suppresses", state: "completed", refresh: 15},
		{name: "failed suppresses", state: "lost", refresh: 20},
		{name: "cancelled suppresses", state: "cancelled", refresh: 15},
	}
	for _, tc := range cases {
		body := ciLogPage(path, "log line", tc.state, tc.refresh)
		assertCIZeroJS(t, body)
		m := ciMetaRefRe.FindStringSubmatch(body)
		if tc.wantMeta && (m == nil || m[1] != strconv.Itoa(tc.wantInterval)) {
			t.Fatalf("%s: refresh meta = %v; want interval %d", tc.name, m, tc.wantInterval)
		}
		if !tc.wantMeta && m != nil {
			t.Fatalf("%s: refresh meta present with %s; want none", tc.name, m[1])
		}
		if got := strings.Contains(body, "refresh_seconds="+strconv.Itoa(ciRefreshMinSeconds)); got != tc.wantOffer {
			t.Fatalf("%s: refresh offer = %v; want %v", tc.name, got, tc.wantOffer)
		}
	}
}

func TestCIPageShellRefreshGuard(t *testing.T) {
	if got := ciRefreshMeta(14); got != "" {
		t.Fatalf("ciRefreshMeta(14) = %q; want empty", got)
	}
	if got := ciRefreshMeta(0); got != "" {
		t.Fatalf("ciRefreshMeta(0) = %q; want empty", got)
	}
	if got := ciRefreshMeta(15); !strings.Contains(got, `content="15"`) {
		t.Fatalf("ciRefreshMeta(15) = %q; want content 15", got)
	}
	if got := ciRefreshMeta(16); !strings.Contains(got, `content="16"`) {
		t.Fatalf("ciRefreshMeta(16) = %q; want content 16", got)
	}
}

func TestCIBadgeSVG(t *testing.T) {
	cases := []struct {
		status string
		color  string
		label  string
	}{
		{"succeeded", "#2da44e", "succeeded"},
		{"failed", "#cf222e", "failed"},
		{"running", "#bf8700", "running"},
		{"pending", "#bf8700", "pending"},
		{"errored", "#6e7781", "errored"},
		{"", "#6e7781", "unknown"},
	}
	for _, tc := range cases {
		svg := string(ciBadgeSVG(tc.status))
		if !strings.Contains(svg, `fill="`+tc.color+`"`) {
			t.Fatalf("badge(%q) misses color %s: %s", tc.status, tc.color, svg)
		}
		if !strings.Contains(svg, "ci: "+tc.label) {
			t.Fatalf("badge(%q) misses label %s: %s", tc.status, tc.label, svg)
		}
		if !strings.Contains(svg, `<svg xmlns="http://www.w3.org/2000/svg"`) {
			t.Fatalf("badge(%q) is not an svg document: %s", tc.status, svg)
		}
	}
	svg := string(ciBadgeSVG("bad<script>"))
	if strings.Contains(svg, "<script>") {
		t.Fatalf("badge does not escape the status: %s", svg)
	}
}
