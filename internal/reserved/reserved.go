package reserved

import (
	"maps"
	"slices"
)

var names = map[string]bool{
	"admin": true, "root": true, "system": true, "rukie": true, "api": true,
	"git": true, "support": true, "abuse": true, "login": true,
	"logout": true, "settings": true, "account": true, "explore": true,
	"ops": true, "problems": true, "mcp": true, "static": true,
	"session": true, "healthz": true, "readyz": true,
	"llms.txt": true, "llms-full.txt": true, "-": true,
	"openapi.json": true, "favicon.ico": true, "robots.txt": true,
	".well-known": true, "new": true, "repos": true,
	"search": true, "inbox": true, "changes": true, "tasks": true,
	"ci": true,
}

func Is(name string) bool {
	return names[name]
}

func Names() []string {
	return slices.Sorted(maps.Keys(names))
}
