package mirror

import (
	"testing"
	"time"
)

func TestBackoffDelay(t *testing.T) {
	for _, tc := range []struct {
		attempt         int
		base, cap, want time.Duration
		j               float64
	}{{0, time.Second, 8 * time.Second, time.Second, 1}, {3, time.Second, 8 * time.Second, 4 * time.Second, 1}, {4, time.Second, 8 * time.Second, 8 * time.Second, 1}, {5, time.Second, 8 * time.Second, 8 * time.Second, 1}, {2, 10 * time.Second, 20 * time.Second, 10 * time.Second, .5}, {1, time.Second, 8 * time.Second, 0, 0}} {
		if got := BackoffDelay(tc.attempt, tc.base, tc.cap, tc.j); got != tc.want {
			t.Errorf("BackoffDelay=%v want %v", got, tc.want)
		}
	}
}
func TestRateLimitDelay(t *testing.T) {
	if got := RateLimitDelay(0, false, 0); got != RateLimitDefault {
		t.Fatal(got)
	}
	if got := RateLimitDelay(5*time.Second, true, 0); got != 5*time.Second {
		t.Fatalf("header 5s ignored: %v", got)
	}
	if got := RateLimitDelay(10*time.Hour, true, 0); got != RateLimitMax {
		t.Fatalf("header not capped: %v", got)
	}
	if got := RateLimitDelay(2*time.Hour, true, 1); got != 2*time.Hour+12*time.Minute {
		t.Fatal(got)
	}
	if got := RateLimitDelay(9*time.Hour, true, 1); got != RateLimitMax {
		t.Fatal(got)
	}
}
func TestHostLimits(t *testing.T) {
	if LimitsFor(HostGitHub) != (HostLimits{10 * time.Second, 2}) || LimitsFor(HostGitLab) != (HostLimits{5 * time.Second, 2}) || LimitsFor(HostForgejo) != (HostLimits{5 * time.Second, 2}) || LimitsFor(HostGeneric) != (HostLimits{10 * time.Second, 1}) {
		t.Fatal("unexpected host limits")
	}
	if got := LimitsForHost(HostForgejo, "https://CodeBerg.org:123/a"); got != (HostLimits{10 * time.Second, 1}) {
		t.Fatal(got)
	}
}
func TestCircuit(t *testing.T) {
	var c Circuit
	now := time.Now()
	for range CircuitThreshold {
		c.Record(now, false, ClassNetwork)
	}
	if c.Allow(now) {
		t.Fatal("circuit did not open")
	}
	after := now.Add(CircuitBase)
	if !c.Allow(after) || c.Allow(after) {
		t.Fatal("half-open probe not exclusive")
	}
	c.Record(after, false, ClassServer)
	if c.Allow(after) {
		t.Fatal("failed probe did not reopen")
	}
	if !c.Allow(after.Add(2*CircuitBase)) || c.Allow(after.Add(2*CircuitBase)) {
		t.Fatal("second probe unavailable")
	}
	c.Record(after.Add(2*CircuitBase), true, ClassOK)
	if !c.Allow(after) {
		t.Fatal("success did not close circuit")
	}
}
func TestShouldRetry(t *testing.T) {
	cases := []struct {
		cl           Class
		retry        bool
		n, protected int
		want         bool
		state        string
	}{{ClassAuth, true, 0, 0, false, "needs_attention"}, {ClassRepoMoved, true, 0, 0, false, "needs_attention"}, {ClassTooLarge, true, 0, 0, false, "needs_attention"}, {ClassPolicy, true, 0, 0, false, "needs_attention"}, {ClassSecret, true, 0, 0, false, "needs_attention"}, {ClassRepoMissing, true, 0, 0, true, "failing"}, {ClassRepoMissing, true, 1, 0, false, "needs_attention"}, {ClassRefProtected, true, 0, 2, true, "partial"}, {ClassRefProtected, true, 0, 3, false, "needs_attention"}, {ClassProtocol, true, 2, 0, true, "failing"}, {ClassProtocol, true, 3, 0, false, "needs_attention"}, {ClassServer, true, 8, 0, true, "failing"}}
	for _, tc := range cases {
		v := Verdict{Class: tc.cl, Retry: tc.retry, State: "failing"}
		if tc.cl == ClassRefProtected {
			v.State = "partial"
		}
		got, state := ShouldRetry(v, tc.n, tc.protected)
		if got != tc.want || state != tc.state {
			t.Errorf("ShouldRetry(%s)=(%v,%s)", tc.cl, got, state)
		}
	}
}
