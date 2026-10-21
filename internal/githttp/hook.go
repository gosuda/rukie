package githttp

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/review"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

const changeReviewUnavailable = "change review is not available"

func isRefsFor(name string) bool {
	return name == "refs/for" || strings.HasPrefix(name, refsForPrefix)
}

func truncateReason(reason string) string {
	if len(reason) <= maxReasonBytes {
		return reason
	}
	end := maxReasonBytes
	for end > 0 && !isCharBoundary(reason, end) {
		end--
	}
	return reason[:end]
}

func isCharBoundary(text string, i int) bool {
	if i <= 0 || i >= len(text) {
		return true
	}
	c := text[i]
	return c < 0x80 || c >= 0xC0
}

func (s *Server) hookHandler(caller access.Caller, repoID int64, invocationID string) ruk.HookHandler {
	return func(ctx context.Context, call wire.HookCall) wire.HookReply {
		if call.Phase == wire.HookPostReceive {
			return s.answerPostReceive(ctx, caller, repoID, invocationID, call)
		}
		verdicts := make([]wire.HookRefVerdict, 0, len(call.Commands))
		for _, cmd := range call.Commands {
			verdicts = append(verdicts, wire.HookRefVerdict{
				Name:    cmd.Name,
				Verdict: s.judge(ctx, caller, repoID, call.Phase, cmd),
			})
		}
		s.recordPushAttempt(ctx, caller, repoID, invocationID, call, verdicts)
		return wire.HookReply{Verdicts: verdicts}
	}
}

func (s *Server) answerPostReceive(ctx context.Context, caller access.Caller, repoID int64, invocationID string, call wire.HookCall) wire.HookReply {
	if s.feed == nil {
		verdicts := make([]wire.HookRefVerdict, 0, len(call.Commands))
		for _, cmd := range call.Commands {
			verdicts = append(verdicts, wire.HookRefVerdict{
				Name:    cmd.Name,
				Verdict: wire.AcceptVerdict(),
			})
		}
		return wire.HookReply{Verdicts: verdicts}
	}
	return s.feed.Handle(ctx, events.PushMeta{
		InvocationID: invocationID,
		RepoID:       repoID,
		ActorID:      caller.UserID,
	}, call)
}

func (s *Server) recordPushAttempt(ctx context.Context, caller access.Caller, repoID int64, invocationID string, call wire.HookCall, verdicts []wire.HookRefVerdict) {
	if invocationID == "" {
		return
	}
	landed := make([]events.LandedRef, 0, len(call.Commands))
	for i, cmd := range call.Commands {
		if i >= len(verdicts) || verdicts[i].Verdict.Kind != wire.HookAccept {
			continue
		}
		landed = append(landed, events.LandedRef{Name: cmd.Name, Old: cmd.Old, New: cmd.New})
	}
	if len(landed) == 0 {
		return
	}
	want := make(map[string]string, len(landed))
	for _, ref := range landed {
		want[ref.Name] = ref.New
	}
	s.stashWant(invocationID, want)
	if s.feed == nil {
		return
	}
	_, _ = s.feed.Record(ctx, events.PushRecord{
		Landed:       landed,
		InvocationID: invocationID,
		RepoID:       repoID,
		ActorID:      caller.UserID,
		AttemptedAt:  s.clock().UnixMilli(),
	})
}

func (s *Server) judge(ctx context.Context, caller access.Caller, repoID int64, phase wire.HookPhase, cmd wire.HookCommand) wire.HookVerdict {
	if phase == wire.HookPostReceive {
		return wire.AcceptVerdict()
	}
	if isRefsFor(cmd.Name) {
		return s.divertChange(ctx, caller, repoID, cmd)
	}
	if !cmd.Authorized {
		return wire.RejectVerdict(truncateReason("ref " + cmd.Name + " is not authorized by the session"))
	}
	decision := access.Check(s.resolver, caller, "repo.push", access.Resource{
		Kind:   access.KindRepo,
		RepoID: repoID,
		Ref:    cmd.Name,
	})
	if decision.NotFound {
		return wire.RejectVerdict(truncateReason("repository is not visible"))
	}
	if !decision.Allow {
		return wire.RejectVerdict(truncateReason("push to " + cmd.Name + " is not allowed by policy"))
	}
	return wire.AcceptVerdict()
}

