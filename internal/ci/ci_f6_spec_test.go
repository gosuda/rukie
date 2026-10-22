package ci_test

import (
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/ci"
)

func f6NetSpec(t *testing.T, network string) string {
	t.Helper()
	return `schema = "rukie.ci/v1"

[triggers]
change = true

[[job]]
name = "solo"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
network = "` + network + `"
steps = [{ name = "s", run = "echo hi" }]
`
}

func TestF6NetworkWhitelist(t *testing.T) {
	for _, network := range []string{"none", "egress"} {
		if _, err := ci.DecodeSpec([]byte(f6NetSpec(t, network))); err != nil {
			t.Errorf("network %q rejected: %v", network, err)
		}
	}
	for _, network := range []string{"host", "bridge", "ns:test", "NONE"} {
		if _, err := ci.DecodeSpec([]byte(f6NetSpec(t, network))); err == nil {
			t.Errorf("network %q accepted", network)
		}
	}
	spec, err := ci.DecodeSpec([]byte(f6NetSpec(t, "none")))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	spec.Jobs[0].Network = "host"
	if err := ci.ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "/network") {
		t.Fatalf("ValidateSpec accepted host network: %v", err)
	}
}

func f6CacheSpec(t *testing.T, extra string) string {
	t.Helper()
	return `schema = "rukie.ci/v1"

[triggers]
change = true

[[job]]
name = "solo"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
` + extra + `steps = [{ name = "s", run = "echo hi" }]
`
}

func TestF6CacheSpecValidation(t *testing.T) {
	valid := f6CacheSpec(t, "caches = [{ name = \"go\", path = \"/cache/go\" }]\n")
	spec, err := ci.DecodeSpec([]byte(valid))
	if err != nil {
		t.Fatalf("valid caches rejected: %v", err)
	}
	if spec.Jobs[0].Caches[0].Mode != "exclusive" {
		t.Fatalf("default mode = %q, want exclusive", spec.Jobs[0].Caches[0].Mode)
	}
	for _, row := range []struct {
		name  string
		extra string
	}{
		{"bad name", "caches = [{ name = \"Go\", path = \"/cache/Go\" }]\n"},
		{"bad path", "caches = [{ name = \"go\", path = \"/other/go\" }]\n"},
		{"bad mode", "caches = [{ name = \"go\", path = \"/cache/go\", mode = \"wild\" }]\n"},
		{"duplicate", "caches = [{ name = \"go\", path = \"/cache/go\" }, { name = \"go\", path = \"/cache/go\" }]\n"},
	} {
		if _, rerr := ci.DecodeSpec([]byte(f6CacheSpec(t, row.extra))); rerr == nil {
			t.Errorf("%s accepted", row.name)
		}
	}
	entries := make([]string, 0, 9)
	for i := range 9 {
		letter := string(rune('a' + i))
		entries = append(entries, "{ name = \"c"+letter+"\", path = \"/cache/c"+letter+"\" }")
	}
	nine := "caches = [" + strings.Join(entries, ", ") + "]\n"
	if _, rerr := ci.DecodeSpec([]byte(f6CacheSpec(t, nine))); rerr == nil {
		t.Error("nine caches accepted")
	}
	plain, err := ci.DecodeSpec([]byte(f6CacheSpec(t, "")))
	if err != nil {
		t.Fatalf("plain spec rejected: %v", err)
	}
	if len(plain.Jobs[0].Caches) != 0 {
		t.Fatal("plain spec gained caches")
	}
}
