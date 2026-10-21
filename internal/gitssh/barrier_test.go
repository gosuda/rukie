package gitssh

import (
	"bytes"
	"io"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

type fakeChannel struct {
	mu       sync.Mutex
	stderr   bytes.Buffer
	requests []fakeRequest
	closed   bool
}

type fakeRequest struct {
	name    string
	payload []byte
}

func (f *fakeChannel) Read([]byte) (int, error)    { return 0, io.EOF }
func (f *fakeChannel) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeChannel) Close() error                { f.closed = true; return nil }
func (f *fakeChannel) CloseWrite() error           { return nil }
func (f *fakeChannel) Stderr() io.ReadWriter       { return &f.stderr }
func (f *fakeChannel) SendRequest(name string, _ bool, payload []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, fakeRequest{name: name, payload: payload})
	return false, nil
}

func (f *fakeChannel) exitStatus() (uint32, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.requests {
		if r.name != "exit-status" {
			continue
		}
		var msg struct {
			Status uint32
		}
		if err := ssh.Unmarshal(r.payload, &msg); err != nil {
			return 0, false
		}
		return msg.Status, true
	}
	return 0, false
}

func execPayload(command string) []byte {
	return ssh.Marshal(struct {
		Command string
	}{Command: command})
}

func TestServeExecGuardedRefusesReceiveDuringBackup(t *testing.T) {
	acquired := 0
	released := 0
	srv := &Server{barrier: func() (func(), bool) {
		acquired++
		return func() { released++ }, false
	}}
	fake := &fakeChannel{}
	srv.serveExecGuarded(t.Context(), nil, fake, execPayload("git-receive-pack '/ada/hello.git'"), &sessionLive{})
	if acquired != 1 {
		t.Fatalf("acquired = %d", acquired)
	}
	if released != 0 {
		t.Fatalf("released = %d", released)
	}
	status, ok := fake.exitStatus()
	if !ok || status != 128 {
		t.Fatalf("exit = %d, %v", status, ok)
	}
}

func TestServeExecGuardedSkipsBarrierForInvalidCommand(t *testing.T) {
	acquired := 0
	srv := &Server{barrier: func() (func(), bool) {
		acquired++
		return func() {}, true
	}}
	fake := &fakeChannel{}
	srv.serveExecGuarded(t.Context(), nil, fake, execPayload("bogus"), &sessionLive{})
	if acquired != 0 {
		t.Fatalf("acquired = %d", acquired)
	}
	status, ok := fake.exitStatus()
	if !ok || status != 1 {
		t.Fatalf("exit = %d, %v", status, ok)
	}
}

func TestParseExecCommand(t *testing.T) {
	cmd, err := parseExecCommand(execPayload("git-receive-pack '/ada/hello.git'"))
	if err != nil || !cmd.receive || cmd.owner != "ada" || cmd.repo != "hello" {
		t.Fatalf("receive = %+v, %v", cmd, err)
	}
	cmd, err = parseExecCommand(execPayload("git-upload-pack '/ada/hello.git'"))
	if err != nil || cmd.receive {
		t.Fatalf("upload = %+v, %v", cmd, err)
	}
	if _, err := parseExecCommand(execPayload("bogus")); err == nil {
		t.Fatalf("invalid command parsed")
	}
}
