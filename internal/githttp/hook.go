package githttp

import (
	"context"
	"strings"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
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
				Verdict: s.judge(caller, repoID, call.Phase, cmd),
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

func (s *Server) judge(caller access.Caller, repoID int64, phase wire.HookPhase, cmd wire.HookCommand) wire.HookVerdict {
	if phase == wire.HookPostReceive {
		return wire.AcceptVerdict()
	}
	if isRefsFor(cmd.Name) {
		return wire.RejectVerdict(changeReviewUnavailable)
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
	if repo, ok := s.resolver.Repo(repoID); ok && repo.Archived {
		return wire.RejectVerdict(truncateReason("repository is archived"))
	}
	return wire.AcceptVerdict()
}
