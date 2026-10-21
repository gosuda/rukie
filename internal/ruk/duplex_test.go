package ruk

import (
	"bytes"
	"context"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

type scriptFake struct {
	t      *testing.T
	mu     sync.Mutex
	stdin  []byte
	naked  bool
	exited bool
}

func (f *scriptFake) writeKind(nc net.Conn, k Kind, stream uint64, payload []byte) {
	f.t.Helper()
	if err := writeFrame(nc, k, stream, payload); err != nil {
		f.t.Logf("fake write: %v", err)
	}
}

func (f *scriptFake) grant(nc net.Conn, stream uint64, n uint64) {
	raw, err := jsonv2.Marshal(wire.WindowCredit{Direction: "stdin", Bytes: n})
	if err != nil {
		f.t.Fatalf("fake grant encodes: %v", err)
	}
	f.writeKind(nc, KindWindowUpdate, stream, raw)
}

func (f *scriptFake) serveConn(nc net.Conn) {
	defer func() { _ = nc.Close() }()
	frame, err := readFrame(nc)
	if err != nil {
		return
	}
	if frame.Kind != KindOpen || frame.Stream != ControlStream {
		return
	}
	accept, _ := jsonv2.Marshal(map[string]any{
		"protocol_major": 1,
		"protocol_minor": 1,
		"profile":        "fake",
		"object_formats": []string{"sha1"},
		"features":       []string{"credits", "hooks.v1", "events.v1", "archive.v1"},
	})
	f.writeKind(nc, KindAccepted, ControlStream, accept)
	f.command(nc)
}

func (f *scriptFake) command(nc net.Conn) {
	var stream uint64
	for {
		frame, err := readFrame(nc)
		if err != nil {
			return
		}
		switch frame.Kind {
		case KindOpen:
			if frame.Stream == ControlStream {
				return
			}
			stream = frame.Stream
			opened, _ := jsonv2.Marshal(wire.CommandAccepted{
				InvocationID: "inv-1",
				StreamID:     stream,
				StdinCredit:  64,
			})
			f.writeKind(nc, KindAccepted, stream, opened)
		case KindStdin:
			f.mu.Lock()
			f.stdin = append(f.stdin, frame.Payload...)
			seen := string(f.stdin)
			naked := f.naked
			done := f.exited
			f.mu.Unlock()
			f.grant(nc, stream, uint64(len(frame.Payload)))
			if !naked && strings.Contains(seen, "0000") {
				f.mu.Lock()
				f.naked = true
				f.mu.Unlock()
				f.writeKind(nc, KindStdout, stream, []byte("NAK\n"))
			}
			if !done && strings.Contains(seen, "done") {
				f.mu.Lock()
				f.exited = true
				f.mu.Unlock()
				f.writeKind(nc, KindStdout, stream, []byte("PACK"))
				report, _ := jsonv2.Marshal(wire.CommandExitReport{
					InvocationID: "inv-1",
				})
				f.writeKind(nc, KindExit, stream, report)
			}
		case KindStdinEOF:
			return
		case KindWindowUpdate, KindCancel:
		default:
			return
		}
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, what string, have func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for !have() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunCommandStreamMultiplexesRounds(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "fake.sock")
	addr, err := net.ResolveUnixAddr("unix", sock)
	if err != nil {
		t.Fatalf("resolve fake socket: %v", err)
	}
	ln, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatalf("listen fake socket: %v", err)
	}
	defer func() { _ = ln.Close() }()
	fake := &scriptFake{t: t}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go fake.serveConn(nc)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	client, err := Dial(ctx, sock)
	if err != nil {
		t.Fatalf("dial fake: %v", err)
	}
	defer func() { _ = client.Close() }()
	stdinR, stdinW := io.Pipe()
	var stdout syncBuffer
	type outcome struct {
		report wire.CommandExitReport
		stderr []byte
		err    error
	}
	outcomeDone := make(chan outcome, 1)
	go func() {
		report, stderr, err := RunCommandStream(ctx, client, StreamCommand{
			Open: wire.ServiceOpen{
				Service:      "upload-pack",
				Repository:   "/repo.git/.ruk",
				StdoutCredit: 1 << 20,
				Limits:       wire.DefaultGitLimits(),
			},
			Stdin:  stdinR,
			Stdout: &stdout,
		})
		outcomeDone <- outcome{report: report, stderr: stderr, err: err}
	}()
	if _, err := io.WriteString(stdinW, "want abc\n0000"); err != nil {
		t.Fatalf("write turn one: %v", err)
	}
	waitFor(t, "NAK before stdin EOF", func() bool {
		return strings.Contains(stdout.String(), "NAK\n")
	})
	if _, err := io.WriteString(stdinW, "have def\n0000done\n0000"); err != nil {
		t.Fatalf("write turn two: %v", err)
	}
	waitFor(t, "PACK before stdin EOF", func() bool {
		return strings.Contains(stdout.String(), "PACK")
	})
	if err := stdinW.Close(); err != nil {
		t.Fatalf("close stdin: %v", err)
	}
	select {
	case out := <-outcomeDone:
		if out.err != nil {
			t.Fatalf("stream command: %v (stderr %q)", out.err, out.stderr)
		}
		if out.report.ExitCode != 0 || out.report.Cancelled {
			t.Fatalf("stream report = %+v, want clean exit", out.report)
		}
	case <-ctx.Done():
		t.Fatalf("stream command timed out: %v", ctx.Err())
	}
	fake.mu.Lock()
	got := string(fake.stdin)
	fake.mu.Unlock()
	if !strings.Contains(got, "want abc") || !strings.Contains(got, "have def") || !strings.Contains(got, "done") {
		t.Fatalf("fake received stdin = %q, want both turns in order", got)
	}
}
