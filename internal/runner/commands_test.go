package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSecretFileModes(t *testing.T) {
	dir := t.TempDir()
	open := filepath.Join(dir, "open.txt")
	if err := os.WriteFile(open, []byte("rkt_x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(open, strings.NewReader("")); err == nil {
		t.Fatal("group-readable secret file was accepted")
	}
	closed := filepath.Join(dir, "closed.txt")
	if err := os.WriteFile(closed, []byte("  rkt_y  \n"), 0600); err != nil {
		t.Fatal(err)
	}
	secret, err := readSecretFile(closed, strings.NewReader(""))
	if err != nil || secret != "rkt_y" {
		t.Fatalf("secret = %q, %v", secret, err)
	}
	secret, err = readSecretFile("-", strings.NewReader("rkt_z\n"))
	if err != nil || secret != "rkt_z" {
		t.Fatalf("stdin secret = %q, %v", secret, err)
	}
	if _, err := readSecretFile(filepath.Join(dir, "missing"), strings.NewReader("")); err == nil {
		t.Fatal("missing secret file was accepted")
	}
}

func TestRegisterRejectsBareToken(t *testing.T) {
	out, errOut := &strings.Builder{}, &strings.Builder{}
	code := Main(IO{Stdout: out, Stderr: errOut}, []string{"register", "--server", "http://x", "--token", "sekret"})
	if code != ExitUsage {
		t.Fatalf("deprecated token exit = %d, want 64", code)
	}
	if !strings.Contains(errOut.String(), "--token-file") {
		t.Fatalf("deprecation message misses the file form: %s", errOut.String())
	}
}
