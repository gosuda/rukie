package runner

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	jsonv2 "encoding/json/v2"
)

func runStatus(io IO, argv []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Stderr)
	stateDir := stateDirFlag(fs, io)
	if err := fs.Parse(argv); err != nil {
		return ExitUsage
	}
	cred, err := LoadCredential(*stateDir)
	if err != nil {
		_, _ = fmt.Fprintln(io.Stdout, "not registered")
		return ExitOK
	}
	_, _ = fmt.Fprintln(io.Stdout, "runner "+cred.RunnerID)
	raw, err := os.ReadFile(filepath.Join(*stateDir, "probe.json"))
	if err != nil {
		_, _ = fmt.Fprintln(io.Stdout, "probe: never ran")
		return ExitOK
	}
	var report ProbeReport
	if err := jsonv2.Unmarshal(raw, &report); err != nil {
		_, _ = fmt.Fprintln(io.Stdout, "probe: unreadable")
		return ExitOK
	}
	_, _ = fmt.Fprintln(io.Stdout, "probe checks: "+fmt.Sprintf("%d", len(report.Checks)))
	entries, rerr := os.ReadDir(filepath.Join(*stateDir, "attempts"))
	if rerr != nil {
		_, _ = fmt.Fprintln(io.Stdout, "attempts: unreadable")
		return ExitOK
	}
	_, _ = fmt.Fprintln(io.Stdout, "attempts: "+fmt.Sprintf("%d", len(entries)))
	unlock, rerr := flockFile(filepath.Join(*stateDir, "runner.lock"))
	if rerr != nil {
		_, _ = fmt.Fprintln(io.Stdout, "lock: held")
		return ExitOK
	}
	unlock()
	_, _ = fmt.Fprintln(io.Stdout, "lock: free")
	return ExitOK
}

func runLoop(io IO, argv []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Stderr)
	stateDir := stateDirFlag(fs, io)
	server := fs.String("server", "", "rukie server base URL")
	once := fs.Bool("once", false, "exit after one lease")
	if err := fs.Parse(argv); err != nil {
		return ExitUsage
	}
	cfg := DefaultConfig(*stateDir)
	if raw, err := os.ReadFile(filepath.Join(*stateDir, "runner.toml")); err == nil {
		if uerr := tomlUnmarshal(raw, &cfg); uerr != nil {
			_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: runner.toml does not parse. fix the config file.")
			return ExitGeneral
		}
	}
	if *server != "" {
		cfg.Server = *server
	}
	if cfg.Server == "" {
		_, _ = fmt.Fprintln(io.Stderr, "usage: rukie runner run --server URL [--once]")
		return ExitUsage
	}
	cred, err := LoadCredential(cfg.StateDir)
	if err != nil {
		_, _ = fmt.Fprintln(io.Stderr, "E_INVALID: the runner is not registered. run register first.")
		return ExitGeneral
	}
	if serr := EnsureStateDir(cfg.StateDir); serr != nil {
		emitErr(io.Stderr, fail("STATE_DIR", serr.Error(), "create a writable state directory"))
		return ExitConfigUnavailable
	}
	report, prerr := FullProbe(cfg, LiveProbeEnv())
	_ = WriteProbeReport(cfg.StateDir, report)
	if prerr != nil {
		emitErr(io.Stderr, prerr)
		return prerr.Exit
	}
	cfg.PodmanPath = report.Podman
	if cfg.PodmanPath == "" {
		cfg.PodmanPath = "podman"
	}
	unlock, err := flockFile(filepath.Join(cfg.StateDir, "runner.lock"))
	if err != nil {
		emitErr(io.Stderr, fail("RUNNER_ALREADY_RUNNING", "another runner holds the state directory lock", "stop the other runner first"))
		return ExitConfigUnavailable
	}
	defer unlock()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	loop := &Loop{
		Config: cfg,
		Client: NewClient(cfg.Server, cred.RunnerID, cred.Token),
		Exec:   PodmanExec{Podman: cfg.PodmanPath},
		Once:   *once,
	}
	return loop.RunLoop(ctx)
}
