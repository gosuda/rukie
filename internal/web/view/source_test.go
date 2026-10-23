package view

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func webRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Dir(filepath.Dir(file))
}

func templRel(rel string) (string, bool) {
	if filepath.Clean(rel) != rel || strings.Contains(rel, "..") {
		return "", false
	}
	if !strings.HasSuffix(rel, ".templ") {
		return "", false
	}
	return rel, true
}

func collectTempl(root string) ([]string, error) {
	var files []string
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if kept, ok := templRel(rel); ok {
			files = append(files, kept)
		}
		return nil
	})
	return files, walkErr
}

func TestTemplSourcesZeroJS(t *testing.T) {
	root := webRoot(t)
	files, err := collectTempl(root)
	if err != nil {
		t.Fatalf("walk = %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no .templ sources found")
	}
	attrHandler := regexp.MustCompile(`\son[a-z]+\s*=`)
	fsys := os.DirFS(root)
	for _, rel := range files {
		src, readErr := fs.ReadFile(fsys, rel)
		if readErr != nil {
			t.Fatalf("read %s = %v", rel, readErr)
		}
		s := string(src)
		for _, needle := range []string{"<script", "templ.JS", "templ.Once", "javascript:"} {
			if strings.Contains(s, needle) {
				t.Fatalf("%s contains %q", rel, needle)
			}
		}
		if strings.Contains(s, "style=") || strings.Contains(s, "style={") {
			t.Fatalf("%s contains an inline style binding", rel)
		}
		if attrHandler.MatchString(s) {
			t.Fatalf("%s contains an on* attribute", rel)
		}
	}
}
