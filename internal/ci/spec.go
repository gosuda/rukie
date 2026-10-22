package ci

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/google/jsonschema-go/jsonschema"
)

func schemaPointer(instance any) string {
	if m, ok := instance.(map[string]any); ok {
		if _, ok := m["job"]; ok {
			return "/job"
		}
	}
	return "/"
}

//go:embed schema.json
var schemaBytes []byte

type Spec struct {
	Schema   string    `toml:"schema" json:"schema"`
	Land     Land      `toml:"land" json:"land"`
	Jobs     []JobSpec `toml:"job" json:"job"`
	Triggers Triggers  `toml:"triggers" json:"triggers"`
}

type Triggers struct {
	Push     []string   `toml:"push" json:"push"`
	Land     []string   `toml:"land" json:"land"`
	Schedule []Schedule `toml:"schedule" json:"schedule"`
	Change   bool       `toml:"change" json:"change"`
}

type Schedule struct {
	Cron   string `toml:"cron" json:"cron"`
	Branch string `toml:"branch" json:"branch"`
}
type Land struct {
	Required []string `toml:"required" json:"required"`
}
type Step struct {
	Name string `toml:"name" json:"name"`
	Run  string `toml:"run" json:"run"`
}
type Retry struct {
	On  []string `toml:"on" json:"on"`
	Max int      `toml:"max" json:"max"`
}
type JobSpec struct {
	Matrix        map[string][]string `toml:"matrix,omitempty" json:"matrix,omitempty"`
	Env           map[string]string   `toml:"env" json:"env"`
	Effects       string              `toml:"effects" json:"effects"`
	Timeout       string              `toml:"timeout" json:"timeout"`
	Network       string              `toml:"network" json:"network"`
	Name          string              `toml:"name" json:"name"`
	Image         string              `toml:"image" json:"image"`
	Secrets       []string            `toml:"secrets" json:"secrets"`
	Needs         []string            `toml:"needs" json:"needs"`
	Artifacts     []string            `toml:"artifacts" json:"artifacts"`
	Steps         []Step              `toml:"steps" json:"steps"`
	RunsOn        []string            `toml:"runs_on" json:"runs_on"`
	Retry         Retry               `toml:"retry" json:"retry"`
	Interruptible bool                `toml:"interruptible" json:"interruptible"`
	Pure          bool                `toml:"pure" json:"pure"`
}

func Schema() []byte { return bytes.Clone(schemaBytes) }

