package ruk

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

var supportedFeatures = []string{"credits", "hooks.v1", "events.v1", "archive.v1"}

type conn struct {
	hooks      Hooks
	nc         net.Conn
	streams    map[uint64]*stream
	readerDone chan struct{}
	firstErr   error
	id         uint64
	nextID     uint64
	writeEvery time.Duration
	live       int
	maxStreams int
	wmu        sync.Mutex
	mu         sync.Mutex
	broken     bool
}

func dialConn(ctx context.Context, id uint64, socketPath, profile string, timeouts Timeouts, hooks Hooks) (*conn, error) {
	c, err := dialRawConn(ctx, id, socketPath, profile, timeouts, hooks)
	if err != nil {
		return nil, err
	}
	go c.readLoop()
	return c, nil
}

func dialRawConn(ctx context.Context, id uint64, socketPath, profile string, timeouts Timeouts, hooks Hooks) (*conn, error) {
	dialer := net.Dialer{Timeout: timeouts.Dial}
	nc, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("dialRawConn: %w: %w", ErrNoDaemon, err)
	}
	c := &conn{
		id:         id,
		nc:         nc,
		streams:    make(map[uint64]*stream),
		nextID:     1,
		maxStreams: 8,
		writeEvery: timeouts.Write,
		readerDone: make(chan struct{}),
		hooks:      hooks,
	}
	if err := c.handshake(ctx, profile, timeouts.Handshake); err != nil {
		_ = nc.Close()
		return nil, err
	}
	return c, nil
}

func (c *conn) handshake(ctx context.Context, profile string, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := c.nc.SetDeadline(deadline); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	hello := wire.HandshakeHello{
		ProtocolMajor: 1,
		ProtocolMinor: 1,
		Profile:       profile,
		ObjectFormats: []string{"sha1"},
		Features:      supportedFeatures,
	}
	raw, marshalErr := jsonv2.Marshal(hello)
	if marshalErr != nil {
		return fmt.Errorf("handshake: %w", marshalErr)
	}
	if err := writeFrame(c.nc, KindOpen, ControlStream, raw); err != nil {
		return fmt.Errorf("handshake: %w: %w", ErrHandshake, err)
	}
	frame, err := readFrame(c.nc)
	if err != nil {
		return fmt.Errorf("handshake: %w: %w", ErrHandshake, err)
	}
	if err := c.nc.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	if frame.Kind != KindAccepted || frame.Stream != ControlStream {
		return fmt.Errorf("handshake: %w: kind %d stream %d", ErrHandshake, uint8(frame.Kind), frame.Stream)
	}
	var accept wire.HandshakeAccept
	if err := jsonv2.Unmarshal(frame.Payload, &accept, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("handshake: %w: %w", ErrHandshake, err)
	}
	if accept.ProtocolMajor != 1 {
		return fmt.Errorf("handshake: %w: major %d", ErrVersionMismatch, accept.ProtocolMajor)
	}
	credited := false
	for _, f := range accept.Features {
		if f == "credits" {
			credited = true
		}
	}
	if !credited {
		return fmt.Errorf("handshake: %w: missing credits feature", ErrHandshake)
	}
	return nil
}

func (c *conn) reserve() (*stream, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return nil, false
	}
	if c.live >= c.maxStreams {
		return nil, false
	}
	s := newStream(c.nextID)
	c.nextID++
	c.streams[s.id] = s
	c.live++
	return s, true
}

func (c *conn) release(s *stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.streams[s.id]; ok {
		delete(c.streams, s.id)
		c.live--
	}
}

func (c *conn) remove(s *stream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.streams[s.id]; ok {
		delete(c.streams, s.id)
		c.live--
	}
}

func (c *conn) load() (int, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live, c.broken
}

func (c *conn) writeKind(kind Kind, stream uint64, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.nc.SetWriteDeadline(time.Now().Add(c.writeEvery)); err != nil {
		return fmt.Errorf("writeKind: %w", err)
	}
	err := writeFrame(c.nc, kind, stream, payload)
	_ = c.nc.SetWriteDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("writeKind: %w", err)
	}
	return nil
}

func (c *conn) fail(err error) {
	c.mu.Lock()
	if c.broken {
		c.mu.Unlock()
		return
	}
	c.broken = true
	c.firstErr = err
	live := make([]*stream, 0, len(c.streams))
	for _, s := range c.streams {
		live = append(live, s)
	}
	c.mu.Unlock()
	_ = c.nc.Close()
	for _, s := range live {
		s.mu.Lock()
		if !s.doneClosed {
			s.doneClosed = true
			close(s.done)
		}
		s.mu.Unlock()
	}
}

func (c *conn) cause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.firstErr
}

func (c *conn) lookup(id uint64) (*stream, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.streams[id]
	return s, ok
}

