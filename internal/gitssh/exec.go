package gitssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/githttp"
	"github.com/gosuda/rukie/internal/op"
	"github.com/gosuda/rukie/internal/ruk"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

const (
	defaultUploadCap   = 10 * time.Minute
	defaultReceiveCap  = 5 * time.Minute
	stdoutCredit       = 1 << 20
	uploadServiceName  = "upload-pack"
	receiveServiceName = "receive-pack"
)

type gitCommand struct {
	service string
	owner   string
	repo    string
	receive bool
}

func parseGitCommand(raw string) (gitCommand, error) {
	var zero gitCommand
	svc, arg, ok := strings.Cut(strings.TrimSpace(raw), " ")
	if !ok {
		return zero, fmt.Errorf("invalid git command")
	}
	var service string
	var receive bool
	switch svc {
	case "git-upload-pack":
		service = uploadServiceName
	case "git-receive-pack":
		service = receiveServiceName
		receive = true
	default:
		return zero, fmt.Errorf("invalid git command")
	}
	path, err := unquotePath(strings.TrimSpace(arg))
	if err != nil {
		return zero, err
	}
	path = strings.TrimPrefix(strings.TrimPrefix(path, "/~/"), "~/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	owner, repo, ok := githttp.RouteGitPath(path)
	if !ok {
		return zero, fmt.Errorf("invalid git command")
	}
	return gitCommand{service: service, owner: owner, repo: repo, receive: receive}, nil
}

func unquotePath(arg string) (string, error) {
	if rest, ok := strings.CutPrefix(arg, "'"); ok {
		inner, ok := strings.CutSuffix(rest, "'")
		if !ok || strings.Contains(inner, "'") || inner == "" {
			return "", fmt.Errorf("invalid git command")
		}
		return inner, nil
	}
	if rest, ok := strings.CutPrefix(arg, "\""); ok {
		inner, ok := strings.CutSuffix(rest, "\"")
		if !ok || strings.ContainsAny(inner, "\"\n\r") || inner == "" {
			return "", fmt.Errorf("invalid git command")
		}
		return strings.ReplaceAll(inner, `\\`, `\`), nil
	}
	if arg == "" || strings.ContainsAny(arg, " \t\n\r'\";|&$`\\") {
		return "", fmt.Errorf("invalid git command")
	}
	return arg, nil
}

func (s *Server) serveExec(ctx context.Context, perms *ssh.Permissions, channel ssh.Channel, payload []byte, live *sessionLive) {
	live.touch()
	defer live.touch()
	var msg struct {
		Command string
	}
	if err := ssh.Unmarshal(payload, &msg); err != nil {
		failExec(channel, 1, "invalid git command")
		return
	}
	cmd, err := parseGitCommand(msg.Command)
	if err != nil {
		failExec(channel, 1, "invalid git command")
		return
	}
	caller, keyID, ok := sessionCaller(perms)
	if !ok {
		failExec(channel, 1, "Permission denied (publickey)")
		return
	}
	s.auth.TouchSSHKey(ctx, keyID, s.clock())
	repoID, found := s.git.ResolveRepoID(ctx, cmd.owner, cmd.repo)
	if !found {
		failExec(channel, 1, "repository not found")
		return
	}
	decision := access.Check(s.resolver, caller, "repo.clone", access.Resource{
		Kind:   access.KindRepo,
		RepoID: repoID,
	})
	if decision.NotFound {
		failExec(channel, 1, "repository not found")
		return
	}
	if !decision.Allow {
		if cmd.receive {
			failExec(channel, 1, "push access is required")
			return
		}
		failExec(channel, 1, "read access is required")
		return
	}
	path := githttp.StorePathFor(s.repoRoot, cmd.owner, cmd.repo)
	if _, statErr := os.Stat(path); statErr != nil {
		failExec(channel, 1, "repository not found")
		return
	}
	budget := defaultUploadCap
	if cmd.receive {
		budget = defaultReceiveCap
	}
	runCtx, stop := context.WithTimeout(ctx, budget)
	defer stop()
	client, err := s.clientFor(runCtx)
	if err != nil {
		failExec(channel, 128, "daemon is unavailable")
		return
	}
	advertise := wire.ServiceOpen{
		Service:      cmd.service,
		Repository:   path,
		StdoutCredit: stdoutCredit,
		Limits:       s.limits,
		Advertise:    true,
	}
	if cmd.receive {
		session, request, openErr := s.git.OpenPushSession(runCtx, client, path)
		if openErr != nil {
			failExec(channel, 128, "session failed")
			return
		}
		advertise.SessionID = &session
		advertise.RequestID = &request
	} else {
		sessionOnly, _, openErr := s.git.OpenPushSession(runCtx, client, path)
		if openErr != nil {
			failExec(channel, 128, "session failed")
			return
		}
		advertise.SessionID = &sessionOnly
	}
	advReport, advStderr, err := ruk.RunCommand(runCtx, client, ruk.Command{
		Open: advertise, Stdout: channel, HookBudget: s.hookAfter,
	})
	if err != nil {
		failExec(channel, 128, "advertise: "+commandFailure(err, advStderr))
		return
	}
	if advReport.ExitCode != 0 || advReport.Cancelled {
		failExec(channel, 128, "advertise: "+commandFailure(nil, advStderr))
		return
	}
	open := wire.ServiceOpen{
		Service:      cmd.service,
		Repository:   path,
		StdoutCredit: stdoutCredit,
		Limits:       s.limits,
	}
	if cmd.receive {
		session, request, openErr := s.git.OpenPushSession(runCtx, client, path)
		if openErr != nil {
			failExec(channel, 128, "session failed")
			return
		}
		open.SessionID = &session
		open.RequestID = &request
		pushReport, pushStderr, pushErr := ruk.RunCommand(runCtx, client, ruk.Command{
			Open: open, Stdin: io.Reader(channel), Stdout: channel, Hook: s.git.HookHandler(caller, repoID, request), HookBudget: s.hookAfter,
		})
		if pushErr != nil {
			failExec(channel, 128, "round: "+commandFailure(pushErr, pushStderr))
			return
		}
		if pushReport.ExitCode != 0 || pushReport.Cancelled {
			failExec(channel, 128, "round: "+commandFailure(nil, pushStderr))
			return
		}
		sendExit(channel, int(pushReport.ExitCode))
		return
	}
	uploadSession, _, openErr := s.git.OpenPushSession(runCtx, client, path)
	if openErr != nil {
		failExec(channel, 128, "session failed")
		return
	}
	open.SessionID = &uploadSession
	report, stderr, err := ruk.RunCommandStream(runCtx, client, ruk.StreamCommand{
		Open: open, Stdin: io.Reader(channel), Stdout: channel,
	})
	if err != nil {
		failExec(channel, 128, "round: "+commandFailure(err, stderr))
		return
	}
	if report.ExitCode != 0 || report.Cancelled {
		failExec(channel, 128, "round: "+commandFailure(nil, stderr))
		return
	}
	sendExit(channel, int(report.ExitCode))
}

func sessionCaller(perms *ssh.Permissions) (access.Caller, int64, bool) {
	var zero access.Caller
	if perms == nil {
		return zero, 0, false
	}
	userID, err := strconv.ParseInt(perms.Extensions["user-id"], 10, 64)
	if err != nil || userID == 0 {
		return zero, 0, false
	}
	keyID, err := strconv.ParseInt(perms.Extensions["key-id"], 10, 64)
	if err != nil || keyID == 0 {
		return zero, 0, false
	}
	return access.Caller{
		UserID: userID,
		Scopes: op.Scopes(0).Add(op.ScopeRepoRead).Add(op.ScopeRepoWrite),
		Active: true,
		Authed: true,
	}, keyID, true
}

func commandFailure(err error, stderr []byte) string {
	if err == nil {
		if len(stderr) > 0 {
			return firstLine(stderr)
		}
		return "git service failed"
	}
	if rerr, ok := errors.AsType[*ruk.Error](err); ok {
		switch rerr.Code {
		case wire.CodeNotFound:
			return "repository not found"
		case wire.CodeForbidden:
			return "access is required"
		case wire.CodeLimit:
			return "request body is too large"
		case wire.CodeInvalid:
			return "invalid git service"
		}
		if len(stderr) > 0 {
			return firstLine(stderr)
		}
		return "git service failed: " + firstLine([]byte(rerr.Message))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "git service timed out"
	}
	if len(stderr) > 0 {
		return firstLine(stderr)
	}
	return "git service failed: " + firstLine([]byte(err.Error()))
}

func firstLine(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if line, _, ok := strings.Cut(text, "\n"); ok {
		return line
	}
	if text == "" {
		return "git service failed"
	}
	return text
}

func failExec(channel ssh.Channel, code int, msg string) {
	_, _ = io.WriteString(channel.Stderr(), msg+"\n")
	sendExit(channel, code)
}

func sendExit(channel ssh.Channel, code int) {
	if code < 0 || code > 255 {
		code = 128
	}
	_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct {
		Status uint32
	}{Status: uint32(code)}))
}
