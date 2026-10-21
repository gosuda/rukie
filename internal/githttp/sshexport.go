package githttp

import (
	"context"
	"path/filepath"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

// ResolveRepoID maps an owner and repository name to the repo row for non-HTTP git callers.
func (s *Server) ResolveRepoID(ctx context.Context, owner, repo string) (int64, bool) {
	return s.lookupRepo(ctx, owner, repo)
}

// StorePathFor maps an owner and repository name to the daemon repository path.
func StorePathFor(repoRoot, owner, repo string) string {
	return filepath.Join(repoRoot, owner, repo+".git", ".ruk")
}

// OpenPushSession opens the daemon session and mints the push request id for non-HTTP git callers.
func (s *Server) OpenPushSession(ctx context.Context, client *ruk.Client, path string) (string, string, error) {
	opened, err := ruk.SessionOpen(ctx, client, ruk.Repo{Path: path}, wire.SessionOpenArgs{})
	if err != nil {
		return "", "", err
	}
	return opened.Session, s.newRequestID(), nil
}

// HookHandler returns the pre-receive and post-receive policy handler for a non-HTTP git caller.
func (s *Server) HookHandler(caller access.Caller, repoID int64, invocationID string) ruk.HookHandler {
	return s.hookHandler(caller, repoID, invocationID)
}
