package ruk

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"fmt"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

// Call runs one typed method and returns its reply.
func Call[Req, Resp any](ctx context.Context, c *Client, repo Repo, method wire.Method, req Req) (Resp, error) {
	args, err := jsonv2.Marshal(req)
	var zero Resp
	if err != nil {
		return zero, fmt.Errorf("Call: %w", err)
	}
	return callDecoded[Resp](ctx, c, repo, "Call", method, newRequestID(), args, true)
}

// Retry resends one call with its first request id.
func Retry[Req, Resp any](ctx context.Context, c *Client, repo Repo, method wire.Method, requestID string, req Req) (Resp, error) {
	args, err := jsonv2.Marshal(req)
	var zero Resp
	if err != nil {
		return zero, fmt.Errorf("Retry: %w", err)
	}
	return callDecoded[Resp](ctx, c, repo, "Retry", method, requestID, args, false)
}

// Schema reads the daemon model document.
func Schema(ctx context.Context, c *Client, repo Repo, args wire.SchemaArgs) (jsonv1.RawMessage, error) {
	return Call[wire.SchemaArgs, jsonv1.RawMessage](ctx, c, repo, wire.MethodSchema, args)
}

// SessionOpen opens one session without an envelope.
func SessionOpen(ctx context.Context, c *Client, repo Repo, args wire.SessionOpenArgs) (wire.SessionOpened, error) {
	return Call[wire.SessionOpenArgs, wire.SessionOpened](ctx, c, repo, wire.MethodSessionOpen, args)
}

// SessionResume resumes one session inside an envelope.
func SessionResume(ctx context.Context, c *Client, repo Repo, args wire.SessionResumeArgs) (wire.SessionResumeReply, error) {
	return Call[wire.SessionResumeArgs, wire.SessionResumeReply](ctx, c, repo, wire.MethodSessionResume, args)
}

type refusalError struct {
	err *Error
}

func (r *refusalError) Error() string {
	return r.err.Error()
}

func (r *refusalError) Unwrap() error {
	return r.err
}

func opFor(method wire.Method) string {
	return "Call " + string(method)
}

