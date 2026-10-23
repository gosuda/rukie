package main

import (
	"cmp"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/gosuda/rukie/internal/access"
	"github.com/gosuda/rukie/internal/auth"
	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/events"
	"github.com/gosuda/rukie/internal/gitssh"
	"github.com/gosuda/rukie/internal/ruk/wire"
	"github.com/gosuda/rukie/internal/store"
)

const sshKeepaliveInterval = 60 * time.Second

func setupSSH(cfg config.Config, authSvc *auth.Service, limiter *access.Limiter, st *store.Store, spool *events.Spool, barrier func() (release func(), ok bool)) (*gitssh.Server, error) {
	if !cfg.SSH.Enabled {
		return nil, nil
	}
	keys, err := loadSSHHostKeys(cfg.SSH.HostKeyFiles)
	if err != nil {
		return nil, err
	}
	return gitssh.New(gitssh.Config{
		Auth:              authSvc,
		Limiter:           limiter,
		SocketPath:        cmp.Or(cfg.Ruk.Socket, cfg.Ruk.SocketPath),
		RepoRoot:          cfg.Ruk.RepoRoot,
		Limits:            wire.DefaultGitLimits(),
		Feed:              events.OpenHookSink(st, spool, time.Now),
		Store:             st,
		Clock:             time.Now,
		HostKeys:          keys,
		MaxSessions:       cfg.SSH.MaxSessions,
		IdleTimeout:       time.Duration(cfg.SSH.IdleTimeoutMs) * time.Millisecond,
		KeepaliveInterval: sshKeepaliveInterval,
		Barrier:           barrier,
	})
}

func loadSSHHostKeys(files []string) ([]ssh.Signer, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("serve: ssh.enabled requires ssh.host_key_files")
	}
	keys := make([]ssh.Signer, 0, len(files))
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			return nil, fmt.Errorf("serve: ssh host key %s: %w", name, err)
		}
		key, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("serve: ssh host key %s: %w", name, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func generateSSHHostKey(path string) (string, error) {
	return encodeSSHHostKey(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
}

func rotateSSHHostKey(path string) (string, error) {
	return encodeSSHHostKey(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
}

func encodeSSHHostKey(path string, flag int) (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return "", err
	}
	out, err := os.OpenFile(filepath.Clean(path), flag, 0o600)
	if err != nil {
		return "", err
	}
	if err := pem.Encode(out, block); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(filepath.Clean(path), 0o600); err != nil {
		return "", err
	}
	return ssh.FingerprintSHA256(signer.PublicKey()), nil
}

func serveSSHListeners(ctx context.Context, srv *gitssh.Server, addrs []string) ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			for _, open := range listeners {
				_ = open.Close()
			}
			return nil, err
		}
		listeners = append(listeners, ln)
		go func() {
			_ = srv.Serve(ctx, ln)
		}()
	}
	return listeners, nil
}
