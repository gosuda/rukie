package githttp

import (
	"context"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

func TestRecordPushAttemptStashesWant(t *testing.T) {
	s := &Server{clock: time.Now}
	call := wire.HookCall{
		Phase: wire.HookPreReceive,
		Commands: []wire.HookCommand{
			{Name: "refs/heads/main", Old: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", New: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			{Name: "refs/heads/dev", Old: "cccccccccccccccccccccccccccccccccccccccc", New: "dddddddddddddddddddddddddddddddddddddddd"},
		},
	}
	verdicts := []wire.HookRefVerdict{
		{Name: "refs/heads/main", Verdict: wire.AcceptVerdict()},
		{Name: "refs/heads/dev", Verdict: wire.RejectVerdict("denied")},
	}
	s.recordPushAttempt(context.Background(), access.Caller{}, 7, "push-want-1", call, verdicts)
	want := s.takeWant("push-want-1")
	if len(want) != 1 || want["refs/heads/main"] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("want = %v, want only the accepted main target", want)
	}
	if again := s.takeWant("push-want-1"); again != nil {
		t.Fatalf("second take = %v, want nil: reconcile consumes the targets once", again)
	}
}
