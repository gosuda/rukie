package diff

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/web/vm"
	"github.com/gosuda/rukie/internal/web/zerojs"
)

const renderPatch = `@@ -1,4 +1,4 @@ pkg
 package main
-func old() {}
+func fresh() {}
 func keep() {}
`

func TestRenderUnified(t *testing.T) {
	f, err := Parse("main.go", []byte(renderPatch))
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	hl := vm.LineRange{Start: 2, End: 2}
	var buf bytes.Buffer
	if err := FileViewUnified(f, &hl).Render(t.Context(), &buf); err != nil {
		t.Fatalf("Render = %v", err)
	}
	s := buf.String()
	for _, want := range []string{`class="diff unified"`, "tokchg", `id="L2"`, "@@ -1", `id="` + f.Anchor + `"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("unified render missing %q", want)
		}
	}
	if err := zerojs.Assert(buf.Bytes()); err != nil {
		t.Fatalf("unified render failed zero-JS: %v", err)
	}
}

func TestRenderSplit(t *testing.T) {
	f, err := Parse("main.go", []byte(renderPatch))
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	var buf bytes.Buffer
	if err := FileViewSplit(f).Render(t.Context(), &buf); err != nil {
		t.Fatalf("Render = %v", err)
	}
	s := buf.String()
	if !strings.Contains(s, `class="diff split"`) {
		t.Fatalf("split render missing split table: %q", s)
	}
	if !strings.Contains(s, "Before") || !strings.Contains(s, "After") {
		t.Fatalf("split render missing column headers")
	}
	if err := zerojs.Assert(buf.Bytes()); err != nil {
		t.Fatalf("split render failed zero-JS: %v", err)
	}
}

func TestRichPairing(t *testing.T) {
	f, err := Parse("main.go", []byte(renderPatch))
	if err != nil {
		t.Fatalf("Parse = %v", err)
	}
	rich := Rich(f.Hunks[0])
	if len(rich) != 4 {
		t.Fatalf("rich lines = %d, want 4", len(rich))
	}
	if !rich[1].Paired || rich[1].Mid != "old" {
		t.Fatalf("del pairing = %+v, want mid old", rich[1])
	}
	if !rich[2].Paired || rich[2].Mid != "fresh" {
		t.Fatalf("add pairing = %+v, want mid fresh", rich[2])
	}
}
