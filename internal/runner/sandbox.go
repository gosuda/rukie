package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const gitTimeout = 5 * time.Minute

var errBoundedTimeout = errors.New("bounded command timed out")

func runBounded(name string, env []string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), timeout, errBoundedTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.Output()
	if errors.Is(context.Cause(ctx), errBoundedTimeout) {
		return out, fmt.Errorf("runBounded: %s %w", name, errBoundedTimeout)
	}
	return out, err
}

func backoffWithJitter(base time.Duration, attempt int) time.Duration {
	wait := base << min(attempt, 4)
	jitter, err := rand.Int(rand.Reader, big.NewInt(int64(base)))
	if err != nil {
		return wait
	}
	return wait + time.Duration(jitter.Int64())
}

// SandboxRun describes one container invocation.
type SandboxRun struct {
	ServerHost string
	Name       string
	Image      string
	Network    string
	WorkHost   string
	LogHost    string
	EnvFile    string
	RukieDir   string
	Caches     []CacheMount
	UID        int
	GID        int
	CPUs       float64
	MemoryMB   int64
	PidsLimit  int64
	TmpMB      int64
	TmpExec    bool
	Probe      bool
}

// CacheMount binds one cache volume into the container.
type CacheMount struct {
	Host string
	Name string
}

// ForbiddenFlags never appear in a builder command.
func ForbiddenFlags() []string {
	return []string{"--device", "--privileged", "--volume", "--group-add", "--publish", "--add-host", "--sysctl", "--cgroup-parent"}
}

// ValidateImageRef rejects option-like or blank image references.
func ValidateImageRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return fmt.Errorf("ValidateImageRef: image reference %q is not runnable", ref)
	}
	for _, letter := range ref {
		if letter == ' ' || letter == '\t' || letter == '\n' || letter == '\r' {
			return fmt.Errorf("ValidateImageRef: image reference %q is not runnable", ref)
		}
	}
	return nil
}

// BuildPodmanArgs renders one podman invocation for probe and jobs.
func BuildPodmanArgs(podman string, run SandboxRun) []string {
	memory := run.MemoryMB << 20
	args := []string{
		podman, "run", "--rm", "--name", run.Name,
		"--pull=never", "--init",
		"--entrypoint=/.rukie/rukie",
		"--user=" + strconv.Itoa(run.UID) + ":" + strconv.Itoa(run.GID),
		"--userns=keep-id:uid=" + strconv.Itoa(run.UID) + ",gid=" + strconv.Itoa(run.GID),
		"--workdir=/work", "--hostname=rukie-job",
		"--network=" + cmpOr(run.Network, "none"),
		"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--ipc=private", "--pid=private", "--uts=private", "--cgroupns=private",
		"--cpus=" + strconv.FormatFloat(run.CPUs, 'f', 2, 64),
		"--memory=" + strconv.FormatInt(memory, 10),
		"--memory-swap=" + strconv.FormatInt(memory, 10),
		"--pids-limit=" + strconv.FormatInt(run.PidsLimit, 10),
		"--shm-size=64m",
		"--ulimit=core=0:0",
		"--log-driver=none",
		"--stop-signal=SIGTERM", "--stop-timeout=15",
		"--env-file=" + run.EnvFile,
		"--mount=type=bind,src=" + run.WorkHost + ",dst=/work,nosuid,nodev",
		"--mount=type=bind,src=" + run.LogHost + ",dst=/log,nosuid,nodev",
		"--mount=type=bind,src=" + run.RukieDir + ",dst=/.rukie,ro,nodev",
	}
	tmpOpt := "size=" + strconv.FormatInt(run.TmpMB, 10) + "m,mode=1777"
	if !run.TmpExec {
		tmpOpt += ",noexec"
	}
	args = append(args, "--tmpfs=/tmp:"+tmpOpt, "--tmpfs=/run:rw,size=16m,mode=755")
	for _, cache := range run.Caches {
		args = append(args, "--mount=type=bind,src="+cache.Host+",dst=/cache/"+cache.Name+",nosuid,nodev")
	}
	args = append(args, "--", run.Image)
	if run.Probe {
		args = append(args, "--probe")
		return args
	}
	args = append(args, "job-exec", "--steps", "/.rukie/steps.json", "--log-dir", "/log")
	return args
}

func cmpOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

// linkOrCopyBinary places the runner executable into the container-visible directory.
func linkOrCopyBinary(dir string) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("linkOrCopyBinary: %w", err)
	}
	target := filepath.Join(dir, "rukie")
	_ = os.Remove(target)
	if err := os.Link(self, target); err == nil {
		return nil
	}
	raw, rerr := os.ReadFile(self)
	if rerr != nil {
		return fmt.Errorf("linkOrCopyBinary: %w", rerr)
	}
	if werr := os.WriteFile(target, raw, 0755); werr != nil {
		return fmt.Errorf("linkOrCopyBinary: %w", werr)
	}
	return nil
}

// AssertBuilderSafe rejects forbidden flags in a rendered command.
func AssertBuilderSafe(args []string) error {
	joined := "\x00" + strings.Join(args, "\x00") + "\x00"
	for _, flag := range ForbiddenFlags() {
		if strings.Contains(joined, "\x00"+flag+"\x00") || strings.Contains(joined, "\x00"+flag+"=") {
			return fmt.Errorf("AssertBuilderSafe: forbidden flag %s", flag)
		}
	}
	return nil
}

// RunPodman executes a builder command with a timeout guard.
func RunPodman(args []string, timeout time.Duration) (string, string, error) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), timeout, errBoundedTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(context.Cause(ctx), errBoundedTimeout) {
		return stdout.String(), stderr.String(), fmt.Errorf("RunPodman: container %w", errBoundedTimeout)
	}
	return stdout.String(), stderr.String(), err
}

// MirrorPath returns the per-repo bare mirror path.
func MirrorPath(stateDir, repoKey string) string {
	return filepath.Join(stateDir, "git", repoKey+".git")
}

// fetchedRef pins the fetched commit in the mirror.
const fetchedRef = "refs/rukie/fetched"

// UpdateMirror fetches the mirror up to the wanted commit.
func UpdateMirror(mirror, url, want string, env []string) error {
	if err := os.MkdirAll(filepath.Dir(mirror), 0700); err != nil {
		return fmt.Errorf("UpdateMirror: %w", err)
	}
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err != nil {
		ctx, cancel := context.WithTimeoutCause(context.Background(), gitTimeout, errBoundedTimeout)
		init := exec.CommandContext(ctx, "git", "init", "--bare", mirror)
		init.Env = env
		out, err := init.CombinedOutput()
		cancel()
		if errors.Is(context.Cause(ctx), errBoundedTimeout) {
			return fmt.Errorf("UpdateMirror: init %w", errBoundedTimeout)
		}
		if err != nil {
			return fmt.Errorf("UpdateMirror: init %s: %w", strings.TrimSpace(string(out)), err)
		}
	}
	fetchOut, err := runBounded("git", env, gitTimeout, "--git-dir="+mirror, "fetch", url, want)
	if err != nil {
		return fmt.Errorf("UpdateMirror: fetch %s: %w", strings.TrimSpace(string(fetchOut)), err)
	}
	headOut, err := runBounded("git", env, gitTimeout, "--git-dir="+mirror, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("UpdateMirror: %w", err)
	}
	if _, err := runBounded("git", env, gitTimeout, "--git-dir="+mirror, "update-ref", fetchedRef, strings.TrimSpace(string(headOut))); err != nil {
		return fmt.Errorf("UpdateMirror: %w", err)
	}
	return nil
}

