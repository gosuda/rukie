package ruk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync/atomic"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

const (
	commandStdinChunk = 32 << 10
	commandStderrCap  = 1 << 20
	defaultHookBudget = 10 * time.Second
	cancelGrace       = 2 * time.Second
)

var errHookTimeout = errors.New("hook handler timed out")

type HookHandler func(ctx context.Context, call wire.HookCall) wire.HookReply

type Command struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Hook       HookHandler
	Open       wire.ServiceOpen
	HookBudget time.Duration
}

func RunCommand(ctx context.Context, c *Client, cmd Command) (wire.CommandExitReport, []byte, error) {
	var zero wire.CommandExitReport
	open := cmd.Open
	if open.Service == "" {
		return zero, nil, fmt.Errorf("RunCommand: %w: empty service", ErrProtocol)
	}
	if open.Repository == "" {
		return zero, nil, fmt.Errorf("RunCommand: %w: empty repository", ErrProtocol)
	}
	if cmd.Stdout == nil {
		return zero, nil, fmt.Errorf("RunCommand: %w: nil stdout", ErrProtocol)
	}
	rawOpen, err := jsonv2.Marshal(open)
	if err != nil {
		return zero, nil, fmt.Errorf("RunCommand: %w", err)
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
		return zero, nil, fmt.Errorf("RunCommand: %w: connection broken", ErrClosed)
	}
	defer pc.release(s)
	cmdCtx, stop := context.WithCancel(ctx)
	defer stop()
	hookBudget := cmd.HookBudget
	if hookBudget <= 0 {
		hookBudget = defaultHookBudget
	}
	d := &commandDriver{pc: pc, id: s.id, stdout: cmd.Stdout, handle: cmd.Hook, service: open.Service, hookBudget: hookBudget}
	var finished atomic.Bool
	go func() {
		select {
		case <-cmdCtx.Done():
			if finished.Load() {
				return
			}
			_ = pc.writeKind(KindCancel, s.id, nil)
			if !d.stdinEOF.Load() {
				_ = pc.nc.Close()
				return
			}
			select {
			case <-closed:
			case <-time.After(cancelGrace):
				_ = pc.nc.Close()
			}
		case <-closed:
		}
	}()
	if openErr := pc.writeKind(KindOpen, s.id, rawOpen); openErr != nil {
		return zero, nil, fmt.Errorf("RunCommand: %w: %w", openErr, ErrOutcomeUnknown)
	}
	var report wire.CommandExitReport
	var stderr []byte
	var completed bool
	var runErr error
	if open.Advertise {
		report, stderr, completed, runErr = d.runAdvertise(cmdCtx)
	} else {
		report, stderr, completed, runErr = d.runRound(cmdCtx, cmd.Stdin)
	}
	finished.Store(true)
	if completed {
		return report, stderr, runErr
	}
	err = runErr
	if ctx.Err() != nil {
		return report, stderr, fmt.Errorf("RunCommand: %w: %w", ctx.Err(), ErrOutcomeUnknown)
	}
	return report, stderr, err
}

type commandDriver struct {
	stdout     io.Writer
	pc         *conn
	handle     HookHandler
	service    string
	invoking   string
	refusal    wire.ErrorReply
	stderr     []byte
	hookBudget time.Duration
	hookErr    error
	id         uint64
	window     uint64
	hasRef     bool
	stdinEOF   atomic.Bool
}

func (d *commandDriver) fail(format string, args ...any) (wire.CommandExitReport, []byte, bool, error) {
	return wire.CommandExitReport{}, d.stderr, false, fmt.Errorf("RunCommand: %w: %s", ErrProtocol, fmt.Sprintf(format, args...))
}

func (d *commandDriver) read() (Frame, error) {
	return readFrame(d.pc.nc)
}

func (d *commandDriver) runAdvertise(ctx context.Context) (wire.CommandExitReport, []byte, bool, error) {
	accepted, err := d.awaitAccepted(ctx)
	if err != nil {
		return wire.CommandExitReport{}, d.stderr, false, err
	}
	d.window = accepted.StdinCredit
	d.invoking = accepted.InvocationID
	return d.drain(ctx)
}

func (d *commandDriver) runRound(ctx context.Context, stdin io.Reader) (wire.CommandExitReport, []byte, bool, error) {
	accepted, err := d.awaitAccepted(ctx)
	if err != nil {
		return wire.CommandExitReport{}, d.stderr, false, err
	}
	d.window = accepted.StdinCredit
	d.invoking = accepted.InvocationID
	if stdin == nil {
		if err := d.pc.writeKind(KindStdinEOF, d.id, nil); err != nil {
			return wire.CommandExitReport{}, d.stderr, false, fmt.Errorf("RunCommand: %w: %w", err, ErrOutcomeUnknown)
		}
		d.stdinEOF.Store(true)
		return d.drain(ctx)
	}
	if d.window == 0 {
		return d.fail("zero stdin credit with a request body")
	}
	if err := d.sendStdin(ctx, stdin); err != nil {
		return wire.CommandExitReport{}, d.stderr, false, err
	}
	return d.drain(ctx)
}

