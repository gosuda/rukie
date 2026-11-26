package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	jsonv2 "encoding/json/v2"
)

func gitCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_NOSYSTEM=1", "HOME="+dir,
	)
	return cmd
}

func execLookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func TestSandboxP01PodmanResolvable(t *testing.T) {
	env := passingEnv()
	cfg := DefaultConfig(t.TempDir())
	if _, rerr := RunProbe(cfg, env); rerr != nil {
		t.Fatalf("resolvable podman fails P01: %v", rerr)
	}
	env.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if _, rerr := RunProbe(cfg, env); rerr == nil || rerr.Code != "PODMAN_MISSING" || rerr.Exit != 78 {
		t.Fatalf("missing podman = %v, want E_PODMAN_MISSING/78", rerr)
	}
}

func versionEnv(version string) ProbeEnv {
	env := passingEnv()
	env.Run = func(name string, args ...string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")
		if name == "systemctl" {
			return []byte("ActiveState=active\nControlGroup=/user.slice/user-1000.slice/user@1000.service\nDelegate=yes\n"), nil, nil
		}
		switch {
		case strings.Contains(joined, "version --format json"):
			return []byte(version), nil, nil
		case strings.Contains(joined, "info --format json"):
			return []byte(`{"host":{"cgroupManager":"systemd","security":{"rootless":true}}}`), nil, nil
		case strings.Contains(joined, "image exists"):
			return []byte(""), nil, nil
		case name == "git":
			return []byte("git version 2.31.0"), nil, nil
		}
		return []byte(""), nil, nil
	}
	return env
}

func TestSandboxP02VersionFloor(t *testing.T) {
	for _, row := range []struct {
		version string
		pass    bool
	}{
		{`{"Client":{"Version":"6.1.3"}}`, false},
		{`{"Client":{"Version":"6.1.4"}}`, true},
		{`{"Client":{"Version":"6.1.4+build1"}}`, true},
		{`{"Client":{"Version":"5.0.0-dev"}}`, false},
		{`{"Client":{"Version":"8.0.1"}}`, true},
	} {
		env := versionEnv(row.version)
		cfg := DefaultConfig(t.TempDir())
		_, rerr := RunProbe(cfg, env)
		if (rerr == nil) != row.pass {
			t.Errorf("version %s pass = %v, want %v (err %v)", row.version, rerr == nil, row.pass, rerr)
		}
	}
}

func TestSandboxP03RootlessOnly(t *testing.T) {
	env := passingEnv()
	env.UID = func() int { return 0 }
	cfg := DefaultConfig(t.TempDir())
	if _, rerr := RunProbe(cfg, env); rerr == nil || rerr.Code != "PODMAN_ROOTFUL" {
		t.Fatalf("uid 0 = %v, want E_PODMAN_ROOTFUL", rerr)
	}
}

