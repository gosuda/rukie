package runner

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"flag"
	"fmt"
	stdIo "io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

func tomlUnmarshal(raw []byte, cfg *Config) error {
	return toml.Unmarshal(raw, cfg)
}

func writeConfig(path string, cfg Config) error {
	var buf strings.Builder
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("writeConfig: %w", err)
	}
	if err := os.WriteFile(path, []byte(buf.String()), 0600); err != nil {
		return fmt.Errorf("writeConfig: %w", err)
	}
	return nil
}

func stateDirFlag(fs *flag.FlagSet, io IO) *string {
	return fs.String("state-dir", defaultStateDir(io.getenv), "runner state directory")
}

func loadProbeConfig(io IO, argv []string) (Config, *flag.FlagSet, bool) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Stderr)
	stateDir := stateDirFlag(fs, io)
	configPath := fs.String("config", "", "runner.toml path")
	if err := fs.Parse(argv); err != nil {
		return Config{}, fs, false
	}
	if *configPath != "" {
		cfg, err := LoadConfig(*configPath)
		if err != nil {
			_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". fix the config file.")
			return Config{}, fs, false
		}
		return cfg, fs, true
	}
	cfg := DefaultConfig(*stateDir)
	if raw, err := os.ReadFile(filepath.Join(*stateDir, "runner.toml")); err == nil {
		if uerr := tomlUnmarshal(raw, &cfg); uerr != nil {
			_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: runner.toml does not parse. fix the config file.")
			return Config{}, fs, false
		}
	}
	return cfg, fs, true
}

func runProbe(io IO, argv []string) int {
	cfg, _, ok := loadProbeConfig(io, argv)
	if !ok {
		return ExitUsage
	}
	if err := EnsureStateDir(cfg.StateDir); err != nil {
		emitErr(io.Stderr, fail("STATE_DIR", err.Error(), "create a writable state directory"))
		return ExitConfigUnavailable
	}
	report, rerr := FullProbe(cfg, LiveProbeEnv())
	_ = WriteProbeReport(cfg.StateDir, report)
	if rerr != nil {
		emitErr(io.Stderr, rerr)
		return rerr.Exit
	}
	_, _ = fmt.Fprintln(io.Stdout, "probe ok: podman "+report.Podman)
	return ExitOK
}

func registerHTTP(server, token string, body any, out any) (int, error) {
	raw, err := jsonv2.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("registerHTTP: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(server, "/")+"/-/ci/v1/runners", bytes.NewReader(raw))
	if err != nil {
		return 0, fmt.Errorf("registerHTTP: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("registerHTTP: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := stdIo.ReadAll(stdIo.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, fmt.Errorf("registerHTTP: %w", err)
	}
	if resp.StatusCode != http.StatusCreated {
		return resp.StatusCode, fmt.Errorf("registerHTTP: server refused registration")
	}
	if err := jsonv2.Unmarshal(data, out); err != nil {
		return 0, fmt.Errorf("registerHTTP: %w", err)
	}
	return resp.StatusCode, nil
}

func runRegister(io IO, argv []string) int {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(io.Stderr)
	stateDir := stateDirFlag(fs, io)
	server := fs.String("server", "", "rukie server base URL")
	token := fs.String("token", "", "admin token for ci.runner.create")
	labels := fs.String("labels", "", "comma-separated runner labels")
	scope := fs.String("scope", "untrusted", "trusted or untrusted")
	capacity := fs.Int("capacity", 1, "concurrent slots")
	if err := fs.Parse(argv); err != nil {
		return ExitUsage
	}
	if *server == "" || *token == "" || *labels == "" {
		_, _ = fmt.Fprintln(io.Stderr, "usage: rukie runner register --server URL --token TOKEN --labels a,b [--scope S] [--capacity N]")
		return ExitUsage
	}
	cfg := DefaultConfig(*stateDir)
	cfg.Server = *server
	if err := EnsureStateDir(cfg.StateDir); err != nil {
		emitErr(io.Stderr, fail("STATE_DIR", err.Error(), "create a writable state directory"))
		return ExitConfigUnavailable
	}
	if _, rerr := FullProbe(cfg, LiveProbeEnv()); rerr != nil {
		emitErr(io.Stderr, rerr)
		return rerr.Exit
	}
	var created struct {
		Token  string `json:"token"`
		Runner struct {
			ID string `json:"id"`
		} `json:"runner"`
	}
	if _, err := registerHTTP(*server, *token, map[string]any{
		"labels": strings.Split(*labels, ","), "scope": *scope, "capacity": *capacity,
	}, &created); err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". check the server URL and token.")
		return ExitGeneral
	}
	if err := StoreCredential(cfg.StateDir, Credential{RunnerID: created.Runner.ID, Token: created.Token}); err != nil {
		emitErr(io.Stderr, fail("STATE_DIR", err.Error(), "make the state directory writable"))
		return ExitConfigUnavailable
	}
	cfg.RunnerID = created.Runner.ID
	cfg.Labels = strings.Split(*labels, ",")
	cfg.Scope = *scope
	cfg.Capacity = *capacity
	if err := writeConfig(filepath.Join(cfg.StateDir, "runner.toml"), cfg); err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". make the state directory writable.")
		return ExitGeneral
	}
	_, _ = fmt.Fprintln(io.Stdout, "registered "+created.Runner.ID)
	return ExitOK
}

func runRotate(io IO, argv []string) int {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	fs.SetOutput(io.Stderr)
	stateDir := stateDirFlag(fs, io)
	server := fs.String("server", "", "rukie server base URL")
	token := fs.String("token", "", "admin token for ci.runner.create")
	if err := fs.Parse(argv); err != nil {
		return ExitUsage
	}
	if *server == "" || *token == "" {
		_, _ = fmt.Fprintln(io.Stderr, "usage: rukie runner rotate --server URL --token TOKEN")
		return ExitUsage
	}
	cred, err := LoadCredential(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: no credential to rotate. register first.")
		return ExitGeneral
	}
	prior := DefaultConfig(*stateDir)
	if raw, rerr := os.ReadFile(filepath.Join(*stateDir, "runner.toml")); rerr == nil {
		if uerr := tomlUnmarshal(raw, &prior); uerr != nil {
			_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: runner.toml does not parse. fix the config file.")
			return ExitGeneral
		}
	}
	revoke, err := http.NewRequest(http.MethodPost, strings.TrimSuffix(*server, "/")+"/-/ci/v1/runners/"+cred.RunnerID+"/revoke", strings.NewReader(`{}`))
	if err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". check the server URL.")
		return ExitGeneral
	}
	revoke.Header.Set("Content-Type", "application/json")
	revoke.Header.Set("Authorization", "Bearer "+*token)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(revoke)
	if err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". check the server URL.")
		return ExitGeneral
	}
	_, _ = stdIo.Copy(stdIo.Discard, stdIo.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: the server refused revocation. check the token scope.")
		return ExitGeneral
	}
	var created struct {
		Token  string `json:"token"`
		Runner struct {
			ID string `json:"id"`
		} `json:"runner"`
	}
	if _, err := registerHTTP(*server, *token, map[string]any{
		"labels": prior.Labels, "scope": prior.Scope, "capacity": prior.Capacity,
	}, &created); err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". re-register the runner.")
		return ExitGeneral
	}
	if err := StoreCredential(*stateDir, Credential{RunnerID: created.Runner.ID, Token: created.Token}); err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". make the state directory writable.")
		return ExitGeneral
	}
	_, _ = fmt.Fprintln(io.Stdout, "rotated "+created.Runner.ID)
	return ExitOK
}
