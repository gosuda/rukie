package ruk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

type StreamCommand struct {
	Stdin  io.Reader
	Stdout io.Writer
	Open   wire.ServiceOpen
}

func RunCommandStream(ctx context.Context, c *Client, cmd StreamCommand) (wire.CommandExitReport, []byte, error) {
	var zero wire.CommandExitReport
	open := cmd.Open
	if open.Service == "" {
		return zero, nil, fmt.Errorf("RunCommandStream: %w: empty service", ErrProtocol)
	}
	if open.Repository == "" {
		return zero, nil, fmt.Errorf("RunCommandStream: %w: empty repository", ErrProtocol)
	}
	if cmd.Stdout == nil {
		return zero, nil, fmt.Errorf("RunCommandStream: %w: nil stdout", ErrProtocol)
	}
	if cmd.Stdin == nil {
		return zero, nil, fmt.Errorf("RunCommandStream: %w: nil stdin", ErrProtocol)
	}
	rawOpen, err := jsonv2.Marshal(open)
	if err != nil {
		return zero, nil, fmt.Errorf("RunCommandStream: %w", err)
	}
	pc, err := dialRawConn(ctx, c.pool.nextID(), c.socket, c.profile, c.timeouts, c.hooks)
	if err != nil {
		return zero, nil, err
	}
	closed := make(chan struct{})
	defer func() {
		close(closed)
		_ = pc.nc.Close()
	}()
	s, ok := pc.reserve()
	if !ok {
		return zero, nil, fmt.Errorf("RunCommandStream: %w: connection broken", ErrClosed)
	}
	defer pc.release(s)
	cmdCtx, stop := context.WithCancel(ctx)
	defer stop()
	d := &streamDriver{
		pc:      pc,
		id:      s.id,
		stdout:  cmd.Stdout,
		service: open.Service,
		signal:  make(chan struct{}, 1),
	}
	go func() {
		select {
		case <-cmdCtx.Done():
			if !d.terminal.Load() {
				d.sendCancel()
			}
			_ = pc.nc.Close()
		case <-closed:
		}
	}()
	if openErr := pc.writeKind(KindOpen, s.id, rawOpen); openErr != nil {
		return zero, nil, fmt.Errorf("RunCommandStream: %w: %w", openErr, ErrOutcomeUnknown)
	}
	if err := d.awaitAccepted(cmdCtx); err != nil {
		return zero, nil, err
	}
	if d.credit() == 0 {
		return zero, nil, fmt.Errorf("RunCommandStream: %w: zero stdin credit with a request body", ErrProtocol)
	}
	pumpDone := make(chan error, 1)
	go d.pump(cmdCtx, cmd.Stdin, pumpDone)
	return d.drain(cmdCtx, pumpDone)
}

type streamDriver struct {
	pc       *conn
	stdout   io.Writer
	signal   chan struct{}
	service  string
	invoking string
	stderr   []byte
	refusal  wire.ErrorReply
	window   uint64
	mu       sync.Mutex
	id       uint64
	stdinEOF atomic.Bool
	terminal atomic.Bool
	cancel   sync.Once
	hasRef   bool
}

func (d *streamDriver) sendCancel() {
	d.cancel.Do(func() {
		_ = d.pc.writeKind(KindCancel, d.id, nil)
	})
}

func (d *streamDriver) kick() {
	select {
	case d.signal <- struct{}{}:
	default:
	}
}

func (d *streamDriver) credit() uint64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.window
}

func (d *streamDriver) awaitWindow(ctx context.Context) bool {
	for {
		d.mu.Lock()
		ready := d.window > 0 || d.terminal.Load()
		d.mu.Unlock()
		if ready {
			return !d.terminal.Load() && ctx.Err() == nil
		}
		select {
		case <-d.signal:
		case <-ctx.Done():
			return false
		}
	}
}

func (d *streamDriver) awaitAccepted(ctx context.Context) error {
	frame, err := d.readAbortable(ctx)
	if err != nil {
		return err
	}
	if frame.Stream != d.id {
		return fmt.Errorf("RunCommandStream: %w: frame on stream %d", ErrProtocol, frame.Stream)
	}
	if frame.Kind == KindError {
		d.recordRefusal(frame.Payload)
		return d.awaitRefusedExit()
	}
	if frame.Kind != KindAccepted {
		return fmt.Errorf("RunCommandStream: %w: expected accepted, got kind %d stream %d", ErrProtocol, uint8(frame.Kind), frame.Stream)
	}
	var accepted wire.CommandAccepted
	if err := jsonv2.Unmarshal(frame.Payload, &accepted, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("RunCommandStream: %w: bad accepted: %w", ErrProtocol, err)
	}
	if accepted.StreamID != d.id {
		return fmt.Errorf("RunCommandStream: %w: accepted stream mismatch", ErrProtocol)
	}
	d.mu.Lock()
	d.window = accepted.StdinCredit
	d.mu.Unlock()
	d.invoking = accepted.InvocationID
	return nil
}

