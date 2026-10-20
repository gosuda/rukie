package api

import (
	"maps"
	"slices"
	"strings"
)

type Route struct {
	Pattern  string
	Op       string
	PageOnly bool
}

func Routes() []Route {
	return []Route{
		{Pattern: "GET /-/api", Op: "api.index"},
		{Pattern: "GET /-/api/openapi.json", Op: "api.openapi"},
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
