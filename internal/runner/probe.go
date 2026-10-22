package runner

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	jsonv2 "encoding/json/v2"
)

const (
	minPodmanMajor = 6
	minPodmanMinor = 1
	minPodmanPatch = 4
	minGitMajor    = 2
	minGitMinor    = 31
)

// ProbeCheck is one ordered startup probe step.
type ProbeCheck struct {
	Name string `json:"name"`
	Code string `json:"code"`
	Msg  string `json:"msg"`
	Exit int    `json:"exit"`
}

// ProbeReport is the ordered probe outcome and probe.json body.
type ProbeReport struct {
	Checks   []ProbeCheck `json:"checks"`
	Failures []ProbeCheck `json:"failures"`
	Podman   string       `json:"podman"`
	Egress   bool         `json:"egress"`
}

// ProbeEnv observes the host. Tests inject fixtures.
type ProbeEnv struct {
	LookPath func(name string) (string, error)
	Run      func(name string, args ...string) (stdout []byte, stderr []byte, err error)
	ReadFile func(path string) ([]byte, error)
	Stat     func(path string) (os.FileInfo, error)
	Getenv   func(string) string
	UID      func() int
	Username func() string
	Lock     func(path string) (func(), error)
}

// LiveProbeEnv observes the real host.
func LiveProbeEnv() ProbeEnv {
	return ProbeEnv{
		LookPath: exec.LookPath,
		Run: func(name string, args ...string) ([]byte, []byte, error) {
			cmd := exec.Command(name, args...)
			var stdout, stderr strings.Builder
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			err := cmd.Run()
			return []byte(stdout.String()), []byte(stderr.String()), err
		},
		ReadFile: os.ReadFile,
		Stat:     os.Stat,
		Getenv:   os.Getenv,
		UID:      os.Getuid,
		Username: func() string {
			u, err := user.Current()
			if err != nil {
				return strconv.Itoa(os.Getuid())
			}
			return u.Username
		},
		Lock: flockFile,
	}
}

func flockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("flockFile: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flockFile: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

type probeCtx struct {
	cfg    Config
	env    ProbeEnv
	podman string
	report ProbeReport
}

func (c *probeCtx) fail(name, code, msg string, exit int) *Failure {
	check := ProbeCheck{Name: name, Code: "E_" + code, Msg: msg, Exit: exit}
	c.report.Checks = append(c.report.Checks, check)
	c.report.Failures = append(c.report.Failures, check)
	rerr := fail(code, msg, "fix the host and run the probe again")
	rerr.Exit = exit
	return rerr
}

func (c *probeCtx) pass(name string) {
	c.report.Checks = append(c.report.Checks, ProbeCheck{Name: name})
}

func probeP01(c *probeCtx) *Failure {
	if c.cfg.PodmanPath != "" {
		if _, err := c.env.Stat(c.cfg.PodmanPath); err != nil {
			return c.fail("P01", "PODMAN_MISSING", "podman is not at "+c.cfg.PodmanPath, ExitConfigUnavailable)
		}
		c.podman = c.cfg.PodmanPath
		c.pass("P01")
		return nil
	}
	path, err := c.env.LookPath("podman")
	if err != nil || path == "" {
		return c.fail("P01", "PODMAN_MISSING", "podman is not on PATH", ExitConfigUnavailable)
	}
	c.podman = path
	c.pass("P01")
	return nil
}

func parseTriple(text string) (int, int, int, bool) {
	fields := strings.FieldsFunc(strings.TrimSpace(text), func(r rune) bool {
		return r < '0' || r > '9'
	})
	if len(fields) < 3 {
		return 0, 0, 0, false
	}
	tail := fields[len(fields)-3:]
	major, err1 := strconv.Atoi(tail[0])
	minor, err2 := strconv.Atoi(tail[1])
	patch, err3 := strconv.Atoi(tail[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, false
	}
	return major, minor, patch, true
}

func atLeast(major, minor, patch, wantMajor, wantMinor, wantPatch int) bool {
	if major != wantMajor {
		return major > wantMajor
	}
	if minor != wantMinor {
		return minor > wantMinor
	}
	return patch >= wantPatch
}

func nestedMap(m map[string]any, keys ...string) (any, bool) {
	var cur any = m
	for _, key := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		lower := strings.ToLower(key)
		next, ok := obj[key]
		if !ok {
			next, ok = obj[lower]
		}
		if !ok {
			for name, value := range obj {
				if strings.ToLower(name) == lower {
					next, ok = value, true
					break
				}
			}
		}
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

func probeP02(c *probeCtx) *Failure {
	stdout, _, err := c.env.Run(c.podman, "version", "--format", "json")
	if err != nil {
		return c.fail("P02", "PODMAN_VERSION", "podman version did not run", ExitConfigUnavailable)
	}
	var decoded map[string]any
	if jerr := jsonv2.Unmarshal(stdout, &decoded); jerr != nil {
		return c.fail("P02", "PODMAN_VERSION", "podman version output did not parse", ExitConfigUnavailable)
	}
	found, _ := nestedMap(decoded, "client", "version")
	versionStr, _ := found.(string)
	major, minor, patch, ok := parseTriple(versionStr)
	if !ok || !atLeast(major, minor, patch, minPodmanMajor, minPodmanMinor, minPodmanPatch) {
		return c.fail("P02", "PODMAN_VERSION", "podman "+versionStr+" is below 6.1.4", ExitConfigUnavailable)
	}
	c.pass("P02")
	return nil
}

func probeP03(c *probeCtx) *Failure {
	stdout, _, err := c.env.Run(c.podman, "info", "--format", "json")
	if err != nil {
		return c.fail("P03", "PODMAN_ROOTFUL", "podman info did not run", ExitConfigUnavailable)
	}
	var decoded map[string]any
	if jerr := jsonv2.Unmarshal(stdout, &decoded); jerr != nil {
		return c.fail("P03", "PODMAN_ROOTFUL", "podman info output did not parse", ExitConfigUnavailable)
	}
	rootless, _ := nestedMap(decoded, "host", "security", "rootless")
	rootlessTrue := rootless == true || rootless == "true"
	if !rootlessTrue || c.env.UID() == 0 {
		return c.fail("P03", "PODMAN_ROOTFUL", "the runner must run rootless podman as a non-root user", ExitConfigUnavailable)
	}
	c.pass("P03")
	return nil
}

func probeP04(c *probeCtx) *Failure {
	raw, err := c.env.ReadFile("/sys/fs/cgroup/cgroup.controllers")
	if err != nil || len(strings.Fields(string(raw))) == 0 {
		return c.fail("P04", "CGROUP_V1", "cgroup v2 controllers are not visible", ExitConfigUnavailable)
	}
	c.pass("P04")
	return nil
}

func probeP05(c *probeCtx) *Failure {
	self, err := c.env.ReadFile("/proc/self/cgroup")
	if err != nil {
		return c.fail("P05", "CGROUP_NOT_DELEGATED", "the runner cgroup is not readable", ExitConfigUnavailable)
	}
	rel := ""
	for line := range strings.SplitSeq(string(self), "\n") {
		hierarchy, rest, _ := strings.Cut(line, ":")
		_, path, _ := strings.Cut(rest, ":")
		if strings.TrimSpace(hierarchy) == "0" {
			rel = strings.TrimSpace(path)
		}
	}
	if rel == "" {
		return c.fail("P05", "CGROUP_NOT_DELEGATED", "the runner is not in a v2 cgroup", ExitConfigUnavailable)
	}
	raw, err := c.env.ReadFile("/sys/fs/cgroup" + rel + "/cgroup.subtree_control")
	if err != nil {
		return c.fail("P05", "CGROUP_NOT_DELEGATED", "the delegating cgroup controllers are not readable", ExitConfigUnavailable)
	}
	present := " "
	for field := range strings.FieldsSeq(string(raw)) {
		present += field + " "
	}
	for _, want := range []string{"cpu", "memory", "pids"} {
		if !strings.Contains(present, " "+want+" ") {
			return c.fail("P05", "CGROUP_NOT_DELEGATED", "cgroup controller "+want+" is not delegated", ExitConfigUnavailable)
		}
	}
	c.pass("P05")
	return nil
}

func probeP06(c *probeCtx) *Failure {
	dir := c.env.Getenv("XDG_RUNTIME_DIR")
	info, err := c.env.Stat(cmp.Or(dir, "\x00"))
	if err != nil || dir == "" || !info.IsDir() {
		return c.fail("P06", "XDG_RUNTIME", "XDG_RUNTIME_DIR is not a directory", ExitConfigUnavailable)
	}
	c.pass("P06")
	return nil
}

func subidHas(raw []byte, name string, uid int) bool {
	ident := cmp.Or(name, strconv.Itoa(uid))
	for line := range strings.SplitSeq(string(raw), "\n") {
		parts := strings.Split(line, ":")
		if len(parts) == 3 && parts[0] == ident {
			return true
		}
	}
	return false
}

func probeP07(c *probeCtx) *Failure {
	subuid, uerr := c.env.ReadFile("/etc/subuid")
	subgid, gerr := c.env.ReadFile("/etc/subgid")
	if uerr != nil || gerr != nil {
		return c.fail("P07", "SUBUID_MISSING", "subuid or subgid ranges are not configured", ExitConfigUnavailable)
	}
	if !subidHas(subuid, c.env.Username(), c.env.UID()) || !subidHas(subgid, c.env.Username(), c.env.UID()) {
		return c.fail("P07", "SUBUID_MISSING", "no subuid and subgid range covers the runner user", ExitConfigUnavailable)
	}
	c.pass("P07")
	return nil
}

// ClassifyUsernsError reports whether podman stderr shows a denied user namespace.
func ClassifyUsernsError(stderr string) bool {
	lowered := strings.ToLower(stderr)
	for _, marker := range []string{"userns", "user namespace", "unprivileged", "apparmor", "newuidmap", "newgidmap", "subuid", "subgid"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

func probeP09(c *probeCtx) *Failure {
	stdout, _, err := c.env.Run("git", "--version")
	if err != nil {
		return c.fail("P09", "GIT_MISSING", "git is not on PATH", ExitConfigUnavailable)
	}
	major, minor, _, ok := parseTriple(string(stdout))
	if !ok || major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return c.fail("P09", "GIT_VERSION", "git is below 2.31", ExitConfigUnavailable)
	}
	c.pass("P09")
	return nil
}

func probeP13(c *probeCtx) *Failure {
	info, err := c.env.Stat(c.cfg.StateDir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return c.fail("P13", "STATE_DIR", "the state directory is missing or not mode 0700", ExitConfigUnavailable)
	}
	unlock, err := c.env.Lock(filepath.Join(c.cfg.StateDir, "runner.lock"))
	if err != nil {
		return c.fail("P13", "RUNNER_ALREADY_RUNNING", "another runner holds the state directory lock", ExitConfigUnavailable)
	}
	unlock()
	c.pass("P13")
	return nil
}

type probeStep struct {
	name string
	run  func(c *probeCtx) *Failure
}

// RunProbe executes P1-P9 and P13 against the host. P10-P12 need a
// container runtime and run inside RunSandboxProbe once podman exists.
func RunProbe(cfg Config, env ProbeEnv) (ProbeReport, *Failure) {
	c := &probeCtx{cfg: cfg, env: env}
	c.report.Egress = cfg.Egress
	for _, step := range []probeStep{
		{name: "P01", run: probeP01},
		{name: "P02", run: probeP02},
		{name: "P03", run: probeP03},
		{name: "P04", run: probeP04},
		{name: "P05", run: probeP05},
		{name: "P06", run: probeP06},
		{name: "P07", run: probeP07},
		{name: "P09", run: probeP09},
		{name: "P13", run: probeP13},
	} {
		if rerr := step.run(c); rerr != nil {
			c.report.Podman = c.podman
			return c.report, rerr
		}
	}
	c.report.Podman = c.podman
	return c.report, nil
}

// WriteProbeReport stores probe.json in the state directory.
func WriteProbeReport(stateDir string, report ProbeReport) error {
	raw, err := jsonv2.Marshal(report)
	if err != nil {
		return fmt.Errorf("WriteProbeReport: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "probe.json"), raw, 0600); err != nil {
		return fmt.Errorf("WriteProbeReport: %w", err)
	}
	return nil
}

func probeP10Pull(c *probeCtx) *Failure {
	if _, _, err := c.env.Run(c.podman, "image", "exists", c.cfg.ProbeImage); err == nil {
		c.pass("P10")
		return nil
	}
	if _, _, err := c.env.Run(c.podman, "pull", "--quiet", c.cfg.ProbeImage); err != nil {
		return c.fail("P10", "PROBE_IMAGE", "the probe image did not pull", ExitDrainAbandoned)
	}
	c.pass("P10")
	return nil
}

// SandboxProbe runs job-exec --probe through the production builder.
func SandboxProbe(cfg Config, env ProbeEnv, podman string) ([]ProbeFinding, *Failure) {
	work, err := os.MkdirTemp("", "rukie-probe-work")
	if err != nil {
		return nil, fail("SANDBOX_PROBE", err.Error(), "make temporary directories writable")
	}
	defer func() { _ = os.RemoveAll(work) }()
	logDir, err := os.MkdirTemp("", "rukie-probe-log")
	if err != nil {
		return nil, fail("SANDBOX_PROBE", err.Error(), "make temporary directories writable")
	}
	defer func() { _ = os.RemoveAll(logDir) }()
	envFile, err := os.CreateTemp("", "rukie-probe-env")
	if err != nil {
		return nil, fail("SANDBOX_PROBE", err.Error(), "make temporary directories writable")
	}
	_ = envFile.Close()
	defer func() { _ = os.Remove(envFile.Name()) }()
	rukieDir, err := os.MkdirTemp("", "rukie-probe-rukie")
	if err != nil {
		return nil, fail("SANDBOX_PROBE", err.Error(), "make temporary directories writable")
	}
	defer func() { _ = os.RemoveAll(rukieDir) }()
	if lerr := linkOrCopyBinary(rukieDir); lerr != nil {
		return nil, fail("SANDBOX_PROBE", lerr.Error(), "make temporary directories writable")
	}
	args := BuildPodmanArgs(podman, SandboxRun{
		Name: "rukie-probe", Image: cfg.ProbeImage,
		UID: env.UID(), GID: env.UID(),
		CPUs: 0.25, MemoryMB: 64, PidsLimit: 64, TmpMB: 64,
		WorkHost: work, LogHost: logDir, EnvFile: envFile.Name(),
		RukieDir: rukieDir, Probe: true,
	})
	if serr := AssertBuilderSafe(args); serr != nil {
		return nil, fail("SANDBOX_PROBE", serr.Error(), "fix the argument builder")
	}
	stdout, stderr, err := env.Run(args[0], args[1:]...)
	if err != nil {
		if ClassifyUsernsError(string(stderr)) {
			rerr := fail("USERNS_DENIED", "the container runtime denied the user namespace", "allow unprivileged user namespaces or add an AppArmor profile")
			rerr.Exit = ExitConfigUnavailable
			return nil, rerr
		}
		rerr := fail("SANDBOX_PROBE", strings.TrimSpace(string(stderr)), "fix the container runtime")
		rerr.Exit = ExitConfigUnavailable
		return nil, rerr
	}
	findings, rerr := parseProbeOutput(string(stdout))
	if rerr != nil {
		rerr.Exit = ExitConfigUnavailable
		return nil, rerr
	}
	for _, finding := range findings {
		if !finding.Pass {
			rerr := fail("SANDBOX_PROBE:"+finding.Name, finding.Info, "fix the host sandbox")
			rerr.Exit = ExitConfigUnavailable
			return findings, rerr
		}
	}
	return findings, nil
}

func parseProbeOutput(stdout string) ([]ProbeFinding, *Failure) {
	var findings []ProbeFinding
	for line := range strings.SplitSeq(stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := fields[1]
		info := strings.Join(fields[2:], " ")
		switch fields[0] {
		case "pass":
			findings = append(findings, ProbeFinding{Name: name, Pass: true, Info: info})
		case "FAIL":
			findings = append(findings, ProbeFinding{Name: name, Pass: false, Info: info})
		default:
			return nil, fail("SANDBOX_PROBE", "probe printed "+line, "fix the executor output")
		}
	}
	if len(findings) == 0 {
		return nil, fail("SANDBOX_PROBE", "the probe printed no checks", "fix the executor output")
	}
	return findings, nil
}

// EgressProbe fails when any denied target dials successfully.
func EgressProbe(dial func(network, addr string) error, targets []string) *Failure {
	for _, target := range targets {
		if derr := dial("tcp", target); derr == nil {
			rerr := fail("EGRESS_PROBE", target+" is reachable", "isolate the egress network")
			rerr.Exit = ExitConfigUnavailable
			return rerr
		}
	}
	return nil
}

// IsolationTargets lists the addresses a contained job must not reach.
func IsolationTargets(serverHost string) []string {
	return []string{serverHost, "169.254.169.254:80"}
}

// FullProbe runs the host checks plus the container sandbox checks.
func FullProbe(cfg Config, env ProbeEnv) (ProbeReport, *Failure) {
	report, rerr := RunProbe(cfg, env)
	if rerr != nil {
		return report, rerr
	}
	c := &probeCtx{cfg: cfg, env: env, podman: report.Podman, report: report}
	if rerr := probeP10Pull(c); rerr != nil {
		report = c.report
		return report, rerr
	}
	if _, rerr := SandboxProbe(cfg, env, c.podman); rerr != nil {
		report = c.report
		report.Checks = append(report.Checks, ProbeCheck{Name: "P11", Code: "E_" + rerr.Code, Msg: rerr.Msg, Exit: rerr.Exit})
		report.Failures = append(report.Failures, ProbeCheck{Name: "P11", Code: "E_" + rerr.Code, Msg: rerr.Msg, Exit: rerr.Exit})
		return report, rerr
	}
	report.Checks = append(report.Checks, ProbeCheck{Name: "P11"})
	report.Egress = cfg.Egress
	if rerr := probeP12(c); rerr != nil {
		report = c.report
		return report, rerr
	}
	report = c.report
	return report, nil
}

// probeP12 passes unconfigured egress and checks pasta availability otherwise.
func probeP12(c *probeCtx) *Failure {
	if !c.cfg.Egress {
		c.pass("P12")
		return nil
	}
	if _, _, err := c.env.Run(c.podman, "run", "--rm", "--network=pasta", c.cfg.ProbeImage, "true"); err != nil {
		return c.fail("P12", "EGRESS_PROBE", "pasta egress is not available", ExitConfigUnavailable)
	}
	c.pass("P12")
	return nil
}
