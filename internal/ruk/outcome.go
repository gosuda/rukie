package ruk

import (
	"github.com/gosuda/rukie/internal/ruk/wire"
)

// Outcome names what the daemon did with one call.
type Outcome uint8

const (
	// Unknown marks a call the daemon may have run.
	Unknown Outcome = iota
	// NotCommitted marks a call the daemon never dispatched.
	NotCommitted
	// Committed marks a call the daemon finished with success.
	Committed
)

func (o Outcome) String() string {
	switch o {
	case NotCommitted:
		return "not-committed"
	case Committed:
		return "committed"
	case Unknown:
		return "unknown"
	default:
		return "unknown"
	}
}

var preDispatchCodes = map[wire.Code]bool{
	wire.CodeInvalid:               true,
	wire.CodeNotFound:              true,
	wire.CodeForbidden:             true,
	wire.CodeVersion:               true,
	wire.CodeLimit:                 true,
	wire.CodeRequestReused:         true,
	wire.CodeHosted:                true,
	wire.CodeObjectFormat:          true,
	wire.CodeUnsupportedLfs:        true,
	wire.CodeUnsupportedGitCommand: true,
}

type typedFacts struct {
	Refusal   wire.Code
	Attempted bool
	Accepted  bool
	Terminal  bool
	Success   bool
}

func outcomeTyped(f typedFacts) Outcome {
	if !f.Attempted {
		return NotCommitted
	}
	if f.Terminal && f.Success {
		return Committed
	}
	if f.Terminal && !f.Accepted {
		return NotCommitted
	}
	if f.Terminal && preDispatchCodes[f.Refusal] {
		return NotCommitted
	}
	return Unknown
}
