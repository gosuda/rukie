package diff

import (
	"strings"
	"testing"
)

const samplePatch = `@@ -1,4 +1,4 @@ pkg
 package main
-func old() {}
+func fresh() {}
 func keep() {}
 context here
`

func TestParseHunks(t *testing.T) {
	f, err := Parse("main.go", []byte(samplePatch))
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("Parse hunks = %d, want 1", len(f.Hunks))
	}
	h := f.Hunks[0]
	if h.OldStart != 1 || h.NewStart != 1 {
		t.Fatalf("hunk starts = %d/%d, want 1/1", h.OldStart, h.NewStart)
	}
	if len(h.Lines) != 5 {
		t.Fatalf("hunk lines = %d, want 5", len(h.Lines))
	}
	if h.Lines[1].Kind != '-' || h.Lines[1].Old != 2 || h.Lines[1].New != 0 {
		t.Fatalf("del line = %+v, want old 2", h.Lines[1])
	}
	if h.Lines[2].Kind != '+' || h.Lines[2].New != 2 || h.Lines[2].Old != 0 {
		t.Fatalf("add line = %+v, want new 2", h.Lines[2])
	}
	if !strings.HasPrefix(f.Anchor, "f-") || len(f.Anchor) != 10 {
		t.Fatalf("anchor = %q, want f- plus 8 hex", f.Anchor)
	}
}

func TestParseCap(t *testing.T) {
	big := make([]byte, MaxFilePatch+1)
	f, err := Parse("big.go", big)
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	if !f.Trunc || len(f.Hunks) != 0 {
		t.Fatalf("oversize file = %+v, want truncated shell", f)
	}
}

func TestParseBadPatch(t *testing.T) {
	if _, err := Parse("x.go", []byte("@@ broken\n")); err == nil {
		t.Fatal("Parse(broken) = nil, want error")
	}
}