func TestSandboxP04CgroupV2(t *testing.T) {
	env := passingEnv()
	env.ReadFile = func(string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	cfg := DefaultConfig(t.TempDir())
	if _, rerr := RunProbe(cfg, env); rerr == nil || rerr.Code != "CGROUP_V1" {
		t.Fatalf("no cgroupfs = %v, want E_CGROUP_V1", rerr)
	}
}

func TestSandboxP05ControllersDelegated(t *testing.T) {
	for _, tc := range []struct {
		name, manager, state, controllers string
		unreadable                        bool
		want                              bool
	}{
		{name: "delegated user manager", manager: "systemd", state: "ActiveState=active\nDelegate=yes\nControlGroup=/user.slice/user-1000.slice/user@1000.service\n", controllers: "cpu memory pids", want: true},
		{name: "missing pids", manager: "systemd", state: "ActiveState=active\nDelegate=yes\nControlGroup=/user.slice/user-1000.slice/user@1000.service\n", controllers: "cpu memory"},
		{name: "cpuset does not supply cpu", manager: "systemd", state: "ActiveState=active\nDelegate=yes\nControlGroup=/user.slice/user-1000.slice/user@1000.service\n", controllers: "cpuset memory pids"},
		{name: "unreadable controllers", manager: "systemd", state: "ActiveState=active\nDelegate=yes\nControlGroup=/user.slice/user-1000.slice/user@1000.service\n", unreadable: true},
		{name: "delegation disabled", manager: "systemd", state: "ActiveState=active\nDelegate=no\nControlGroup=/user.slice/user-1000.slice/user@1000.service\n", controllers: "cpu memory pids"},
		{name: "inactive user manager", manager: "systemd", state: "ActiveState=inactive\nDelegate=yes\nControlGroup=/user.slice/user-1000.slice/user@1000.service\n", controllers: "cpu memory pids"},
		{name: "missing path", manager: "systemd", state: "ActiveState=active\nDelegate=yes\n", controllers: "cpu memory pids"},
		{name: "root path", manager: "systemd", state: "ActiveState=active\nDelegate=yes\nControlGroup=/\n", controllers: "cpu memory pids"},
		{name: "relative path", manager: "systemd", state: "ActiveState=active\nDelegate=yes\nControlGroup=user.slice\n", controllers: "cpu memory pids"},
		{name: "parent traversal", manager: "systemd", state: "ActiveState=active\nDelegate=yes\nControlGroup=/user.slice/../../tmp\n", controllers: "cpu memory pids"},
		{name: "rootless cgroupfs", manager: "cgroupfs", controllers: "cpu memory pids"},
		{name: "missing manager", controllers: "cpu memory pids"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := passingEnv()
			env.Run = func(name string, args ...string) ([]byte, []byte, error) {
				if name != "systemctl" || strings.Join(args, " ") != "show user@1000.service --property=ControlGroup --property=Delegate --property=ActiveState --no-pager" {
					t.Fatalf("unexpected P05 command: %s %v", name, args)
				}
				return []byte(tc.state), nil, nil
			}
			env.ReadFile = func(path string) ([]byte, error) {
				switch path {
				case "/proc/self/cgroup":
					return []byte("0::/system.slice/actions-runner.service\n"), nil
				case "/sys/fs/cgroup/system.slice/actions-runner.service/cgroup.subtree_control":
					return []byte{}, nil
				case "/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/cgroup.controllers":
					if tc.unreadable {
						return nil, os.ErrPermission
					}
					return []byte(tc.controllers), nil
				default:
					return nil, os.ErrNotExist
				}
			}
			c := &probeCtx{env: env, cgroupManager: tc.manager}
			err := probeP05(c)
			if (err == nil) != tc.want {
				t.Fatalf("P05 = %v, want pass %t", err, tc.want)
			}
			if err != nil && err.Code != "CGROUP_NOT_DELEGATED" {
				t.Fatalf("P05 = %v, want E_CGROUP_NOT_DELEGATED", err)
			}
		})
	}
}

func TestSandboxP06RuntimeDir(t *testing.T) {
	env := passingEnv()
	env.Getenv = func(string) string { return "" }
	cfg := DefaultConfig(t.TempDir())
	if _, rerr := RunProbe(cfg, env); rerr == nil || rerr.Code != "XDG_RUNTIME" {
		t.Fatalf("unset runtime dir = %v, want E_XDG_RUNTIME", rerr)
	}
}

func TestSandboxP07SubuidRanges(t *testing.T) {
	env := passingEnv()
	env.ReadFile = func(path string) ([]byte, error) {
		if path == "/etc/subuid" || path == "/etc/subgid" {
			return []byte(""), nil
		}
		if strings.HasSuffix(path, "cgroup.controllers") {
			return []byte("cpu memory pids"), nil
		}
		if path == "/proc/self/cgroup" {
			return []byte("0::/user.slice/user-1000.slice/session-9.scope\n"), nil
		}
		if strings.HasSuffix(path, "cgroup.subtree_control") {
			return []byte("cpu memory pids"), nil
		}
		return nil, os.ErrNotExist
	}
	cfg := DefaultConfig(t.TempDir())
	if _, rerr := RunProbe(cfg, env); rerr == nil || rerr.Code != "SUBUID_MISSING" {
		t.Fatalf("empty subuid = %v, want E_SUBUID_MISSING", rerr)
	}
}

func TestSandboxP08UsernsDenied(t *testing.T) {
	if !ClassifyUsernsError("Error: creating container: unprivileged user namespaces are denied by AppArmor") {
		t.Fatal("apparmor denial is not classified as userns-denied")
	}
	if ClassifyUsernsError("Error: pull access denied") {
		t.Fatal("pull failure is misclassified as userns-denied")
	}
}

