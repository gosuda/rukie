package githttp

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

func TestCountReaderExactLimitLands(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 64)
	r := &countReader{reader: bytes.NewReader(body), limit: 65}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("exact-limit read: %v", err)
	}
	if len(got) != 64 {
		t.Fatalf("exact-limit read = %d bytes, want 64", len(got))
	}
	if r.overflow {
		t.Fatal("exact-limit read set overflow")
	}
}

func TestCountReaderOverflowBeyondLimit(t *testing.T) {
	body := bytes.Repeat([]byte("a"), 65)
	r := &countReader{reader: bytes.NewReader(body), limit: 65}
	if _, err := io.ReadAll(r); !errors.Is(err, errTooLarge) {
		t.Fatalf("over-limit read err = %v, want overflow", err)
	}
	if !r.overflow {
		t.Fatal("over-limit read did not latch overflow")
	}
	if _, err := r.Read(make([]byte, 8)); !errors.Is(err, errTooLarge) {
		t.Fatalf("latched read err = %v, want overflow", err)
	}
}

func TestJudgeUnauthorizedCommandRejected(t *testing.T) {
	s := &Server{}
	v := s.judge(access.Caller{Active: true}, 7, wire.HookPreReceive, wire.HookCommand{
		Name:       "refs/heads/main",
		Authorized: false,
	})
	if v.Kind != wire.HookReject {
		t.Fatalf("unauthorized verdict = %q, want reject", v.Kind)
	}
	if !bytes.Contains([]byte(v.Reason), []byte("not authorized")) {
		t.Fatalf("unauthorized reason = %q, want the session hint", v.Reason)
	}
}
