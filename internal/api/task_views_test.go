package api

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

const taskViewCanary = "CANARY-7t2q-intent-token-secret"

var (
	taskScriptRe = regexp.MustCompile(`(?i)<script`)
	taskOnAttrRe = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	taskJSURLRe  = regexp.MustCompile(`(?i)javascript:`)
	taskFrameRe  = regexp.MustCompile(`(?i)<(iframe|object|embed)`)
)

func assertTaskZeroJS(t *testing.T, body string) {
	t.Helper()
	if err := zerojs.Assert([]byte(body)); err != nil {
		t.Fatalf("zero-js: %v", err)
	}
	for name, re := range map[string]*regexp.Regexp{
		"script": taskScriptRe, "on-attr": taskOnAttrRe,
		"js-url": taskJSURLRe, "frame": taskFrameRe,
	} {
		if re.MatchString(body) {
			t.Fatalf("page contains %s construct", name)
		}
	}
	if strings.Contains(body, "srcdoc=") {
		t.Fatalf("page contains srcdoc")
	}
	if strings.Contains(body, taskViewCanary) {
		t.Fatalf("page carries the canary token")
	}
}

func taskViewCtx(path string) op.ViewCtx {
	return op.ViewCtx{
		Path: path,
		CSRF: "csrf-token",
		Input: url.Values{
			"token": {taskViewCanary},
		},
		Errors: []op.FieldError{{Location: "body.title", Message: "boom <x>"}},
	}
}

func taskSample(n int64, title, state, mode, holder string, expires int64, paths ...string) IntentJSON {
	return IntentJSON{
		Number: n, PublicID: "01TASK" + intentNumber(n), Title: title, Body: "body " + intentNumber(n),
		State: state, LeaseMode: mode, Holder: holder, ExpiresAt: expires, Version: 3,
		Owner: "ada", Repo: "hello", ClaimPaths: paths,
	}
}

func askSample(id, question, state string, blocks bool, answer string) AskJSON {
	return AskJSON{
		PublicID: id, Question: question, State: state,
		BlocksClaim: blocks, DeadlineMs: 9, Answer: answer,
	}
}