func TestSandboxP09GitPresent(t *testing.T) {
	env := passingEnv()
	env.Run = func(name string, args ...string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")
		if name == "systemctl" {
			return []byte("ActiveState=active\nControlGroup=/user.slice/user-1000.slice/user@1000.service\nDelegate=yes\n"), nil, nil
		}
		switch {
		case name == "git":
			return []byte("git version 2.31.0"), nil, nil
		case strings.Contains(joined, "version"):
			return []byte(`{"Client":{"Version":"6.1.4"}}`), nil, nil
		case strings.Contains(joined, "info"):
			return []byte(`{"host":{"cgroupManager":"systemd","security":{"rootless":"true"}}}`), nil, nil
		}
		return []byte(""), nil, nil
	}
	cfg := DefaultConfig(t.TempDir())
	if _, rerr := RunProbe(cfg, env); rerr != nil {
		t.Fatalf("git 2.31 fails P09: %v", rerr)
	}
}

func TestSandboxP10DefaultImageDigest(t *testing.T) {
	image := DefaultConfig(t.TempDir()).ProbeImage
	name, digest, ok := strings.Cut(image, "@sha256:")
	if !ok || name != "docker.io/library/alpine" {
		t.Fatalf("probe image must pin the official Alpine digest: %q", image)
	}
	raw, err := hex.DecodeString(digest)
	if err != nil || len(raw) != sha256.Size {
		t.Fatalf("probe image has an invalid SHA-256 digest: %q: %v", image, err)
	}
}

