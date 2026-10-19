package view

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/gosuda/rukie/internal/web/secheaders"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

func renderRoute(t *testing.T, r Route) (http.Header, []byte) {
	t.Helper()
	h := http.Header{}
	var buf bytes.Buffer
	if err := r.Render(h, &buf); err != nil {
		t.Fatalf("Render %s %s = %v", r.Method, r.Path, err)
	}
	return h, buf.Bytes()
}

func TestRenderedPagesNoScript(t *testing.T) {
	for _, r := range Routes() {
		_, body := renderRoute(t, r)
		if err := zerojs.Assert(body); err != nil {
			t.Fatalf("%s %s failed zero-JS: %v", r.Method, r.Path, err)
		}
	}
}

func TestRenderedPagesSecurityHeaders(t *testing.T) {
	for _, r := range Routes() {
		h, _ := renderRoute(t, r)
		if got := h.Get("Content-Security-Policy"); got != secheaders.CSP {
			t.Fatalf("%s CSP = %q", r.Path, got)
		}
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s XCTO = %q", r.Path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
			t.Fatalf("%s referrer = %q", r.Path, got)
		}
		if got := h.Get("Content-Type"); got != "text/html; charset=utf-8" {
			t.Fatalf("%s content-type = %q", r.Path, got)
		}
		if got := h.Get("Vary"); got != "Accept" {
			t.Fatalf("%s vary = %q", r.Path, got)
		}
	}
}

func TestAllHTMLRoutesZeroJS(t *testing.T) {
	routes := Routes()
	if len(routes) == 0 {
		t.Fatal("no HTML routes registered")
	}
	seen := map[string]bool{}
	for _, r := range routes {
		if seen[r.Path] {
			t.Fatalf("duplicate route %s", r.Path)
		}
		seen[r.Path] = true
		h, body := renderRoute(t, r)
		if err := zerojs.Assert(body); err != nil {
			t.Fatalf("%s %s: %v", r.Method, r.Path, err)
		}
		if h.Get("Content-Security-Policy") != secheaders.CSP {
			t.Fatalf("%s %s missing exact CSP", r.Method, r.Path)
		}
		if !bytes.Contains(body, []byte(`fonts.bunny.net`)) {
			t.Fatalf("%s %s missing the font link", r.Method, r.Path)
		}
	}
}

func TestRefreshFloor(t *testing.T) {
	c := sampleCtx()
	c.Refresh = 14
	h := http.Header{}
	var buf bytes.Buffer
	if err := WriteAPIIndex(h, &buf, c, sampleOps()); err == nil {
		t.Fatal("WriteAPIIndex(refresh=14) = nil, want floor error")
	}
	c.Refresh = 30
	h = http.Header{}
	buf.Reset()
	if err := WriteAPIIndex(h, &buf, c, sampleOps()); err != nil {
		t.Fatalf("WriteAPIIndex(refresh=30) = %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`http-equiv="refresh"`)) {
		t.Fatal("refresh=30 missing the meta refresh tag")
	}
}