func newRequestID() string {
	var raw [16]byte
	_, _ = rand.Read(raw[:])
	raw[6] = raw[6]&0x0f | 0x40
	raw[8] = raw[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

func decodeTypedBody(method wire.Method, sentID string, payload []byte, out any) error {
	var top map[string]jsonv1.RawMessage
	if err := jsonv2.Unmarshal(payload, &top, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("decodeTypedBody: %w: %w", ErrProtocol, err)
	}
	rawBody, enveloped := top["body"]
	if !enveloped {
		if err := jsonv2.Unmarshal(payload, out, jsonv1.DefaultOptionsV1()); err != nil {
			return fmt.Errorf("decodeTypedBody: %w: %w", ErrProtocol, err)
		}
		return nil
	}
	_ = rawBody
	var envelope wire.ReplyEnvelope
	if err := jsonv2.Unmarshal(payload, &envelope, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("decodeTypedBody: %w: %w", ErrProtocol, err)
	}
	if envelope.RequestID != nil && *envelope.RequestID != sentID {
		return fmt.Errorf("decodeTypedBody: %w: request id mismatch", ErrProtocol)
	}
	var body map[string]jsonv1.RawMessage
	if err := jsonv2.Unmarshal(envelope.Body, &body, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("decodeTypedBody: %w: %w", ErrProtocol, err)
	}
	if len(body) != 1 {
		return fmt.Errorf("decodeTypedBody: %w: body keys %d", ErrProtocol, len(body))
	}
	if rawFailure, ok := body["failure"]; ok {
		reply, err := decodeErrorReply(rawFailure)
		if err != nil {
			return err
		}
		return &refusalError{err: errorFromReply(opFor(method), reply, sentID, Unknown)}
	}
	rawSuccess, ok := body["success"]
	if !ok {
		return fmt.Errorf("decodeTypedBody: %w: no success", ErrProtocol)
	}
	var success map[string]jsonv1.RawMessage
	if err := jsonv2.Unmarshal(rawSuccess, &success, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("decodeTypedBody: %w: %w", ErrProtocol, err)
	}
	if len(success) != 1 {
		return fmt.Errorf("decodeTypedBody: %w: success keys %d", ErrProtocol, len(success))
	}
	rawPayload, ok := success[string(method)]
	if !ok {
		return fmt.Errorf("decodeTypedBody: %w: success names another method", ErrProtocol)
	}
	if err := jsonv2.Unmarshal(rawPayload, out, jsonv1.DefaultOptionsV1()); err != nil {
		return fmt.Errorf("decodeTypedBody: %w: %w", ErrProtocol, err)
	}
	return nil
}

type snapshot struct {
	refusal  wire.ErrorReply
	accepted wire.InvocationAccepted
	output   []byte
	facts    typedFacts
	exit     wire.ExitReport
	hasAcc   bool
	hasRef   bool
	hasExit  bool
}

func callDecoded[Resp any](ctx context.Context, c *Client, repo Repo, op string, method wire.Method, requestID string, args []byte, autoRetry bool) (Resp, error) {
	var zero Resp
	start := time.Now()
	for attempt := range 2 {
		resp, outcome, code, outBytes, again, err := attemptCall[Resp](ctx, c, repo, op, method, requestID, args, autoRetry)
		if c.hooks.OnCall != nil {
			c.hooks.OnCall(CallEvent{
				Method:   string(method),
				Duration: time.Since(start),
				Attempts: uint8(attempt + 1),
				Code:     code,
				Outcome:  outcome,
				OutBytes: outBytes,
			})
		}
		if !again || attempt == 1 {
			return resp, err
		}
	}
	return zero, fmt.Errorf("callDecoded: %w", ErrProtocol)
}

func attemptCall[Resp any](ctx context.Context, c *Client, repo Repo, op string, method wire.Method, requestID string, args []byte, autoRetry bool) (Resp, Outcome, string, int64, bool, error) {
	var zero Resp
	kind := method.Kind()
	sessionful := kind != wire.MethodAnonymous && kind != wire.MethodBootstrap
	var sessionID *string
	if sessionful {
		entry, err := c.ensureSession(ctx, repo)
		if err != nil {
			return zero, NotCommitted, "", 0, false, err
		}
		if kind == wire.MethodMutation && !entry.active {
			forbidden := &Error{Code: wire.CodeForbidden, Op: op, Message: "session lacks active authority", Outcome: NotCommitted}
			return zero, NotCommitted, string(wire.CodeForbidden), 0, false, forbidden
		}
		sessionID = &entry.id
	}
	argsText := string(args)
	open := wire.InvocationOpen{
		Repo:      cmp.Or(repo.Handle, repo.Path),
		Method:    string(method),
		RequestID: &requestID,
		ArgsJSON:  &argsText,
		SessionID: sessionID,
	}
	rawOpen, err := jsonv2.Marshal(open)
	if err != nil {
		return zero, NotCommitted, "", 0, false, fmt.Errorf("attemptCall: %w", err)
	}
	pc, s, queued, err := c.pool.acquire(ctx)
	if err != nil {
		return zero, NotCommitted, "", 0, false, err
	}
	openedAt := time.Now()
	if err := pc.writeKind(KindOpen, s.id, rawOpen); err != nil {
		pc.fail(err)
		pc.release(s)
		facts := typedFacts{Attempted: true}
		outcome := outcomeTyped(facts)
		if kind == wire.MethodRead {
			return zero, outcome, "", 0, autoRetry, fmt.Errorf("attemptCall: %w", err)
		}
		return zero, outcome, "", 0, false, fmt.Errorf("attemptCall: %w: %w", err, ErrOutcomeUnknown)
	}
	s.mu.Lock()
	s.facts.Attempted = true
	s.mu.Unlock()
	snap, waitErr := waitTerminal(ctx, pc, s)
	if waitErr != nil {
		outcome := Unknown
		if kind == wire.MethodRead {
			outcome = NotCommitted
		}
		if ctx.Err() != nil {
			return zero, outcome, "", int64(len(snap.output)), false, fmt.Errorf("attemptCall: %w", ctx.Err())
		}
		facts := snap.facts
		outcome = outcomeTyped(facts)
		if cause := pc.cause(); cause != nil {
			waitErr = fmt.Errorf("attemptCall: %w: %w", waitErr, cause)
		}
		if kind == wire.MethodRead {
			return zero, outcome, "", int64(len(snap.output)), autoRetry, fmt.Errorf("attemptCall: %w", waitErr)
		}
		return zero, outcome, "", int64(len(snap.output)), false, fmt.Errorf("attemptCall: %w: %w", waitErr, ErrOutcomeUnknown)
	}
	if snap.hasAcc {
		c.handles.put(repo.Path, snap.accepted.RepoHandle)
		if c.hooks.OnAccept != nil {
			c.hooks.OnAccept(AcceptEvent{Wait: time.Since(openedAt), Conn: pc.id, Queued: queued})
		}
	}
	outBytes := int64(len(snap.output))
	if snap.exit.Status == 0 {
		var out Resp
		if err := decodeTypedBody(method, requestID, snap.output, &out); err != nil {
			return zero, Committed, "OK", outBytes, false, err
		}
		return out, Committed, "OK", outBytes, false, nil
	}
	code := snap.exit.Code
	if snap.hasRef {
		code = string(snap.refusal.Code)
	}
	if sentinelFor(wire.Code(code)) == nil {
		return zero, Unknown, code, outBytes, false, fmt.Errorf("attemptCall: %w: exit code %q", ErrProtocol, code)
	}
	outcome := outcomeTyped(snap.facts)
	if !snap.facts.Accepted && code == string(wire.CodeNotFound) && repo.Handle != "" {
		c.handles.drop(repo.Path)
		if _, err := c.OpenRepo(ctx, repo.Path); err == nil {
			rerr := refusalErrorOf(op, snap, requestID, outcome)
			return zero, outcome, code, outBytes, true, rerr
		}
	}
	if snap.facts.Accepted && sessionful && (code == string(wire.CodeForbidden) || code == string(wire.CodeNotFound)) {
		if _, err := c.recoverSession(ctx, repo, wire.Code(code)); err == nil {
			rerr := refusalErrorOf(op, snap, requestID, outcome)
			return zero, outcome, code, outBytes, true, rerr
		}
	}
	return zero, outcome, code, outBytes, false, refusalErrorOf(op, snap, requestID, outcome)
}

func refusalErrorOf(op string, snap snapshot, requestID string, outcome Outcome) error {
	if snap.hasRef {
		return &refusalError{err: errorFromReply(op, snap.refusal, requestID, outcome)}
	}
	return &refusalError{err: &Error{Code: wire.Code(snap.exit.Code), Op: op, Message: snap.exit.Outcome, Outcome: outcome}}
}

func waitTerminal(ctx context.Context, pc *conn, s *stream) (snapshot, error) {
	for {
		s.mu.Lock()
		terminal := s.state == streamTerminal
		var snap snapshot
		if terminal {
			snap = snapshot{
				output:   bytes.Clone(s.output),
				accepted: s.accepted,
				hasAcc:   s.facts.Accepted,
				refusal:  s.refusal,
				hasRef:   s.hasRef,
				exit:     s.exit,
				hasExit:  s.hasExit,
				facts:    s.facts,
			}
		} else {
			delta := s.received - s.granted
			orphan := s.state == streamOrphan
			s.mu.Unlock()
			if !orphan && delta > 0 {
				if err := grantStream(pc, s, delta); err != nil {
					return snapshot{}, err
				}
			}
			select {
			case <-s.signal:
			case <-s.done:
				s.mu.Lock()
				terminal := s.state == streamTerminal
				facts := s.facts
				s.mu.Unlock()
				if !terminal {
					return snapshot{facts: facts}, fmt.Errorf("waitTerminal: %w", ErrOutcomeUnknown)
				}
			case <-ctx.Done():
				s.mu.Lock()
				facts := s.facts
				s.mu.Unlock()
				facts.Attempted = true
				cancelTyped(pc, s)
				return snapshot{facts: facts}, ctx.Err()
			}
			continue
		}
		s.mu.Unlock()
		return snap, nil
	}
}

func grantStream(pc *conn, s *stream, delta uint64) error {
	credit := wire.WindowCredit{CreditBytes: delta, Direction: "stdout"}
	raw, err := jsonv2.Marshal(credit)
	if err != nil {
		return fmt.Errorf("grantStream: %w", err)
	}
	if err := pc.writeKind(KindWindowUpdate, s.id, raw); err != nil {
		return err
	}
	s.mu.Lock()
	s.granted += delta
	s.mu.Unlock()
	return nil
}

func cancelTyped(pc *conn, s *stream) {
	s.mu.Lock()
	if s.state == streamTerminal || s.state == streamOrphan {
		s.mu.Unlock()
		return
	}
	s.state = streamOrphan
	s.mu.Unlock()
	_ = pc.writeKind(KindCancel, s.id, nil)
	pc.release(s)
}
