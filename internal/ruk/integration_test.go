//go:build integration

package ruk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/rukproc"
)

func rukBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("RUK_BIN"); bin != "" {
		return bin
	}
	return "/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk"
}

type daemon struct {
	child  *rukproc.Child
	logs   *bytes.Buffer
	socket string
}

func spawnDaemon(t *testing.T, allowedUID int) *daemon {
	t.Helper()
	bin := rukBin(t)
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("ruk binary missing at %s: build it first", bin)
	}
	dir := t.TempDir()
	roots := filepath.Join(dir, "roots")
	if err := os.MkdirAll(roots, 0o750); err != nil {
		t.Fatalf("roots: %v", err)
	}
	socket := filepath.Join(dir, "ruk.sock")
	config := fmt.Sprintf("socket_path = %q\nrepo_roots = [%q]\nallowed_uids = [%s]\nframe_timeout_ms = 5000\nretention_interval_ms = 0\n",
		socket, roots, strconv.Itoa(allowedUID))
	configPath := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("config: %v", err)
	}
	var logs bytes.Buffer
	child, err := rukproc.StartServe(bin, configPath, os.Environ(), &logs, &logs)
	if err != nil {
		t.Fatalf("daemon start: %v\n%s", err, logs.String())
	}
	d := &daemon{socket: socket, child: child, logs: &logs}
	t.Cleanup(d.kill)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			time.Sleep(200 * time.Millisecond)
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.kill()
	t.Fatalf("socket never appeared\n%s", logs.String())
	return nil
}

func (d *daemon) kill() {
	_ = d.child.Kill()
}

func seedStore(t *testing.T, dir string) {
	t.Helper()
	bin := rukBin(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if out, err := rukproc.Run(ctx, bin, []string{"git", "init", dir}, append(os.Environ(), "RUK_COORDINATOR=off")); err != nil {
		t.Fatalf("seed store: %v\n%s", err, out)
	}
}

func currentUID(t *testing.T) int {
	t.Helper()
	uid := os.Getuid()
	if uid < 0 {
		t.Fatalf("unexpected uid %d", uid)
	}
	return uid
}

func testCtx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(t.Context(), 10*time.Second)
}

func dialDaemon(t *testing.T, socket string) *Client {
	t.Helper()
	ctx, cancel := testCtx(t)
	defer cancel()
	client, err := Dial(ctx, socket)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})
	return client
}

func TestDaemonLifecycle(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	client := dialDaemon(t, d.socket)
	ctx, cancel := testCtx(t)
	defer cancel()
	missing := filepath.Join(filepath.Dir(d.socket), "roots", "missing")
	_, err := client.OpenRepo(ctx, missing)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("open missing err = %v", err)
	}
	_ = client.Close()
	dialDaemon(t, d.socket)
}

func TestDaemonRejectsRepoOutsideRoots(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	client := dialDaemon(t, d.socket)
	outside := t.TempDir()
	ctx, cancel := testCtx(t)
	defer cancel()
	_, err := client.OpenRepo(ctx, outside)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("outside roots err = %v", err)
	}
}

func TestDaemonRefusesUnlistedPeer(t *testing.T) {
	d := spawnDaemon(t, 4294967294)
	ctx, cancel := testCtx(t)
	defer cancel()
	_, err := Dial(ctx, d.socket)
	if err == nil {
		t.Fatal("dial as unlisted peer succeeded")
	}
	if !errors.Is(err, ErrHandshake) {
		t.Fatalf("err = %v", err)
	}
}