func (d *streamDriver) readAbortable(ctx context.Context) (Frame, error) {
	type result struct {
		err   error
		frame Frame
	}
	box := make(chan result, 1)
	go func() {
		frame, err := readFrame(d.pc.nc)
		box <- result{frame: frame, err: err}
	}()
	select {
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case out := <-box:
		return out.frame, out.err
	}
}

func (d *streamDriver) recordRefusal(payload []byte) {
	reply, err := decodeErrorReply(payload)
	if err != nil {
		return
	}
	d.refusal = reply
	d.hasRef = true
}

func (d *streamDriver) pump(ctx context.Context, stdin io.Reader, done chan<- error) {
	buf := make([]byte, commandStdinChunk)
	for {
		if !d.awaitWindow(ctx) {
			done <- ctx.Err()
			return
		}
		n, readErr := stdin.Read(buf)
		if n > 0 && !d.writeAll(ctx, buf[:n], done) {
			return
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				d.stdinEOF.Store(true)
				if err := d.pc.writeKind(KindStdinEOF, d.id, nil); err != nil {
					done <- fmt.Errorf("RunCommandStream: %w: %w", err, ErrOutcomeUnknown)
					return
				}
				done <- nil
				return
			}
			d.sendCancel()
			done <- fmt.Errorf("RunCommandStream: stdin: %w", readErr)
			return
		}
	}
}

func (d *streamDriver) writeAll(ctx context.Context, data []byte, done chan<- error) bool {
	rest := data
	for len(rest) > 0 {
		if !d.awaitWindow(ctx) {
			done <- ctx.Err()
			return false
		}
		d.mu.Lock()
		take := uint64(len(rest))
		if take > d.window {
			take = d.window
		}
		if take > commandStdinChunk {
			take = commandStdinChunk
		}
		chunk := rest[:take]
		rest = rest[take:]
		d.window -= take
		d.mu.Unlock()
		if err := d.pc.writeKind(KindStdin, d.id, chunk); err != nil {
			d.sendCancel()
			done <- fmt.Errorf("RunCommandStream: %w: %w", err, ErrOutcomeUnknown)
			return false
		}
	}
	return true
}

func (d *streamDriver) drain(ctx context.Context, pumpDone <-chan error) (wire.CommandExitReport, []byte, error) {
	for {
		if ctx.Err() != nil {
			d.sendCancel()
			return wire.CommandExitReport{}, d.stderr, fmt.Errorf("RunCommandStream: %w: %w", ctx.Err(), ErrOutcomeUnknown)
		}
		select {
		case err := <-pumpDone:
			if err != nil {
				d.sendCancel()
				return wire.CommandExitReport{}, d.stderr, err
			}
		default:
		}
		frame, err := d.readAbortable(ctx)
		if err != nil {
			if ctx.Err() != nil {
				d.sendCancel()
				return wire.CommandExitReport{}, d.stderr, fmt.Errorf("RunCommandStream: %w: %w", ctx.Err(), ErrOutcomeUnknown)
			}
			return wire.CommandExitReport{}, d.stderr, fmt.Errorf("RunCommandStream: %w: %w", err, ErrOutcomeUnknown)
		}
		if frame.Stream != d.id {
			return wire.CommandExitReport{}, d.stderr, fmt.Errorf("RunCommandStream: %w: frame on stream %d", ErrProtocol, frame.Stream)
		}
		switch frame.Kind {
		case KindStdout:
			if err := d.forwardStdout(frame.Payload); err != nil {
				return wire.CommandExitReport{}, d.stderr, err
			}
		case KindStderr:
			d.bufferStderr(frame.Payload)
		case KindWindowUpdate:
			if err := d.grantWindow(frame.Payload); err != nil {
				return wire.CommandExitReport{}, d.stderr, err
			}
		case KindError:
			d.recordRefusal(frame.Payload)
		case KindExit:
			return d.finishExit(frame.Payload, pumpDone)
		case KindHookCall:
			if err := d.rejectHook(frame.Payload); err != nil {
				return wire.CommandExitReport{}, d.stderr, err
			}
		case KindOpen, KindAccepted, KindStdin, KindStdinEOF, KindCancel, KindHookReply:
			return wire.CommandExitReport{}, d.stderr, fmt.Errorf("RunCommandStream: %w: kind %d from daemon", ErrProtocol, uint8(frame.Kind))
		}
	}
}

func (d *streamDriver) forwardStdout(payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if _, err := d.stdout.Write(payload); err != nil {
		return fmt.Errorf("RunCommandStream: stdout: %w", err)
	}
	credit, err := jsonv2.Marshal(wire.WindowCredit{Direction: "stdout", CreditBytes: uint64(len(payload))})
	if err != nil {
		return fmt.Errorf("RunCommandStream: %w: credit encodes", ErrProtocol)
	}
	if err := d.pc.writeKind(KindWindowUpdate, d.id, credit); err != nil {
		return fmt.Errorf("RunCommandStream: %w: %w", err, ErrOutcomeUnknown)
	}
	return nil
}