// VerifyTree resolves the fetched commit and tree in the mirror and compares them.
func VerifyTree(mirror, want, expect string) error {
	commitOut, err := runBounded("git", nil, gitTimeout, "--git-dir="+mirror, "rev-parse", fetchedRef)
	if err != nil {
		return fmt.Errorf("VerifyTree: %w", err)
	}
	if got := strings.TrimSpace(string(commitOut)); got != want {
		return fmt.Errorf("VerifyTree: commit %s does not match %s", got, want)
	}
	treeOut, err := runBounded("git", nil, gitTimeout, "--git-dir="+mirror, "rev-parse", fetchedRef+"^{tree}")
	if err != nil {
		return fmt.Errorf("VerifyTree: %w", err)
	}
	if got := strings.TrimSpace(string(treeOut)); got != expect {
		return fmt.Errorf("VerifyTree: tree %s does not match %s", got, expect)
	}
	return nil
}

// Materialize writes the exact tree into dir with no .git directory.
// Symlinks materialize last so no file is ever written through a link.
func Materialize(mirror, tree, dir string) error {
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("Materialize: %w", err)
	}
	entries, err := runBounded("git", nil, gitTimeout, "--git-dir="+mirror, "ls-tree", "-r", "-l", "-z", "--full-tree", tree)
	if err != nil {
		return fmt.Errorf("Materialize: %w", err)
	}
	type link struct {
		name   string
		target string
	}
	links := []link{}
	for entry := range strings.SplitSeq(string(entries), "\x00") {
		if entry == "" {
			continue
		}
		meta, name, _ := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if len(fields) < 4 || name == "" {
			return fmt.Errorf("Materialize: malformed entry %q", entry)
		}
		mode, kind, oid := fields[0], fields[1], fields[2]
		clean := filepath.Clean(name)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
			return fmt.Errorf("Materialize: entry escapes the workspace %q", name)
		}
		if strings.Contains(clean, ".git") {
			return fmt.Errorf("Materialize: entry touches .git %q", name)
		}
		target := filepath.Join(dir, clean)
		if kind == "tree" {
			if err := os.MkdirAll(target, 0750); err != nil {
				return fmt.Errorf("Materialize: %w", err)
			}
			continue
		}
		if kind != "blob" {
			return fmt.Errorf("Materialize: unsupported kind %s", kind)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
			return fmt.Errorf("Materialize: %w", err)
		}
		content, err := runBounded("git", nil, gitTimeout, "--git-dir="+mirror, "cat-file", "blob", oid)
		if err != nil {
			return fmt.Errorf("Materialize: %w", err)
		}
		if mode == "120000" {
			links = append(links, link{name: clean, target: string(content)})
			continue
		}
		perm := fs.FileMode(0644)
		if mode == "100755" {
			perm = 0755
		} else if mode != "100644" {
			return fmt.Errorf("Materialize: unsupported mode %s", mode)
		}
		if err := os.WriteFile(target, content, perm); err != nil {
			return fmt.Errorf("Materialize: %w", err)
		}
	}
	for _, pending := range links {
		resolved, rerr := resolveLinkTarget(dir, pending.name, pending.target)
		if rerr != nil {
			return rerr
		}
		if err := os.Symlink(resolved, filepath.Join(dir, pending.name)); err != nil {
			return fmt.Errorf("Materialize: %w", err)
		}
	}
	return nil
}

func resolveLinkTarget(dir, name, target string) (string, error) {
	if filepath.IsAbs(target) {
		return "", fmt.Errorf("Materialize: link escapes the workspace %q", name)
	}
	resolved := filepath.Join(filepath.Dir(filepath.Join(dir, name)), target)
	rel, err := filepath.Rel(dir, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("Materialize: link escapes the workspace %q", name)
	}
	parent := filepath.Dir(filepath.Join(dir, name))
	for current := parent; current != dir && strings.HasPrefix(current, dir); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return "", fmt.Errorf("Materialize: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("Materialize: link traverses a link %q", name)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("Materialize: link traverses a file %q", name)
		}
	}
	return target, nil
}

