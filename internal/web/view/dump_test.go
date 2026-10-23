//go:build render

package view

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var dumpName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func TestDumpRenderedPages(t *testing.T) {
	dir := os.Getenv("RUKIE_RENDER_DIR")
	if dir == "" {
		t.Fatal("RUKIE_RENDER_DIR is empty")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("MkdirAll = %v", err)
	}
	e := buildRenderEnv(t)
	pages := collectHTMLPages(t, e)
	assertHTMLCoverage(t, e, pages)
	for _, p := range pages {
		if p.Check != nil {
			p.Check(t, p.Body)
		}
		if !dumpName.MatchString(p.File) {
			t.Fatalf("unsafe dump name %q", p.File)
		}
		if err := os.WriteFile(filepath.Join(dir, p.File), []byte(p.Body), 0o600); err != nil {
			t.Fatalf("WriteFile %s = %v", p.File, err)
		}
		t.Logf("wrote %s (%d bytes)", p.File, len(p.Body))
	}
}