func (d *commandDriver) awaitAccepted(ctx context.Context) (wire.CommandAccepted, error) {
	var zero wire.CommandAccepted
	frame, err := d.readAbortable(ctx)
	if err != nil {
		return zero, err
	}
	if frame.Stream != d.id {
		return zero, fmt.Errorf("RunCommand: %w: frame on stream %d", ErrProtocol, frame.Stream)
	}
	if frame.Kind == KindError {
		d.recordRefusal(frame.Payload)
		return zero, d.awaitRefusedExit()
	}
	if frame.Kind != KindAccepted {
		return zero, fmt.Errorf("RunCommand: %w: expected accepted, got kind %d stream %d", ErrProtocol, uint8(frame.Kind), frame.Stream)
	}
	var accepted wire.CommandAccepted
	if err := jsonv2.Unmarshal(frame.Payload, &accepted, jsonv1.DefaultOptionsV1()); err != nil {
		return zero, fmt.Errorf("RunCommand: %w: bad accepted: %w", ErrProtocol, err)
	}
	if accepted.StreamID != d.id {
		return zero, fmt.Errorf("RunCommand: %w: accepted stream mismatch", ErrProtocol)
	}
	return accepted, nil
}

func (d *commandDriver) readAbortable(ctx context.Context) (Frame, error) {
	type result struct {
		err   error
		frame Frame
	}
	box := make(chan result, 1)
	go func() {
		frame, err := d.read()
		box <- result{frame: frame, err: err}
	}()
	select {
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case out := <-box:
		return out.frame, out.err
	}
}

func (d *commandDriver) sendStdin(ctx context.Context, stdin io.Reader) error {
	buf := make([]byte, commandStdinChunk)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for d.window == 0 {
			if err := d.refill(ctx); err != nil {
				return err
			}
		}
		n, readErr := stdin.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			for len(chunk) > 0 {
				for d.window == 0 {
					if err := d.refill(ctx); err != nil {
						return err
					}
				}
				take := uint64(len(chunk))
				if take > d.window {
					take = d.window
				}
				if take > commandStdinChunk {
					take = commandStdinChunk
				}
				if err := d.pc.writeKind(KindStdin, d.id, chunk[:take]); err != nil {
					return fmt.Errorf("RunCommand: %w: %w", err, ErrOutcomeUnknown)
				}
				d.window -= take
				chunk = chunk[take:]
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				if err := d.pc.writeKind(KindStdinEOF, d.id, nil); err != nil {
					return fmt.Errorf("RunCommand: %w: %w", err, ErrOutcomeUnknown)
				}
				d.stdinEOF.Store(true)
				return nil
			}
			return fmt.Errorf("RunCommand: stdin: %w", readErr)
		}
	}
}

func (d *commandDriver) refill(ctx context.Context) error {
	for {
		frame, err := d.readAbortable(ctx)
		if err != nil {
			return fmt.Errorf("RunCommand: %w: %w", err, ErrOutcomeUnknown)
		}
		if frame.Stream != d.id {
			return fmt.Errorf("RunCommand: %w: frame on stream %d", ErrProtocol, frame.Stream)
		}
		switch frame.Kind {
		case KindWindowUpdate:
			var credit wire.WindowCredit
			if err := jsonv2.Unmarshal(frame.Payload, &credit, jsonv1.DefaultOptionsV1()); err != nil {
				return fmt.Errorf("RunCommand: %w: bad credit: %w", ErrProtocol, err)
			}
			if credit.Direction != "" && credit.Direction != "stdin" {
				return fmt.Errorf("RunCommand: %w: credit for %q", ErrProtocol, credit.Direction)
			}
			d.window += credit.Credit()
			return nil
		case KindError:
			d.recordRefusal(frame.Payload)
		case KindExit:
			report, completed, err := d.decodeExit(frame.Payload)
			if err != nil {
				return err
			}
			if d.hasRef {
				return errorFromReply("Command "+d.service, d.refusal, d.invoking, Unknown)
			}
			return commandTerminal{report: report, completed: completed}
		case KindOpen, KindAccepted, KindStdin, KindStdinEOF, KindStdout, KindStderr, KindCancel, KindHookCall, KindHookReply:
			return fmt.Errorf("RunCommand: %w: kind %d during stdin", ErrProtocol, uint8(frame.Kind))
		}
	}
}

