//go:build integration

package ruk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

func rukBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("RUK_BIN"); bin != "" {
		return bin
	}
	return "/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk"
}

type daemon struct {
	cmd    *exec.Cmd
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
	//nolint:gosec // G204: bin is RUK_BIN or the pinned debug binary; args are fixed serve flags plus a temp config.
	cmd := exec.Command(bin, "serve", "--config", configPath)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("daemon start: %v\n%s", err, logs.String())
	}
	d := &daemon{socket: socket, cmd: cmd, logs: &logs}
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
	if d.cmd.Process == nil {
		return
	}
	_ = d.cmd.Process.Kill()
	_, _ = d.cmd.Process.Wait()
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
	//nolint:gosec // G204: bin is RUK_BIN or the pinned debug binary; args are fixed serve flags plus a temp config.
	cmd := exec.Command(bin, "serve", "--config", configPath)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("daemon restart: %v\n%s", err, logs.String())
	}
	d := &daemon{socket: socket, cmd: cmd, logs: &logs}
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
	//nolint:gosec // G204: bin is RUK_BIN or the pinned debug binary; args are fixed model flags.
	out, cmdErr := exec.Command(bin, "model", "--format", "json").Output()
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
