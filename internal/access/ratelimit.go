package access

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/gosuda/rukie/internal/op"
)

const (
	tokenRatePerMinute = 300.0
	tokenBurst         = 600.0
	anonRatePerMinute  = 60.0
	anonBurst          = 60.0
	gitFailRatePerMin  = 60.0
	loginMaxFailures   = 5
	loginWindow        = 10 * time.Minute
)

type bucket struct {
	last   time.Time
	tokens float64
}

type gitState struct {
	blocked  time.Time
	failures []time.Time
}

type Limiter struct {
	now         func() time.Time
	tokens      map[string]*bucket
	anons       map[string]*bucket
	logins      map[string][]time.Time
	gitFails    map[string]*gitState
	tokenRate   float64
	anonRate    float64
	tokenCap    float64
	gitCooldown time.Duration
	enabled     bool
	mu          sync.Mutex
}

// LimiterConfig sets request-rate limits and the master enable switch.
type LimiterConfig struct {
	RequestsPerMinute  uint
	AnonymousPerMinute uint
	Burst              uint
	BanMinutes         uint
	Enabled            bool
}

const gitCooldownMinutes = 10

// DefaultLimiterConfig returns the limits that preserve current production behavior.
func DefaultLimiterConfig() LimiterConfig {
	return LimiterConfig{
		RequestsPerMinute:  uint(tokenRatePerMinute),
		AnonymousPerMinute: uint(anonRatePerMinute),
		Burst:              uint(tokenBurst),
		BanMinutes:         gitCooldownMinutes,
		Enabled:            true,
	}
}

func NewLimiter() *Limiter {
	return NewLimiterWithConfig(DefaultLimiterConfig())
}

// NewLimiterWithConfig creates a limiter from explicit rate-limit settings.
func cooldownDuration(minutes uint) time.Duration {
	duration, err := time.ParseDuration(strconv.FormatUint(uint64(minutes), 10) + "m")
	if err != nil {
		return time.Duration(1<<63 - 1)
	}
	return duration
}

func NewLimiterWithConfig(cfg LimiterConfig) *Limiter {
	return &Limiter{
		now:         time.Now,
		tokens:      map[string]*bucket{},
		anons:       map[string]*bucket{},
		logins:      map[string][]time.Time{},
		gitFails:    map[string]*gitState{},
		tokenRate:   float64(cfg.RequestsPerMinute) / 60.0,
		anonRate:    float64(cfg.AnonymousPerMinute) / 60.0,
		tokenCap:    float64(cfg.Burst),
		gitCooldown: cooldownDuration(cfg.BanMinutes),
		enabled:     cfg.Enabled,
	}
}

func takeToken(b *bucket, now time.Time, rate, capacity float64) (bool, time.Duration) {
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * rate
		if b.tokens > capacity {
			b.tokens = capacity
		}
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	deficit := 1 - b.tokens
	wait := time.Duration(deficit / rate * float64(time.Second))
	return false, wait
}

func (l *Limiter) GitCooldown(id string) (bool, time.Duration) {
	if !l.enabled {
		return false, 0
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	st, ok := l.gitFails[id]
	if !ok {
		return false, 0
	}
	if now.Before(st.blocked) {
		return true, st.blocked.Sub(now)
	}
	return false, 0
}

func (l *Limiter) Admit(ctx context.Context, key op.RateKey) op.RateDecision {
	_ = ctx
	if !l.enabled {
		return op.RateDecision{Allow: true}
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	switch key.Kind {
	case op.RateToken:
		b, ok := l.tokens[key.ID]
		if !ok {
			b = &bucket{tokens: l.tokenCap, last: now}
			l.tokens[key.ID] = b
		}
		allow, wait := takeToken(b, now, l.tokenRate, l.tokenCap)
		return op.RateDecision{Allow: allow, RetryAfter: wait}
	case op.RateIPAnon:
		b, ok := l.anons[key.ID]
		if !ok {
			b = &bucket{tokens: anonBurst, last: now}
			l.anons[key.ID] = b
		}
		allow, wait := takeToken(b, now, l.anonRate, anonBurst)
		return op.RateDecision{Allow: allow, RetryAfter: wait}
	case op.RateIPLogin:
		var kept []time.Time
		for _, t := range l.logins[key.ID] {
			if now.Sub(t) < loginWindow {
				kept = append(kept, t)
			}
		}
		if len(kept) >= loginMaxFailures {
			l.logins[key.ID] = kept
			return op.RateDecision{RetryAfter: loginWindow - now.Sub(kept[0])}
		}
		l.logins[key.ID] = append(kept, now)
		return op.RateDecision{Allow: true}
	case op.RateIPGit:
		st, ok := l.gitFails[key.ID]
		if !ok {
			st = &gitState{}
			l.gitFails[key.ID] = st
		}
		if now.Before(st.blocked) {
			return op.RateDecision{RetryAfter: st.blocked.Sub(now)}
		}
		var kept []time.Time
		for _, t := range st.failures {
			if now.Sub(t) < time.Minute {
				kept = append(kept, t)
			}
		}
		st.failures = kept
		if float64(len(kept)) >= gitFailRatePerMin {
			st.blocked = now.Add(l.gitCooldown)
			return op.RateDecision{RetryAfter: l.gitCooldown}
		}
		st.failures = append(st.failures, now)
		return op.RateDecision{Allow: true}
	default:
		return op.RateDecision{Allow: true}
	}
}