type commandTerminal struct {
	report    wire.CommandExitReport
	completed bool
}

func (e commandTerminal) Error() string {
	return "RunCommand: stream ended during stdin"
}

func (d *commandDriver) awaitRefusedExit() error {
	frame, err := d.read()
	if err != nil {
		return fmt.Errorf("RunCommand: %w: %w", err, ErrOutcomeUnknown)
	}
	if frame.Stream != d.id || frame.Kind != KindExit {
		return fmt.Errorf("RunCommand: %w: expected exit after refusal", ErrProtocol)
	}
	report, _, err := d.decodeExit(frame.Payload)
	if err != nil {
		return err
	}
	_ = report
	if d.hasRef {
		return errorFromReply("Command "+d.service, d.refusal, d.invoking, Unknown)
	}
	return fmt.Errorf("RunCommand: %w: refused without a reason", ErrProtocol)
}

func (d *commandDriver) recordRefusal(payload []byte) {
	reply, err := decodeErrorReply(payload)
	if err != nil {
		return
	}
	d.refusal = reply
	d.hasRef = true
}

func (d *commandDriver) drain(ctx context.Context) (wire.CommandExitReport, []byte, bool, error) {
	grace := false
	for {
		if ctx.Err() != nil {
			if !d.stdinEOF.Load() || grace {
				return wire.CommandExitReport{}, d.stderr, false, fmt.Errorf("RunCommand: %w: %w", ctx.Err(), ErrOutcomeUnknown)
			}
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.Background(), cancelGrace)
			defer cancel()
			grace = true
		}
		frame, err := d.read()
		if err != nil {
			if ctx.Err() != nil {
				return wire.CommandExitReport{}, d.stderr, false, fmt.Errorf("RunCommand: %w: %w", ctx.Err(), ErrOutcomeUnknown)
			}
			return wire.CommandExitReport{}, d.stderr, false, fmt.Errorf("RunCommand: %w: %w", err, ErrOutcomeUnknown)
		}
		if frame.Stream != d.id {
			return d.fail("frame on stream %d", frame.Stream)
		}
		switch frame.Kind {
		case KindStdout:
			if len(frame.Payload) > 0 {
				if _, err := d.stdout.Write(frame.Payload); err != nil {
					return wire.CommandExitReport{}, d.stderr, false, fmt.Errorf("RunCommand: stdout: %w", err)
				}
				credit, err := jsonv2.Marshal(wire.WindowCredit{Direction: "stdout", CreditBytes: uint64(len(frame.Payload))})
				if err != nil {
					return d.fail("credit encodes")
				}
				if err := d.pc.writeKind(KindWindowUpdate, d.id, credit); err != nil {
					return wire.CommandExitReport{}, d.stderr, false, fmt.Errorf("RunCommand: %w: %w", err, ErrOutcomeUnknown)
				}
			}
		case KindStderr:
			if uint64(len(d.stderr)) < commandStderrCap {
				room := commandStderrCap - len(d.stderr)
				if len(frame.Payload) > room {
					d.stderr = append(d.stderr, frame.Payload[:room]...)
				} else {
					d.stderr = append(d.stderr, frame.Payload...)
				}
			}
		case KindWindowUpdate:
			var credit wire.WindowCredit
			if err := jsonv2.Unmarshal(frame.Payload, &credit, jsonv1.DefaultOptionsV1()); err != nil {
				return d.fail("bad credit")
			}
			if credit.Direction != "" && credit.Direction != "stdin" {
				return d.fail("credit for %q", credit.Direction)
			}
			d.window += credit.Credit()
		case KindHookCall:
			if herr := d.answerHook(ctx, frame.Payload); herr != nil {
				if errors.Is(herr, errHookTimeout) {
					d.hookErr = herr
					continue
				}
				return wire.CommandExitReport{}, d.stderr, false, herr
			}
		case KindError:
			d.recordRefusal(frame.Payload)
		case KindExit:
			return d.finishExit(frame.Payload)
		case KindOpen, KindAccepted, KindStdin, KindStdinEOF, KindCancel, KindHookReply:
			return d.fail("kind %d from daemon", uint8(frame.Kind))
		}
	}
}