func TestSandboxP10ProbeImage(t *testing.T) {
	for _, tc := range []struct {
		name, stderr, want string
	}{
		{name: "text", stderr: "manifest unknown\n", want: "manifest unknown"},
		{name: "invalid UTF-8", stderr: "manifest \xff unknown\n", want: "manifest \uFFFD unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := passingEnv()
			env.Run = func(name string, args ...string) ([]byte, []byte, error) {
				joined := strings.Join(args, " ")
				if name == "systemctl" {
					return []byte("ActiveState=active\nControlGroup=/user.slice/user-1000.slice/user@1000.service\nDelegate=yes\n"), nil, nil
				}
				if strings.Contains(joined, "image exists") {
					return nil, nil, os.ErrNotExist
				}
				if strings.Contains(joined, "pull") {
					return nil, []byte(tc.stderr), os.ErrPermission
				}
				return []byte(""), nil, nil
			}
			c := &probeCtx{cfg: DefaultConfig(t.TempDir()), env: env, podman: "/usr/bin/podman"}
			rerr := probeP10Pull(c)
			if rerr == nil || rerr.Code != "PROBE_IMAGE" || rerr.Exit != 75 || !strings.Contains(rerr.Msg, tc.want) {
				t.Fatalf("unpullable image = %v, want E_PROBE_IMAGE/75", rerr)
			}
			var line map[string]string
			if err := jsonv2.Unmarshal([]byte(rerr.LogLine()), &line); err != nil || line["code"] != "E_PROBE_IMAGE" || !strings.Contains(line["msg"], tc.want) {
				t.Fatalf("probe diagnostic = %q, error = %v", rerr.LogLine(), err)
			}
			if err := WriteProbeReport(c.cfg.StateDir, c.report); err != nil {
				t.Fatalf("write probe report: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(c.cfg.StateDir, "probe.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report ProbeReport
			if err := jsonv2.Unmarshal(raw, &report); err != nil || len(report.Failures) != 1 || report.Failures[0].Code != "E_PROBE_IMAGE" || !strings.Contains(report.Failures[0].Msg, tc.want) {
				t.Fatalf("probe report = %s, error = %v", raw, err)
			}
		})
	}
}

var belowFloorPattern = regexp.MustCompile(`podman \d+\.\d+\.\d+\S* is below `)

func TestSandboxP11LiveProbe(t *testing.T) {
	if _, err := os.Stat("/usr/bin/podman"); err != nil {
		t.Skip("podman unavailable on this host: the probe exits 78 E_PODMAN_MISSING")
	}
	cfg := DefaultConfig(t.TempDir())
	if err := os.Chmod(cfg.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	_, rerr := RunProbe(cfg, LiveProbeEnv())
	if rerr != nil && rerr.Code == "PODMAN_VERSION" && belowFloorPattern.MatchString(rerr.Msg) {
		t.Skipf("host podman is below the supported floor: %s", rerr.Msg)
	}
	if rerr != nil {
		t.Fatalf("live sandbox probe: %v", rerr)
	}
	binary := filepath.Join(t.TempDir(), "rukie")
	build := exec.CommandContext(t.Context(), "go", "build", "-trimpath", "-o", binary, "../../cmd/rukie")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static Rukie: %v\n%s", err, output)
	}
	probe := exec.CommandContext(t.Context(), binary, "runner", "probe", "--state-dir", cfg.StateDir)
	if output, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("live sandbox probe: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.StateDir, "probe.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report ProbeReport
	if err := jsonv2.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, check := range report.Checks {
		if check.Name == "P11" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("live probe skipped P11")
	}
}

func TestSandboxFullProbeRetainsSuccessfulChecks(t *testing.T) {
	for _, egress := range []bool{false, true} {
		t.Run(strconv.FormatBool(egress), func(t *testing.T) {
			env := passingEnv()
			run := env.Run
			env.Run = func(name string, args ...string) ([]byte, []byte, error) {
				if name == "/usr/bin/podman" && len(args) > 0 && args[0] == "run" {
					return []byte("pass uid uid=1000\n"), nil, nil
				}
				return run(name, args...)
			}
			cfg := DefaultConfig(t.TempDir())
			cfg.Egress = egress
			report, rerr := FullProbe(cfg, env)
			if rerr != nil {
				t.Fatal(rerr)
			}
			var checks []string
			for _, check := range report.Checks {
				if check.Name == "P10" || check.Name == "P11" || check.Name == "P12" {
					checks = append(checks, check.Name)
					if check.Code != "" {
						t.Fatalf("successful check contains a failure: %+v", check)
					}
				}
			}
			if !slices.Equal(checks, []string{"P10", "P11", "P12"}) || report.Egress != egress {
				t.Fatalf("checks=%v egress=%v, want P10/P11/P12 and egress=%v", checks, report.Egress, egress)
			}
		})
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	cmd := gitCmd(dir, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func mustGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := gitCmd(dir, args...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

func mustGitFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestSandboxP11ParsesFindings(t *testing.T) {
	env := passingEnv()
	env.Run = func(string, ...string) ([]byte, []byte, error) {
		return []byte("pass uid uid=1000\nFAIL no_caps CapEff is nonzero\n"), nil, nil
	}
	cfg := DefaultConfig(t.TempDir())
	_, rerr := SandboxProbe(cfg, env, "/usr/bin/podman")
	if rerr == nil || rerr.Code != "SANDBOX_PROBE:no_caps" || rerr.Exit != 78 {
		t.Fatalf("failing check = %v, want E_SANDBOX_PROBE:no_caps/78", rerr)
	}
}

func TestSandboxP11RejectsEmptyOutput(t *testing.T) {
	env := passingEnv()
	env.Run = func(string, ...string) ([]byte, []byte, error) {
		return []byte("podman ready\n"), nil, nil
	}
	cfg := DefaultConfig(t.TempDir())
	if _, rerr := SandboxProbe(cfg, env, "/usr/bin/podman"); rerr == nil || rerr.Code != "SANDBOX_PROBE" {
		t.Fatalf("garbage probe output = %v, want E_SANDBOX_PROBE", rerr)
	}
}

func TestSandboxP11ClassifiesUserns(t *testing.T) {
	for _, row := range []struct {
		stderr string
		code   string
	}{
		{"Error: userns denied", "USERNS_DENIED"},
		{"Error: creating container: apparmor: unprivileged user namespaces are denied", "USERNS_DENIED"},
		{"Error: newuidmap: subuid range not found", "USERNS_DENIED"},
	} {
		env := passingEnv()
		env.Run = func(string, ...string) ([]byte, []byte, error) {
			return nil, []byte(row.stderr), os.ErrPermission
		}
		cfg := DefaultConfig(t.TempDir())
		if _, rerr := SandboxProbe(cfg, env, "/usr/bin/podman"); rerr == nil || rerr.Code != row.code {
			t.Fatalf("userns denial %q = %v, want E_%s", row.stderr, rerr, row.code)
		}
	}
	env := passingEnv()
	env.Run = func(string, ...string) ([]byte, []byte, error) {
		return nil, []byte("Error: pull access denied"), os.ErrNotExist
	}
	cfg := DefaultConfig(t.TempDir())
	if _, rerr := SandboxProbe(cfg, env, "/usr/bin/podman"); rerr == nil || rerr.Code != "SANDBOX_PROBE" {
		t.Fatalf("pull failure = %v, want E_SANDBOX_PROBE", rerr)
	}
}

func TestSandboxP12EgressAdvertisedOnlyWhenConfigured(t *testing.T) {
	env := passingEnv()
	cfg := DefaultConfig(t.TempDir())
	cfg.Egress = false
	c := &probeCtx{cfg: cfg, env: env, podman: "/usr/bin/podman"}
	if rerr := probeP12(c); rerr != nil {
		t.Fatalf("disabled egress fails P12: %v", rerr)
	}
	cfg.Egress = true
	env.Run = func(string, ...string) ([]byte, []byte, error) {
		return nil, []byte("pasta missing"), os.ErrNotExist
	}
	c = &probeCtx{cfg: cfg, env: env, podman: "/usr/bin/podman"}
	if rerr := probeP12(c); rerr == nil || rerr.Code != "EGRESS_PROBE" || rerr.Exit != 78 {
		t.Fatalf("missing pasta = %v, want E_EGRESS_PROBE/78", rerr)
	}
	if rerr := EgressProbe(func(string, string) error { return nil }, IsolationTargets("forge:1")); rerr == nil || rerr.Code != "EGRESS_PROBE" {
		t.Fatalf("reachable target = %v, want E_EGRESS_PROBE", rerr)
	}
	if rerr := EgressProbe(func(string, string) error { return os.ErrClosed }, IsolationTargets("forge:1")); rerr != nil {
		t.Fatalf("isolated targets fail: %v", rerr)
	}
}

func TestSandboxP13StateDirLocked(t *testing.T) {
	env := passingEnv()
	cfg := DefaultConfig(t.TempDir())
	c := &probeCtx{cfg: cfg, env: env}
	if rerr := probeP13(c); rerr != nil {
		t.Fatalf("good state dir fails P13: %v", rerr)
	}
	env.Stat = func(string) (os.FileInfo, error) { return fakeFile{perm: 0755, dir: true}, nil }
	c = &probeCtx{cfg: cfg, env: env}
	if rerr := probeP13(c); rerr == nil || rerr.Code != "STATE_DIR" {
		t.Fatalf("loose state dir = %v, want E_STATE_DIR", rerr)
	}
}

func TestSandboxBuilderFlags(t *testing.T) {
	args := BuildPodmanArgs("/usr/bin/podman", SandboxRun{
		Name: "rukie-att-1", Image: "img@sha256:abc", UID: 1000, GID: 1000,
		CPUs: 2, MemoryMB: 4096, PidsLimit: 512, TmpMB: 512,
		WorkHost: "/s/work", LogHost: "/s/log", EnvFile: "/s/attempt.env",
		RukieDir: "/s/rukie",
	})
	joined := strings.Join(args, " ")
	for _, want := range []string{"--pull=never", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--log-driver=none", "--stop-signal=SIGTERM", "--userns=keep-id:uid=1000,gid=1000", "--entrypoint=/.rukie/rukie", "dst=/.rukie", "job-exec", "/.rukie/steps.json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("builder misses %s in %v", want, args)
		}
	}
	if err := AssertBuilderSafe(args); err != nil {
		t.Fatalf("production builder is unsafe: %v", err)
	}
	if err := AssertBuilderSafe(append(args, "--privileged")); err == nil {
		t.Fatal("privileged flag passes the safety gate")
	}
}

func TestSandboxBuilderInheritsNetwork(t *testing.T) {
	args := BuildPodmanArgs("/usr/bin/podman", SandboxRun{Image: "i", Network: "pasta", RukieDir: "/s/rukie"})
	if !strings.Contains(strings.Join(args, " "), "--network=pasta") {
		t.Fatalf("egress network is not inherited: %v", args)
	}
}

func TestSandboxBuilderProbeCommand(t *testing.T) {
	args := BuildPodmanArgs("/usr/bin/podman", SandboxRun{Image: "i", RukieDir: "/s/rukie", Probe: true})
	if !slices.Equal(args[len(args)-4:], []string{"--", "i", "job-exec", "--probe"}) {
		t.Fatalf("probe command is wrong: %v", args)
	}
}

func TestSandboxMaterializeExactTree(t *testing.T) {
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		if _, err := execLookPath("git"); err != nil {
			t.Skip("git is not installed")
		}
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	mustGit(t, src, "init")
	mustGitFile(t, src, "main.go", "package main\n")
	mustGitFile(t, src, "sub/in.txt", "data\n")
	mustGit(t, src, "add", "-A")
	mustGit(t, src, "commit", "-m", "one")
	tree := mustGitOut(t, src, "rev-parse", "HEAD^{tree}")
	mirror := filepath.Join(dir, "mirror.git")
	want := mustGitOut(t, src, "rev-parse", "HEAD")
	if err := UpdateMirror(mirror, src, "HEAD", os.Environ()); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if err := VerifyTree(mirror, want, tree); err != nil {
		t.Fatalf("tree: %v", err)
	}
	if err := VerifyTree(mirror, want, strings.Repeat("0", 40)); err == nil {
		t.Fatal("wrong tree verifies")
	}
	if err := VerifyTree(mirror, strings.Repeat("1", 40), tree); err == nil {
		t.Fatal("wrong commit verifies")
	}
	out := filepath.Join(dir, "work")
	if err := Materialize(mirror, tree, out); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, ".git")); !os.IsNotExist(err) {
		t.Fatal("workspace contains .git")
	}
	raw, err := os.ReadFile(filepath.Join(out, "sub", "in.txt"))
	if err != nil || string(raw) != "data\n" {
		t.Fatalf("workspace content = %q, %v", raw, err)
	}
}

func TestSandboxMaterializeRejectsEscape(t *testing.T) {
	if err := Materialize(t.TempDir(), "nope", filepath.Join(t.TempDir(), "w")); err == nil {
		t.Fatal("missing mirror materializes")
	}
}

func TestSandboxMaterializeRejectsLinkEscape(t *testing.T) {
	for _, row := range []struct {
		name   string
		target string
		ok     bool
	}{
		{"absolute", "/etc/hostname", false},
		{"parent", "../outside", false},
		{"dotdot", "sub/../../outside", false},
		{"ok", "real.txt", true},
		{"ok-sub", "sub/in.txt", true},
	} {
		dir := t.TempDir()
		src := filepath.Join(dir, "src")
		mustGit(t, src, "init")
		mustGitFile(t, src, "real.txt", "data\n")
		mustGitFile(t, src, "sub/in.txt", "data\n")
		if err := os.Symlink(row.target, filepath.Join(src, "link")); err != nil {
			t.Fatal(err)
		}
		tree := mustGitTree(t, src)
		mirror := filepath.Join(dir, "mirror.git")
		if err := UpdateMirror(mirror, src, "HEAD", os.Environ()); err != nil {
			t.Fatalf("%s: mirror: %v", row.name, err)
		}
		out := filepath.Join(dir, "work")
		err := Materialize(mirror, tree, out)
		if row.ok && err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		if !row.ok && err == nil {
			t.Fatalf("%s materialized", row.name)
		}
		if row.ok {
			raw, err := os.ReadFile(filepath.Join(out, "link"))
			if err != nil || string(raw) != "data\n" {
				t.Fatalf("%s content = %q, %v", row.name, raw, err)
			}
		}
	}
}

func mustGitTree(t *testing.T, dir string) string {
	t.Helper()
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-m", "one")
	return mustGitOut(t, dir, "rev-parse", "HEAD^{tree}")
}

func TestSandboxLinkTraversalRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real")
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLinkTarget(dir, "link/evil", "x"); err == nil {
		t.Fatal("link traversal accepted")
	}
	if _, err := resolveLinkTarget(dir, "a", "../b"); err == nil {
		t.Fatal("parent escape accepted")
	}
	if _, err := resolveLinkTarget(dir, "a", "/abs"); err == nil {
		t.Fatal("absolute target accepted")
	}
	got, err := resolveLinkTarget(dir, "a", "sub/f.txt")
	if err != nil || got != "sub/f.txt" {
		t.Fatalf("relative target = %q, %v", got, err)
	}
}

func TestSandboxCollectRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "real.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(work, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CollectArtifact(work, "link.txt"); err == nil {
		t.Fatal("symlinked artifact collected")
	} else if !strings.Contains(err.Error(), "HOST_PATH_VIOLATION") {
		t.Fatalf("link artifact = %v, want E_HOST_PATH_VIOLATION", err)
	}
	if digest, raw, err := CollectArtifact(work, "real.txt"); err != nil || string(raw) != "data" || digest == "" {
		t.Fatalf("real artifact = %q %v", raw, err)
	}
}

