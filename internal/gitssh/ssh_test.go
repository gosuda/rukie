package gitssh

import (
	"errors"
	"io"
	"testing"
	"time"
)

var errChunkBoom = errors.New("boom")

func TestParseGitCommandTable(t *testing.T) {
	valid := map[string]gitCommand{
		"git-upload-pack '/ada/hello.git'":       {service: "upload-pack", owner: "ada", repo: "hello"},
		"git-receive-pack '/ada/hello.git'":      {service: "receive-pack", owner: "ada", repo: "hello", receive: true},
		`git-upload-pack "/ada/hello.git"`:       {service: "upload-pack", owner: "ada", repo: "hello"},
		"git-upload-pack 'ada/hello.git'":        {service: "upload-pack", owner: "ada", repo: "hello"},
		"git-upload-pack ada/hello.git":          {service: "upload-pack", owner: "ada", repo: "hello"},
		"git-upload-pack '/~/ada/hello.git'":     {service: "upload-pack", owner: "ada", repo: "hello"},
		"git-upload-pack '/a-d_0.9/x.y-z_9.git'": {service: "upload-pack", owner: "a-d_0.9", repo: "x.y-z_9"},
	}
	for raw, want := range valid {
		got, err := parseGitCommand(raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got != want {
			t.Fatalf("%q = %+v, want %+v", raw, got, want)
		}
	}
	invalid := []string{
		"",
		"git-upload-pack",
		"git-upload-archive '/ada/hello.git'",
		"git-upload-pack '/ada/hello.git' extra",
		"git-upload-pack ''",
		"git-upload-pack 'ada/hello.git",
		"git-upload-pack '/ada/hello'",
		"git-upload-pack '/Ada/hello.git'",
		"git-upload-pack '/admin/hello.git'",
		"git-upload-pack '/../../etc/passwd.git'",
		"git-upload-pack '/ada/hello.git'; id",
		"git-upload-pack 'a b.git'",
		"git-receive-pack '/ada/.git'",
		"shell",
	}
	for _, raw := range invalid {
		if _, err := parseGitCommand(raw); err == nil {
			t.Fatalf("%q accepted", raw)
		}
	}
}

func TestBufferUploadRequestTable(t *testing.T) {
	full := []byte("00a4want 0123456789abcdef0123456789abcdef01234567 multi_ack ofs-delta\n0000")
	done := []byte("0009done\n")
	if out, err := bufferUploadRequest(singleRead(full, done), 1<<20); err != nil || string(out) != string(full)+string(done) {
		t.Fatalf("split marker = %q, %v", out, err)
	}
	if out, err := bufferUploadRequest(singleRead(full), 1<<20); err != nil || string(out) != string(full) {
		t.Fatalf("unsplit marker = %q, %v", out, err)
	}
	if out, err := bufferUploadRequest(emptyRead(), 1<<20); err != nil || len(out) != 0 {
		t.Fatalf("empty = %q, %v", out, err)
	}
	if _, err := bufferUploadRequest(singleRead(full, done), uint64(len(full)+len(done)-1)); err == nil {
		t.Fatal("over-cap accepted")
	}
	if _, err := bufferUploadRequest(singleRead(full, done), uint64(len(full)+len(done))); err != nil {
		t.Fatalf("exact cap rejected: %v", err)
	}
	if _, err := bufferUploadRequest(failRead(), 1<<20); err == nil {
		t.Fatal("read error swallowed")
	}
}

func emptyRead() *chunkReader {
	return &chunkReader{chunks: [][]byte{}}
}

func singleRead(parts ...[]byte) *chunkReader {
	return &chunkReader{chunks: parts}
}

func failRead() *chunkReader {
	return &chunkReader{fail: true}
}

type chunkReader struct {
	chunks [][]byte
	fail   bool
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.fail {
		return 0, errChunkBoom
	}
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.chunks[0])
	r.chunks = r.chunks[1:]
	return n, nil
}

func TestTryAcquireBoundary(t *testing.T) {
	s := &Server{maxSess: 1}
	if !s.tryAcquire() {
		t.Fatal("first acquire refused")
	}
	if s.tryAcquire() {
		t.Fatal("second acquire past the cap allowed")
	}
	s.releaseSession()
	if !s.tryAcquire() {
		t.Fatal("reacquire after release refused")
	}
	s.releaseSession()
	s.releaseSession()
	if !s.tryAcquire() {
		t.Fatal("release below zero broke the counter")
	}
}

func TestIdleTickBounds(t *testing.T) {
	if got := (&Server{idleAfter: 10 * time.Minute}).idleTick(); got != 30*time.Second {
		t.Fatalf("10m idle tick = %v", got)
	}
	if got := (&Server{idleAfter: 2 * time.Second}).idleTick(); got != 500*time.Millisecond {
		t.Fatalf("2s idle tick = %v", got)
	}
	if got := (&Server{idleAfter: 200 * time.Millisecond}).idleTick(); got != 100*time.Millisecond {
		t.Fatalf("200ms idle tick = %v", got)
	}
}
