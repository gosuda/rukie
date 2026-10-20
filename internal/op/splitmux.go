package op

import (
	"fmt"
	"net/http"
	"strings"
)

func firstPathSegment(path string) string {
	trimmed := strings.TrimPrefix(path, "/")
	if seg, _, ok := strings.Cut(trimmed, "/"); ok {
		return seg
	}
	return trimmed
}

func isOwnerPattern(pattern string) bool {
	path := pattern
	if _, rest, ok := strings.Cut(pattern, " "); ok {
		path = rest
	}
	return strings.HasPrefix(firstPathSegment(path), "{")
}

func splitMuxMessage(pattern, seg string) string {
	return fmt.Sprintf("op: route %q first segment %q is not a reserved site name; reserved names never reach owner handlers", pattern, seg)
}

type splitMux struct {
	site   *http.ServeMux
	owner  *http.ServeMux
	isSite func(string) bool
}

func newSplitMux(isSite func(string) bool) *splitMux {
	return &splitMux{site: http.NewServeMux(), owner: http.NewServeMux(), isSite: isSite}
}

func (m *splitMux) route(pattern string) *http.ServeMux {
	if m.isSite == nil {
		return m.site
	}
	if isOwnerPattern(pattern) {
		return m.owner
	}
	path := pattern
	if _, rest, ok := strings.Cut(pattern, " "); ok {
		path = rest
	}
	if seg := firstPathSegment(path); !m.isSite(seg) {
		panic(splitMuxMessage(pattern, seg))
	}
	return m.site
}

func (m *splitMux) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	m.route(pattern).HandleFunc(pattern, handler)
}

func (m *splitMux) Handle(pattern string, handler http.Handler) {
	m.route(pattern).Handle(pattern, handler)
}

func (m *splitMux) forRequest(path string) *http.ServeMux {
	if m.isSite != nil && !m.isSite(firstPathSegment(path)) {
		return m.owner
	}
	if m.isSite == nil {
		return m.site
	}
	return m.site
}

func (m *splitMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.forRequest(r.URL.Path).ServeHTTP(w, r)
}