func (d *commandDriver) answerHook(ctx context.Context, payload []byte) error {
	var call wire.HookCall
	if err := jsonv2.Unmarshal(payload, &call, jsonv1.DefaultOptionsV1()); err != nil {
		return d.sendReply(wire.RejectAllHookReply("hook call malformed"))
	}
	budget := hookDeadline(d.hookBudget, call.DeadlineMs)
	if d.handle == nil {
		return d.sendReply(wire.RejectAllHookReply("hook handler missing"))
	}
	type answered struct {
		reply wire.HookReply
	}
	hctx, stop := context.WithTimeout(ctx, budget)
	defer stop()
	box := make(chan answered, 1)
	go func() {
		box <- answered{reply: d.handle(hctx, call)}
	}()
	select {
	case <-ctx.Done():
		return fmt.Errorf("RunCommand: %w: %w", ctx.Err(), ErrOutcomeUnknown)
	case out := <-box:
		return d.sendReply(out.reply)
	case <-hctx.Done():
		select {
		case out := <-box:
			return d.sendReply(out.reply)
		default:
		}
		_ = d.sendReply(wire.RejectAllHookReply("hook handler timed out"))
		if ctx.Err() != nil {
			return fmt.Errorf("RunCommand: %w: %w", ctx.Err(), ErrOutcomeUnknown)
		}
		return fmt.Errorf("RunCommand: %w: %w", errHookTimeout, ErrOutcomeUnknown)
	}
}

func hookDeadline(ceiling time.Duration, deadlineMs uint64) time.Duration {
	const maxMs = uint64(math.MaxInt64 / int64(time.Millisecond))
	if deadlineMs == 0 || deadlineMs > maxMs {
		return ceiling
	}
	if narrowed := time.Duration(deadlineMs) * time.Millisecond; narrowed < ceiling {
		return narrowed
	}
	return ceiling
}

func (d *commandDriver) sendReply(reply wire.HookReply) error {
	raw, err := jsonv2.Marshal(reply)
	if err != nil {
		return fmt.Errorf("RunCommand: %w: hook reply encodes: %w", ErrProtocol, err)
	}
	if err := d.pc.writeKind(KindHookReply, d.id, raw); err != nil {
		return fmt.Errorf("RunCommand: %w: %w", err, ErrOutcomeUnknown)
	}
	return nil
}

func (d *commandDriver) decodeExit(payload []byte) (wire.CommandExitReport, bool, error) {
	var probe map[string]jsonv1.RawMessage
	if err := jsonv2.Unmarshal(payload, &probe, jsonv1.DefaultOptionsV1()); err != nil {
		return wire.CommandExitReport{}, false, fmt.Errorf("RunCommand: %w: bad exit: %w", ErrProtocol, err)
	}
	if _, ok := probe["exit_code"]; ok {
		var report wire.CommandExitReport
		if err := jsonv2.Unmarshal(payload, &report, jsonv1.DefaultOptionsV1()); err != nil {
			return wire.CommandExitReport{}, false, fmt.Errorf("RunCommand: %w: bad exit: %w", ErrProtocol, err)
		}
		return report, true, nil
	}
	var exit wire.ExitReport
	if err := jsonv2.Unmarshal(payload, &exit, jsonv1.DefaultOptionsV1()); err != nil {
		return wire.CommandExitReport{}, false, fmt.Errorf("RunCommand: %w: bad exit: %w", ErrProtocol, err)
	}
	return wire.CommandExitReport{Outcome: exit.Outcome, InvocationID: d.invoking}, false, nil
}

func (d *commandDriver) finishExit(payload []byte) (wire.CommandExitReport, []byte, bool, error) {
	report, isCommand, err := d.decodeExit(payload)
	if err != nil {
		return wire.CommandExitReport{}, d.stderr, false, err
	}
	if d.hasRef {
		rerr := errorFromReply("Command "+d.service, d.refusal, d.invoking, Unknown)
		return report, d.stderr, false, rerr
	}
	if !isCommand {
		var exit wire.ExitReport
		_ = jsonv2.Unmarshal(payload, &exit, jsonv1.DefaultOptionsV1())
		if exit.Status != 0 {
			code := wire.Code(exit.Code)
			if sentinelFor(code) == nil {
				return report, d.stderr, false, fmt.Errorf("RunCommand: %w: exit code %q", ErrProtocol, exit.Code)
			}
			return report, d.stderr, false, &Error{Code: code, Op: "Command " + d.service, Message: exit.Outcome, Invocation: d.invoking, Outcome: Unknown}
		}
		return report, d.stderr, true, nil
	}
	if report.ExitCode != 0 || report.Cancelled {
		return report, d.stderr, report.Cancelled, fmt.Errorf("RunCommand: %w: %s exit %d cancelled %v", ErrOutcomeUnknown, d.service, report.ExitCode, report.Cancelled)
	}
	if d.hookErr != nil {
		return report, d.stderr, false, d.hookErr
	}
	return report, d.stderr, true, nil
}
