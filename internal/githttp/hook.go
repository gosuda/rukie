package githttp

import (
	"context"
	"strings"

	"github.com/gosuda/rukie/internal/access"
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

func (s *Server) hookHandler(caller access.Caller, repoID int64) ruk.HookHandler {
	return func(_ context.Context, call wire.HookCall) wire.HookReply {
		verdicts := make([]wire.HookRefVerdict, 0, len(call.Commands))
		for _, cmd := range call.Commands {
			verdicts = append(verdicts, wire.HookRefVerdict{
				Name:    cmd.Name,
				Verdict: s.judge(caller, repoID, call.Phase, cmd),
			})
		}
		return wire.HookReply{Verdicts: verdicts}
	}
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
	return wire.AcceptVerdict()
}