func (s *Server) divertChange(ctx context.Context, caller access.Caller, repoID int64, cmd wire.HookCommand) wire.HookVerdict {
	if isZeroOid(cmd.New) {
		return wire.RejectVerdict(truncateReason("refs/for does not delete"))
	}
	branch, topic, ok := review.IngressTarget(cmd.Name)
	if !ok {
		return wire.RejectVerdict(truncateReason("refs/for needs a target branch"))
	}
	if s.store == nil {
		return wire.RejectVerdict(changeReviewUnavailable)
	}
	decision := access.Check(s.resolver, caller, "repo.view", access.Resource{
		Kind:   access.KindRepo,
		RepoID: repoID,
	})
	if decision.NotFound {
		return wire.RejectVerdict(truncateReason("repository is not visible"))
	}
	if !decision.Allow {
		return wire.RejectVerdict(truncateReason("push to " + cmd.Name + " is not allowed by policy"))
	}
	hookCtx, cancel := context.WithTimeout(ctx, s.hookTimeout)
	defer cancel()
	spans := s.changeSpans(hookCtx, repoID, caller.UserID, branch, topic, cmd.New)
	var reviewRef string
	werr := s.store.Write(hookCtx, func(tx *store.WriteTx) error {
		_, ref, ierr := review.IngressChange(tx, s.clock(), review.Ingress{
			RepoID: repoID, AuthorID: caller.UserID, Branch: branch,
			Topic: topic, HeadOid: cmd.New, Spans: spans,
		})
		if ierr != nil {
			return ierr
		}
		reviewRef = ref
		return nil
	})
	if werr != nil {
		if errors.Is(werr, context.DeadlineExceeded) {
			return wire.RejectVerdict(truncateReason("change creation timed out"))
		}
		return wire.RejectVerdict(truncateReason("change was not created: " + firstChangeLine(werr)))
	}
	return wire.DivertVerdict(reviewRef)
}

func isZeroOid(hex string) bool {
	if hex == "" {
		return true
	}
	for i := 0; i < len(hex); i++ {
		if hex[i] != '0' {
			return false
		}
	}
	return true
}

func firstChangeLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return line
}

func (s *Server) changeSpans(ctx context.Context, repoID, authorID int64, branch, topic, newHead string) map[string][]review.Span {
	key := branch + "~" + topic
	if topic == "" {
		key = branch + "~" + strconv.FormatInt(authorID, 10)
	}
	var oldHead string
	_ = s.store.Read(ctx, func(tx *store.ReadTx) error {
		row, gerr := tx.ReadChangeByKey(repoID, key)
		if gerr != nil {
			return gerr
		}
		if row.State == "open" {
			oldHead = row.HeadOid
		}
		return nil
	})
	if oldHead == "" || oldHead == newHead {
		return nil
	}
	path, ok := s.repoDaemonPath(repoID)
	if !ok {
		return nil
	}
	client, cerr := s.clientFor(ctx)
	if cerr != nil {
		return nil
	}
	repo, oerr := client.OpenRepo(ctx, path)
	if oerr != nil {
		return nil
	}
	raw, rerr := ruk.Call[map[string]any, jsonv1.RawMessage](ctx, client, repo, wire.MethodDiff, map[string]any{
		"left":  map[string]any{"head": map[string]any{"head": map[string]any{"oid": oldHead, "type": "git"}}},
		"right": map[string]any{"head": map[string]any{"head": map[string]any{"oid": newHead, "type": "git"}}},
		"paths": "all",
		"page":  map[string]any{"limit": 1000},
	})
	if rerr != nil {
		return nil
	}
	return spansFromDiffReply(raw)
}

func (s *Server) repoDaemonPath(repoID int64) (string, bool) {
	var owner, name sql.NullString
	if err := s.db.QueryRow(`SELECT u.name, r.name FROM repos r
LEFT JOIN users u ON u.id = r.owner_user_id WHERE r.id = ?`, repoID).Scan(&owner, &name); err != nil {
		return "", false
	}
	if !owner.Valid || !name.Valid || owner.String == "" || name.String == "" {
		return "", false
	}
	return filepath.Join(s.repoRoot, owner.String, name.String+".git", ".ruk"), true
}

type diffFileItem struct {
	Body map[string]any `json:"body"`
	Path string         `json:"path"`
}

type diffFilesPage struct {
	Items []diffFileItem `json:"items"`
}

type diffReplyJSON struct {
	Files diffFilesPage `json:"files"`
}

func spansFromDiffReply(raw jsonv1.RawMessage) map[string][]review.Span {
	var reply diffReplyJSON
	if uerr := jsonv2.Unmarshal([]byte(raw), &reply, jsonv1.DefaultOptionsV1()); uerr != nil {
		return nil
	}
	patches := make(map[string]string, len(reply.Files.Items))
	for _, file := range reply.Files.Items {
		patch, ok := unifiedPatchBytes(file.Body)
		if !ok || file.Path == "" {
			continue
		}
		patches[file.Path] = patch
	}
	return review.SpansFromPatch(patches)
}

func unifiedPatchBytes(body map[string]any) (string, bool) {
	unified, ok := body["unified"].(map[string]any)
	if !ok {
		return "", false
	}
	patch, ok := unified["patch"]
	if !ok {
		return "", false
	}
	switch holder := patch.(type) {
	case string:
		return holder, true
	case map[string]any:
		rawInline, ok := holder["inline"]
		if !ok {
			rawHex, ok := holder["hex"].(string)
			if !ok {
				return "", false
			}
			return rawHex, true
		}
		switch inline := rawInline.(type) {
		case string:
			return inline, true
		case map[string]any:
			hexRaw, ok := inline["hex"].(string)
			if !ok {
				if data, ok := inline["data"].(string); ok {
					return data, true
				}
				return "", false
			}
			return hexRaw, true
		default:
			return "", false
		}
	default:
		return "", false
	}
}
