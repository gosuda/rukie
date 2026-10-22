package runner

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	jsonv2 "encoding/json/v2"

	"github.com/BurntSushi/toml"
)

const (
	ExitOK                = 0
	ExitGeneral           = 1
	ExitDrainAbandoned    = 75
	ExitConfigUnavailable = 78
	ExitUsage             = 64
)

const (
	HeartbeatIntervalS = 10
	LeaseTTLS          = 30
	SelfFenceS         = 300
	LeaseWaitMaxS      = 25
	ChunkMax           = 64 << 10
)

// Failure is a named runner failure with a fix.
type Failure struct {
	Code   string
	Msg    string
	Remedy string
	Exit   int
}

func (e *Failure) Error() string {
	return "E_" + e.Code + ": " + e.Msg + ". " + e.Remedy
}

// LogLine renders the failure as one JSON log line.
func (e *Failure) LogLine() string {
	raw, err := jsonv2.Marshal(map[string]string{
		"level": "error", "code": "E_" + e.Code, "msg": e.Msg, "remedy": e.Remedy,
	})
	if err != nil {
		return `{"level":"error","code":"E_INTERNAL","msg":"marshal failed"}`
	}
	return string(raw)
}

func fail(code, msg, remedy string) *Failure {
	return &Failure{Code: code, Msg: msg, Remedy: remedy, Exit: ExitGeneral}
}

// Config is the runner.toml shape.
type Config struct {
	Labels      []string `toml:"labels" json:"labels"`
	Server      string   `toml:"server" json:"server"`
	RunnerID    string   `toml:"runner_id" json:"runner_id"`
	Scope       string   `toml:"scope" json:"scope"`
	PodmanPath  string   `toml:"podman_path" json:"podman_path"`
	ProbeImage  string   `toml:"probe_image" json:"probe_image"`
	StateDir    string   `toml:"state_dir" json:"state_dir"`
	Capacity    int      `toml:"capacity" json:"capacity"`
	MaxJobs     int      `toml:"max_jobs" json:"max_jobs"`
	CPUs        float64  `toml:"cpus" json:"cpus"`
	MemoryBytes int64    `toml:"memory_bytes" json:"memory_bytes"`
	Egress      bool     `toml:"egress" json:"egress"`
}

// DefaultConfig returns usable defaults for the given state directory.
func DefaultConfig(stateDir string) Config {
	return Config{
		Scope:       "untrusted",
		Capacity:    1,
		MaxJobs:     1,
		CPUs:        2,
		MemoryBytes: 4 << 30,
		ProbeImage:  "docker.io/library/alpine@sha256:21a3deaa0d32a8057914f36584b5288d2e5ecc984380bc01133930a3a19e4fcd76d",
		StateDir:    stateDir,
	}
}

// LoadConfig reads and validates runner.toml.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("LoadConfig: %w", err)
	}
	cfg := DefaultConfig(filepath.Dir(path))
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("LoadConfig: %w", err)
	}
	if cfg.Scope != "trusted" && cfg.Scope != "untrusted" {
		return Config{}, fmt.Errorf("LoadConfig: scope must be trusted or untrusted")
	}
	if cfg.Capacity < 1 || cfg.Capacity > 256 {
		return Config{}, fmt.Errorf("LoadConfig: capacity must be 1-256")
	}
	if cfg.MaxJobs < 1 {
		cfg.MaxJobs = 1
	}
	return cfg, nil
}

// Credential is the stored runner identity.
type Credential struct {
	RunnerID string `json:"runner_id"`
	Token    string `json:"token"`
}

func credentialPath(stateDir string) string {
	return filepath.Join(stateDir, "credential")
}

// LoadCredential reads the stored runner credential.
func LoadCredential(stateDir string) (Credential, error) {
	raw, err := os.ReadFile(credentialPath(stateDir))
	if err != nil {
		return Credential{}, fmt.Errorf("LoadCredential: %w", err)
	}
	var cred Credential
	if err := jsonv2.Unmarshal(raw, &cred); err != nil {
		return Credential{}, fmt.Errorf("LoadCredential: %w", err)
	}
	if cred.RunnerID == "" || cred.Token == "" {
		return Credential{}, fmt.Errorf("LoadCredential: credential is incomplete")
	}
	return cred, nil
}

// StoreCredential writes the runner credential with mode 0600.
func StoreCredential(stateDir string, cred Credential) error {
	raw, err := jsonv2.Marshal(cred)
	if err != nil {
		return fmt.Errorf("StoreCredential: %w", err)
	}
	tmp, err := os.CreateTemp(stateDir, "credential-*")
	if err != nil {
		return fmt.Errorf("StoreCredential: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("StoreCredential: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("StoreCredential: %w", err)
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("StoreCredential: %w", err)
	}
	if err := os.Rename(tmpName, credentialPath(stateDir)); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("StoreCredential: %w", err)
	}
	return nil
}

// StatePaths lists the runner state layout.
func StatePaths(stateDir string) []string {
	return []string{
		stateDir,
		filepath.Join(stateDir, "attempts"),
		filepath.Join(stateDir, "git"),
		filepath.Join(stateDir, "cache"),
	}
}

// EnsureStateDir creates the state directory with mode 0700.
func EnsureStateDir(stateDir string) error {
	for _, dir := range StatePaths(stateDir) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("EnsureStateDir: %w", err)
		}
	}
	return nil
}

// IO carries runner command input and output.
type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
}

func (io IO) getenv(key string) string {
	get := io.Getenv
	if get == nil {
		get = os.Getenv
	}
	return get(key)
}

func emitErr(w io.Writer, err error) {
	if rerr, ok := errors.AsType[*Failure](err); ok {
		_, _ = fmt.Fprintln(w, rerr.LogLine())
		_, _ = fmt.Fprintln(w, rerr.Error())
		return
	}
	_, _ = fmt.Fprintln(w, "E_INTERNAL: "+err.Error()+". Retry the command.")
}

func defaultStateDir(getenv func(string) string) string {
	get := getenv
	if get == nil {
		get = os.Getenv
	}
	home, _ := os.UserHomeDir()
	base := cmp.Or(get("XDG_STATE_HOME"), filepath.Join(cmp.Or(home, "."), ".local", "state"))
	return filepath.Join(base, "rukie", "runner")
}

// Main dispatches runner subcommands.
func Main(io IO, argv []string) int {
	if len(argv) == 0 {
		_, _ = fmt.Fprintln(io.Stderr, "usage: rukie runner <probe|register|run|status|rotate> [flags]")
		return ExitUsage
	}
	switch argv[0] {
	case "probe":
		return runProbe(io, argv[1:])
	case "register":
		return runRegister(io, argv[1:])
	case "run":
		return runLoop(io, argv[1:])
	case "status":
		return runStatus(io, argv[1:])
	case "rotate":
		return runRotate(io, argv[1:])
	default:
		_, _ = fmt.Fprintln(io.Stderr, "usage: rukie runner <probe|register|run|status|rotate> [flags]")
		return ExitUsage
	}
}
