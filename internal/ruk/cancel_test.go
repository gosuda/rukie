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
	"sync"
	"testing"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

type scriptedServer struct {
	t      *testing.T
	errs   chan error
	ln     net.Listener
	socket string
	wg     sync.WaitGroup
}

func newScriptedServer(t *testing.T) *scriptedServer {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "script.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return &scriptedServer{t: t, ln: ln, socket: socket, errs: make(chan error, 16)}
}

func (s *scriptedServer) fail(err error) {
	select {
	case s.errs <- err:
	default:
	}
}

func (s *scriptedServer) serveOne(handle func(conn net.Conn)) {
	s.wg.Go(func() {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		handle(conn)
	})
}

func (s *scriptedServer) client() *Client {
	s.t.Helper()
	client := &Client{
		socket:   s.socket,
		profile:  "rukie",
		timeouts: DefaultTimeouts(),
		closed:   make(chan struct{}),
	}
	client.pool = newPool(client, 8, 8)
	s.t.Cleanup(func() { _ = client.Close() })
	return client
}

func (s *scriptedServer) assertNoErrors(t *testing.T) {
	t.Helper()
	s.wg.Wait()
	close(s.errs)
	for err := range s.errs {
		t.Fatalf("scripted daemon: %v", err)
	}
}

func mustMarshal(v any) ([]byte, error) {
	return jsonv2.Marshal(v)
}

func scriptedHandshake(conn net.Conn) error {
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	hello, err := readFrame(conn)
	if err != nil {
		return fmt.Errorf("handshake hello: %w", err)
	}
	if hello.Kind != KindOpen || hello.Stream != ControlStream {
		return fmt.Errorf("handshake hello = kind %d stream %d", uint8(hello.Kind), hello.Stream)
	}
	accept := wire.HandshakeAccept{
		Profile:       "rukie",
		ObjectFormats: []string{"sha1"},
		Features:      []string{"credits", "hooks.v1", "events.v1", "archive.v1"},
		ProtocolMajor: 1,
		ProtocolMinor: 1,
	}
	raw, err := mustMarshal(accept)
	if err != nil {
		return err
	}
	if err := writeFrame(conn, KindAccepted, ControlStream, raw); err != nil {
		return fmt.Errorf("handshake accept: %w", err)
	}
	return conn.SetDeadline(time.Time{})
}

func acceptCommand(conn net.Conn, stdinCredit uint64) (uint64, error) {
	open, err := readFrame(conn)
	if err != nil {
		return 0, fmt.Errorf("open: %w", err)
	}
	if open.Kind != KindOpen {
		return 0, fmt.Errorf("first stream frame = kind %d, want open", uint8(open.Kind))
	}
	raw, err := mustMarshal(wire.CommandAccepted{
		InvocationID: "inv-script",
		StreamID:     open.Stream,
		StdinCredit:  stdinCredit,
	})
	if err != nil {
		return 0, err
	}
	if err := writeFrame(conn, KindAccepted, open.Stream, raw); err != nil {
		return 0, fmt.Errorf("accepted: %w", err)
	}
	return open.Stream, nil
}

func drainStdin(conn net.Conn, stream uint64) error {
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	for {
		frame, err := readFrame(conn)
		if err != nil {
			return fmt.Errorf("stdin: %w", err)
		}
		if frame.Stream != stream {
			return fmt.Errorf("stdin frame on stream %d, want %d", frame.Stream, stream)
		}
		switch frame.Kind {
		case KindStdin:
		case KindStdinEOF:
			return nil
		case KindCancel:
			return errors.New("cancel arrived before stdin EOF")
		case KindOpen, KindAccepted, KindStdout, KindStderr, KindWindowUpdate,
			KindExit, KindError, KindHookCall, KindHookReply:
			return fmt.Errorf("stdin frame kind %d", uint8(frame.Kind))
		}
	}
}

