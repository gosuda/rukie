package ruk

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

// ArchiveFormat names one streamed archive payload.
type ArchiveFormat string

// Archive payload formats served by the daemon.
const (
	ArchiveTar ArchiveFormat = "tar"
	ArchiveZip ArchiveFormat = "zip"
)

// ArchiveRev names one archived commit by literal oid or by ref.
type ArchiveRev struct {
	commit string
	ref    string
	isRef  bool
}

// ArchiveCommitRev names one archived commit by literal oid.
func ArchiveCommitRev(commit string) ArchiveRev {
	return ArchiveRev{commit: commit}
}

// ArchiveRefRev names one archived commit by ref, resolved by the daemon.
func ArchiveRefRev(ref string) ArchiveRev {
	return ArchiveRev{ref: ref, isRef: true}
}

// MarshalJSON writes {"commit":...} or {"ref":...}.
func (r ArchiveRev) MarshalJSON() ([]byte, error) {
	if r.isRef {
		return jsonv2.Marshal(struct {
			Ref string `json:"ref"`
		}{Ref: r.ref})
	}
	return jsonv2.Marshal(struct {
		Commit string `json:"commit"`
	}{Commit: r.commit})
}

// ArchiveArgs selects one streamed archive body.
type ArchiveArgs struct {
	Format *ArchiveFormat `json:"format,omitempty"`
	Rev    ArchiveRev     `json:"rev"`
}

// ArchiveResult carries the accepted archive metadata and byte count.
type ArchiveResult struct {
	Commit      string
	Format      ArchiveFormat
	ContentType string
	Bytes       int64
}

type archiveAcceptedResult struct {
	Commit      string `json:"commit"`
	Format      string `json:"format"`
	ContentType string `json:"content_type"`
}

type archiveAccepted struct {
	Result     *archiveAcceptedResult `json:"result"`
	Method     string                 `json:"method"`
	RepoHandle string                 `json:"repo_handle"`
	StreamID   uint64                 `json:"stream_id"`
}

type archiveOutcome struct {
	code     string
	result   ArchiveResult
	written  int64
	outcome  Outcome
	accepted bool
	retry    bool
}

// Archive streams one daemon archive body to w and returns its metadata.
func Archive(ctx context.Context, c *Client, repo Repo, args ArchiveArgs, w io.Writer) (ArchiveResult, error) {
	var zero ArchiveResult
	if w == nil {
		return zero, fmt.Errorf("Archive: %w: nil writer", ErrProtocol)
	}
	if err := validateArchiveArgs(args); err != nil {
		return zero, err
	}
	entry, err := c.ensureReplaySession(ctx, repo)
	if err != nil {
		return zero, err
	}
	rawArgs, err := jsonv2.Marshal(args)
	if err != nil {
		return zero, fmt.Errorf("Archive: %w", err)
	}
	start := time.Now()
	out, err := c.archiveAttempt(ctx, repo, entry, newRequestID(), string(rawArgs), w)
	if err != nil && out.retry && out.written == 0 {
		if next, rerr := c.recoverSession(ctx, repo, wire.Code(out.code)); rerr == nil {
			_ = next
			out, err = c.archiveAttempt(ctx, repo, next, newRequestID(), string(rawArgs), w)
		} else if out.code == string(wire.CodeNotFound) && repo.Handle != "" {
			c.handles.drop(repo.Path)
			if _, oerr := c.OpenRepo(ctx, repo.Path); oerr == nil {
				out, err = c.archiveAttempt(ctx, repo, entry, newRequestID(), string(rawArgs), w)
			}
		}
	}
	if c.hooks.OnCall != nil {
		c.hooks.OnCall(CallEvent{
			Method:   string(wire.MethodArchive),
			Duration: time.Since(start),
			Attempts: 1,
			Code:     out.code,
			Outcome:  out.outcome,
			OutBytes: out.written,
		})
	}
	if err != nil {
		return zero, err
	}
	return out.result, nil
}

func validateArchiveArgs(args ArchiveArgs) error {
	if args.Format != nil && *args.Format != ArchiveTar && *args.Format != ArchiveZip {
		return fmt.Errorf("Archive: %w: format %q", ErrProtocol, string(*args.Format))
	}
	if !args.Rev.isRef && args.Rev.commit == "" {
		return fmt.Errorf("Archive: %w: empty commit rev", ErrProtocol)
	}
	if args.Rev.isRef && args.Rev.ref == "" {
		return fmt.Errorf("Archive: %w: empty ref rev", ErrProtocol)
	}
	return nil
}

func (c *Client) archiveAttempt(ctx context.Context, repo Repo, entry *sessionEntry, requestID, argsText string, w io.Writer) (archiveOutcome, error) {
	var out archiveOutcome
	out.outcome = NotCommitted
	pc, err := dialRawConn(ctx, c.pool.nextID(), c.socket, c.profile, c.timeouts, c.hooks)
	if err != nil {
		return out, err
	}
	defer func() { _ = pc.nc.Close() }()
	open := wire.InvocationOpen{
		Repo:      cmp.Or(repo.Handle, repo.Path),
		Method:    string(wire.MethodArchive),
		RequestID: &requestID,
		ArgsJSON:  &argsText,
		SessionID: &entry.id,
	}
	rawOpen, err := jsonv2.Marshal(open)
	if err != nil {
		return out, fmt.Errorf("Archive: %w", err)
	}
	if err := pc.writeKind(KindOpen, archiveStreamID, rawOpen); err != nil {
		return out, fmt.Errorf("Archive: %w", err)
	}
	if err := archiveGrant(pc, archiveInitialCredit); err != nil {
		return out, err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = pc.writeKind(KindCancel, archiveStreamID, nil)
			_ = pc.nc.Close()
		case <-done:
		}
	}()
	return c.archivePump(ctx, pc, repo, requestID, w)
}

