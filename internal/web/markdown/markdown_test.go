package markdown

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	out, err := Render([]byte("# Hello\n\nSay `hi` to **rukie**.\n"))
	if err != nil {
		t.Fatalf("Render = %v", err)
	}
	s := out.String()
	for _, want := range []string{"<h1>Hello</h1>", "<code>hi</code>", "<strong>rukie</strong>"} {
		if !strings.Contains(s, want) {
			t.Fatalf("Render missing %q in %q", want, s)
		}
	}
}

func TestRenderKillsRawHTML(t *testing.T) {
	out, err := Render([]byte("x <script>alert(1)</script> y\n"))
	if err != nil {
		t.Fatalf("Render = %v", err)
	}
	if strings.Contains(out.String(), "<script>") {
		t.Fatalf("raw HTML survived: %q", out.String())
	}
}

func TestRenderTables(t *testing.T) {
	out, err := Render([]byte("| a | b |\n|---|--:|\n| 1 | 2 |\n"))
	if err != nil {
		t.Fatalf("Render = %v", err)
	}
	if !strings.Contains(out.String(), "<table>") {
		t.Fatalf("GFM table missing: %q", out.String())
	}
	if strings.Contains(out.String(), "style=") {
		t.Fatalf("cell style attribute survived: %q", out.String())
	}
}