func (c *conn) readLoop() {
	defer close(c.readerDone)
	for {
		frame, err := readFrame(c.nc)
		if err != nil {
			c.fail(fmt.Errorf("read: %w", err))
			return
		}
		if frame.Stream == ControlStream {
			c.fail(fmt.Errorf("read: %w: frame on stream 0", ErrProtocol))
			return
		}
		switch frame.Kind {
		case KindOpen, KindStdin, KindStdinEOF, KindCancel:
			c.fail(fmt.Errorf("read: %w: kind %d from daemon", ErrProtocol, uint8(frame.Kind)))
			return
		case KindAccepted, KindStdout, KindStderr, KindWindowUpdate,
			KindExit, KindError, KindHookCall, KindHookReply:
		default:
			c.fail(fmt.Errorf("read: %w: kind %d from daemon", ErrProtocol, uint8(frame.Kind)))
			return
		}
		s, ok := c.lookup(frame.Stream)
		if !ok {
			c.onStray(frame)
			continue
		}
		if !c.dispatch(s, frame) {
			return
		}
	}
}

func (c *conn) onStray(frame Frame) {
	switch frame.Kind {
	case KindExit, KindAccepted, KindStdout, KindStderr:
		return
	case KindError, KindWindowUpdate, KindHookCall, KindHookReply,
		KindOpen, KindStdin, KindStdinEOF, KindCancel:
		break
	default:
		break
	}
	err := fmt.Errorf("read: %w: stray kind %d stream %d", ErrProtocol, uint8(frame.Kind), frame.Stream)
	if c.hooks.OnProtocolViolation != nil {
		c.hooks.OnProtocolViolation(err)
	}
}

func (c *conn) dispatch(s *stream, frame Frame) bool {
	switch frame.Kind {
	case KindAccepted:
		return c.onAccepted(s, frame.Payload)
	case KindStdout, KindStderr:
		return c.onOutput(s, frame.Payload)
	case KindExit:
		return c.onExit(s, frame.Payload)
	case KindError:
		return c.onRefusal(s, frame.Payload)
	case KindWindowUpdate, KindHookCall, KindHookReply:
		c.onStray(frame)
		return true
	case KindOpen, KindStdin, KindStdinEOF, KindCancel:
		c.fail(fmt.Errorf("read: %w: kind %d from daemon", ErrProtocol, uint8(frame.Kind)))
		return false
	default:
		c.fail(fmt.Errorf("read: %w: kind %d from daemon", ErrProtocol, uint8(frame.Kind)))
		return false
	}
}

func (c *conn) onAccepted(s *stream, payload []byte) bool {
	var accepted wire.InvocationAccepted
	if err := jsonv2.Unmarshal(payload, &accepted, jsonv1.DefaultOptionsV1()); err != nil {
		c.fail(fmt.Errorf("read: %w: bad accepted: %w", ErrProtocol, err))
		return false
	}
	if accepted.StreamID != s.id {
		c.fail(fmt.Errorf("read: %w: accepted stream mismatch", ErrProtocol))
		return false
	}
	s.mu.Lock()
	if s.state == streamOpening {
		s.state = streamAccepted
		s.facts.Accepted = true
		s.accepted = accepted
	}
	orphan := s.state == streamOrphan
	s.mu.Unlock()
	if !orphan {
		s.wake()
	}
	return true
}

func (c *conn) onOutput(s *stream, payload []byte) bool {
	s.mu.Lock()
	if s.state == streamOrphan {
		s.mu.Unlock()
		return true
	}
	if s.state == streamTerminal {
		s.mu.Unlock()
		c.fail(fmt.Errorf("read: %w: output after exit", ErrProtocol))
		return false
	}
	if uint64(len(s.output)+len(payload)) > typedOutputCap {
		s.mu.Unlock()
		c.fail(fmt.Errorf("read: %w: output over cap", ErrProtocol))
		return false
	}
	s.output = append(s.output, payload...)
	s.received += uint64(len(payload))
	s.mu.Unlock()
	s.wake()
	return true
}

func (c *conn) onRefusal(s *stream, payload []byte) bool {
	reply, err := decodeErrorReply(payload)
	if err != nil {
		c.fail(err)
		return false
	}
	s.mu.Lock()
	if s.state != streamTerminal && s.state != streamOrphan {
		s.refusal = reply
		s.hasRef = true
		s.facts.Refusal = reply.Code
	}
	s.mu.Unlock()
	s.wake()
	return true
}

func (c *conn) onExit(s *stream, payload []byte) bool {
	var exit wire.ExitReport
	if err := jsonv2.Unmarshal(payload, &exit, jsonv1.DefaultOptionsV1()); err != nil {
		c.fail(fmt.Errorf("read: %w: bad exit: %w", ErrProtocol, err))
		return false
	}
	s.mu.Lock()
	orphan := s.state == streamOrphan
	if !orphan {
		s.state = streamTerminal
		s.facts.Terminal = true
		s.facts.Success = exit.Status == 0
		if exit.Status != 0 && !s.hasRef {
			s.facts.Refusal = wire.Code(exit.Code)
		}
		s.exit = exit
		s.hasExit = true
		if !s.doneClosed {
			s.doneClosed = true
			close(s.done)
		}
	}
	s.mu.Unlock()
	c.remove(s)
	if !orphan {
		s.wake()
	}
	return true
}
