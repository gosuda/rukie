package view

import (
	"bytes"
	"net/http"
	"strings"
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
	e := buildRenderEnv(t)
	pages := collectHTMLPages(t, e)
	assertHTMLCoverage(t, e, pages)
	seen := map[string]bool{}
	for _, p := range pages {
		if seen[p.File] {
			t.Fatalf("duplicate dump file %s", p.File)
		}
		seen[p.File] = true
		body := []byte(p.Body)
		if err := zerojs.Assert(body); err != nil {
			t.Fatalf("%s: %v", p.File, err)
		}
		if p.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("%s missing CSP", p.File)
		}
		if p.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s XCTO = %q", p.File, p.Header.Get("X-Content-Type-Options"))
		}
		if !strings.Contains(p.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s content-type = %q", p.File, p.Header.Get("Content-Type"))
		}
		switch p.File {
		case "api-index.html", "unknown-user.html":
		default:
			if !bytes.Contains(body, []byte(`fonts.bunny.net`)) {
				t.Fatalf("%s missing the font link", p.File)
			}
		}
		if p.Check != nil {
			p.Check(t, p.Body)
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
