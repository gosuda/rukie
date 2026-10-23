//go:build integration

package ruk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jsonv2 "encoding/json/v2"

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

func TestCommandRefillAcceptsInterleavedOutput(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	var stdout bytes.Buffer
	driver := &commandDriver{pc: &conn{nc: clientConn, writeEvery: time.Second}, id: 1, stdout: &stdout}
	serverDone := make(chan error, 1)
	go func() {
		if err := writeFrame(serverConn, KindStdout, 1, []byte("NAK\n")); err != nil {
			serverDone <- err
			return
		}
		creditFrame, err := readFrame(serverConn)
		if err != nil {
			serverDone <- err
			return
		}
		if creditFrame.Kind != KindWindowUpdate {
			serverDone <- fmt.Errorf("stdout credit kind = %d", creditFrame.Kind)
			return
		}
		err = writeFrame(serverConn, KindStderr, 1, []byte("warning"))
		if err != nil {
			serverDone <- err
			return
		}
		credit, err := jsonv2.Marshal(wire.WindowCredit{Direction: "stdin", CreditBytes: 5})
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- writeFrame(serverConn, KindWindowUpdate, 1, credit)
	}()

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := driver.refill(ctx); err != nil {
		t.Fatalf("refill: %v", err)
	}
	if got := stdout.String(); got != "NAK\n" {
		t.Fatalf("stdout = %q", got)
	}
	if got := string(driver.stderr); got != "warning" {
		t.Fatalf("stderr = %q", got)
	}
	if driver.window != 5 {
		t.Fatalf("window = %d, want 5", driver.window)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("scripted daemon: %v", err)
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
