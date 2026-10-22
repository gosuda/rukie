package api

import (
	"maps"
	"slices"
	"strings"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/reserved"
)

var siteSegments = func() map[string]bool {
	out := map[string]bool{}
	for _, seg := range Reserved() {
		out[seg] = true
	}
	return out
}()

func SiteSegment(seg string) bool {
	if auth.ReservedName(seg) {
		return true
	}
	return siteSegments[seg]
}

type Route struct {
	Pattern  string
	Op       string
	PageOnly bool
}

func Routes() []Route {
	return []Route{
		{Pattern: "GET /-/api", Op: "api.index"},
		{Pattern: "GET /-/api/openapi.json", Op: "api.openapi"},
		{Pattern: "GET /new/repo", Op: "repo.create.page"},
		{Pattern: "POST /new/repo", Op: "repo.create"},
		{Pattern: "GET /new/org", Op: "org.create.page"},
		{Pattern: "POST /new/org", Op: "org.create"},
		{Pattern: "GET /orgs/{org}", Op: "org.get"},
		{Pattern: "GET /settings/orgs", Op: "org.list"},
		{Pattern: "GET /{org}/-/settings", Op: "org.settings.page"},
		{Pattern: "POST /{org}/-/settings/general", Op: "org.update"},
		{Pattern: "POST /{org}/-/settings/rename", Op: "org.rename"},
		{Pattern: "POST /{org}/-/settings/delete", Op: "org.delete"},
		{Pattern: "GET /{org}/-/members", Op: "org.member.list"},
		{Pattern: "POST /{org}/-/members/{user}/remove", Op: "org.member.remove"},
		{Pattern: "POST /{org}/-/members/leave", Op: "org.leave"},
		{Pattern: "GET /{org}/-/teams", Op: "team.list"},
		{Pattern: "POST /{org}/-/teams/create", Op: "team.create"},
		{Pattern: "GET /{org}/-/teams/{team}", Op: "team.get"},
		{Pattern: "POST /{org}/-/teams/{team}/edit", Op: "team.update"},
		{Pattern: "POST /{org}/-/teams/{team}/delete", Op: "team.delete"},
		{Pattern: "GET /{org}/-/teams/{team}/members", Op: "team.member.list"},
		{Pattern: "POST /{org}/-/teams/{team}/members", Op: "team.member.add"},
		{Pattern: "POST /{org}/-/teams/{team}/members/{user}/remove", Op: "team.member.remove"},
		{Pattern: "GET /{org}/-/teams/{team}/repos", Op: "team.repo.list"},
		{Pattern: "POST /{org}/-/teams/{team}/repos", Op: "team.repo.add"},
		{Pattern: "POST /{org}/-/teams/{team}/repos/{repo}/remove", Op: "team.repo.remove"},
		{Pattern: "GET /repos", Op: "repo.list"},
		{Pattern: "GET /{owner}/{repo}", Op: "repo.get"},
		{Pattern: "GET /{owner}/{repo}/settings", Op: "repo.settings.page"},
		{Pattern: "POST /{owner}/{repo}/settings/general", Op: "repo.update"},
		{Pattern: "POST /{owner}/{repo}/settings/visibility", Op: "repo.visibility.set"},
		{Pattern: "POST /{owner}/{repo}/settings/rename", Op: "repo.rename"},
		{Pattern: "POST /{owner}/{repo}/settings/delete", Op: "repo.delete"},
		{Pattern: "POST /{owner}/{repo}/settings/restore", Op: "repo.restore"},
		{Pattern: "GET /{owner}/{repo}/settings/collaborators", Op: "repo.collaborator.list"},
		{Pattern: "POST /{owner}/{repo}/settings/collaborators/{user}", Op: "repo.collaborator.add"},
		{Pattern: "POST /{owner}/{repo}/settings/collaborators/{user}/remove", Op: "repo.collaborator.remove"},
		{Pattern: "GET /{owner}/{repo}/revs/{rev}", Op: "code.rev.resolve"},
		{Pattern: "GET /{owner}/{repo}/tree/{rev}/{path...}", Op: "code.tree.get"},
		{Pattern: "GET /{owner}/{repo}/blob/{rev}/{path...}", Op: "code.blob.get"},
		{Pattern: "GET /{owner}/{repo}/raw/{rev}/{path...}", Op: "code.raw.get"},
		{Pattern: "GET /{owner}/{repo}/blame/{rev}/{path...}", Op: "code.blame.get"},
		{Pattern: "GET /{owner}/{repo}/log/{rev...}", Op: "code.log.list"},
		{Pattern: "GET /{owner}/{repo}/commit/{oid}", Op: "code.commit.get"},
		{Pattern: "GET /{owner}/{repo}/commit/{oid}/diff", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/commit/{oid}/patch", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/diff/{range}", Op: "code.diff.get"},
		{Pattern: "GET /{owner}/{repo}/compare/{range}", Op: "code.compare.get"},
		{Pattern: "GET /{owner}/{repo}/changes", Op: "change.list"},
		{Pattern: "GET /{owner}/{repo}/changes/{change}", Op: "change.get"},
		{Pattern: "GET /{owner}/{repo}/changes/{change}/diff/{range}", Op: "change.diff.get"},
		{Pattern: "GET /{owner}/{repo}/changes/{change}/blame", Op: "change.blame.get"},
		{Pattern: "GET /{owner}/{repo}/changes/{change}/stack", Op: "change.stack.list"},
		{Pattern: "GET /{owner}/{repo}/branches/{branch}/changes", Op: "branch.change.list"},
		{Pattern: "GET /{owner}/{repo}/changes/{change}/comments", Op: "change.comment.list"},
		{Pattern: "POST /{owner}/{repo}/changes/{change}/comments", Op: "change.comment.create"},
		{Pattern: "POST /{owner}/{repo}/changes/{change}/reviews", Op: "change.review.create"},
		{Pattern: "POST /{owner}/{repo}/changes/{change}/state", Op: "change.state.set"},
		{Pattern: "GET /{owner}/{repo}/changes/{change}/proofs", Op: "proof.list"},
		{Pattern: "POST /{owner}/{repo}/land/plans", Op: "land.plan.create"},
		{Pattern: "GET /{owner}/{repo}/land/plans", Op: "land.plan.list"},
		{Pattern: "GET /{owner}/{repo}/land/plans/{plan}", Op: "land.plan.get"},
		{Pattern: "POST /{owner}/{repo}/land/plans/{plan}/runs", Op: "land.run.create"},
		{Pattern: "GET /{owner}/{repo}/land/plans/{plan}/runs", Op: "land.run.list"},
		{Pattern: "POST /admin/land/runs", Op: "admin.land.run"},
		{Pattern: "GET /ci", PageOnly: true},
		{Pattern: "GET /admin/queue", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/ci", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/ci/runs/{run}", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/ci/runs/{run}/jobs/{job}", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/ci/runs/{run}/jobs/{job}/log", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/ci/runs/{run}/artifacts/{name}", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/ci/badge.svg", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/settings/ci", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/settings/protected", Op: "repo.policy.list"},
		{Pattern: "POST /{owner}/{repo}/settings/protected", Op: "repo.policy.set"},
		{Pattern: "GET /{owner}/{repo}/views", Op: "view.list"},
		{Pattern: "POST /{owner}/{repo}/views", Op: "view.create"},
		{Pattern: "GET /{owner}/{repo}/views/{view}", Op: "view.get"},
		{Pattern: "POST /{owner}/{repo}/views/{view}/update", Op: "view.update"},
		{Pattern: "POST /{owner}/{repo}/views/{view}/delete", Op: "view.delete"},
		{Pattern: "POST /{owner}/{repo}/views/{view}/visibility", Op: "view.visibility.set"},
		{Pattern: "GET /{owner}/{repo}/views/{view}/results", Op: "view.result.list"},
		{Pattern: "GET /{owner}/{repo}/branches", Op: "branch.list"},
		{Pattern: "POST /{owner}/{repo}/branches/create", Op: "branch.create"},
		{Pattern: "GET /{owner}/{repo}/branches/{branch}", Op: "branch.get"},
		{Pattern: "POST /{owner}/{repo}/branches/{branch}/update", Op: "branch.update"},
		{Pattern: "POST /{owner}/{repo}/branches/{branch}/delete", Op: "branch.delete"},
		{Pattern: "GET /{owner}/{repo}/tags", Op: "tag.list"},
		{Pattern: "POST /{owner}/{repo}/tags/create", Op: "tag.create"},
		{Pattern: "GET /{owner}/{repo}/tags/{tag}", Op: "tag.get"},
		{Pattern: "POST /{owner}/{repo}/tags/{tag}/delete", Op: "tag.delete"},
		{Pattern: "GET /llms.txt", Op: "api.llms.get"},
		{Pattern: "GET /llms-full.txt", Op: "api.llmsfull.get"},
		{Pattern: "GET /settings/keys", Op: "auth.sshkey.list"},
		{Pattern: "POST /settings/keys", Op: "auth.sshkey.add"},
		{Pattern: "POST /settings/keys/{id}/delete", Op: "auth.sshkey.remove"},
		{Pattern: "GET " + keysExportPath, Op: "auth.sshkey.keys"},
		{Pattern: "GET /search", Op: "search.code"},
		{Pattern: "GET /search/repos", Op: "search.repos"},
		{Pattern: "GET /search/users", Op: "search.users"},
		{Pattern: "GET /explore/code", Op: "search.code", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/search", Op: "search.code", PageOnly: true},
		{Pattern: "GET /healthz"},
		{Pattern: "GET /readyz"},
		{Pattern: "POST /-/mcp"},
		{Pattern: "GET /-/mcp"},
		{Pattern: "DELETE /-/mcp"},
		{Pattern: "GET /tasks", Op: "intent.list"},
		{Pattern: "GET /tasks/new", PageOnly: true},
		{Pattern: "GET /{owner}/{repo}/tasks", Op: "intent.list"},
		{Pattern: "GET /{owner}/{repo}/tasks/new", PageOnly: true},
		{Pattern: "POST /{owner}/{repo}/tasks/new", Op: "intent.create"},
		{Pattern: "GET /{owner}/{repo}/tasks/{number}", Op: "intent.get"},
		{Pattern: "POST /{owner}/{repo}/tasks/{number}/claim", Op: "intent.lease.create"},
		{Pattern: "POST /{owner}/{repo}/tasks/{number}/claim/renew", Op: "intent.lease.update"},
		{Pattern: "POST /{owner}/{repo}/tasks/{number}/release", Op: "intent.lease.delete"},
		{Pattern: "POST /{owner}/{repo}/tasks/{number}/state", Op: "intent.state.set"},
		{Pattern: "POST /{owner}/{repo}/tasks/{number}/edit", Op: "intent.update"},
		{Pattern: "POST /{owner}/{repo}/tasks/{number}/changes", Op: "intent.change.add"},
		{Pattern: "POST /{owner}/{repo}/tasks/{number}/changes/{change}/remove", Op: "intent.change.remove"},
		{Pattern: "POST /{owner}/{repo}/tasks/{number}/asks", Op: "ask.create"},
		{Pattern: "GET /{owner}/{repo}/asks", Op: "ask.list"},
		{Pattern: "GET /{owner}/{repo}/asks/{id}", Op: "ask.get"},
		{Pattern: "POST /{owner}/{repo}/asks/{id}/answer", Op: "ask.answer.create"},
		{Pattern: "POST /{owner}/{repo}/asks/{id}/cancel", Op: "ask.update"},
	}
}

func Reserved() []string {
	set := map[string]bool{}
	for _, name := range reserved.Names() {
		set[name] = true
	}
	for _, r := range Routes() {
		trimmed := strings.TrimPrefix(r.Pattern, "GET ")
		trimmed = strings.TrimPrefix(trimmed, "POST ")
		trimmed = strings.TrimPrefix(trimmed, "/")
		trimmed = strings.TrimPrefix(trimmed, "-/")
		if first, _, _ := strings.Cut(trimmed, "/"); first != "" && first != "{owner}" {
			set[first] = true
		}
	}
	return slices.Sorted(maps.Keys(set))
}

func StripGitSuffix(repo string) (string, bool) {
	if strings.HasSuffix(repo, ".git") && len(repo) > 4 {
		return strings.TrimSuffix(repo, ".git"), true
	}
	return repo, false
}

func CanonicalRange(r string) string {
	if before, after, ok := strings.Cut(r, "..."); ok {
		return before + "::" + after
	}
	return r
}
