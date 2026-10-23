package ci_test

import (
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/ci"
)

func TestCIMatrixExpansionGolden(t *testing.T) {
	text := `schema = "rukie.ci/v1"

[[job]]
name = "test"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
pure = true
matrix = { GO = ["1.23", "1.24"] }
steps = [{ name = "test", run = "go test ./..." }]
`
	spec := mustDecode(t, text)
	expanded, err := ci.ExpandMatrix(spec)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(expanded) != 2 {
		t.Fatalf("expanded %d jobs, want 2", len(expanded))
	}
	if expanded[0].Name != "test-1" || expanded[1].Name != "test-2" {
		t.Fatalf("expanded names = %q, %q; want test-1, test-2", expanded[0].Name, expanded[1].Name)
	}
	if expanded[0].Env["GO"] != "1.23" || expanded[1].Env["GO"] != "1.24" {
		t.Fatalf("expanded env = %v, %v", expanded[0].Env, expanded[1].Env)
	}
	if expanded[0].Matrix != nil || expanded[1].Matrix != nil {
		t.Fatal("expanded jobs must not carry matrix")
	}
	first, err := ci.JobSpecDigest(expanded[0])
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	second, err := ci.JobSpecDigest(expanded[1])
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if first == second {
		t.Fatal("distinct matrix rows share a digest")
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("digest %q lacks the sha256 prefix", first)
	}
}

func TestCIMatrixExpansionRejectsOverflow(t *testing.T) {
	var many strings.Builder
	many.WriteString(`schema = "rukie.ci/v1"

[[job]]
name = "flood"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
matrix = { X = [`)
	for i := range 300 {
		if i > 0 {
			many.WriteString(", ")
		}
		many.WriteString(`"v` + strings.Repeat("a", 10) + `"`)
	}
	many.WriteString(`] }
steps = [{ name = "s", run = "echo hi" }]
`)
	if _, err := ci.DecodeSpec([]byte(many.String())); err == nil {
		t.Fatal("a 300-row matrix was accepted")
	}
}

func TestCIMatrixNeedsRewire(t *testing.T) {
	text := `schema = "rukie.ci/v1"

[[job]]
name = "build"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
pure = true
matrix = { GO = ["1.23", "1.24"] }
steps = [{ name = "build", run = "go build ./..." }]

[[job]]
name = "test"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
pure = true
needs = ["build"]
matrix = { GO = ["1.23", "1.24"] }
steps = [{ name = "test", run = "go test ./..." }]
`
	spec := mustDecode(t, text)
	expanded, err := ci.ExpandMatrix(spec)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	if len(expanded) != 4 {
		t.Fatalf("expanded %d jobs, want 4", len(expanded))
	}
	for _, job := range expanded {
		if job.Name == "test-1" && len(job.Needs) != 2 {
			t.Fatalf("test-1 needs = %v, want both build rows", job.Needs)
		}
	}
}

func TestCISpecDefaults(t *testing.T) {
	spec := mustDecode(t, baseSpecTOML())
	var lint ci.JobSpec
	for _, job := range spec.Jobs {
		if job.Name == "lint" {
			lint = job
		}
	}
	if lint.Timeout != "30m" || lint.Network != "none" || lint.Effects != "none" {
		t.Fatalf("defaults = %q %q %q", lint.Timeout, lint.Network, lint.Effects)
	}
	if !lint.Interruptible {
		t.Fatal("interruptible must default to true")
	}
	if lint.Retry.Max != 1 || len(lint.Retry.On) != 2 {
		t.Fatalf("retry default = %+v", lint.Retry)
	}
}

func TestCISpecRejectsUnknownKeys(t *testing.T) {
	text := strings.Replace(baseSpecTOML(), `pure = true`, "pure = true\nbogus = 1", 1)
	if _, err := ci.DecodeSpec([]byte(text)); err == nil {
		t.Fatal("unknown key was accepted")
	}
}

func TestCISpecRejectsDuplicateJobNames(t *testing.T) {
	text := baseSpecTOML() + `
[[job]]
name = "lint"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
steps = [{ name = "again", run = "echo hi" }]
`
	if _, err := ci.DecodeSpec([]byte(text)); err == nil {
		t.Fatal("duplicate job name was accepted")
	}
}

func TestCISpecRejectsMissingDependency(t *testing.T) {
	text := strings.Replace(baseSpecTOML(), `needs = ["lint"]`, `needs = ["ghost"]`, 1)
	if _, err := ci.DecodeSpec([]byte(text)); err == nil {
		t.Fatal("missing dependency was accepted")
	}
}

func TestCISpecRejectsCycle(t *testing.T) {
	text := `schema = "rukie.ci/v1"

[[job]]
name = "a"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
needs = ["b"]
steps = [{ name = "s", run = "echo hi" }]

[[job]]
name = "b"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
needs = ["a"]
steps = [{ name = "s", run = "echo hi" }]
`
	if _, err := ci.DecodeSpec([]byte(text)); err == nil {
		t.Fatal("dependency cycle was accepted")
	}
}

func TestCISpecRejectsImpureNeedsForPureJob(t *testing.T) {
	text := `schema = "rukie.ci/v1"

[[job]]
name = "base"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
network = "egress"
steps = [{ name = "s", run = "curl example.com" }]

[[job]]
name = "top"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
needs = ["base"]
pure = true
steps = [{ name = "s", run = "go test ./..." }]
`
	if _, err := ci.DecodeSpec([]byte(text)); err == nil {
		t.Fatal("pure job needing an impure job was accepted")
	}
}

func TestCISpecRejectsRequiredJobWithSecrets(t *testing.T) {
	text := `schema = "rukie.ci/v1"

[land]
required = ["secret-job"]

[[job]]
name = "secret-job"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
secrets = ["DEPLOY_TOKEN"]
steps = [{ name = "s", run = "echo hi" }]
`
	if _, err := ci.DecodeSpec([]byte(text)); err == nil {
		t.Fatal("required job with secrets was accepted")
	}
}

func TestCISpecRejectsExclusiveInterruptible(t *testing.T) {
	text := `schema = "rukie.ci/v1"

[[job]]
name = "deploy"
runs_on = ["linux-amd64"]
image = "` + testImage + `"
effects = "exclusive"
interruptible = true
steps = [{ name = "s", run = "echo hi" }]
`
	if _, err := ci.DecodeSpec([]byte(text)); err == nil {
		t.Fatal("exclusive interruptible job was accepted")
	}
}

func TestCICanonicalJSONGolden(t *testing.T) {
	got, err := ci.CanonicalJSON(map[string]any{
		"b": 1.0, "a": "x<y>&\"q\"\u2028",
		"nested": map[string]any{"z": true, "a": nil},
		"list":   []any{1.0, "s"},
	})
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	want := `{"a":"x<y>&\"q\" ","b":1,"list":[1,"s"],"nested":{"a":null,"z":true}}`
	if string(got) != want {
		t.Fatalf("canonical = %s, want %s", got, want)
	}
}

func TestCIActionDigestIgnoresTimeout(t *testing.T) {
	spec := mustDecode(t, baseSpecTOML())
	first, err := ci.JobSpecDigest(spec.Jobs[0])
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	altered := spec.Jobs[0]
	altered.Timeout = "60m"
	second, err := ci.JobSpecDigest(altered)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if first != second {
		t.Fatalf("timeout changes the job digest: %s vs %s", first, second)
	}
	altered.Steps = []ci.Step{{Name: "vet", Run: "go vet ./... --changed"}}
	third, err := ci.JobSpecDigest(altered)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if third == first {
		t.Fatal("a step change leaves the job digest unchanged")
	}
}