func DecodeSpec(data []byte) (Spec, error) {
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		var parseErr *toml.ParseError
		if errors.As(err, &parseErr) {
			return Spec{}, &SpecError{Line: parseErr.Position.Line, Col: parseErr.Position.Col, Message: parseErr.Message}
		}
		return Spec{}, &SpecError{Message: err.Error()}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return Spec{}, &SpecError{Message: err.Error()}
	}
	var instance any
	if err = json.Unmarshal(b, &instance); err != nil {
		return Spec{}, &SpecError{Message: err.Error()}
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal(schemaBytes, &schema); err != nil {
		return Spec{}, &SpecError{Message: err.Error()}
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return Spec{}, &SpecError{Message: err.Error()}
	}
	if err = resolved.Validate(instance); err != nil {
		return Spec{}, &SpecError{Pointer: schemaPointer(instance), Message: err.Error()}
	}
	var spec Spec
	metadata, err := toml.Decode(string(data), &spec)
	if err != nil {
		var parseErr *toml.ParseError
		if errors.As(err, &parseErr) {
			return Spec{}, &SpecError{Line: parseErr.Position.Line, Col: parseErr.Position.Col, Message: parseErr.Message}
		}
		return Spec{}, &SpecError{Message: err.Error()}
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return Spec{}, &SpecError{Pointer: "/" + strings.Join(undecoded[len(undecoded)-1], "/"), Message: "unknown key"}
	}
	var rawJobs []map[string]any
	switch tables := raw["job"].(type) {
	case []map[string]any:
		rawJobs = tables
	case []any:
		for _, item := range tables {
			table, ok := item.(map[string]any)
			if !ok {
				return Spec{}, fmt.Errorf("DecodeSpec: job table is malformed")
			}
			rawJobs = append(rawJobs, table)
		}
	default:
		return Spec{}, fmt.Errorf("DecodeSpec: job table is missing")
	}
	if len(rawJobs) != len(spec.Jobs) {
		return Spec{}, &SpecError{Pointer: "/job", Message: "job table changed during decode"}
	}
	for i := range spec.Jobs {
		j := &spec.Jobs[i]
		rawJob := rawJobs[i]
		_, hasInterruptible := rawJob["interruptible"]
		_, hasRetry := rawJob["retry"]
		if j.Timeout == "" {
			j.Timeout = "30m"
		}
		if j.Network == "" {
			j.Network = "none"
		}
		if j.Effects == "" {
			j.Effects = "none"
		}
		if !hasInterruptible {
			j.Interruptible = true
		}
		if !hasRetry {
			j.Retry = Retry{Max: 1, On: []string{"infra", "lost"}}
		}
		if j.Effects == "exclusive" {
			j.Interruptible = false
		}
		slices.Sort(j.RunsOn)
		slices.Sort(j.Needs)
		slices.Sort(j.Secrets)
		slices.Sort(j.Artifacts)
		slices.Sort(j.Retry.On)
	}
	if err = ValidateSpec(spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func ValidateSpec(spec Spec) error {
	if spec.Schema != "rukie.ci/v1" || len(spec.Jobs) == 0 || len(spec.Jobs) > 256 {
		return specError("/schema", "schema must be rukie.ci/v1 with 1 to 256 jobs")
	}
	names := make(map[string]JobSpec, len(spec.Jobs))
	for i := range spec.Jobs {
		j := &spec.Jobs[i]
		if _, exists := names[j.Name]; exists {
			return specError("/job", "duplicate job "+strconv.Quote(j.Name))
		}
		names[j.Name] = *j
		timeout, err := time.ParseDuration(j.Timeout)
		if err != nil || timeout < time.Minute || timeout > 24*time.Hour {
			return specError("/job/"+strconv.Itoa(i)+"/timeout", "timeout must be 1m to 24h")
		}
		platforms := 0
		for _, label := range j.RunsOn {
			if label == "linux-amd64" || label == "linux-arm64" {
				platforms++
			}
		}
		if platforms != 1 {
			return specError("/job/"+strconv.Itoa(i)+"/runs_on", "job needs exactly one platform label")
		}
		if j.Pure && (j.Network != "none" || j.Effects != "none" || len(j.Secrets) > 0) {
			return specError("/job/"+strconv.Itoa(i)+"/pure", "pure jobs allow no network, secrets, or effects")
		}
		if j.Effects == "exclusive" && (j.Interruptible || slices.Contains(j.Retry.On, "lost")) {
			return specError("/job/"+strconv.Itoa(i)+"/effects", "exclusive jobs cannot interrupt or retry loss")
		}
		stepNames := make(map[string]bool, len(j.Steps))
		for _, step := range j.Steps {
			if stepNames[step.Name] {
				return specError("/job/"+strconv.Itoa(i)+"/steps", "duplicate step "+strconv.Quote(step.Name))
			}
			stepNames[step.Name] = true
		}
		for _, artifact := range j.Artifacts {
			if strings.ContainsRune(artifact, 0) || path.IsAbs(artifact) || path.Clean(artifact) != artifact || artifact == "." || artifact == ".." || strings.HasPrefix(artifact, "../") {
				return specError("/job/"+strconv.Itoa(i)+"/artifacts", "invalid artifact "+strconv.Quote(artifact))
			}
		}
	}
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] {
			return specError("/job", "cyclic dependency at "+strconv.Quote(name))
		}
		if visited[name] {
			return nil
		}
		visiting[name] = true
		j := names[name]
		for _, need := range j.Needs {
			dep, ok := names[need]
			if !ok {
				return specError("/job", "missing dependency "+strconv.Quote(need))
			}
			if j.Pure && !dep.Pure {
				return specError("/job", "pure job needs impure "+strconv.Quote(need))
			}
			if err := visit(need); err != nil {
				return err
			}
		}
		visiting[name] = false
		visited[name] = true
		return nil
	}
	for name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	for _, name := range spec.Land.Required {
		j, ok := names[name]
		if !ok || j.Network != "none" || j.Effects != "none" || len(j.Secrets) > 0 {
			return specError("/land/required", "invalid required job "+strconv.Quote(name))
		}
	}
	return nil
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func JobSpecDigest(job JobSpec) (string, error) {
	timeless := job
	timeless.Timeout = ""
	b, err := CanonicalJSON(timeless)
	if err != nil {
		return "", err
	}
	return Digest(b), nil
}
