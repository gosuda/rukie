//go:build integration

package gitssh

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
	"github.com/gosuda/rukie/internal/store"
)

type sshStack struct {
	t        *testing.T
	auth     *auth.Service
	client   *ruk.Client
	child    *rukproc.Child
	srv      *Server
	addr     string
	dir      string
	roots    string
	keys     map[string]sshClientKey
	hostLine string
}

type sshClientKey struct {
	line        string
	fingerprint string
	file        string
}

func sshBin(t *testing.T) string {
	t.Helper()
	bin, err := rukproc.ResolveBinary("")
	if err != nil {
		t.Fatalf("ruk binary: %v", err)
	}
	return bin
}

func sshTestDB(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+"?_txlock=immediate&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatalf("sql open: %v", err)
	}
	entries, err := os.ReadDir("../store/migrations")
	if err != nil {
		t.Fatalf("migrations dir: %v", err)
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join("../store/migrations", e.Name()))
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		if _, err := raw.Exec(string(body)); err != nil {
			t.Fatalf("apply %s: %v", e.Name(), err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("sql close: %v", err)
	}
}

func sshDaemonLogs(t *testing.T, dir, roots string) (*rukproc.Child, string, *bytes.Buffer) {
	t.Helper()
	socket := filepath.Join(dir, "ruk.sock")
	config := fmt.Sprintf("socket_path = %q\nrepo_roots = [%q]\nallowed_uids = [%d]\nframe_timeout_ms = 5000\nretention_interval_ms = 0\nhook_timeout_ms = 5000\n",
		socket, roots, os.Getuid())
	configPath := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("daemon config: %v", err)
	}
	logs := &bytes.Buffer{}
	child, err := rukproc.StartServe(sshBin(t), configPath, os.Environ(), logs, logs)
	if err != nil {
		t.Fatalf("daemon start: %v\n%s", err, logs.String())
	}
	t.Cleanup(func() { _ = child.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon socket never appeared\n%s", logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	return child, socket, logs
}

func newSSHStack(t *testing.T) *sshStack {
	t.Helper()
	return newSSHStackWith(t, 0, 0)
}

func newSSHStackWith(t *testing.T, idle, keepalive time.Duration) *sshStack {
	t.Helper()
	dir := t.TempDir()
	roots := filepath.Join(dir, "roots")
	if err := os.MkdirAll(roots, 0o755); err != nil {
		t.Fatalf("roots: %v", err)
	}
	sqlite := filepath.Join(dir, "rukie.sqlite")
	sshTestDB(t, sqlite)
	svc, err := auth.Open(sqlite)
	if err != nil {
		t.Fatalf("auth open: %v", err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	child, socket, logs := sshDaemonLogs(t, dir, roots)
	t.Cleanup(func() {
		if t.Failed() {
			raw := logs.String()
			if len(raw) > 4000 {
				raw = raw[len(raw)-4000:]
			}
			t.Logf("daemon logs:\n%s", raw)
		}
	})
	dialCtx, dialCancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer dialCancel()
	client, err := ruk.Dial(dialCtx, socket)
	if err != nil {
		t.Fatalf("dial daemon: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	hostPub, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatalf("host signer: %v", err)
	}
	hostSSHPub, err := ssh.NewPublicKey(hostPub)
	if err != nil {
		t.Fatalf("host pub: %v", err)
	}
	hostLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostSSHPub)))
	st, err := store.Open(t.Context(), store.Options{Path: sqlite})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	srv, err := New(Config{
		Auth:              svc,
		Limiter:           access.NewLimiter(),
		Client:            client,
		RepoRoot:          roots,
		Limits:            wire.DefaultGitLimits(),
		Store:             st,
		HostKeys:          []ssh.Signer{hostSigner},
		Clock:             time.Now,
		IdleTimeout:       idle,
		KeepaliveInterval: keepalive,
	})
	if err != nil {
		t.Fatalf("gitssh: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	go func() { _ = srv.Serve(t.Context(), ln) }()
	s := &sshStack{
		t: t, auth: svc, client: client, child: child, srv: srv,
		addr: ln.Addr().String(), dir: dir, roots: roots,
		keys: map[string]sshClientKey{}, hostLine: hostLine,
	}
	t.Cleanup(func() { _ = child.Kill() })
	return s
}

func (s *sshStack) addAdmin(t *testing.T, name string) auth.User {
	t.Helper()
	user, err := s.auth.BootstrapAdmin(t.Context(), name, "long-test-password-01", "127.0.0.1")
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	return user
}

func (s *sshStack) addUser(t *testing.T, admin int64, name string) auth.User {
	t.Helper()
	user, err := s.auth.CreateUser(t.Context(), admin, auth.CreateUserInput{
		Name: name, Email: name + "@example.test", Password: "long-test-password-01",
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func (s *sshStack) addKey(t *testing.T, user auth.User) sshClientKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("client key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("client pub: %v", err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	if _, addErr := s.auth.AddSSHKey(t.Context(), user.ID, line+" "+user.Name+"-key", "127.0.0.1"); addErr != nil {
		t.Fatalf("register key: %v", addErr)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal priv: %v", err)
	}
	keyFile := filepath.Join(s.dir, user.Name+".key")
	var buf bytes.Buffer
	if err := pem.Encode(&buf, block); err != nil {
		t.Fatalf("pem: %v", err)
	}
	if err := os.WriteFile(keyFile, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	key := sshClientKey{line: line, fingerprint: ssh.FingerprintSHA256(sshPub), file: keyFile}
	s.keys[user.Name] = key
	return key
}

func (s *sshStack) addRepo(t *testing.T, owner auth.User, name, visibility string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	now := time.Now().UnixMilli()
	_, err := s.auth.DB().ExecContext(ctx, `INSERT INTO repos (owner_user_id, org_id, name, visibility, created_at, updated_at) VALUES (?, 0, ?, ?, ?, ?)`,
		owner.ID, name, visibility, now, now)
	if err != nil {
		t.Fatalf("insert repo: %v", err)
	}
	repoDir := filepath.Join(s.roots, owner.Name, name+".git")
	seedCtx, seedCancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer seedCancel()
	if out, err := rukproc.Run(seedCtx, sshBin(t), []string{"git", "init", repoDir}, append(os.Environ(), "RUK_COORDINATOR=off")); err != nil {
		t.Fatalf("ruk init: %v\n%s", err, out)
	}
}

func (s *sshStack) gitEnv(t *testing.T, keyFile string) []string {
	t.Helper()
	_, port, _ := strings.Cut(s.addr, ":")
	knownHosts := filepath.Join(s.dir, "known_hosts")
	line := "[127.0.0.1]:" + port + " " + s.hostLine + "\n"
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatalf("known hosts: %v", err)
	}
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=e2e",
		"GIT_AUTHOR_EMAIL=e2e@example.test",
		"GIT_COMMITTER_NAME=e2e",
		"GIT_COMMITTER_EMAIL=e2e@example.test",
		"LANG=C",
		"GIT_SSH_COMMAND=ssh -i " + keyFile + " -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes -o UserKnownHostsFile=" + knownHosts + " -o BatchMode=yes -o ConnectTimeout=10 -o LogLevel=ERROR",
	}
}

func sshGitRun(ctx context.Context, t *testing.T, s *sshStack, keyFile, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = s.gitEnv(t, keyFile)
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	return combined.String(), err
}

func (s *sshStack) localRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	base := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_AUTHOR_NAME=e2e", "GIT_AUTHOR_EMAIL=e2e@example.test",
		"GIT_COMMITTER_NAME=e2e", "GIT_COMMITTER_EMAIL=e2e@example.test", "LANG=C"}
	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = dir
		cmd.Env = base
		var combined bytes.Buffer
		cmd.Stdout = &combined
		cmd.Stderr = &combined
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, combined.String())
		}
		return strings.TrimSpace(combined.String())
	}
	run("init", "-b", "main", ".")
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	run("add", "-A")
	run("commit", "-m", "seed")
	return dir, run("rev-parse", "HEAD")
}

