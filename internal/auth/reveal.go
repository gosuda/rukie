package auth

import (
	"maps"
	"sync"
	"time"
)

const revealTTL = 5 * time.Minute

type reveal struct {
	expires time.Time
	secret  string
}

type RevealCache struct {
	items map[string]reveal
	now   func() time.Time
	mu    sync.Mutex
}

func NewRevealCache() *RevealCache {
	return &RevealCache{items: map[string]reveal{}, now: time.Now}
}

func (c *RevealCache) Store(prefix, secret string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	maps.DeleteFunc(c.items, func(_ string, v reveal) bool {
		return !now.Before(v.expires)
	})
	c.items[prefix] = reveal{secret: secret, expires: now.Add(revealTTL)}
}

func (c *RevealCache) Take(prefix string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[prefix]
	if !ok {
		return "", false
	}
	delete(c.items, prefix)
	if !c.now().Before(v.expires) {
		return "", false
	}
	return v.secret, true
}
