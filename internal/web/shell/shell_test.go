package shell

import (
	"strings"
	"testing"
)

const wantFontsLink = `<link rel="stylesheet" href="https://fonts.bunny.net/css?family=space-grotesk:500,600,700%7Csource-sans-3:400,600%7Cibm-plex-mono:400,500,600&display=swap" referrerpolicy="no-referrer">`

func TestFontsLinkExact(t *testing.T) {
	if FontsLink != wantFontsLink {
		t.Fatalf("FontsLink = %q, want the O24 string", FontsLink)
	}
	if !strings.Contains(string(Fonts()), "referrerpolicy=") {
		t.Fatal("font link lost its referrerpolicy")
	}
}