func (d *streamDriver) bufferStderr(payload []byte) {
	if uint64(len(d.stderr)) >= commandStderrCap {
		return
	}
	room := commandStderrCap - len(d.stderr)
	if len(payload) > room {
		d.stderr = append(d.stderr, payload[:room]...)
	} else {
		d.stderr = append(d.stderr, payload...)
	}
}

func (d *streamDriver) grantWindow(payload []byte) error {
	var credit wire.WindowCredit
	if err := jsonv2.Unmarshal(payload, &credit, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("RunCommandStream: %w: bad credit", ErrProtocol)
	}
	if credit.Direction != "" && credit.Direction != "stdin" {
		return fmt.Errorf("RunCommandStream: %w: credit for %q", ErrProtocol, credit.Direction)
	}
	d.mu.Lock()
	d.window += credit.Credit()
	d.mu.Unlock()
	d.kick()
	return nil
}

func (d *streamDriver) rejectHook(payload []byte) error {
	var call wire.HookCall
	if err := jsonv2.Unmarshal(payload, &call, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("RunCommandStream: %w: bad hook call", ErrProtocol)
	}
	raw, err := jsonv2.Marshal(wire.RejectAllHookReply("hook handler missing"))
	if err != nil {
		return fmt.Errorf("RunCommandStream: %w: hook reply encodes: %w", ErrProtocol, err)
	}
	if err := d.pc.writeKind(KindHookReply, d.id, raw); err != nil {
		return fmt.Errorf("RunCommandStream: %w: %w", err, ErrOutcomeUnknown)
	}
	return nil
}

func (d *streamDriver) finishExit(payload []byte, pumpDone <-chan error) (wire.CommandExitReport, []byte, error) {
	d.terminal.Store(true)
	d.kick()
	select {
	case err := <-pumpDone:
		if err != nil {
			return wire.CommandExitReport{}, d.stderr, err
		}
	default:
	}
	report, isCommand, err := d.decodeExit(payload)
	if err != nil {
		return wire.CommandExitReport{}, d.stderr, err
	}
	if d.hasRef {
		return report, d.stderr, errorFromReply("Command "+d.service, d.refusal, d.invoking, Unknown)
	}
	if !isCommand {
		var exit wire.ExitReport
		_ = jsonv2.Unmarshal(payload, &exit, jsonv1.DefaultOptionsV1())
		if exit.Status != 0 {
			code := wire.Code(exit.Code)
			if sentinelFor(code) == nil {
				return wire.CommandExitReport{}, d.stderr, fmt.Errorf("RunCommandStream: %w: exit code %q", ErrProtocol, exit.Code)
			}
			return wire.CommandExitReport{}, d.stderr, &Error{Code: code, Op: "Command " + d.service, Message: exit.Outcome, Invocation: d.invoking, Outcome: Unknown}
		}
		return report, d.stderr, nil
	}
	if report.ExitCode != 0 || report.Cancelled {
		return wire.CommandExitReport{}, d.stderr, fmt.Errorf("RunCommandStream: %w: %s exit %d cancelled %v", ErrOutcomeUnknown, d.service, report.ExitCode, report.Cancelled)
	}
	return report, d.stderr, nil
}

func (d *streamDriver) decodeExit(payload []byte) (wire.CommandExitReport, bool, error) {
	var probe map[string]jsonv1.RawMessage
	if err := jsonv2.Unmarshal(payload, &probe, jsonv1.DefaultOptionsV1()); err != nil {
		return wire.CommandExitReport{}, false, fmt.Errorf("RunCommandStream: %w: bad exit: %w", ErrProtocol, err)
	}
	if _, ok := probe["exit_code"]; ok {
		var report wire.CommandExitReport
		if err := jsonv2.Unmarshal(payload, &report, jsonv1.DefaultOptionsV1()); err != nil {
			return wire.CommandExitReport{}, false, fmt.Errorf("RunCommandStream: %w: bad exit: %w", ErrProtocol, err)
		}
		return report, true, nil
	}
	var exit wire.ExitReport
	if err := jsonv2.Unmarshal(payload, &exit, jsonv1.DefaultOptionsV1()); err != nil {
		return wire.CommandExitReport{}, false, fmt.Errorf("RunCommandStream: %w: bad exit: %w", ErrProtocol, err)
	}
	return wire.CommandExitReport{Outcome: exit.Outcome, InvocationID: d.invoking}, false, nil
}

func (d *streamDriver) awaitRefusedExit() error {
	frame, err := readFrame(d.pc.nc)
	if err != nil {
		return fmt.Errorf("RunCommandStream: %w: %w", err, ErrOutcomeUnknown)
	}
	if frame.Stream != d.id || frame.Kind != KindExit {
		return fmt.Errorf("RunCommandStream: %w: expected exit after refusal", ErrProtocol)
	}
	d.terminal.Store(true)
	d.kick()
	report, _, err := d.decodeExit(frame.Payload)
	if err != nil {
		return err
	}
	_ = report
	if d.hasRef {
		return errorFromReply("Command "+d.service, d.refusal, d.invoking, Unknown)
	}
	return fmt.Errorf("RunCommandStream: %w: refused without a reason", ErrProtocol)
}
