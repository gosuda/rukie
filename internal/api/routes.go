package api

import (
	"maps"
	"slices"
	"strings"

	"github.com/gosuda/rukie/internal/auth"
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
		{Pattern: "GET /healthz"},
		{Pattern: "GET /readyz"},
		{Pattern: "POST /-/mcp"},
		{Pattern: "GET /-/mcp"},
		{Pattern: "DELETE /-/mcp"},
	}
}

func Reserved() []string {
	set := map[string]bool{
		"healthz": true, "readyz": true, "llms.txt": true, "llms-full.txt": true,
		"ops": true, "problems": true, "mcp": true, "openapi.json": true,
		"static": true, "session": true, "account": true, "explore": true,
		"favicon.ico": true, "robots.txt": true, ".well-known": true,
		"search": true, "inbox": true, "changes": true, "tasks": true, "ci": true,
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
