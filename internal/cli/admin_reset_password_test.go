package cli_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gosuda/rukie/internal/auth"
	mcpsdk "github.com/gosuda/rukie/internal/mcp"
	"github.com/gosuda/rukie/internal/op"
)

type stubAdminInvoker struct{}

func (stubAdminInvoker) Invoke(_ context.Context, _ *http.Request) (*op.Response, error) {
	return nil, fmt.Errorf("stub invoker never serves calls")
}

func resetTarget(t *testing.T, e *cliEnv) (tokenPath, target string) {
	t.Helper()
	pw := testPassword(t)
	bootstrapUser(t, e, "resetadmin", pw)
	if _, err := e.svc.CreateUser(t.Context(), 1, auth.CreateUserInput{Name: "resetvictim", Password: pw}, "127.0.0.1"); err != nil {
		t.Fatalf("create target: %v", err)
	}
	minted, err := e.svc.CreateToken(t.Context(), auth.CreateTokenInput{UserID: 1, Name: "reset", Scopes: op.Scopes(0).Add(op.ScopeAdminWrite)})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	tokenPath = filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(minted.Secret+"\n"), 0o600); err != nil {
		t.Fatalf("token file: %v", err)
	}
	return tokenPath, "resetvictim"
}

func TestAdminResetPasswordCommand(t *testing.T) {
	t.Run("hyphenated-words-reach-the-op", func(t *testing.T) {
		e := buildCLIEnv(t)
		tokenPath, target := resetTarget(t, e)
		c := newIO()
		argv := []string{"admin", "user", "reset-password", "--user", target, "--random", "--server", e.url, "--token-file", tokenPath, "--json"}
		if code := runCLI(t, argv, c); code != 0 {
			t.Fatalf("exit %d, want 0: %s", code, c.stderr.String())
		}
		decoded := decodeStdout(t, c)
		temp, _ := decoded["temp_password"].(string)
		if temp == "" {
			t.Fatalf("temp_password missing: %v", decoded)
		}
	})

	t.Run("legacy-spelling-is-unknown", func(t *testing.T) {
		e := buildCLIEnv(t)
		tokenPath, target := resetTarget(t, e)
		legacy := "reset" + "password"
		c := newIO()
		argv := []string{"admin", "user", legacy, "--user", target, "--random", "--server", e.url, "--token-file", tokenPath, "--json"}
		if code := runCLI(t, argv, c); code != 64 {
			t.Fatalf("exit %d, want 64: %s", code, c.stderr.String())
		}
		if !strings.Contains(c.stderr.String(), "unknown command") {
			t.Fatalf("stderr = %q, want an unknown-command report", c.stderr.String())
		}
	})

	t.Run("explicit-flag-beats-positional", func(t *testing.T) {
		e := buildCLIEnv(t)
		_, target := resetTarget(t, e)
		root, err := op.BuildCommands(e.server.Specs(), e.server.Document())
		if err != nil {
			t.Fatalf("commands: %v", err)
		}
		inv, err := op.Parse(root, []string{"admin", "user", "reset-password", "--user", target, "--random", "somebody-else"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if inv.Leaf.Spec.Name != "admin.user.reset-password" {
			t.Fatalf("op = %q", inv.Leaf.Spec.Name)
		}
		if inv.Args["user"] != target {
			t.Fatalf("user = %v, want the flag value %q", inv.Args["user"], target)
		}
	})

	t.Run("mcp-tool-name", func(t *testing.T) {
		const opName = "admin.user.reset-password"
		const wantTool = "admin_user_reset-password"
		if got := op.ToolName(opName); got != wantTool {
			t.Fatalf("tool = %q, want %q", got, wantTool)
		}
		if len(wantTool) == 0 || len(wantTool) > 128 {
			t.Fatalf("tool length %d outside the SDK 1..128 bound", len(wantTool))
		}
		for _, r := range wantTool {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
				continue
			}
			t.Fatalf("tool rune %q outside the SDK alphabet", string(r))
		}
		e := buildCLIEnv(t)
		specs := e.server.Specs()
		found := false
		for _, s := range specs {
			if s.Name == opName {
				found = true
			}
		}
		if !found {
			t.Fatalf("op %q not registered", opName)
		}
		srv, err := mcpsdk.NewServer(specs, e.server.Document(), stubAdminInvoker{}, op.ToolFilter{Toolsets: []string{"admin"}}, "")
		if err != nil {
			t.Fatalf("mcp server: %v", err)
		}
		_ = srv
	})
}
