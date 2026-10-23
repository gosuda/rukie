package ruk

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

func TestErrorSentinelsCoverAllCodes(t *testing.T) {
	for _, code := range wire.AllCodes() {
		sentinel := sentinelFor(code)
		if sentinel == nil {
			t.Fatalf("no sentinel for %s", string(code))
		}
		wrapped := fmt.Errorf("attemptCall: %w", &Error{Code: code, Op: "Call"})
		if !errors.Is(wrapped, sentinel) {
			t.Fatalf("errors.Is fails for %s", string(code))
		}
		other := sentinelFor(wire.CodeStale)
		if code != wire.CodeStale && errors.Is(wrapped, other) {
			t.Fatalf("code %s matches stale sentinel", string(code))
		}
	}
}

func TestErrorUnknownCodeIsProtocol(t *testing.T) {
	payload := []byte(`{"code":"E_BOGUS","message":"nope"}`)
	if _, err := decodeErrorReply(payload); !errors.Is(err, ErrProtocol) {
		t.Fatalf("unknown code err = %v", err)
	}
	legacy := []byte(`{"code":"E_NOT_FOUND","message":"gone"}`)
	reply, err := decodeErrorReply(legacy)
	if err != nil {
		t.Fatalf("legacy shape rejected: %v", err)
	}
	if reply.Code != wire.CodeNotFound {
		t.Fatalf("legacy code = %s", string(reply.Code))
	}
	full := []byte(`{"code":"E_FORBIDDEN","message":"denied","hints":["h"],"fixes":["f"],"detail":{"x":1}}`)
	reply, err = decodeErrorReply(full)
	if err != nil {
		t.Fatalf("full shape rejected: %v", err)
	}
	if len(reply.Hints) != 1 || len(reply.Fixes) != 1 || len(reply.Detail) == 0 {
		t.Fatalf("full shape lost fields: %+v", reply)
	}
	missing := []byte(`{"message":"no code"}`)
	if _, err := decodeErrorReply(missing); !errors.Is(err, ErrProtocol) {
		t.Fatalf("missing code err = %v", err)
	}
}

func TestErrorExitStatusTable(t *testing.T) {
	cases := map[wire.Code]int{
		wire.CodeNotFound:      1,
		wire.CodeInvalid:       1,
		wire.CodeLimit:         1,
		wire.CodeVersion:       1,
		wire.CodeCancelled:     1,
		wire.CodeHosted:        1,
		wire.CodeConflicted:    2,
		wire.CodeDirty:         2,
		wire.CodeStale:         3,
		wire.CodeLeaseLost:     3,
		wire.CodeClaimed:       3,
		wire.CodeForbidden:     3,
		wire.CodePolicy:        3,
		wire.CodeRequestReused: 3,
		wire.CodeRemoteMoved:   4,
		wire.CodePushRejected:  4,
		wire.CodePushUncertain: 4,
		wire.CodeOffline:       4,
		wire.CodeShallow:       4,
		wire.CodeCorrupt:       5,
	}
	for code, want := range cases {
		if got := code.ExitStatus(); got != want {
			t.Fatalf("%s exit = %d, want %d", string(code), got, want)
		}
	}
	for _, code := range wire.AllCodes() {
		sentinel := sentinelFor(code)
		if !IsExitCode(fmt.Errorf("x: %w", sentinel), code.ExitStatus()) {
			t.Fatalf("IsExitCode fails for %s", string(code))
		}
	}
}