func TestSSHStockGitCloneAndPush(t *testing.T) {
	s := newSSHStack(t)
	ada := s.addAdmin(t, "ada")
	adaKey := s.addKey(t, ada)
	s.addRepo(t, ada, "hello", "private")
	work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	url := "ssh://ada@" + s.addr + "/ada/hello.git"
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if out, err := sshGitRun(ctx, t, s, adaKey.file, work, "push", url, "HEAD:refs/heads/main"); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if out, err := sshGitRun(ctx, t, s, adaKey.file, t.TempDir(), "ls-remote", url); err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	} else if !strings.Contains(out, oid) {
		t.Fatalf("ls-remote misses %s:\n%s", oid, out)
	}
	cloneDir := t.TempDir()
	if out, err := sshGitRun(ctx, t, s, adaKey.file, cloneDir, "clone", url, "hello"); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(filepath.Join(cloneDir, "hello", "README.md"))
	if err != nil {
		t.Fatalf("read clone: %v", err)
	}
	if string(raw) != "hello\n" {
		t.Fatalf("cloned content = %q", raw)
	}
}

func newUnknownKey(t *testing.T, s *sshStack, name string) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("pub: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	keyFile := filepath.Join(s.dir, name+".key")
	var buf bytes.Buffer
	if err := pem.Encode(&buf, block); err != nil {
		t.Fatalf("pem: %v", err)
	}
	if err := os.WriteFile(keyFile, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return keyFile, ssh.FingerprintSHA256(sshPub)
}

func TestSSHUnknownKeyGenericDenied(t *testing.T) {
	s := newSSHStack(t)
	ada := s.addAdmin(t, "ada")
	s.addKey(t, ada)
	s.addRepo(t, ada, "hello", "private")
	keyFile, fingerprint := newUnknownKey(t, s, "unknown")
	url := "ssh://ada@" + s.addr + "/ada/hello.git"
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	out, err := sshGitRun(ctx, t, s, keyFile, t.TempDir(), "ls-remote", url)
	if err == nil {
		t.Fatalf("unknown key admitted:\n%s", out)
	}
	if !strings.Contains(out, "Permission denied") {
		t.Fatalf("denial = %q, want generic Permission denied", out)
	}
	if strings.Contains(out, "unknown user") || strings.Contains(out, "no such") {
		t.Fatalf("denial leaks enumeration: %q", out)
	}
	var count int64
	err = s.auth.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events WHERE action = 'ssh.unknown_key' AND target_type = 'ssh_key' AND target_id = ?`, fingerprint).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("audit rows = %d, %v", count, err)
	}
}

func TestSSHKeyScopeCeiling(t *testing.T) {
	s := newSSHStack(t)
	ada := s.addAdmin(t, "ada")
	adaKey := s.addKey(t, ada)
	bob := s.addUser(t, ada.ID, "bob")
	bobKey := s.addKey(t, bob)
	s.addRepo(t, ada, "pub", "public")
	s.addRepo(t, ada, "secret", "private")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	pubURL := "ssh://" + "bob@" + s.addr + "/ada/pub.git"
	secretURL := "ssh://" + "bob@" + s.addr + "/ada/secret.git"
	if out, err := sshGitRun(ctx, t, s, bobKey.file, t.TempDir(), "ls-remote", pubURL); err != nil {
		t.Fatalf("public clone denied: %v\n%s", err, out)
	}
	work, _ := s.localRepo(t, map[string]string{"x.md": "x\n"})
	if out, err := sshGitRun(ctx, t, s, bobKey.file, work, "push", pubURL, "HEAD:refs/heads/main"); err == nil {
		t.Fatal("outsider push to public repo admitted")
	} else if !strings.Contains(out, "not allowed by policy") {
		t.Fatalf("push denial = %q", out)
	}
	if out, err := sshGitRun(ctx, t, s, bobKey.file, t.TempDir(), "ls-remote", secretURL); err == nil {
		t.Fatal("outsider read of private repo admitted")
	} else if !strings.Contains(out, "not found") && !strings.Contains(out, "read access is required") {
		t.Fatalf("private denial = %q", out)
	}
	ownerURL := "ssh://ada@" + s.addr + "/ada/pub.git"
	if out, err := sshGitRun(ctx, t, s, adaKey.file, t.TempDir(), "ls-remote", ownerURL); err != nil {
		t.Fatalf("owner read denied: %v\n%s", err, out)
	}
}

func TestSSHRefsForCreatesChange(t *testing.T) {
	s := newSSHStack(t)
	ada := s.addAdmin(t, "ada")
	adaKey := s.addKey(t, ada)
	s.addRepo(t, ada, "hello", "private")
	work, oid := s.localRepo(t, map[string]string{"README.md": "hello\n"})
	url := "ssh://ada@" + s.addr + "/ada/hello.git"
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if out, err := sshGitRun(ctx, t, s, adaKey.file, work, "push", url, "HEAD:refs/for/main"); err != nil {
		t.Fatalf("divert refs/for push: %v\n%s", err, out)
	}
	out, err := sshGitRun(ctx, t, s, adaKey.file, t.TempDir(), "ls-remote", url)
	if err != nil {
		t.Fatalf("ls-remote: %v\n%s", err, out)
	}
	if !strings.Contains(out, oid+"\trefs/changes/") {
		t.Fatalf("divert left no change ref carrying %s:\n%s", oid, out)
	}
}

func TestSSHMalformedExecRejected(t *testing.T) {
	s := newSSHStack(t)
	ada := s.addAdmin(t, "ada")
	s.addKey(t, ada)
	s.addRepo(t, ada, "hello", "private")
	signer := parseSigner(t, s.keys["ada"].file)
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s.hostLine))
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	config := &ssh.ClientConfig{
		User:            "ada",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         15 * time.Second,
	}
	conn, err := ssh.Dial("tcp", s.addr, config)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	for _, payload := range []string{
		"git-upload-pack",
		"rm -rf /",
		"git-upload-pack '/../../x.git'",
		"git-upload-pack 'a'; touch /tmp/ssh_pwn_probe",
		"git-upload-archive '/ada/hello.git'",
		"git-receive-pack '/ada/.git'",
	} {
		sess, err := conn.NewSession()
		if err != nil {
			t.Fatalf("session: %v", err)
		}
		var stderr bytes.Buffer
		sess.Stderr = &stderr
		err = sess.Run(payload)
		_ = sess.Close()
		if err == nil {
			t.Fatalf("%q admitted", payload)
		}
		if !strings.Contains(stderr.String(), "invalid git command") {
			t.Fatalf("%q stderr = %q", payload, stderr.String())
		}
	}
	if _, err := os.Stat("/tmp/ssh_pwn_probe"); !os.IsNotExist(err) {
		t.Fatal("injection payload executed")
	}
}

func parseSigner(t *testing.T, path string) ssh.Signer {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	return signer
}

func TestSSHUnknownKeyNoEnumeration(t *testing.T) {
	s := newSSHStack(t)
	ada := s.addAdmin(t, "ada")
	s.addKey(t, ada)
	s.addRepo(t, ada, "hello", "private")
	firstFile, _ := newUnknownKey(t, s, "ghost-a")
	secondFile, _ := newUnknownKey(t, s, "ghost-b")
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	first, err := sshGitRun(ctx, t, s, firstFile, t.TempDir(), "ls-remote", "ssh://ada@"+s.addr+"/ada/hello.git")
	if err == nil {
		t.Fatalf("unknown key admitted:\n%s", first)
	}
	second, err := sshGitRun(ctx, t, s, secondFile, t.TempDir(), "ls-remote", "ssh://nosuchuser@"+s.addr+"/ada/hello.git")
	if err == nil {
		t.Fatalf("unknown key admitted:\n%s", second)
	}
	for name, out := range map[string]string{"ada": first, "nosuchuser": second} {
		if !strings.Contains(out, "Permission denied") {
			t.Fatalf("%s denial = %q", name, out)
		}
		if strings.Contains(out, "unknown user") || strings.Contains(out, "no such") {
			t.Fatalf("%s denial leaks enumeration: %q", name, out)
		}
	}
}

func TestSSHIdleTimeoutClosesQuietConn(t *testing.T) {
	s := newSSHStackWith(t, 2*time.Second, 200*time.Millisecond)
	ada := s.addAdmin(t, "ada")
	s.addKey(t, ada)
	s.addRepo(t, ada, "hello", "private")
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s.hostLine))
	if err != nil {
		t.Fatalf("host key: %v", err)
	}
	conn, err := ssh.Dial("tcp", s.addr, &ssh.ClientConfig{
		User:            "ada",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(parseSigner(t, s.keys["ada"].file))},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         15 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	sess, err := conn.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer func() { _ = sess.Close() }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		_, _, err := conn.SendRequest("keepalive@openssh.com", true, nil)
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("quiet connection survived the idle timeout")
		}
		time.Sleep(300 * time.Millisecond)
	}
}
