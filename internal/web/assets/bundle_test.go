package assets

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedAppBundle(t *testing.T) {
	b := Bundle()
	if len(b.Bytes) == 0 {
		t.Fatal("embedded app.js is empty")
	}
	if !strings.Contains(string(b.Bytes), "data-rukie-app") {
		t.Fatal("embedded app.js lacks the data-rukie-app hydration marker")
	}
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + BundleURL())
	if err != nil {
		t.Fatalf("GET bundle = %v", err)
	}
	closeBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET bundle status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/javascript" {
		t.Fatalf("Content-Type = %q, want application/javascript", got)
	}
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "immutable") {
		t.Fatalf("Cache-Control = %q, want immutable", got)
	}
	tag := string(HydrateScript())
	if !strings.Contains(tag, BundleURL()) {
		t.Fatalf("hydrate script %q lacks the bundle URL", tag)
	}
}

func TestNoJSAppSource(t *testing.T) {
	root := filepath.Join("..", "..", "..", "web", "src")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		for _, ext := range []string{".res", ".resi", ".res.js"} {
			if strings.HasSuffix(name, ext) {
				return nil
			}
		}
		t.Errorf("web/src holds %q, want .res sources only", path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk web/src = %v", err)
	}
}