func TestSandboxCacheScopeValidation(t *testing.T) {
	for _, row := range []struct {
		trust, repo, name string
		ok                bool
	}{
		{"trusted", "o-r", "go", true},
		{"untrusted", "o-r", "go", true},
		{"../x", "o-r", "go", false},
		{"trusted", "..", "go", false},
		{"trusted", "o/r", "go", false},
		{"trusted", "o-r", "../go", false},
		{"", "o-r", "go", false},
	} {
		_, err := CacheDir(t.TempDir(), row.trust, row.repo, row.name)
		if (err == nil) != row.ok {
			t.Errorf("cache %v = %v, want ok=%v", row, err, row.ok)
		}
	}
}

func TestSandboxRepoKeyCollisionFree(t *testing.T) {
	grant := func(url string) *Grant {
		return &Grant{Fetch: FetchPayload{URL: url}}
	}
	seen := map[string]string{}
	for _, url := range []string{"/o/a/b.git", "/o/a-b.git", "/o/a--b.git", "/o/a/b/c.git"} {
		key := repoKeyOf(grant(url))
		if prior, exists := seen[key]; exists {
			t.Fatalf("key %q collides for %s and %s", key, prior, url)
		}
		seen[key] = url
	}
}

func TestSandboxLogChunkRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "secret")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "log", "job.stdout")
	if err := os.MkdirAll(filepath.Join(dir, "log"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenLogChunk(link, 0); err == nil {
		t.Fatal("symlinked log opens")
	} else if !strings.Contains(err.Error(), "HOST_PATH_VIOLATION") {
		t.Fatalf("symlink error = %v, want E_HOST_PATH_VIOLATION", err)
	}
}

func TestSandboxCollectRefusesIntermediateSymlink(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(filepath.Join(work, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "attempt.token"), []byte("sekrit"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "sub", "ok.txt"), []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(work, "out")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CollectArtifact(work, "out/attempt.token"); err == nil {
		t.Fatal("intermediate symlink escape collected")
	} else if !strings.Contains(err.Error(), "HOST_PATH_VIOLATION") {
		t.Fatalf("escape error = %v, want E_HOST_PATH_VIOLATION", err)
	}
	if err := os.Symlink("sub", filepath.Join(work, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CollectArtifact(work, "alias/ok.txt"); err == nil {
		t.Fatal("aliased directory traversal collected")
	} else if !strings.Contains(err.Error(), "HOST_PATH_VIOLATION") {
		t.Fatalf("alias error = %v, want E_HOST_PATH_VIOLATION", err)
	}
	if digest, raw, err := CollectArtifact(work, "sub/ok.txt"); err != nil || string(raw) != "data" || digest == "" {
		t.Fatalf("nested artifact = %q %v", raw, err)
	}
}

func TestSandboxCollectFailsOverLimit(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(work, 0755); err != nil {
		t.Fatal(err)
	}
	big, err := os.Create(filepath.Join(work, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if err := big.Truncate(ArtifactMaxBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := big.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CollectArtifact(work, "big.bin"); err == nil {
		t.Fatal("oversized artifact collected")
	} else if !strings.Contains(err.Error(), "ARTIFACT_LIMIT") {
		t.Fatalf("over-limit error = %v, want E_ARTIFACT_LIMIT", err)
	}
}

func TestSandboxCacheTrustSeparation(t *testing.T) {
	trusted, err := CacheDir(t.TempDir(), "trusted", "o-r", "go")
	if err != nil {
		t.Fatal(err)
	}
	untrusted, err := CacheDir(t.TempDir(), "untrusted", "o-r", "go")
	if err != nil {
		t.Fatal(err)
	}
	if trusted == untrusted || !strings.Contains(trusted, "trusted") {
		t.Fatalf("cache volumes share a path: %q %q", trusted, untrusted)
	}
	if _, err := CacheDir(t.TempDir(), "trusted", "o-r", "../escape"); err == nil {
		t.Fatal("cache name escapes")
	}
}

func TestSandboxDigestGolden(t *testing.T) {
	if got := Digest([]byte("abc")); got != "sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("digest = %s", got)
	}
}
