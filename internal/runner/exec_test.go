package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSteps(t *testing.T, steps string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "steps.json")
	if err := os.WriteFile(path, []byte(steps), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "log"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecRunsSteps(t *testing.T) {
	path := writeSteps(t, `{"steps":[{"name":"hi","run":"echo hello"}],"env":{"FOO":"bar"}}`)
	logDir := filepath.Join(filepath.Dir(path), "log")
	if code := RunExec(path, logDir); code != 0 {
		t.Fatalf("exec = %d, want 0", code)
	}
	raw, err := os.ReadFile(filepath.Join(logDir, "job.stdout"))
	if err != nil || !strings.Contains(string(raw), "hello") {
		t.Fatalf("stdout = %q, %v", raw, err)
	}
	events, err := os.ReadFile(filepath.Join(logDir, "events"))
	if err != nil || !strings.Contains(string(events), "step_finished") {
		t.Fatalf("events = %q, %v", events, err)
	}
}

func TestExecReportsStepFailure(t *testing.T) {
	path := writeSteps(t, `{"steps":[{"name":"ok","run":"true"},{"name":"bad","run":"exit 3"}],"env":{}}`)
	if code := RunExec(path, filepath.Join(filepath.Dir(path), "log")); code != 1 {
		t.Fatalf("exec = %d, want 1", code)
	}
}

func TestExecRejectsMissingSpec(t *testing.T) {
	if code := RunExec(filepath.Join(t.TempDir(), "nope.json"), t.TempDir()); code != 4 {
		t.Fatalf("exec = %d, want 4", code)
	}
}

func TestExecRejectsBrokenSpec(t *testing.T) {
	path := writeSteps(t, `{"steps": [`)
	if code := RunExec(path, filepath.Join(filepath.Dir(path), "log")); code != 4 {
		t.Fatalf("exec = %d, want 4", code)
	}
}

func TestExecEnvMissingDigest(t *testing.T) {
	if _, _, ok := ExecEnv(func(string) string { return "" }); ok {
		t.Fatal("empty environment carries a digest")
	}
	if _, path, ok := ExecEnv(func(key string) string {
		if key == "RUKIE_SPEC_DIGEST" {
			return "sha256:abc"
		}
		return ""
	}); !ok || path != "/.rukie/steps.json" {
		t.Fatalf("env = %q %v, want default steps path", path, ok)
	}
}

func TestInContainerProbeFindings(t *testing.T) {
	findings := InContainerProbe()
	if len(findings) != 8 {
		t.Fatalf("probe returned %d findings, want 8", len(findings))
	}
	names := map[string]bool{}
	for _, finding := range findings {
		names[finding.Name] = true
		if finding.Info == "" {
			t.Fatalf("finding %s has no detail", finding.Name)
		}
	}
	for _, want := range []string{"uid", "uid_map", "readonly_root", "no_caps", "no_new_privs", "seccomp", "pid_namespace", "no_network"} {
		if !names[want] {
			t.Errorf("probe misses %s", want)
		}
	}
}

func TestParseProbeOutput(t *testing.T) {
	findings, rerr := parseProbeOutput("pass uid uid=1000\npass no_caps CapEff is zero\n")
	if rerr != nil || len(findings) != 2 {
		t.Fatalf("parse = %v %v", findings, rerr)
	}
	if _, rerr := parseProbeOutput("pass uid uid=1000\nFAIL no_caps bad\n"); rerr != nil {
		t.Fatalf("parse with failure line: %v", rerr)
	}
}
