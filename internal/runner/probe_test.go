package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureEnv() ProbeEnv {
	return ProbeEnv{
		LookPath: func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Run: func(string, ...string) ([]byte, []byte, error) {
			return nil, nil, errors.New("unexpected run")
		},
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		Stat:     func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
		Getenv:   func(string) string { return "" },
		UID:      func() int { return 1000 },
		Username: func() string { return "runner" },
		Lock:     func(string) (func(), error) { return func() {}, nil },
	}
}

func passingEnv() ProbeEnv {
	env := fixtureEnv()
	env.Run = func(name string, args ...string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "version --format json"):
			return []byte(`{"Client":{"Version":"6.1.4"}}`), nil, nil
		case strings.Contains(joined, "info --format json"):
			return []byte(`{"host":{"security":{"rootless":true}}}`), nil, nil
		case strings.Contains(joined, "image exists"):
			return []byte(""), nil, nil
		case name == "git":
			return []byte("git version 2.31.0"), nil, nil
		}
		return nil, nil, errors.New("unexpected run " + name + " " + joined)
	}
	env.ReadFile = func(path string) ([]byte, error) {
		switch path {
		case "/sys/fs/cgroup/cgroup.controllers":
			return []byte("cpuset cpu io memory hugetlb pids"), nil
		case "/proc/self/cgroup":
			return []byte("0::/user.slice/user-1000.slice/session-9.scope\n"), nil
		case "/sys/fs/cgroup/user.slice/user-1000.slice/session-9.scope/cgroup.subtree_control":
			return []byte("cpu memory pids"), nil
		case "/etc/subuid", "/etc/subgid":
			return []byte("runner:100000:65536\n"), nil
		}
		return nil, os.ErrNotExist
	}
	env.Stat = func(path string) (os.FileInfo, error) {
		if strings.HasSuffix(path, "runtime") {
			return fakeRuntimeDir(), nil
		}
		return fakeFile{perm: 0700, dir: true}, nil
	}
	env.Getenv = func(key string) string {
		if key == "XDG_RUNTIME_DIR" {
			return "/run/user/1000"
		}
		if key == "PATH" {
			return "/usr/bin"
		}
		return ""
	}
	return env
}

type fakeFile struct {
	perm uint32
	dir  bool
}

func (f fakeFile) Name() string       { return "fake" }
func (f fakeFile) Size() int64        { return 0 }
func (f fakeFile) Mode() os.FileMode  { return os.FileMode(f.perm) }
func (f fakeFile) ModTime() time.Time { return time.Time{} }
func (f fakeFile) IsDir() bool        { return f.dir }
func (f fakeFile) Sys() any           { return nil }

type fakeDir struct{ fakeFile }

func fakeRuntimeDir() os.FileInfo { return fakeDir{fakeFile{dir: true}} }

func TestProbeTripleBoundaries(t *testing.T) {
	for _, row := range []struct {
		text string
		want bool
	}{
		{"6.1.3", false},
		{"6.1.4", true},
		{"6.1.5", true},
		{"6.2.0", true},
		{"7.0.0", true},
		{"5.9.9", false},
		{"bogus", false},
		{"6.1", false},
	} {
		major, minor, patch, ok := parseTriple("podman version " + row.text)
		got := ok && atLeast(major, minor, patch, minPodmanMajor, minPodmanMinor, minPodmanPatch)
		if got != row.want {
			t.Errorf("version %q accepted = %v, want %v", row.text, got, row.want)
		}
	}
}

func TestProbeRejectsPodmanBelowFloor(t *testing.T) {
	env := passingEnv()
	env.Run = func(string, ...string) ([]byte, []byte, error) {
		return []byte(`{"Client":{"Version":"5.4.0"}}`), nil, nil
	}
	cfg := DefaultConfig(t.TempDir())
	_, rerr := RunProbe(cfg, env)
	if rerr == nil || rerr.Code != "PODMAN_VERSION" || rerr.Exit != ExitConfigUnavailable {
		t.Fatalf("below-floor podman = %v, want E_PODMAN_VERSION/78", rerr)
	}
}

func TestProbeRejectsRootfulRuntime(t *testing.T) {
	env := passingEnv()
	env.Run = func(_ string, args ...string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "version") {
			return []byte(`{"Client":{"Version":"6.1.4"}}`), nil, nil
		}
		return []byte(`{"host":{"security":{"rootless":"false"}}}`), nil, nil
	}
	cfg := DefaultConfig(t.TempDir())
	_, rerr := RunProbe(cfg, env)
	if rerr == nil || rerr.Code != "PODMAN_ROOTFUL" {
		t.Fatalf("rootful podman = %v, want E_PODMAN_ROOTFUL", rerr)
	}
}

