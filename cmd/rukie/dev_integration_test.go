//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/cli"
)

type lockedOutput struct {
	b  bytes.Buffer
	mu sync.Mutex
}

func (l *lockedOutput) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedOutput) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func devRukBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("RUK_BIN"); bin != "" {
		return bin
	}
	return "/home/alpha/rewrite/git-renewed/ruk/target/debug/ruk"
}

func waitDevLine(t *testing.T, out *lockedOutput, prefix string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for line := range strings.Lines(out.String()) {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				return strings.TrimSpace(rest)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no %q line within 30s:\n%s", prefix, out.String())
	return ""
}

func waitDevURL(t *testing.T, base string) {
	t.Helper()
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, base+"/healthz", nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s/healthz never turned 200", base)
}

func TestDevLoopStarts(t *testing.T) {
	bin := devRukBin(t)
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("ruk binary missing at %s: build it first", bin)
	}
	t.Setenv("RUK_BIN", bin)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stdout := &lockedOutput{}
	stderr := &lockedOutput{}
	std := cli.IO{Stdin: strings.NewReader(""), Stdout: stdout, Stderr: stderr}
	done := make(chan int, 1)
	go func() {
		done <- runDevCtx(ctx, std, []string{})
	}()
	base := waitDevLine(t, stdout, "instance: ")
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("instance %q is not a loopback URL", base)
	}
	waitDevURL(t, base)
	ready := waitDevLine(t, stdout, "rukie dev ready in ")
	if ready == "" {
		t.Fatal("empty startup duration")
	}
	t.Logf("dev startup time: %s", ready)
	user := waitDevLine(t, stdout, "user: ")
	if user != "dev" {
		t.Fatalf("user %q, want dev", user)
	}
	password := waitDevLine(t, stdout, "password: ")
	if len(password) < 12 {
		t.Fatal("printed password is too short to pass the policy it was minted under")
	}
	token := waitDevLine(t, stdout, "token: ")
	if !auth.ValidTokenShape(token) {
		t.Fatalf("token %q fails the shape check", token)
	}
	data := waitDevLine(t, stdout, "data: ")
	if data == "" {
		t.Fatal("no data dir printed")
	}
	req, err := http.NewRequest(http.MethodGet, base+"/llms.txt", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Accept", "text/markdown")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("llms.txt: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(string(raw), "# rukie") {
		t.Fatalf("llms.txt status %d body %q", resp.StatusCode, string(raw))
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("dev exit %d:\n%s", code, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("dev did not stop after cancel")
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("ephemeral dir survives: %s", data)
	}
}
