package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/cli"
	"github.com/gosuda/rukie/internal/store"
)

func devTestIO() (cli.IO, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return cli.IO{Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr}, stdout, stderr
}

func devTokenLine(t *testing.T, stdout *bytes.Buffer) string {
	t.Helper()
	for line := range strings.Lines(stdout.String()) {
		if rest, ok := strings.CutPrefix(line, "token: "); ok {
			token := strings.TrimSpace(rest)
			if !auth.ValidTokenShape(token) {
				t.Fatalf("token %q fails the shape check", token)
			}
			return token
		}
	}
	t.Fatal("no token line printed")
	return ""
}

func TestDevResetRemovesDir(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	marker := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	std, stdout, _ := devTestIO()
	if code := runDevCtx(ctx, std, []string{"--dir", dir, "reset"}); code != 0 {
		t.Fatalf("reset exit %d", code)
	}
	if !strings.Contains(stdout.String(), "removed "+dir) {
		t.Fatalf("reset output = %q", stdout.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir survives reset: %v", err)
	}
	std, stdout, _ = devTestIO()
	if code := runDevCtx(ctx, std, []string{"--dir", dir, "reset"}); code != 0 {
		t.Fatalf("second reset exit %d", code)
	}
	if !strings.Contains(stdout.String(), "is absent") {
		t.Fatalf("second reset output = %q", stdout.String())
	}
}

func TestDevSeedPrintsCredentials(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "seeded")
	std, stdout, _ := devTestIO()
	if code := runDevCtx(ctx, std, []string{"--dir", dir, "seed"}); code != 0 {
		t.Fatalf("seed exit %d", code)
	}
	body := stdout.String()
	for _, want := range []string{"user: dev", "password: ", "token: rk_", "data: " + dir} {
		if !strings.Contains(body, want) {
			t.Fatalf("seed output misses %q: %q", want, body)
		}
	}
	first := devTokenLine(t, stdout)
	std, stdout, _ = devTestIO()
	if code := runDevCtx(ctx, std, []string{"--dir", dir, "seed"}); code != 0 {
		t.Fatalf("second seed exit %d", code)
	}
	second := devTokenLine(t, stdout)
	if first == second {
		t.Fatal("reseed must rotate the printed token")
	}
	st, err := store.Open(ctx, store.Options{Path: filepath.Join(dir, "meta.sqlite")})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	defer func() {
		_ = st.Close(context.Background())
	}()
	svc := auth.New(st)
	if _, err := svc.GetUser(ctx, "dev"); err != nil {
		t.Fatalf("dev user missing after two seeds: %v", err)
	}
	if _, err := svc.BootstrapAdmin(ctx, "dev", "Aa1!xxxxxxxxxxxxxxxx", "127.0.0.1"); err == nil {
		t.Fatal("a second dev user must not bootstrap")
	} else if !strings.Contains(err.Error(), "taken") && !strings.Contains(err.Error(), "admin already exists") {
		t.Fatalf("err = %v, want the duplicate report", err)
	}
}

func TestDevSeedHookRuns(t *testing.T) {
	ctx := t.Context()
	saved := devSeedExtras
	t.Cleanup(func() { devSeedExtras = saved })
	var mu sync.Mutex
	calls := 0
	devSeedExtras = []func(context.Context, string) error{
		func(_ context.Context, dir string) error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if dir == "" {
				t.Error("hook got an empty dir")
			}
			return nil
		},
	}
	dir := filepath.Join(t.TempDir(), "hooked")
	std, _, _ := devTestIO()
	if code := runDevCtx(ctx, std, []string{"--dir", dir, "seed"}); code != 0 {
		t.Fatalf("seed exit %d", code)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("hook ran %d times, want 1", calls)
	}
}

func TestDevServeRefusesBadBinary(t *testing.T) {
	ctx := t.Context()
	dir := filepath.Join(t.TempDir(), "nobin")
	std, _, stderr := devTestIO()
	if code := runDevCtx(ctx, std, []string{"--dir", dir, "--ruk-bin", "/nonexistent-ruk-bin-xyz"}); code == 0 {
		t.Fatal("serve with a bad binary must fail")
	}
	if !strings.Contains(stderr.String(), "ruk binary") && !strings.Contains(stderr.String(), "daemon") {
		t.Fatalf("stderr = %q, want the binary report", stderr.String())
	}
}

func TestDevSubcommandsNeedDir(t *testing.T) {
	ctx := t.Context()
	for _, argv := range [][]string{{"seed"}, {"reset"}, {"bogus"}, {"--dir"}, {"--dir", "--addr", "127.0.0.1:0"}} {
		std, _, _ := devTestIO()
		if code := runDevCtx(ctx, std, argv); code != 64 {
			t.Fatalf("%v exit %d, want 64", argv, code)
		}
	}
	std, stdout, _ := devTestIO()
	if code := runDevCtx(ctx, std, []string{"--help"}); code != 0 {
		t.Fatalf("help exit %d", code)
	}
	if !strings.Contains(stdout.String(), "reset|seed") {
		t.Fatalf("help = %q", stdout.String())
	}
}
