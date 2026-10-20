//go:build integration

package ruk

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

func commandRepo(t *testing.T, d *daemon) (string, *Client) {
	t.Helper()
	repoDir := filepath.Join(filepath.Dir(d.socket), "roots", "repo")
	seedStore(t, repoDir)
	client := dialDaemon(t, d.socket)
	return filepath.Join(repoDir, ".ruk"), client
}

func TestCommandAdvertiseRoundTrip(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	storeDir, client := commandRepo(t, d)
	ctx, cancel := testCtx(t)
	defer cancel()
	var stdout bytes.Buffer
	report, _, err := RunCommand(ctx, client, Command{Open: wire.ServiceOpen{
		Service:    "upload-pack",
		Repository: storeDir,
		Advertise:  true,
		Limits:     wire.DefaultGitLimits(),
	}, Stdout: &stdout})
	if err != nil {
		t.Fatalf("advertise: %v", err)
	}
	if report.ExitCode != 0 || report.Cancelled {
		t.Fatalf("report = %+v", report)
	}
	if !strings.HasSuffix(stdout.String(), "0000") || !strings.Contains(stdout.String(), "multi_ack") {
		t.Fatalf("advertisement %q is not a v0 upload-pack document", stdout.String())
	}
}

func TestGitServiceUnknownServiceRefused(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	storeDir, client := commandRepo(t, d)
	ctx, cancel := testCtx(t)
	defer cancel()
	var stdout bytes.Buffer
	_, _, err := RunCommand(ctx, client, Command{Open: wire.ServiceOpen{
		Service:    "bogus-service",
		Repository: storeDir,
		Advertise:  true,
		Limits:     wire.DefaultGitLimits(),
	}, Stdout: &stdout})
	if err == nil {
		t.Fatal("unknown service succeeded")
	}
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want E_INVALID", err)
	}
}

func TestCommandReceivePackOversizeStdinRefused(t *testing.T) {
	d := spawnDaemon(t, currentUID(t))
	storeDir, client := commandRepo(t, d)
	ctx, cancel := testCtx(t)
	defer cancel()
	opened, err := SessionOpen(ctx, client, Repo{Path: storeDir}, wire.SessionOpenArgs{})
	if err != nil {
		t.Fatalf("session open: %v", err)
	}
	limits := wire.DefaultGitLimits()
	limits.MaxStdinBytes = 64
	body := bytes.Repeat([]byte("a"), 65)
	var stdout bytes.Buffer
	_, _, err = RunCommand(ctx, client, Command{Open: wire.ServiceOpen{
		Service:    "receive-pack",
		Repository: storeDir,
		SessionID:  &opened.Session,
		RequestID:  new("push-test-oversize"),
		Limits:     limits,
	}, Stdin: bytes.NewReader(body), Stdout: &stdout})
	if err == nil {
		t.Fatal("oversize stdin succeeded")
	}
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("err = %v, want E_LIMIT", err)
	}
}
