package rukproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveBinaryRefusesMissing(t *testing.T) {
	if _, err := ResolveBinary(filepath.Join(t.TempDir(), "no-such-binary")); err == nil {
		t.Fatal("missing explicit binary accepted")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want the not-found report", err)
	}
}

func TestResolveBinaryRefusesDirectory(t *testing.T) {
	if _, err := ResolveBinary(t.TempDir()); err == nil {
		t.Fatal("directory accepted as binary")
	}
}

func TestResolveBinaryAcceptsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ruk")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ResolveBinary(path)
	if err != nil {
		t.Fatalf("existing file refused: %v", err)
	}
	if got != path {
		t.Fatalf("got %q, want %q", got, path)
	}
}

func TestResolveBinaryMissesPATH(t *testing.T) {
	t.Setenv("RUK_BIN", "definitely-not-a-rukie-binary")
	if _, err := ResolveBinary(""); err == nil {
		t.Fatal("unresolvable RUK_BIN accepted")
	}
}
