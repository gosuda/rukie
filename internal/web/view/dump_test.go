//go:build render

package view

import (
	"cmp"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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
	for _, r := range Routes() {
		h := http.Header{}
		var buf strings.Builder
		if err := r.Render(h, &buf); err != nil {
			t.Fatalf("Render %s = %v", r.Path, err)
		}
		name := cmp.Or(strings.Trim(r.Path, "/"), "index")
		name = strings.ReplaceAll(name, "/", "_") + ".html"
		if !dumpName.MatchString(name) {
			t.Fatalf("unsafe dump name %q", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(buf.String()), 0o600); err != nil {
			t.Fatalf("WriteFile %s = %v", name, err)
		}
		t.Logf("wrote %s (%d bytes)", name, len(buf.String()))
	}
}
