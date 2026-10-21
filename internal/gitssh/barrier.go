package gitssh

import (
	"context"

	"golang.org/x/crypto/ssh"
)

func (s *Server) serveSessionGuarded(ctx context.Context, perms *ssh.Permissions, channel ssh.Channel, reqs <-chan *ssh.Request, live *sessionLive) {
	defer func() { _ = channel.Close() }()
	for req := range reqs {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		_ = req.Reply(true, nil)
		s.serveExecGuarded(ctx, perms, channel, req.Payload, live)
		return
	}
}

func (s *Server) serveExecGuarded(ctx context.Context, perms *ssh.Permissions, channel ssh.Channel, payload []byte, live *sessionLive) {
	if s.barrier != nil {
		if cmd, perr := parseExecCommand(payload); perr == nil && cmd.receive {
			release, ok := s.barrier()
			if !ok {
				failExec(channel, 128, "backup in progress")
				return
			}
			defer release()
		}
	}
	s.serveExec(ctx, perms, channel, payload, live)
}

func parseExecCommand(payload []byte) (gitCommand, error) {
	var msg struct {
		Command string
	}
	if err := ssh.Unmarshal(payload, &msg); err != nil {
		return gitCommand{}, err
	}
	return parseGitCommand(msg.Command)
}
