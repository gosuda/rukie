package main

import (
	"os"

	"github.com/gosuda/rukie/internal/runner"
)

// runRunner dispatches hand-written runner commands.
func runRunner(argv []string) int {
	return runner.Main(runner.IO{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr, Getenv: os.Getenv}, argv)
}

// runJobExec dispatches the hidden in-container executor.
func runJobExec(argv []string) int {
	probe := false
	steps := ""
	logDir := "log"
	kept := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		switch argv[i] {
		case "--probe":
			probe = true
		case "--steps":
			i++
			if i < len(argv) {
				steps = argv[i]
			}
		case "--log-dir":
			i++
			if i < len(argv) {
				logDir = argv[i]
			}
		default:
			kept = append(kept, argv[i])
		}
	}
	_ = kept
	if probe {
		return runProbeExec()
	}
	if steps == "" {
		steps = os.Getenv("RUKIE_STEPS")
		if steps == "" {
			steps = "/.rukie/steps.json"
		}
	}
	if _, _, ok := runner.ExecEnv(os.Getenv); !ok {
		_, _ = os.Stderr.WriteString("E_EXECUTOR_SPEC_DIGEST_MISSING: RUKIE_SPEC_DIGEST is not set. the runner must set it.\n")
		return 4
	}
	return runner.RunExec(steps, logDir)
}

func runProbeExec() int {
	failed := false
	for _, finding := range runner.InContainerProbe() {
		status := "pass"
		if !finding.Pass {
			status = "FAIL"
			failed = true
		}
		_, _ = os.Stdout.WriteString(status + " " + finding.Name + " " + finding.Info + "\n")
	}
	if failed {
		return 1
	}
	return 0
}
