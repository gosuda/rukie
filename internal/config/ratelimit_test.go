package config

import "testing"

func TestRateLimitDefaults(t *testing.T) {
	got := Defaults().RateLimit
	want := RateLimitConfig{RequestsPerMinute: 300, AnonymousPerMinute: 60, Burst: 600, BanMinutes: 10, Enabled: true}
	if got != want {
		t.Fatalf("rate limit defaults = %+v, want %+v", got, want)
	}
}