func TestProbeRejectsVersionGarbage(t *testing.T) {
	env := passingEnv()
	env.Run = func(string, ...string) ([]byte, []byte, error) {
		return []byte("not json"), nil, nil
	}
	cfg := DefaultConfig(t.TempDir())
	_, rerr := RunProbe(cfg, env)
	if rerr == nil || rerr.Code != "PODMAN_VERSION" {
		t.Fatalf("garbage version = %v, want E_PODMAN_VERSION", rerr)
	}
}

func TestProbeRejectsMissingControllers(t *testing.T) {
	env := passingEnv()
	env.ReadFile = func(string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	cfg := DefaultConfig(t.TempDir())
	_, rerr := RunProbe(cfg, env)
	if rerr == nil || rerr.Code != "CGROUP_V1" {
		t.Fatalf("v1 cgroups = %v, want E_CGROUP_V1", rerr)
	}
}

func TestProbeRejectsMissingRuntimeDir(t *testing.T) {
	env := passingEnv()
	env.Getenv = func(string) string { return "" }
	cfg := DefaultConfig(t.TempDir())
	_, rerr := RunProbe(cfg, env)
	if rerr == nil || rerr.Code != "XDG_RUNTIME" {
		t.Fatalf("missing runtime dir = %v, want E_XDG_RUNTIME", rerr)
	}
}

func TestProbeRejectsMissingSubuid(t *testing.T) {
	env := passingEnv()
	env.ReadFile = func(path string) ([]byte, error) {
		if path == "/etc/subuid" {
			return []byte("other:100000:65536\n"), nil
		}
		if path == "/etc/subgid" {
			return []byte("runner:100000:65536\n"), nil
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
	_, rerr := RunProbe(cfg, env)
	if rerr == nil || rerr.Code != "SUBUID_MISSING" {
		t.Fatalf("missing subuid = %v, want E_SUBUID_MISSING", rerr)
	}
}

func TestProbeRejectsOldGit(t *testing.T) {
	env := passingEnv()
	env.Run = func(name string, args ...string) ([]byte, []byte, error) {
		joined := strings.Join(args, " ")
		if strings.HasPrefix(name, "git") {
			return []byte("git version 2.30.0"), nil, nil
		}
		if strings.Contains(joined, "version") {
			return []byte(`{"Client":{"Version":"6.1.4"}}`), nil, nil
		}
		if strings.Contains(joined, "info") {
			return []byte(`{"host":{"security":{"rootless":"true"}}}`), nil, nil
		}
		return []byte(""), nil, nil
	}
	cfg := DefaultConfig(t.TempDir())
	_, rerr := RunProbe(cfg, env)
	if rerr == nil || rerr.Code != "GIT_VERSION" {
		t.Fatalf("old git = %v, want E_GIT_VERSION", rerr)
	}
}

func TestProbeRejectsLockedState(t *testing.T) {
	env := passingEnv()
	env.Lock = func(string) (func(), error) { return nil, errors.New("held") }
	cfg := DefaultConfig(t.TempDir())
	_, rerr := RunProbe(cfg, env)
	if rerr == nil || rerr.Code != "RUNNER_ALREADY_RUNNING" || rerr.Exit != ExitConfigUnavailable {
		t.Fatalf("locked state = %v, want E_RUNNER_ALREADY_RUNNING/78", rerr)
	}
}

func TestRunnerProbeExit78(t *testing.T) {
	empty := t.TempDir()
	t.Setenv("PATH", empty)
	state := t.TempDir()
	out := &strings.Builder{}
	code := Main(IO{Stdout: out, Stderr: &strings.Builder{}, Getenv: func(key string) string {
		if key == "PATH" {
			return empty
		}
		return os.Getenv(key)
	}}, []string{"probe", "--state-dir", state})
	if code != ExitConfigUnavailable {
		t.Fatalf("probe without podman = %d, want 78", code)
	}
	raw, err := os.ReadFile(filepath.Join(state, "probe.json"))
	if err != nil {
		t.Fatalf("probe.json missing: %v", err)
	}
	if !strings.Contains(string(raw), "PODMAN_MISSING") {
		t.Fatalf("probe.json misses the named error: %s", raw)
	}
}
