package ruk

import (
	"errors"
	"fmt"
	"strings"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

var (
	// ErrClosed reports a call on a closed client or connection.
	ErrClosed = errors.New("ruk: client is closed")
	// ErrNoDaemon reports an unreachable daemon socket.
	ErrNoDaemon = errors.New("ruk: daemon socket is unreachable")
	// ErrHandshake reports a rejected handshake.
	ErrHandshake = errors.New("ruk: handshake failed")
	// ErrProtocol reports a wire violation by either peer.
	ErrProtocol = errors.New("ruk: protocol violation")
	// ErrVersionMismatch reports a protocol major mismatch.
	ErrVersionMismatch = errors.New("ruk: protocol version mismatch")
	// ErrInvalidPath reports a relative repository path.
	ErrInvalidPath = errors.New("ruk: repository path must be absolute")
	// ErrOutcomeUnknown reports a call lost before its terminal reply.
	ErrOutcomeUnknown = errors.New("ruk: outcome unknown")
)

// Error carries one typed daemon failure.
type Error struct {
	Code       wire.Code
	Op         string
	Message    string
	Invocation string
	Hints      []string
	Fixes      []string
	Detail     jsonv1.RawMessage
	Outcome    Outcome
}

func (e *Error) Error() string {
	var parts []string
	if e.Op != "" {
		parts = append(parts, e.Op+":")
	}
	parts = append(parts, "ruk "+string(e.Code))
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, " ")
}

// Is matches sentinels and errors by code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return t.Code == e.Code
}

func errorFromReply(op string, reply wire.ErrorReply, invocation string, outcome Outcome) *Error {
	return &Error{
		Code:       reply.Code,
		Op:         op,
		Message:    reply.Message,
		Hints:      reply.Hints,
		Fixes:      reply.Fixes,
		Detail:     reply.Detail,
		Invocation: invocation,
		Outcome:    outcome,
	}
}

func decodeErrorReply(payload []byte) (wire.ErrorReply, error) {
	var reply wire.ErrorReply
	if err := jsonv2.Unmarshal(payload, &reply, jsonv1.DefaultOptionsV1()); err != nil {
		return wire.ErrorReply{}, fmt.Errorf("decodeErrorReply: %w: %w", ErrProtocol, err)
	}
	if reply.Code == "" {
		return wire.ErrorReply{}, fmt.Errorf("decodeErrorReply: %w: missing code", ErrProtocol)
	}
	if sentinelFor(reply.Code) == nil {
		return wire.ErrorReply{}, fmt.Errorf("decodeErrorReply: %w: unknown code %q", ErrProtocol, string(reply.Code))
	}
	return reply, nil
}

// IsExitCode reports whether err carries the model exit status, not the
// wire status byte: a cancelled call exits 130 on the wire while its code
// still maps to 1.
func IsExitCode(err error, code int) bool {
	rerr, ok := errors.AsType[*Error](err)
	if !ok {
		return false
	}
	return rerr.Code.ExitStatus() == code
}
