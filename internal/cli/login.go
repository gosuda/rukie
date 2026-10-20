package cli

import (
	"bytes"
	"cmp"
	"context"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/auth"
)

func runLogin(ctx context.Context, std IO, argv []string) int {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	serverFlag := fs.String("server", "", "server base URL")
	sessionFlag := fs.String("session-file", "", "where to write the session cookie")
	username := fs.String("username", "", "account name")
	secret := auth.RegisterSecretFlags(fs, "password")
	if err := fs.Parse(argv); err != nil {
		emit(std.Stderr, "rukie auth login:", err.Error())
		return 64
	}
	if rest := fs.Args(); len(rest) > 0 {
		emit(std.Stderr, "rukie auth login: too many positional arguments")
		return 64
	}
	serverURL := cmp.Or(*serverFlag, std.getenv("RUKIE_SERVER"))
	if serverURL == "" {
		emit(std.Stderr, "rukie auth login: --server is required")
		return 64
	}
	if *username == "" {
		emit(std.Stderr, "rukie auth login: --username is required")
		return 64
	}
	password, err := secret.Resolve(std.Stdin)
	if err != nil {
		emit(std.Stderr, "rukie auth login:", err.Error())
		return 64
	}
	sessionFile := cmp.Or(*sessionFlag, DefaultSessionFile(std.Getenv))
	body, _ := jsonv2.Marshal(map[string]string{"username": *username, "password": password}, jsonv1.DefaultOptionsV1())
	endpoint := strings.TrimSuffix(serverURL, "/") + "/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		emit(std.Stderr, "rukie auth login:", err.Error())
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		emit(std.Stderr, "rukie auth login:", err.Error())
		return 4
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		emit(std.Stderr, "rukie auth login:", err.Error())
		return 4
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		emit(std.Stderr, "rukie auth login: "+problemDetail(raw))
		return exitForProblem(raw)
	}
	cookie := sessionCookieValue(resp)
	if cookie == "" {
		emit(std.Stderr, "rukie auth login: server did not set a session cookie")
		return 1
	}
	if err := writeSessionFile(sessionFile, cookie); err != nil {
		emit(std.Stderr, "rukie auth login:", err.Error())
		return 1
	}
	if !emitf(std.Stdout, "logged in as %s\n", loginName(raw, *username)) {
		return 1
	}
	return 0
}

func sessionCookieValue(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName() {
			return c.Name + "=" + c.Value
		}
	}
	return ""
}

func loginName(raw []byte, fallback string) string {
	var decoded map[string]any
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv1.DefaultOptionsV1()); err != nil {
		return fallback
	}
	if name, ok := decoded["username"].(string); ok && name != "" {
		return name
	}
	return fallback
}

func writeSessionFile(path, cookie string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	if err := os.WriteFile(path, []byte(cookie+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