func waitScripted(t *testing.T, ch <-chan [2]time.Time, timeout time.Duration) ([2]time.Time, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !time.Now().After(deadline) {
		select {
		case got := <-ch:
			return got, true
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	return [2]time.Time{}, false
}

func watchClose(conn net.Conn, stream uint64, seen chan<- [2]time.Time) {
	var at [2]time.Time
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	for {
		frame, err := readFrame(conn)
		if err != nil {
			at[1] = time.Now()
			seen <- at
			return
		}
		if frame.Stream == stream && frame.Kind == KindCancel && at[0].IsZero() {
			at[0] = time.Now()
		}
	}
}

func TestCommandHookTimeoutDrainsToExit(t *testing.T) {
	srv := newScriptedServer(t)
	srv.serveOne(func(conn net.Conn) {
		if err := scriptedHandshake(conn); err != nil {
			srv.fail(err)
			return
		}
		stream, err := acceptCommand(conn, 1<<20)
		if err != nil {
			srv.fail(err)
			return
		}
		if err = drainStdin(conn, stream); err != nil {
			srv.fail(err)
			return
		}
		call := wire.HookCall{
			Phase:      wire.HookPreReceive,
			Commands:   []wire.HookCommand{{Name: "refs/heads/main", Old: strings.Repeat("0", 40), New: strings.Repeat("1", 40), Authorized: true}},
			DeadlineMs: 5000,
		}
		raw, err := mustMarshal(call)
		if err != nil {
			srv.fail(err)
			return
		}
		if err = writeFrame(conn, KindHookCall, stream, raw); err != nil {
			srv.fail(err)
			return
		}
		reply, err := readFrame(conn)
		if err != nil || reply.Kind != KindHookReply {
			srv.fail(fmt.Errorf("hook reply: %w", err))
			return
		}
		if err = writeFrame(conn, KindStdout, stream, []byte("ng refs/heads/main hook handler timed out\n")); err != nil {
			srv.fail(err)
			return
		}
		report, err := mustMarshal(wire.CommandExitReport{ExitCode: 0, InvocationID: "inv-script"})
		if err != nil {
			srv.fail(err)
			return
		}
		if err = writeFrame(conn, KindExit, stream, report); err != nil {
			srv.fail(err)
			return
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = readFrame(conn)
	})
	client := srv.client()
	hook := func(_ context.Context, _ wire.HookCall) wire.HookReply {
		time.Sleep(300 * time.Millisecond)
		return wire.HookReply{Verdicts: []wire.HookRefVerdict{{Name: "refs/heads/main", Verdict: wire.AcceptVerdict()}}}
	}
	var stdout bytes.Buffer
	_, _, err := RunCommand(t.Context(), client, Command{
		Open: wire.ServiceOpen{
			Service:      "receive-pack",
			Repository:   "/repo",
			StdoutCredit: 1 << 20,
			Limits:       wire.DefaultGitLimits(),
		},
		Stdin:      strings.NewReader("hello"),
		Stdout:     &stdout,
		Hook:       hook,
		HookBudget: 50 * time.Millisecond,
	})
	srv.assertNoErrors(t)
	if err == nil || !errors.Is(err, errHookTimeout) {
		t.Fatalf("err = %v, want the hook-timeout error", err)
	}
	if !strings.Contains(stdout.String(), "ng refs/heads/main hook handler timed out") {
		t.Fatalf("stdout = %q, want the surfaced hook reason", stdout.String())
	}
}

func TestCommandCancelAfterStdinEOFDrains(t *testing.T) {
	srv := newScriptedServer(t)
	eofSeen := make(chan struct{})
	srv.serveOne(func(conn net.Conn) {
		if err := scriptedHandshake(conn); err != nil {
			srv.fail(err)
			return
		}
		stream, err := acceptCommand(conn, 1<<20)
		if err != nil {
			srv.fail(err)
			return
		}
		if err = drainStdin(conn, stream); err != nil {
			srv.fail(err)
			return
		}
		close(eofSeen)
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		for {
			frame, rerr := readFrame(conn)
			if rerr != nil {
				srv.fail(fmt.Errorf("cancel wait: %w", rerr))
				return
			}
			if frame.Kind == KindCancel {
				break
			}
		}
		time.Sleep(300 * time.Millisecond)
		if err = writeFrame(conn, KindStdout, stream, []byte("late-bytes")); err != nil {
			srv.fail(err)
			return
		}
		report, err := mustMarshal(wire.CommandExitReport{ExitCode: 0, InvocationID: "inv-script"})
		if err != nil {
			srv.fail(err)
			return
		}
		if err = writeFrame(conn, KindExit, stream, report); err != nil {
			srv.fail(err)
			return
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = readFrame(conn)
	})
	client := srv.client()
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		select {
		case <-eofSeen:
			time.Sleep(200 * time.Millisecond)
			cancel()
		case <-ctx.Done():
		}
	}()
	var stdout bytes.Buffer
	report, _, err := RunCommand(ctx, client, Command{
		Open: wire.ServiceOpen{
			Service:      "upload-pack",
			Repository:   "/repo",
			StdoutCredit: 1 << 20,
			Limits:       wire.DefaultGitLimits(),
		},
		Stdin:  strings.NewReader("hello"),
		Stdout: &stdout,
	})
	srv.assertNoErrors(t)
	if err != nil {
		t.Fatalf("drained run err = %v, want the received terminal report", err)
	}
	if report.ExitCode != 0 {
		t.Fatalf("drained run exit = %d, want 0", report.ExitCode)
	}
	if !strings.Contains(stdout.String(), "late-bytes") {
		t.Fatalf("stdout = %q, want the post-cancel bytes", stdout.String())
	}
}

func TestCommandCancelBeforeStdinEOFClosesFast(t *testing.T) {
	srv := newScriptedServer(t)
	seen := make(chan [2]time.Time, 1)
	streaming := make(chan struct{})
	srv.serveOne(func(conn net.Conn) {
		if err := scriptedHandshake(conn); err != nil {
			srv.fail(err)
			return
		}
		stream, err := acceptCommand(conn, 16)
		if err != nil {
			srv.fail(err)
			return
		}
		first, rerr := readFrame(conn)
		if rerr != nil {
			srv.fail(rerr)
			return
		}
		if first.Kind != KindStdin {
			srv.fail(fmt.Errorf("first stream frame = kind %d, want stdin", uint8(first.Kind)))
			return
		}
		close(streaming)
		watchClose(conn, stream, seen)
	})
	client := srv.client()
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		select {
		case <-streaming:
			time.Sleep(200 * time.Millisecond)
			cancel()
		case <-ctx.Done():
		}
	}()
	var stdout bytes.Buffer
	_, _, _ = RunCommand(ctx, client, Command{
		Open: wire.ServiceOpen{
			Service:      "upload-pack",
			Repository:   "/repo",
			StdoutCredit: 1 << 20,
			Limits:       wire.DefaultGitLimits(),
		},
		Stdin:  strings.NewReader(strings.Repeat("a", 1<<20)),
		Stdout: &stdout,
	})
	at, ok := waitScripted(t, seen, 10*time.Second)
	if !ok {
		t.Fatal("scripted daemon saw no close")
	}
	srv.wg.Wait()
	srv.assertNoErrors(t)
	if at[0].IsZero() {
		t.Fatal("scripted daemon saw no cancel")
	}
	if gap := at[1].Sub(at[0]); gap >= time.Second {
		t.Fatalf("close followed cancel by %v, want a fast close before EOF", gap)
	}
}
