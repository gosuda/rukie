package mirror

import (
	"net/http"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		code   string
		after  string
		reason string
		class  Class
		state  string
		status int
		retry  bool
	}{
		{code: "E_FORBIDDEN", status: 403, class: ClassAuth, retry: false, state: "auth_failed"},
		{code: "", status: 429, class: ClassRateLimited, retry: true, state: "failing"},
		{code: "", status: 403, after: "10", class: ClassRateLimited, retry: true, state: "failing"},
		{code: "E_NOT_FOUND", status: 404, class: ClassRepoMissing, retry: true, state: "failing"},
		{code: "E_REMOTE_MOVED", class: ClassRepoMoved, retry: false, state: "needs_attention"},
		{code: "", status: 302, class: ClassRepoMoved, retry: false, state: "needs_attention"},
		{code: "", status: 500, class: ClassServer, retry: true, state: "failing"},
		{code: "E_OFFLINE", class: ClassNetwork, retry: true, state: "failing"},
		{code: "", reason: "timeout", class: ClassNetwork, retry: true, state: "failing"},
		{code: "E_PUSH_UNCERTAIN", class: ClassUncertain, retry: true, state: "failing"},
		{code: "", reason: "deny updating a hidden ref", class: ClassRefHidden, retry: false, state: "partial"},
		{code: "", reason: "protected branch", class: ClassRefProtected, retry: false, state: "partial"},
		{code: "", reason: "push protection GH013", class: ClassRefPolicy, retry: false, state: "needs_attention"},
		{code: "", reason: "file size limit exceeded", class: ClassTooLarge, retry: false, state: "needs_attention"},
		{code: "", reason: "pre-receive hook declined", class: ClassRefHook, retry: false, state: "partial"},
		{code: "E_CORRUPT", class: ClassProtocol, retry: true, state: "failing"},
		{code: "E_CAPACITY", class: ClassBusy, retry: true, state: "failing"},
		{code: "E_CANCELLED", class: ClassBusy, retry: true, state: "failing"},
		{code: "E_DIVERGED", class: ClassDiverged, retry: false, state: "partial"},
		{code: "E_SECRET", class: ClassSecret, retry: false, state: "needs_attention"},
		{code: "E_POLICY", class: ClassPolicy, retry: false, state: "needs_attention"},
		{code: "", reason: "mystery", class: ClassRefOther, retry: false, state: "partial"},
		{code: "", class: ClassOK, retry: false, state: "ok"},
		{code: "E_PUSH_REJECTED", class: ClassRefOther, retry: false, state: "partial"},
		{code: "E_PUSH_REJECTED", reason: "protected branch", class: ClassRefProtected, retry: false, state: "partial"},
		{code: "E_PUSH_REJECTED", reason: "pre-receive hook declined", class: ClassRefHook, retry: false, state: "partial"},
		{code: "E_FORBIDDEN", status: 403, reason: "rate limit", class: ClassRateLimited, retry: true, state: "failing"},
	}
	for _, tc := range cases {
		v := Classify(tc.code, tc.status, tc.after, tc.reason)
		if v.Class != tc.class || v.Retry != tc.retry || v.State != tc.state {
			t.Errorf("Classify(%q,%d,%q)=%+v", tc.code, tc.status, tc.reason, v)
		}
	}
}
func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	date := now.Add(30 * time.Second).Format(http.TimeFormat)
	for _, tc := range []struct {
		v    string
		want time.Duration
		ok   bool
	}{{"12", 12 * time.Second, true}, {date, 30 * time.Second, true}, {"-1", 0, false}, {"garbage", 0, false}, {now.Add(-time.Second).Format(http.TimeFormat), 0, false}} {
		got, ok := ParseRetryAfter(tc.v, now)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ParseRetryAfter(%q)=(%v,%v)", tc.v, got, ok)
		}
	}
}
