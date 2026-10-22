package runner

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	jsonv2 "encoding/json/v2"
)

// ExecSpec is the in-container step plan.
type ExecSpec struct {
	Steps []ExecStep        `json:"steps"`
	Env   map[string]string `json:"env"`
}

// ExecStep is one shell step.
type ExecStep struct {
	Name string `json:"name"`
	Run  string `json:"run"`
}

// ExecEnv carries the executor contract from the runner.
func ExecEnv(getenv func(string) string) (specDigest, stepsPath string, ok bool) {
	specDigest = getenv("RUKIE_SPEC_DIGEST")
	stepsPath = cmp.Or(getenv("RUKIE_STEPS"), "/.rukie/steps.json")
	return specDigest, stepsPath, specDigest != ""
}

// ProbeFinding is one in-container sandbox check.
type ProbeFinding struct {
	Name string `json:"name"`
	Info string `json:"info"`
	Pass bool   `json:"pass"`
}

// InContainerProbe runs the P11 checklist inside the container.
func InContainerProbe() []ProbeFinding {
	uid := os.Getuid()
	findings := []ProbeFinding{
		{Name: "uid", Pass: uid != 0, Info: "uid=" + strconv.Itoa(uid)},
		{Name: "uid_map", Pass: uidMapCovers(uid), Info: "uid_map covers the runner uid"},
		{Name: "readonly_root", Pass: touchFails("/x-rukie-probe"), Info: "root filesystem is read-only"},
		{Name: "no_caps", Pass: capEffZero(), Info: "CapEff is zero"},
		{Name: "no_new_privs", Pass: noNewPrivs(), Info: "NoNewPrivs is set"},
		{Name: "seccomp", Pass: seccompEnforcing(), Info: "seccomp is enforcing"},
		{Name: "pid_namespace", Pass: os.Getppid() == 1 || ppidIsInit(), Info: "pid namespace is private"},
		{Name: "no_network", Pass: netDevLoopbackOnly(), Info: "only loopback is present"},
	}
	return findings
}

func readProc(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

func uidMapCovers(uid int) bool {
	for line := range strings.SplitSeq(readProc("/proc/self/uid_map"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[2] != "1" {
			inner, _ := strconv.Atoi(fields[0])
			outer, _ := strconv.Atoi(fields[1])
			count, _ := strconv.Atoi(fields[2])
			if inner <= uid && uid < inner+count && outer != 0 {
				return true
			}
		}
	}
	return false
}

func touchFails(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return true
	}
	_ = f.Close()
	_ = os.Remove(path)
	return false
}

func capEffZero() bool {
	for line := range strings.SplitSeq(readProc("/proc/self/status"), "\n") {
		name, value, _ := strings.Cut(line, ":")
		if strings.TrimSpace(name) == "CapEff" {
			return strings.TrimSpace(value) == "0000000000000000"
		}
	}
	return false
}

func noNewPrivs() bool {
	for line := range strings.SplitSeq(readProc("/proc/self/status"), "\n") {
		name, value, _ := strings.Cut(line, ":")
		if strings.TrimSpace(name) == "NoNewPrivs" {
			return strings.TrimSpace(value) == "1"
		}
	}
	return false
}

func seccompEnforcing() bool {
	for line := range strings.SplitSeq(readProc("/proc/self/status"), "\n") {
		name, value, _ := strings.Cut(line, ":")
		if strings.TrimSpace(name) == "Seccomp" {
			return strings.TrimSpace(value) == "2"
		}
	}
	return false
}

func ppidIsInit() bool {
	raw := readProc("/proc/1/comm")
	return strings.TrimSpace(raw) != ""
}

func netDevLoopbackOnly() bool {
	found := false
	for line := range strings.SplitSeq(readProc("/proc/net/dev"), "\n") {
		name, _, _ := strings.Cut(strings.TrimSpace(line), ":")
		name = strings.TrimSpace(name)
		if name == "" || name == "Inter-" {
			continue
		}
		found = true
		if name != "lo" {
			return false
		}
	}
	return found
}

// RunExec executes steps.json and writes log files. Exit codes:
// 0 all passed, 1 a step failed, 3 job deadline, 4 internal, 143 signalled.
func RunExec(specPath, logDir string) int {
	raw, err := os.ReadFile(specPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "E_EXECUTOR_SPEC_DIGEST_MISSING: steps are unreadable. mount /.rukie/steps.json.")
		return 4
	}
	var spec ExecSpec
	if err := jsonv2.Unmarshal(raw, &spec); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "E_EXECUTOR_SPEC_DIGEST_MISSING: steps do not parse. fix the runner payload.")
		return 4
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	for i, step := range spec.Steps {
		writeEvent(logDir, "step_started", i, 0)
		code := runStep(ctx, step, spec.Env, logDir)
		writeEvent(logDir, "step_finished", i, code)
		if code != 0 {
			if ctx.Err() != nil {
				return 143
			}
			return 1
		}
	}
	return 0
}

func writeEvent(logDir, kind string, index, code int) {
	line, err := jsonv2.Marshal(map[string]any{"kind": kind, "index": index, "exit_code": code})
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(logDir, "events"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(line, '\n'))
	_ = f.Close()
}

func runStep(ctx context.Context, step ExecStep, env map[string]string, logDir string) int {
	stdout, err := os.OpenFile(filepath.Join(logDir, "job.stdout"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 4
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile(filepath.Join(logDir, "job.stderr"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 4
	}
	defer func() { _ = stderr.Close() }()
	cmd := exec.Command("/bin/sh", "-c", step.Run)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Env = os.Environ()
	for key, value := range env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 4
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return 0
		}
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return exit.ExitCode()
		}
		return 4
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		select {
		case kerr := <-done:
			_ = kerr
			return 143
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return 143
		}
	}
}