func TestDaemonSessionOpen(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	repoDir := filepath.Join(filepath.Dir(d.socket), "roots", "repo")
	seedStore(t, repoDir)
	client := dialDaemon(t, d.socket)
	ctx, cancel := testCtx(t)
	defer cancel()
	repo, err := client.OpenRepo(ctx, filepath.Join(repoDir, ".ruk"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	if repo.Handle == "" {
		t.Fatal("empty handle")
	}
	opened, err := SessionOpen(ctx, client, repo, wire.SessionOpenArgs{})
	if err != nil {
		t.Fatalf("session open: %v", err)
	}
	if opened.Session == "" || opened.ReconnectSecret.Raw() == "" || opened.Principal == "" {
		t.Fatalf("session opened = %+v", opened)
	}
	resumed, err := SessionResume(ctx, client, repo, wire.SessionResumeArgs{
		Session:         opened.Session,
		ReconnectSecret: opened.ReconnectSecret,
	})
	if err != nil {
		t.Fatalf("session resume: %v", err)
	}
	if !resumed.Authority.IsActive() {
		t.Fatal("resumed authority inactive")
	}
}

func TestDaemonClaimListRead(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	repoDir := filepath.Join(filepath.Dir(d.socket), "roots", "repo")
	seedStore(t, repoDir)
	client := dialDaemon(t, d.socket)
	ctx, cancel := testCtx(t)
	defer cancel()
	repo, err := client.OpenRepo(ctx, filepath.Join(repoDir, ".ruk"))
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	raw, err := Call[map[string]any, jsonv1.RawMessage](ctx, client, repo, wire.MethodClaimList, map[string]any{})
	if err != nil {
		t.Fatalf("claim list: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty claim list reply")
	}
	const requestID = "123e4567-e89b-12d3-a456-426614174000"
	first, err := Retry[map[string]any, jsonv1.RawMessage](ctx, client, repo, wire.MethodClaimList, requestID, map[string]any{})
	if err != nil {
		t.Fatalf("retry one: %v", err)
	}
	second, err := Retry[map[string]any, jsonv1.RawMessage](ctx, client, repo, wire.MethodClaimList, requestID, map[string]any{})
	if err != nil {
		t.Fatalf("retry two: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("same request id gave different replies")
	}
}

func TestDaemonReconnect(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	client := dialDaemon(t, d.socket)
	ctx, cancel := testCtx(t)
	defer cancel()
	missing := filepath.Join(filepath.Dir(d.socket), "roots", "missing")
	if _, err := client.OpenRepo(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first err = %v", err)
	}
	d.kill()
	if _, err := client.OpenRepo(ctx, missing); err == nil {
		t.Fatal("call on dead daemon succeeded")
	}
	d2 := spawnDaemonOn(t, d.socket, filepath.Dir(d.socket), currentUID(t))
	_ = d2
	if _, err := client.OpenRepo(ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reconnected err = %v", err)
	}
}

func spawnDaemonOn(t *testing.T, socket, dir string, allowedUID int) *daemon {
	t.Helper()
	bin := rukBin(t)
	roots := filepath.Join(dir, "roots")
	config := fmt.Sprintf("socket_path = %q\nrepo_roots = [%q]\nallowed_uids = [%s]\nframe_timeout_ms = 5000\nretention_interval_ms = 0\n",
		socket, roots, strconv.Itoa(allowedUID))
	configPath := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatalf("config: %v", err)
	}
	var logs bytes.Buffer
	child, err := rukproc.StartServe(bin, configPath, os.Environ(), &logs, &logs)
	if err != nil {
		t.Fatalf("daemon restart: %v\n%s", err, logs.String())
	}
	d := &daemon{socket: socket, child: child, logs: &logs}
	t.Cleanup(d.kill)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			time.Sleep(200 * time.Millisecond)
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	d.kill()
	t.Fatalf("socket never reappeared\n%s", logs.String())
	return nil
}

func TestModelCLIMatchesSnapshot(t *testing.T) {
	bin := rukBin(t)
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("ruk binary missing at %s", bin)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	out, cmdErr := rukproc.Run(ctx, bin, []string{"model", "--format", "json"}, os.Environ())
	if cmdErr != nil {
		t.Fatalf("ruk model: %v", cmdErr)
	}
	var live struct {
		Methods []struct {
			Kind   string `json:"kind"`
			Method string `json:"method"`
		} `json:"methods"`
		ErrorCodes []struct {
			Code       string `json:"code"`
			ExitStatus int    `json:"exit_status"`
		} `json:"error_codes"`
		SchemaVersion struct {
			Major uint32 `json:"major"`
			Minor uint32 `json:"minor"`
		} `json:"schema_version"`
	}
	if err := jsonv2.Unmarshal(out, &live, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode live model: %v", err)
	}
	raw, err := os.ReadFile("testdata/ruk-model.json")
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	var snap struct {
		Methods []struct {
			Kind   string `json:"kind"`
			Method string `json:"method"`
		} `json:"methods"`
		ErrorCodes []struct {
			Code       string `json:"code"`
			ExitStatus int    `json:"exit_status"`
		} `json:"error_codes"`
		SchemaVersion struct {
			Major uint32 `json:"major"`
			Minor uint32 `json:"minor"`
		} `json:"schema_version"`
	}
	if err := jsonv2.Unmarshal(raw, &snap, jsonv1.DefaultOptionsV1()); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if len(live.Methods) != len(snap.Methods) {
		t.Fatalf("live methods = %d, snapshot has %d", len(live.Methods), len(snap.Methods))
	}
	for i := range live.Methods {
		if live.Methods[i] != snap.Methods[i] {
			t.Fatalf("method %d differs: %+v vs %+v", i, live.Methods[i], snap.Methods[i])
		}
	}
	if len(live.ErrorCodes) != len(snap.ErrorCodes) {
		t.Fatalf("live codes = %d, snapshot has %d", len(live.ErrorCodes), len(snap.ErrorCodes))
	}
	for i := range live.ErrorCodes {
		if live.ErrorCodes[i] != snap.ErrorCodes[i] {
			t.Fatalf("code %d differs", i)
		}
	}
	if live.SchemaVersion != snap.SchemaVersion {
		t.Fatalf("version differs: %+v vs %+v", live.SchemaVersion, snap.SchemaVersion)
	}
}
