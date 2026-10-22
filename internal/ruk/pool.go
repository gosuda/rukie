package ruk

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type pool struct {
	client     *Client
	changed    chan struct{}
	conns      []*conn
	ids        atomic.Uint64
	maxConns   int
	maxStreams int
	mu         sync.Mutex
}

func newPool(c *Client, maxConns, maxStreams int) *pool {
	return &pool{
		client:     c,
		changed:    make(chan struct{}),
		maxConns:   maxConns,
		maxStreams: maxStreams,
	}
}

func (p *pool) nextID() uint64 {
	return p.ids.Add(1)
}

func (p *pool) add(pc *conn) {
	pc.maxStreams = p.maxStreams
	// A finished stream frees capacity; waiters in acquire sleep on changed
	// and only a broadcast tells them to look again.
	pc.onFree = p.broadcast
	p.mu.Lock()
	p.conns = append(p.conns, pc)
	p.mu.Unlock()
	p.broadcast()
}

func (p *pool) broadcast() {
	p.mu.Lock()
	old := p.changed
	p.changed = make(chan struct{})
	p.mu.Unlock()
	close(old)
}

func (p *pool) acquire(ctx context.Context) (*conn, *stream, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, false, fmt.Errorf("acquire: %w", err)
		}
		p.mu.Lock()
		var idle, light *conn
		lightLoad := 0
		changed := p.changed
		live := 0
		kept := make([]*conn, 0, len(p.conns))
		for _, pc := range p.conns {
			load, broken := pc.load()
			if broken {
				continue
			}
			kept = append(kept, pc)
			live++
			if load == 0 && idle == nil {
				idle = pc
			}
			if load < p.maxStreams && (light == nil || load < lightLoad) {
				light = pc
				lightLoad = load
			}
		}
		p.conns = kept
		var chosen *conn
		needDial := false
		switch {
		case idle != nil:
			chosen = idle
		case live < p.maxConns:
			needDial = true
		default:
			chosen = light
		}
		p.mu.Unlock()
		if needDial {
			pc, err := dialBackoff(ctx, p.client, p.client.timeouts)
			if err != nil {
				return nil, nil, false, err
			}
			p.add(pc)
			if s, ok := pc.reserve(); ok {
				return pc, s, false, nil
			}
			continue
		}
		if chosen != nil {
			if s, ok := chosen.reserve(); ok {
				load, _ := chosen.load()
				return chosen, s, load > 1, nil
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, nil, false, fmt.Errorf("acquire: %w", ctx.Err())
		case <-changed:
		}
	}
}

func (p *pool) close() {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, pc := range conns {
		pc.fail(fmt.Errorf("close: %w", ErrClosed))
	}
	for _, pc := range conns {
		select {
		case <-pc.readerDone:
		case <-time.After(2 * time.Second):
		}
	}
	p.broadcast()
}
