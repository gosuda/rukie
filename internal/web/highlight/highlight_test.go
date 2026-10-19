package highlight

import (
	"strings"
	"testing"
)

func TestHighlightGo(t *testing.T) {
	out, err := Code("go", "package main\n\nfunc main() {}\n")
	if err != nil {
		t.Fatalf("Code = %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "<pre") || !strings.Contains(s, "<span") {
		t.Fatalf("highlight missing spans: %q", s)
	}
	if !strings.Contains(s, "chroma") {
		t.Fatalf("highlight missing CSS classes: %q", s)
	}
}

func TestHighlightUnknownLexer(t *testing.T) {
	out, err := Code("not-a-language", "plain <b>text</b>\n")
	if err != nil {
		t.Fatalf("Code = %v", err)
	}
	if !strings.Contains(out.String(), "plain") {
		t.Fatalf("fallback lost source: %q", out.String())
	}
}

func TestHighlightCap(t *testing.T) {
	big := strings.Repeat("x", MaxHighlightBytes+1)
	if _, err := Code("go", big); err == nil {
		t.Fatal("Code(oversize) = nil, want cap error")
	}
}
