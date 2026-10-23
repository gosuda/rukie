package access

import (
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/op"
)

func TestLimiterConfigDefaultsMatchCurrentLimits(t *testing.T) {
	cfg := DefaultLimiterConfig()
	if cfg.RequestsPerMinute != 300 || cfg.AnonymousPerMinute != 60 || cfg.Burst != 600 || cfg.BanMinutes != 10 || !cfg.Enabled {
		t.Fatalf("defaults = %+v", cfg)
	}
	limiter := NewLimiterWithConfig(cfg)
	for range cfg.Burst {
		if decision := limiter.Admit(t.Context(), op.RateKey{Kind: op.RateToken, ID: "token"}); !decision.Allow {
			t.Fatal("default token bucket denied before burst")
		}
	}
	if decision := limiter.Admit(t.Context(), op.RateKey{Kind: op.RateToken, ID: "token"}); decision.Allow {
		t.Fatal("default token bucket allowed request past burst")
	}
}

func TestLimiterConfigOverridesAndDisabledMode(t *testing.T) {
	cfg := LimiterConfig{RequestsPerMinute: 60, AnonymousPerMinute: 30, Burst: 1, BanMinutes: 2, Enabled: true}
	limiter := NewLimiterWithConfig(cfg)
	key := op.RateKey{Kind: op.RateToken, ID: "token"}
	if decision := limiter.Admit(t.Context(), key); !decision.Allow {
		t.Fatal("configured burst denied first request")
	}
	if decision := limiter.Admit(t.Context(), key); decision.Allow {
		t.Fatal("configured burst allowed second request")
	}
	if len(limiter.tokens) != 1 || limiter.tokenRate != 1 || limiter.tokenCap != 1 || limiter.anonRate != 0.5 || limiter.gitCooldown != 2*time.Minute {
		t.Fatalf("configured limiter = %+v", limiter)
	}

	limiter = NewLimiterWithConfig(LimiterConfig{})
	for range 1000 {
		if decision := limiter.Admit(t.Context(), op.RateKey{Kind: op.RateIPAnon, ID: "127.0.0.1"}); !decision.Allow {
			t.Fatal("disabled limiter denied a request")
		}
	}
	if blocked, retry := limiter.GitCooldown("127.0.0.1"); blocked || retry != 0 {
		t.Fatalf("disabled GitCooldown = %v, %s", blocked, retry)
	}
}
