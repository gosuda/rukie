package mirror

import (
	"net/http"
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		code          string
		status        int
		after, reason string
		class         Class
		retry         bool
		state         string
	}{{"E_FORBIDDEN", 403, "", "", ClassAuth, false, "auth_failed"}, {"", 429, "", "", ClassRateLimited, true, "failing"}, {"", 403, "10", "", ClassRateLimited, true, "failing"}, {"E_NOT_FOUND", 404, "", "", ClassRepoMissing, true, "failing"}, {"E_REMOTE_MOVED", 0, "", "", ClassRepoMoved, false, "needs_attention"}, {"", 302, "", "", ClassRepoMoved, false, "needs_attention"}, {"", 500, "", "", ClassServer, true, "failing"}, {"E_OFFLINE", 0, "", "", ClassNetwork, true, "failing"}, {"", 0, "", "timeout", ClassNetwork, true, "failing"}, {"E_PUSH_UNCERTAIN", 0, "", "", ClassUncertain, true, "failing"}, {"", 0, "", "deny updating a hidden ref", ClassRefHidden, false, "partial"}, {"", 0, "", "protected branch", ClassRefProtected, false, "partial"}, {"", 0, "", "push protection GH013", ClassRefPolicy, false, "needs_attention"}, {"", 0, "", "file size limit exceeded", ClassTooLarge, false, "needs_attention"}, {"", 0, "", "pre-receive hook declined", ClassRefHook, false, "partial"}, {"E_CORRUPT", 0, "", "", ClassProtocol, true, "failing"}, {"E_CAPACITY", 0, "", "", ClassBusy, true, "failing"}, {"E_CANCELLED", 0, "", "", ClassBusy, true, "failing"}, {"E_DIVERGED", 0, "", "", ClassDiverged, false, "partial"}, {"E_SECRET", 0, "", "", ClassSecret, false, "needs_attention"}, {"E_POLICY", 0, "", "", ClassPolicy, false, "needs_attention"}, {"", 0, "", "mystery", ClassRefOther, false, "partial"}, {"", 0, "", "", ClassOK, false, "ok"}, {"E_PUSH_REJECTED", 0, "", "", ClassRefOther, false, "partial"}, {"E_PUSH_REJECTED", 0, "", "protected branch", ClassRefProtected, false, "partial"}, {"E_PUSH_REJECTED", 0, "", "pre-receive hook declined", ClassRefHook, false, "partial"}, {"E_FORBIDDEN", 403, "", "rate limit", ClassRateLimited, true, "failing"}}
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