func TestIntentListView(t *testing.T) {
	held := taskSample(1, "Hold the ridge <x>", "open", "enforce", "ada", 70, "src/a.go")
	free := taskSample(2, "Plain task", "open", "advise", "", 0, "src/b.go")
	var b strings.Builder
	if err := writeIntentList(&b, taskViewCtx("/ada/hello/tasks"), IntentListOut{Intents: []IntentJSON{held, free}}); err != nil {
		t.Fatalf("list: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	for _, want := range []string{
		"/ada/hello/tasks/1",
		"/ada/hello/tasks/2",
		"Hold the ridge &lt;x&gt;",
		"Active claims",
		"Supersede radar",
		"No overlapping claims.",
		"ada",
		"boom &lt;x&gt;",
		`<form method="get"`,
		`action="/ada/hello/tasks">`,
		`action="/ada/hello/tasks/new">`,
		`<textarea name="claim_paths"`,
		`name="state"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("list missing %q", want)
		}
	}
}

func TestIntentListGlobalView(t *testing.T) {
	held := taskSample(1, "Held", "open", "enforce", "ada", 70, "src/a.go")
	var b strings.Builder
	if err := writeIntentList(&b, taskViewCtx("/tasks"), IntentListOut{Intents: []IntentJSON{held}}); err != nil {
		t.Fatalf("global list: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	if strings.Contains(body, "Create task") {
		t.Fatalf("global queue must not carry a repo-scoped create form")
	}
	if !strings.Contains(body, `href="/ada/hello/tasks/1"`) {
		t.Fatalf("global queue must link rows through the owning repo")
	}
	if !strings.Contains(body, "Active claims") {
		t.Fatalf("global queue missing claims panel")
	}
}

func TestIntentRadarOverlap(t *testing.T) {
	a := taskSample(1, "First", "open", "enforce", "ada", 70, "src/shared.go")
	b := taskSample(2, "Second", "open", "enforce", "bob", 80, "src/shared.go", "src/other.go")
	var buf strings.Builder
	if err := writeIntentList(&buf, taskViewCtx("/ada/hello/tasks"), IntentListOut{Intents: []IntentJSON{a, b}}); err != nil {
		t.Fatalf("radar overlap: %v", err)
	}
	body := buf.String()
	assertTaskZeroJS(t, body)
	for _, want := range []string{"src/shared.go", "ada, bob", "1, 2", "Abandon the superseded plan."} {
		if !strings.Contains(body, want) {
			t.Fatalf("radar overlap missing %q", want)
		}
	}
}

func TestIntentRadarEmpty(t *testing.T) {
	free := taskSample(1, "Free", "open", "advise", "", 0, "src/a.go")
	var b strings.Builder
	if err := writeIntentList(&b, taskViewCtx("/ada/hello/tasks"), IntentListOut{Intents: []IntentJSON{free}}); err != nil {
		t.Fatalf("radar empty: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	for _, want := range []string{"No active claims.", "No overlapping claims."} {
		if !strings.Contains(body, want) {
			t.Fatalf("radar empty missing %q", want)
		}
	}
}

func TestIntentViewUnclaimed(t *testing.T) {
	out := IntentGetOut{
		Intent:  taskSample(1, "Work <x>", "open", "advise", "", 0),
		Changes: []IntentChangeJSON{{ChangeID: "kkkk0001", HeadOid: "aaa", Relation: "implements"}},
		Asks:    []AskJSON{askSample("a1", "Which scope? <x>", "asked", true, "")},
	}
	var b strings.Builder
	if err := writeIntent(&b, taskViewCtx("/ada/hello/tasks/1"), out); err != nil {
		t.Fatalf("detail: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	for _, want := range []string{
		`action="/ada/hello/tasks/1/claim"`,
		`name="ttl_s"`,
		`action="/ada/hello/tasks/1/state"`,
		`name="state"`,
		`action="/ada/hello/tasks/1/edit"`,
		"/ada/hello/changes/kkkk0001",
		`action="/ada/hello/tasks/1/changes/kkkk0001/remove"`,
		`action="/ada/hello/changes/kkkk0001/comments"`,
		"Comment on kkkk0001",
		"/ada/hello/asks/a1",
		"Which scope? &lt;x&gt;",
		"Work &lt;x&gt;",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail missing %q", want)
		}
	}
	if strings.Contains(body, "/release\"") {
		t.Fatalf("unclaimed intent must not carry a release form")
	}
	if strings.Contains(body, "/tasks/1/comment") {
		t.Fatalf("task views must not invent a task-scoped comment route")
	}
}

func TestIntentViewHeld(t *testing.T) {
	out := IntentGetOut{Intent: taskSample(1, "Held work", "open", "enforce", "ada", 70)}
	var b strings.Builder
	if err := writeIntent(&b, taskViewCtx("/ada/hello/tasks/1"), out); err != nil {
		t.Fatalf("held detail: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	if !strings.Contains(body, `action="/ada/hello/tasks/1/release"`) {
		t.Fatalf("held detail missing release form")
	}
	if !strings.Contains(body, `action="/ada/hello/tasks/1/claim/renew"`) {
		t.Fatalf("held detail missing renew form")
	}
	if strings.Contains(body, "/claim\"") {
		t.Fatalf("held intent must not carry a claim form")
	}
}

func TestIntentViewZeroOut(t *testing.T) {
	var b strings.Builder
	if err := writeIntent(&b, taskViewCtx("/ada/hello/tasks/0"), IntentGetOut{}); err != nil {
		t.Fatalf("zero detail: %v", err)
	}
	assertTaskZeroJS(t, b.String())
	var l strings.Builder
	if err := writeIntentList(&l, taskViewCtx("/ada/hello/tasks"), IntentListOut{}); err != nil {
		t.Fatalf("zero list: %v", err)
	}
	assertTaskZeroJS(t, l.String())
}

func TestAskListView(t *testing.T) {
	out := AskListOut{Asks: []AskJSON{
		askSample("a1", "First? <x>", "asked", true, ""),
		askSample("a2", "Second?", "answered", false, "done"),
	}}
	var b strings.Builder
	if err := writeAskList(&b, taskViewCtx("/ada/hello/asks"), out); err != nil {
		t.Fatalf("ask list: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	for _, want := range []string{
		"/ada/hello/asks/a1",
		"/ada/hello/asks/a2",
		"First? &lt;x&gt;",
		"yes",
		"no",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ask list missing %q", want)
		}
	}
}

func TestAskViewAsked(t *testing.T) {
	askOut := AskGetOut{Ask: askSample("a1", "Scope? <x>", "asked", true, "")}
	askOut.Ask.Options = []AskOptionJSON{{Key: "wider", Label: "Wider scope"}, {Key: "narrow", Label: "Narrower scope"}}
	var b strings.Builder
	if err := writeAsk(&b, taskViewCtx("/ada/hello/asks/a1"), askOut); err != nil {
		t.Fatalf("ask: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	for _, want := range []string{
		`action="/ada/hello/asks/a1/answer"`,
		"Scope? &lt;x&gt;",
		"Awaiting answer.",
		"wider — Wider scope",
		"narrow — Narrower scope",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("ask missing %q", want)
		}
	}
}

func TestAskViewAnswered(t *testing.T) {
	var b strings.Builder
	if err := writeAsk(&b, taskViewCtx("/ada/hello/asks/a2"), AskGetOut{Ask: askSample("a2", "Scope?", "answered", false, "wider")}); err != nil {
		t.Fatalf("answered ask: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	if strings.Contains(body, "/answer\"") {
		t.Fatalf("answered ask must not carry an answer form")
	}
	if !strings.Contains(body, "wider") {
		t.Fatalf("answered ask missing recorded answer")
	}
}

func TestAskViewExpired(t *testing.T) {
	var b strings.Builder
	if err := writeAsk(&b, taskViewCtx("/ada/hello/asks/a3"), AskGetOut{Ask: askSample("a3", "Late?", "expired", true, "")}); err != nil {
		t.Fatalf("expired ask: %v", err)
	}
	body := b.String()
	assertTaskZeroJS(t, body)
	if strings.Contains(body, "/answer\"") {
		t.Fatalf("expired ask must not carry an answer form")
	}
	if !strings.Contains(body, "Expired asks cannot be answered.") {
		t.Fatalf("expired ask missing expiry notice")
	}
}

func TestIntentRoutes(t *testing.T) {
	byPattern := map[string]Route{}
	for _, r := range Routes() {
		byPattern[r.Pattern] = r
	}
	want := map[string]string{
		"GET /tasks":                                                  "intent.list",
		"GET /{owner}/{repo}/tasks":                                   "intent.list",
		"POST /{owner}/{repo}/tasks/new":                              "intent.create",
		"GET /{owner}/{repo}/tasks/{number}":                          "intent.get",
		"POST /{owner}/{repo}/tasks/{number}/claim":                   "intent.lease.create",
		"POST /{owner}/{repo}/tasks/{number}/claim/renew":             "intent.lease.update",
		"POST /{owner}/{repo}/tasks/{number}/release":                 "intent.lease.delete",
		"POST /{owner}/{repo}/tasks/{number}/state":                   "intent.state.set",
		"POST /{owner}/{repo}/tasks/{number}/edit":                    "intent.update",
		"POST /{owner}/{repo}/tasks/{number}/changes":                 "intent.change.add",
		"POST /{owner}/{repo}/tasks/{number}/changes/{change}/remove": "intent.change.remove",
		"POST /{owner}/{repo}/tasks/{number}/asks":                    "ask.create",
		"POST /{owner}/{repo}/changes/{change}/comments":              "change.comment.create",
		"GET /{owner}/{repo}/asks":                                    "ask.list",
		"GET /{owner}/{repo}/asks/{id}":                               "ask.get",
		"POST /{owner}/{repo}/asks/{id}/answer":                       "ask.answer.create",
		"POST /{owner}/{repo}/asks/{id}/cancel":                       "ask.update",
	}
	for pattern, opName := range want {
		r, ok := byPattern[pattern]
		if !ok {
			t.Fatalf("route table misses %q", pattern)
		}
		if r.Op != opName {
			t.Fatalf("route %q maps to %q, want %q", pattern, r.Op, opName)
		}
		if r.PageOnly {
			t.Fatalf("route %q must be op-backed, not page-only", pattern)
		}
		if strings.HasPrefix(r.Op, "task.") {
			t.Fatalf("route %q uses retired task.* op name %q", pattern, r.Op)
		}
	}
	for _, page := range []string{"GET /{owner}/{repo}/tasks/new", "GET /tasks/new"} {
		r, ok := byPattern[page]
		if !ok {
			t.Fatalf("route table misses the form page %q", page)
		}
		if !r.PageOnly || r.Op != "" {
			t.Fatalf("%s must be a page-only row without an op", page)
		}
	}
	seed, ok := byPattern["GET /-/api"]
	if !ok || seed.Op != "api.index" {
		t.Fatalf("append-only edit disturbed the pre-existing route head")
	}
}
