package runner

import (
	"flag"
	"fmt"
	stdIo "io"
	"os"
	"path/filepath"
	"strings"

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

func readSecretFile(path string, stdin stdIo.Reader) (string, error) {
	if path == "-" {
		raw, err := stdIo.ReadAll(stdIo.LimitReader(stdin, 4096))
		if err != nil {
			return "", fmt.Errorf("readSecretFile: %w", err)
		}
		return strings.TrimSpace(string(raw)), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("readSecretFile: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("readSecretFile: token file must not be group or world readable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("readSecretFile: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func runRegister(io IO, argv []string) int {
	fs := flag.NewFlagSet("register", flag.ContinueOnError)
	fs.SetOutput(io.Stderr)
	stateDir := stateDirFlag(fs, io)
	server := fs.String("server", "", "rukie server base URL")
	tokenFile := fs.String("token-file", "", "single-use registration token file (0600) or - for stdin")
	legacy := fs.String("token", "", "deprecated: use --token-file")
	if err := fs.Parse(argv); err != nil {
		return ExitUsage
	}
	if *legacy != "" {
		_, _ = fmt.Fprintln(io.Stderr, "usage: --token is deprecated; put the registration token in a 0600 file and pass --token-file PATH (or - for stdin)")
		return ExitUsage
	}
	if *server == "" || *tokenFile == "" {
		_, _ = fmt.Fprintln(io.Stderr, "usage: rukie runner register --server URL --token-file PATH")
		return ExitUsage
	}
	secret, serr := readSecretFile(*tokenFile, io.Stdin)
	if serr != nil || secret == "" {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: the registration token is unreadable. check the token file.")
		return ExitGeneral
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
	sealPriv, sealPub, kerr := GenerateSealKey()
	if kerr != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+kerr.Error()+". check the system RNG.")
		return ExitGeneral
	}
	runnerID, credential, err := NewClient(*server, "", "").Register(secret, sealPub)
	if err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". check the server URL and token.")
		return ExitGeneral
	}
	if err := StoreCredential(cfg.StateDir, Credential{RunnerID: runnerID, Token: credential, SealPrivate: sealPriv}); err != nil {
		emitErr(io.Stderr, fail("STATE_DIR", err.Error(), "make the state directory writable"))
		return ExitConfigUnavailable
	}
	cfg.RunnerID = runnerID
	if err := writeConfig(filepath.Join(cfg.StateDir, "runner.toml"), cfg); err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". make the state directory writable.")
		return ExitGeneral
	}
	_, _ = fmt.Fprintln(io.Stdout, "registered "+runnerID)
	return ExitOK
}

func runRotate(io IO, argv []string) int {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	fs.SetOutput(io.Stderr)
	stateDir := stateDirFlag(fs, io)
	server := fs.String("server", "", "rukie server base URL")
	if err := fs.Parse(argv); err != nil {
		return ExitUsage
	}
	if *server == "" {
		_, _ = fmt.Fprintln(io.Stderr, "usage: rukie runner rotate --server URL")
		return ExitUsage
	}
	cred, err := LoadCredential(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: no credential to rotate. register first.")
		return ExitGeneral
	}
	secret, fence, err := NewClient(*server, cred.RunnerID, cred.Token).Rotate(cred.RotFence)
	if err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". check the server URL and clock.")
		return ExitGeneral
	}
	if err := StoreCredential(*stateDir, Credential{RunnerID: cred.RunnerID, Token: secret, RotFence: fence, SealPrivate: cred.SealPrivate}); err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: "+err.Error()+". make the state directory writable.")
		return ExitGeneral
	}
	_, _ = fmt.Fprintln(io.Stdout, "rotated "+cred.RunnerID)
	return ExitOK
}
