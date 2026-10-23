package api

import (
	"testing"

	"github.com/gosuda/rukie/internal/ruk"
)

func TestPageRefsUsesLastReturnedAsCursor(t *testing.T) {
	entries := []ruk.RefEntry{
		{Name: "refs/heads/a"},
		{Name: "refs/heads/b"},
		{Name: "refs/heads/c"},
	}
	shown, next := pageRefs(entries, headsPrefix, 1, "refs/heads/b")
	if len(shown) != 1 || shown[0].Name != "refs/heads/a" {
		t.Fatalf("shown refs = %v, want only refs/heads/a", shown)
	}
	if next != "refs/heads/a" {
		t.Fatalf("next = %q, want last returned ref %q", next, "refs/heads/a")
	}
}
