package ruk

import (
	"sync"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

const typedOutputCap = 16 << 20

type streamState uint8

const (
	streamOpening streamState = iota + 1
	streamAccepted
	streamTerminal
	streamOrphan
)

type stream struct {
	signal     chan struct{}
	done       chan struct{}
	refusal    wire.ErrorReply
	accepted   wire.InvocationAccepted
	output     []byte
	facts      typedFacts
	exit       wire.ExitReport
	id         uint64
	granted    uint64
	received   uint64
	mu         sync.Mutex
	state      streamState
	hasRef     bool
	hasExit    bool
	doneClosed bool
}

func newStream(id uint64) *stream {
	return &stream{
		id:     id,
		state:  streamOpening,
		signal: make(chan struct{}, 1),
		done:   make(chan struct{}),
	}
}

func (s *stream) wake() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}
