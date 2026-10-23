package ruk

import (
	"sync"
)

type handleCache struct {
	byPath map[string]string
	mu     sync.Mutex
}

func newHandleCache() *handleCache {
	return &handleCache{byPath: make(map[string]string)}
}

func (h *handleCache) get(path string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	handle, ok := h.byPath[path]
	return handle, ok
}

func (h *handleCache) put(path, handle string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.byPath[path] = handle
}

func (h *handleCache) drop(path string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.byPath, path)
}