const (
	archiveStreamID      = 1
	archiveInitialCredit = 1 << 20
)

func archiveGrant(pc *conn, delta uint64) error {
	raw, err := jsonv2.Marshal(wire.WindowCredit{CreditBytes: delta, Direction: "stdout"})
	if err != nil {
		return fmt.Errorf("Archive: %w", err)
	}
	if err := pc.writeKind(KindWindowUpdate, archiveStreamID, raw); err != nil {
		return fmt.Errorf("Archive: %w", err)
	}
	return nil
}

func (c *Client) archivePump(ctx context.Context, pc *conn, repo Repo, requestID string, w io.Writer) (archiveOutcome, error) {
	var out archiveOutcome
	out.outcome = NotCommitted
	var refusal wire.ErrorReply
	var hasRef bool
	var accepted *archiveAcceptedResult
	var written int64
	seenAccepted := false
	for {
		frame, err := readFrame(pc.nc)
		if err != nil {
			if ctx.Err() != nil {
				return out, fmt.Errorf("Archive: %w", ctx.Err())
			}
			out.outcome = Unknown
			return out, fmt.Errorf("Archive: %w", err)
		}
		if frame.Stream != archiveStreamID {
			continue
		}
		switch frame.Kind {
		case KindAccepted:
			if seenAccepted {
				return out, fmt.Errorf("Archive: %w: duplicate accepted", ErrProtocol)
			}
			var decoded archiveAccepted
			if uerr := jsonv2.Unmarshal(frame.Payload, &decoded, jsonv1.DefaultOptionsV1()); uerr != nil {
				return out, fmt.Errorf("Archive: %w: bad accepted: %w", ErrProtocol, uerr)
			}
			if decoded.StreamID != archiveStreamID {
				return out, fmt.Errorf("Archive: %w: accepted stream mismatch", ErrProtocol)
			}
			seenAccepted = true
			out.accepted = true
			c.handles.put(repo.Path, decoded.RepoHandle)
			accepted = decoded.Result
		case KindStdout:
			if !seenAccepted {
				return out, fmt.Errorf("Archive: %w: body before accepted", ErrProtocol)
			}
			if len(frame.Payload) > 0 {
				if werr := archiveWriteAll(w, frame.Payload); werr != nil {
					return out, fmt.Errorf("Archive: %w", werr)
				}
				written += int64(len(frame.Payload))
				if gerr := archiveGrant(pc, uint64(len(frame.Payload))); gerr != nil {
					out.written = written
					out.outcome = Unknown
					return out, gerr
				}
			}
		case KindStderr:
			if len(frame.Payload) > 0 {
				if gerr := archiveGrant(pc, uint64(len(frame.Payload))); gerr != nil {
					out.written = written
					out.outcome = Unknown
					return out, gerr
				}
			}
		case KindError:
			reply, derr := decodeErrorReply(frame.Payload)
			if derr != nil {
				return out, derr
			}
			refusal = reply
			hasRef = true
		case KindExit:
			var exit wire.ExitReport
			if uerr := jsonv2.Unmarshal(frame.Payload, &exit, jsonv1.DefaultOptionsV1()); uerr != nil {
				return out, fmt.Errorf("Archive: %w: bad exit: %w", ErrProtocol, uerr)
			}
			out.written = written
			if exit.Status == 0 && !hasRef {
				out.outcome = Committed
				out.code = "OK"
				out.result = ArchiveResult{Bytes: written}
				if accepted != nil {
					out.result.Commit = accepted.Commit
					out.result.Format = ArchiveFormat(accepted.Format)
					out.result.ContentType = accepted.ContentType
				}
				return out, nil
			}
			code := exit.Code
			if hasRef {
				code = string(refusal.Code)
			}
			out.code = code
			facts := typedFacts{Attempted: true, Accepted: seenAccepted, Terminal: true, Refusal: wire.Code(code)}
			if exit.Status == 0 {
				facts.Success = true
			}
			out.outcome = outcomeTyped(facts)
			if hasRef {
				if code == string(wire.CodeForbidden) || code == string(wire.CodeNotFound) {
					out.retry = true
				}
				return out, errorFromReply("Archive "+string(wire.MethodArchive), refusal, requestID, out.outcome)
			}
			if sentinelFor(wire.Code(code)) == nil {
				out.outcome = Unknown
				return out, fmt.Errorf("Archive: %w: exit code %q", ErrProtocol, code)
			}
			if code == string(wire.CodeForbidden) || code == string(wire.CodeNotFound) {
				out.retry = true
			}
			return out, &Error{Code: wire.Code(code), Op: "Archive " + string(wire.MethodArchive), Message: exit.Outcome, Outcome: out.outcome}
		case KindWindowUpdate, KindHookCall, KindHookReply:
			continue
		case KindOpen, KindStdin, KindStdinEOF, KindCancel:
			return out, fmt.Errorf("Archive: %w: kind %d from daemon", ErrProtocol, uint8(frame.Kind))
		default:
			return out, fmt.Errorf("Archive: %w: kind %d from daemon", ErrProtocol, uint8(frame.Kind))
		}
	}
}

func archiveWriteAll(w io.Writer, payload []byte) error {
	for len(payload) > 0 {
		n, err := w.Write(payload)
		if err != nil {
			return err
		}
		if n <= 0 {
			return fmt.Errorf("archiveWriteAll: %w: zero write", ErrProtocol)
		}
		payload = payload[n:]
	}
	return nil
}
