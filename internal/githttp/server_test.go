package githttp

import (
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClientIPIgnoresForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.RemoteAddr = "192.0.2.5:54321"
	r.Header.Set("X-Forwarded-For", "198.51.100.77")
	if got := clientIP(r); got != "192.0.2.5" {
		t.Fatalf("clientIP = %q, want peer address", got)
	}
}

func TestParseRouteTable(t *testing.T) {
	cases := []struct {
		path  string
		owner string
		repo  string
		kind  routeKind
	}{
		{"/ada/hello.git/info/refs", "ada", "hello", routeInfoRefs},
		{"/ada/hello.git/info/refs/", "ada", "hello", routeInfoRefs},
		{"/ada/hello.git/git-upload-pack", "ada", "hello", routeUploadPack},
		{"/ada/hello.git/git-receive-pack", "ada", "hello", routeReceivePack},
		{"/ada/hello.git", "ada", "hello", routeBare},
		{"/ada/hello.git/", "ada", "hello", routeBare},
		{"/a-d_0.9/x.y-z_9.git/info/refs", "a-d_0.9", "x.y-z_9", routeInfoRefs},
		{"", "", "", routeNone},
		{"/", "", "", routeNone},
		{"/ada", "", "", routeNone},
		{"/ada/hello/info/refs", "", "", routeNone},
		{"/ada/hello.git/info/pack", "", "", routeNone},
		{"/ada/hello.git/git-upload-archive", "", "", routeNone},
		{"/ada/hello.git/extra/deep", "", "", routeNone},
		{"/Ada/hello.git/info/refs", "", "", routeNone},
		{"/-bad/hello.git/info/refs", "", "", routeNone},
		{"/ada/.git/info/refs", "", "", routeNone},
		{"/ada/.git", "", "", routeNone},
		{"/ada/.github.git/info/refs", "ada", ".github", routeInfoRefs},
		{"/ada/a.git/info/refs", "ada", "a", routeInfoRefs},
		{"/healthz", "", "", routeNone},
		{"/admin/hello.git/info/refs", "", "", routeNone},
		{"/mcp/hello.git/info/refs", "", "", routeNone},
		{"/ada/hello.git//", "", "", routeNone},
	}
	for _, tc := range cases {
		got := parseRoute(tc.path)
		if got.owner != tc.owner || got.repo != tc.repo || got.kind != tc.kind {
			t.Fatalf("parseRoute(%q) = %+v, want owner %q repo %q kind %d", tc.path, got, tc.owner, tc.repo, tc.kind)
		}
		if _, _, ok := RouteGitPath(tc.path); ok != (tc.kind != routeNone) {
			t.Fatalf("RouteGitPath(%q) ok = %v", tc.path, ok)
		}
	}
}

func TestTruncateReasonBoundaries(t *testing.T) {
	short := "change review is not available"
	if truncateReason(short) != short {
		t.Fatal("short reason changed")
	}
	exact := strings.Repeat("a", maxReasonBytes)
	if truncateReason(exact) != exact {
		t.Fatal("512-byte reason changed")
	}
	over := strings.Repeat("a", maxReasonBytes+1)
	if got := truncateReason(over); len(got) != maxReasonBytes {
		t.Fatalf("long reason = %d bytes", len(got))
	}
	multi := strings.Repeat("é", 300)
	got := truncateReason(multi)
	if len(got) > maxReasonBytes {
		t.Fatalf("multibyte reason = %d bytes", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a character")
	}
}

func TestIsRefsFor(t *testing.T) {
	for _, name := range []string{"refs/for", "refs/for/main", "refs/for/a/b"} {
		if !isRefsFor(name) {
			t.Fatalf("%s is not refs/for", name)
		}
	}
	for _, name := range []string{"refs/forx", "refs/heads/for", "refs/heads/main", "refs/changes/1"} {
		if isRefsFor(name) {
			t.Fatalf("%s misclassified as refs/for", name)
		}
	}
}
