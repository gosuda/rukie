package main

import (
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/op"
)

func TestRateLimiterConfigWiring(t *testing.T) {
	limiter := newRateLimiter(config.RateLimitConfig{
		RequestsPerMinute: 60, AnonymousPerMinute: 30, Burst: 1, BanMinutes: 2, Enabled: true,
	})
	key := op.RateKey{Kind: op.RateToken, ID: "token"}
	if decision := limiter.Admit(t.Context(), key); !decision.Allow {
		t.Fatal("configured burst denied first request")
	}
	if decision := limiter.Admit(t.Context(), key); decision.Allow {
		t.Fatal("configured burst allowed second request")
	}
	gitKey := op.RateKey{Kind: op.RateIPGit, ID: "127.0.0.1"}
	for range 60 {
		if decision := limiter.Admit(t.Context(), gitKey); !decision.Allow {
			t.Fatal("Git failure threshold denied before ban")
		}
	}
	if decision := limiter.Admit(t.Context(), gitKey); decision.Allow {
		t.Fatal("Git failure threshold did not start cooldown")
	}
	if blocked, remaining := limiter.GitCooldown(gitKey.ID); !blocked || remaining > 2*time.Minute || remaining < 119*time.Second {
		t.Fatalf("configured cooldown = %v, %s", blocked, remaining)
	}

	limiter = newRateLimiter(config.RateLimitConfig{})
	for range 1000 {
		if decision := limiter.Admit(t.Context(), key); !decision.Allow {
			t.Fatal("disabled limiter rejected request")
		}
	}
	if blocked, remaining := limiter.GitCooldown(gitKey.ID); blocked || remaining != 0 {
		t.Fatalf("disabled cooldown = %v, %s", blocked, remaining)
	}
}
