package mirror

import (
	"math"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	BaseBackoff       = 30 * time.Second
	MaxBackoff        = 60 * time.Minute
	RateLimitDefault  = 60 * time.Second
	RateLimitMax      = 6 * time.Hour
	FailureCap        = 8
	CircuitThreshold  = 5
	CircuitBase       = 15 * time.Minute
	CircuitMax        = 6 * time.Hour
	MaxMirrorWorkers  = 4
	MaxRukInvocations = 2
)

type HostLimits struct {
	Spacing    time.Duration
	Concurrent int
}

func LimitsFor(host HostKind) HostLimits {
	switch host {
	case HostGitHub:
		return HostLimits{10 * time.Second, 2}
	case HostGitLab, HostForgejo:
		return HostLimits{5 * time.Second, 2}
	default:
		return HostLimits{10 * time.Second, 1}
	}
}
func LimitsForHost(host HostKind, rawURL string) HostLimits {
	u, err := url.Parse(rawURL)
	if err == nil && strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), "codeberg.org") {
		return HostLimits{10 * time.Second, 1}
	}
	return LimitsFor(host)
}
func BackoffDelay(attempt int, base, cap time.Duration, rand01 float64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if base < 0 {
		base = 0
	}
	if cap < 0 {
		cap = 0
	}
	if math.IsNaN(rand01) || rand01 < 0 {
		rand01 = 0
	}
	if rand01 > 1 {
		rand01 = 1
	}
	d := base
	for n := 1; n < attempt && d < cap; n++ {
		if d > time.Duration(math.MaxInt64/2) {
			d = cap
			break
		}
		d *= 2
	}
	if d > cap {
		d = cap
	}
	return time.Duration(float64(d) * rand01)
}
func RateLimitDelay(retryAfter time.Duration, hasHeader bool, rand01 float64) time.Duration {
	d := RateLimitDefault
	if hasHeader {
		d = retryAfter
	}
	if d > RateLimitMax {
		d = RateLimitMax
	}
	if math.IsNaN(rand01) || rand01 < 0 {
		rand01 = 0
	}
	if rand01 > 1 {
		rand01 = 1
	}
	return min(RateLimitMax, d+time.Duration(float64(d)*0.1*rand01))
}

type Circuit struct {
	mu               sync.Mutex
	failures         int
	open             bool
	probe            bool
	openedAt         time.Time
	duration         time.Duration
	consecutiveOpens int
}

func (c *Circuit) Allow(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.open {
		return true
	}
	if now.Sub(c.openedAt) < c.duration {
		return false
	}
	if c.probe {
		return false
	}
	c.probe = true
	return true
}
func (c *Circuit) Record(now time.Time, success bool, class Class) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if success {
		c.failures = 0
		c.open = false
		c.probe = false
		c.duration = 0
		c.consecutiveOpens = 0
		return
	}
	if c.open && c.probe {
		c.probe = false
		c.openedAt = now
		c.consecutiveOpens++
		if c.duration == 0 {
			c.duration = CircuitBase
		} else if c.duration < CircuitMax/2 {
			c.duration *= 2
		} else {
			c.duration = CircuitMax
		}
		return
	}
	if class == ClassNetwork || class == ClassServer {
		c.failures++
		if c.failures >= CircuitThreshold {
			c.open = true
			c.probe = false
			c.openedAt = now
			c.duration = CircuitBase
			c.failures = 0
		}
	} else {
		c.failures = 0
	}
}
func ShouldRetry(v Verdict, consecutive, protectedSameReason int) (bool, string) {
	switch v.Class {
	case ClassAuth, ClassRepoMoved, ClassTooLarge, ClassPolicy, ClassSecret, ClassRefPolicy:
		return false, "needs_attention"
	case ClassRepoMissing:
		if consecutive >= 1 {
			return false, "needs_attention"
		}
	case ClassRefProtected:
		if protectedSameReason >= 3 {
			return false, "needs_attention"
		}
	case ClassProtocol:
		if consecutive >= 3 {
			return false, "needs_attention"
		}
	}
	if !v.Retry {
		return false, v.State
	}
	if consecutive >= FailureCap {
		return true, "failing"
	}
	return true, v.State
}
