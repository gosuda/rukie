package runner

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPassgateReportsEveryNestedFailureOnce(t *testing.T) {
	script, err := filepath.Abs("../../tools/passgate.sh")
	if err != nil {
		t.Fatalf("passgate script path: %v", err)
	}
	inner := "bash " + script + " NEVER -- bash -c 'echo FAIL: nested boom; exit 2'"
	outer := "echo '--- FAIL: TestAlpha (0.00s)'; " + inner + "; echo 'make: *** [lint] Error 1'; echo 'runner/foo.go:12: vet failed'; exit 3"
	cmd := exec.Command("bash", script, "NEVER-MATCH", "--", "bash", "-c", outer)
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "RUKIE_PASSGATE_ACTIVE=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GITHUB_ACTIONS=true")
	cmd.Env = env
	out, runErr := cmd.CombinedOutput()
	text := string(out)
	if runErr == nil {
		t.Fatalf("outer gate exits 0, want nonzero: %q", text)
	}
	exitErr := &exec.ExitError{}
	if !errors.As(runErr, &exitErr) {
		t.Fatalf("outer gate error is %T, want *exec.ExitError: %q", runErr, text)
	}
	if code := exitErr.ExitCode(); code != 3 {
		t.Fatalf("outer gate exits %d, want 3: %q", code, text)
	}
	if n := strings.Count(text, "::error title=Required check failed::"); n != 1 {
		t.Fatalf("outer gate emits %d annotations, want 1: %q", n, text)
	}
	annotation := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "::error title=Required check failed::") {
			annotation = line
		}
	}
	if !strings.Contains(annotation, "TestAlpha") {
		t.Fatalf("aggregate annotation omits the test failure: %q", text)
	}
	if !strings.Contains(annotation, "[lint]") {
		t.Fatalf("aggregate annotation omits the independent failure: %q", text)
	}
	if !strings.Contains(annotation, "foo.go") {
		t.Fatalf("aggregate annotation omits the file-scoped failure: %q", text)
	}
}
