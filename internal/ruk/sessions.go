package ruk

import (
	"context"
	"fmt"
	"sync"

	"github.com/gosuda/rukie/internal/ruk/wire"
)

type sessionEntry struct {
	id     string
	secret wire.ReconnectSecret
	active bool
}

type sessionCache struct {
	byRepo map[string]*sessionEntry
	mu     sync.Mutex
}

func newSessionCache() *sessionCache {
	return &sessionCache{byRepo: make(map[string]*sessionEntry)}
}

func (s *sessionCache) get(repo string) (*sessionEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.byRepo[repo]
	return entry, ok
}

func (s *sessionCache) put(repo string, entry *sessionEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byRepo[repo] = entry
}

func (s *sessionCache) drop(repo string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byRepo, repo)
}

func (c *Client) ensureSession(ctx context.Context, repo Repo) (*sessionEntry, error) {
	if entry, ok := c.sessions.get(repo.Path); ok && entry.active {
		return entry, nil
	}
	return c.openSession(ctx, repo)
}

func (c *Client) openSession(ctx context.Context, repo Repo) (*sessionEntry, error) {
	bare := Repo{Path: repo.Path}
	opened, err := SessionOpen(ctx, c, bare, wire.SessionOpenArgs{})
	if err != nil {
		return nil, err
	}
	entry := &sessionEntry{id: opened.Session, secret: opened.ReconnectSecret, active: true}
	c.sessions.put(repo.Path, entry)
	return entry, nil
}

func (c *Client) resumeSession(ctx context.Context, repo Repo, entry *sessionEntry) (*sessionEntry, error) {
	bare := Repo{Path: repo.Path}
	reply, err := SessionResume(ctx, c, bare, wire.SessionResumeArgs{
		Session:         entry.id,
		ReconnectSecret: entry.secret,
	})
	if err != nil {
		return nil, err
	}
	next := &sessionEntry{id: reply.Session, secret: entry.secret, active: reply.Authority.IsActive()}
	c.sessions.put(repo.Path, next)
	return next, nil
}

func (c *Client) recoverSession(ctx context.Context, repo Repo, code wire.Code) (*sessionEntry, error) {
	if code != wire.CodeForbidden && code != wire.CodeNotFound {
		return nil, fmt.Errorf("recoverSession: %w: %s", ErrProtocol, string(code))
	}
	entry, ok := c.sessions.get(repo.Path)
	if !ok {
		return nil, fmt.Errorf("recoverSession: %w: no session", ErrProtocol)
	}
	c.sessions.drop(repo.Path)
	if next, err := c.resumeSession(ctx, repo, entry); err == nil {
		return next, nil
	}
	return c.openSession(ctx, repo)
}
