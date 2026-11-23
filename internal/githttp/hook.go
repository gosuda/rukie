package githttp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/queue"
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
	verdicts := make([]wire.HookRefVerdict, 0, len(call.Commands))
	for _, cmd := range call.Commands {
		verdicts = append(verdicts, wire.HookRefVerdict{
			Name:    cmd.Name,
			Verdict: wire.AcceptVerdict(),
		})
	}
	reply := wire.HookReply{Verdicts: verdicts}
	if s.feed != nil {
		reply = s.feed.Handle(ctx, events.PushMeta{
			InvocationID: invocationID,
			RepoID:       repoID,
			ActorID:      caller.UserID,
		}, call)
	}
	for _, cmd := range call.Commands {
		if err := s.remapPublishedChange(ctx, repoID, cmd.Name, cmd.New); err != nil {
			return wire.RejectAllHookReply(truncateReason("comment remap is pending: " + firstChangeLine(err)))
		}
	}
	return reply
}

// HandleReviewRemap retries comment remaps from a durable landed-push job.
func (s *Server) HandleReviewRemap(ctx context.Context, job store.Job) error {
	if job.Kind != string(queue.KindReviewRemap) {
		return fmt.Errorf("HandleReviewRemap: unexpected job kind %q", job.Kind)
	}
	var payload struct {
		Landed []events.LandedRef `json:"landed"`
		Repo   int64              `json:"repo"`
	}
	if err := jsonv2.Unmarshal([]byte(job.Payload), &payload); err != nil {
		return fmt.Errorf("HandleReviewRemap: %w", err)
	}
	if payload.Repo != job.RepoID {
		return fmt.Errorf("HandleReviewRemap: repository mismatch")
	}
	for _, ref := range payload.Landed {
		if err := s.remapPublishedChange(ctx, job.RepoID, ref.Name, ref.New); err != nil {
			return fmt.Errorf("HandleReviewRemap: %w", err)
		}
	}
	return nil
}

func (s *Server) remapPublishedChange(ctx context.Context, repoID int64, name, head string) error {
	target, ok := review.PublishedTarget(repoID, name, head)
	if !ok {
		return nil
	}
	if s.store == nil {
		return fmt.Errorf("remapPublishedChange: %s", changeReviewUnavailable)
	}
	return review.RemapPublished(ctx, s.store, target, func(ctx context.Context, oldHead, newHead string) (map[string][]review.Span, error) {
		return s.changeSpans(ctx, repoID, oldHead, newHead)
	})
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
	if s.resolver != nil {
		if repo, ok := s.resolver.Repo(repoID); ok && repo.Archived {
			return wire.RejectVerdict(truncateReason("repository is archived"))
		}
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
		return wire.RejectVerdict(truncateReason(pushDenyReason(cmd.Name, decision.Reason)))
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
	edit := access.Check(s.resolver, caller, "change.edit", access.Resource{
		Kind:   access.KindRepo,
		RepoID: repoID,
	})
	hookCtx, cancel := context.WithTimeout(ctx, s.hookTimeout)
	defer cancel()
	var reviewRef string
	werr := s.store.Write(hookCtx, func(tx *store.WriteTx) error {
		_, ref, ierr := review.IngressChange(tx, s.clock(), review.Ingress{
			RepoID: repoID, AuthorID: caller.UserID, Branch: branch,
			Topic: topic, HeadOid: cmd.New, MayEdit: edit.Allow,
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

func pushDenyReason(name, reason string) string {
	if strings.HasPrefix(reason, "protected:") {
		return "push to " + name + " is not allowed by policy: " + reason
	}
	return "push to " + name + " is not allowed by policy"
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

func (s *Server) repoDaemonPath(ctx context.Context, repoID int64) (string, error) {
	var row store.RepoRow
	if err := s.store.Read(ctx, func(tx *store.ReadTx) error {
		var gerr error
		row, gerr = tx.GetRepo(repoID)
		return gerr
	}); err != nil {
		return "", fmt.Errorf("repoDaemonPath: %w", err)
	}
	if row.RukPath == "" {
		return "", fmt.Errorf("repoDaemonPath: repository path is unavailable")
	}
	abs := row.RukPath
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(s.repoRoot, abs)
	}
	if !strings.HasSuffix(abs, ".ruk") {
		abs = filepath.Join(abs, ".ruk")
	}
	return abs, nil
}