// OpenLogChunk reads one bounded log window starting at offset from a
// container-written log file, enforcing the host-path rule. Callers ship
// successive windows until exhaustion; an empty return means no new bytes.
func OpenLogChunk(path string, offset int64) ([]byte, error) {
	if info, serr := os.Lstat(path); serr == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fail("HOST_PATH_VIOLATION", path+" is a symlink", "treat the attempt as compromised and kill the container")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("OpenLogChunk: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("OpenLogChunk: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fail("HOST_PATH_VIOLATION", path+" is not a regular file", "treat the attempt as compromised and kill the container")
	}
	if _, serr := f.Seek(offset, io.SeekStart); serr != nil {
		return nil, fmt.Errorf("OpenLogChunk: %w", serr)
	}
	raw, err := io.ReadAll(io.LimitReader(f, ChunkMax))
	if err != nil {
		return nil, fmt.Errorf("OpenLogChunk: %w", err)
	}
	return raw, nil
}

// Digest returns the sha256 hex digest of data.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CacheDir returns the trust-scoped cache volume path.
func CacheDir(stateDir, trust, repoKey, name string) (string, error) {
	for _, part := range []string{trust, repoKey, name} {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) {
			return "", fmt.Errorf("CacheDir: cache scope escapes %q", part)
		}
	}
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("CacheDir: cache name escapes %q", name)
	}
	return filepath.Join(stateDir, "cache", trust, repoKey, clean), nil
}

// ArtifactMaxBytes caps one collected artifact. It matches the server's
// per-request artifact cap so one limit applies end-to-end; the log-chunk
// size (ChunkMax) is never an artifact boundary.
const ArtifactMaxBytes = 512 << 20

// CollectArtifact reads one declared artifact path for upload. Artifacts
// over ArtifactMaxBytes fail instead of uploading a silent prefix.
func CollectArtifact(work, name string) (string, []byte, error) {
	clean := filepath.Clean(name)
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", nil, fmt.Errorf("CollectArtifact: artifact escapes %q", name)
	}
	if err := confineToWork(work, clean); err != nil {
		return "", nil, err
	}
	root, err := os.OpenRoot(work)
	if err != nil {
		return "", nil, fmt.Errorf("CollectArtifact: %w", err)
	}
	defer func() { _ = root.Close() }()
	f, err := root.Open(clean)
	if err != nil {
		if info, serr := os.Lstat(filepath.Join(work, clean)); serr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", nil, fail("HOST_PATH_VIOLATION", name+" is a symlink", "declare the file itself as the artifact")
		}
		return "", nil, fmt.Errorf("CollectArtifact: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("CollectArtifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fail("HOST_PATH_VIOLATION", name+" is not a regular file", "declare regular files as artifacts")
	}
	raw, err := io.ReadAll(io.LimitReader(f, ArtifactMaxBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("CollectArtifact: %w", err)
	}
	if int64(len(raw)) > ArtifactMaxBytes {
		return "", nil, fail("ARTIFACT_LIMIT", name+" exceeds the artifact size limit", "shrink the artifact or split it across declarations")
	}
	return Digest(raw), raw, nil
}

func confineToWork(work, clean string) error {
	prefix := work
	for part := range strings.SplitSeq(clean, string(os.PathSeparator)) {
		prefix = filepath.Join(prefix, part)
		info, err := os.Lstat(prefix)
		if err != nil {
			return fmt.Errorf("CollectArtifact: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fail("HOST_PATH_VIOLATION", clean+" traverses a symlink", "declare the file itself as the artifact")
		}
		if !info.IsDir() && prefix != filepath.Join(work, clean) {
			return fail("HOST_PATH_VIOLATION", clean+" traverses a non-directory", "declare regular files as artifacts")
		}
	}
	return nil
}
