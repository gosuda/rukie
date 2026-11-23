package ruk

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type pool struct {
	lifetime   context.Context
	client     *Client
	changed    chan struct{}
	dedicated  map[*conn]struct{}
	stop       context.CancelFunc
	conns      []*conn
	ids        atomic.Uint64
	dials      sync.WaitGroup
	maxConns   int
	maxStreams int
	dialing    int
	mu         sync.Mutex
}

func newPool(c *Client, maxConns, maxStreams int) *pool {
	lifetime, stop := context.WithCancel(context.Background())
	return &pool{
		client:     c,
		lifetime:   lifetime,
		stop:       stop,
		changed:    make(chan struct{}),
		dedicated:  make(map[*conn]struct{}),
		maxConns:   maxConns,
		maxStreams: maxStreams,
	}
}

func (p *pool) nextID() uint64 {
	return p.ids.Add(1)
}

func (p *pool) add(pc *conn) {
	p.mu.Lock()
	p.addLocked(pc)
	p.broadcastLocked()
	p.mu.Unlock()
}

func (p *pool) addLocked(pc *conn) {
	pc.maxStreams = p.maxStreams
	pc.onFree = p.broadcast
	p.conns = append(p.conns, pc)
	go pc.readLoop()
}

func (p *pool) broadcast() {
	p.mu.Lock()
	p.broadcastLocked()
	p.mu.Unlock()
}

func (p *pool) broadcastLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *pool) dial(ctx context.Context) (*conn, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(p.lifetime, func() { cancel(ErrClosed) })
	defer func() {
		stop()
		cancel(context.Canceled)
	}()
	pc, err := dialBackoff(ctx, p.client, p.client.timeouts)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("dial: %w: %w", ctx.Err(), context.Cause(ctx))
		}
		return nil, fmt.Errorf("dial: %w", err)
	}
	return pc, nil
}

func (p *pool) acquire(ctx context.Context) (*conn, *stream, bool, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, false, fmt.Errorf("acquire: %w", err)
		}
		p.mu.Lock()
		if p.lifetime.Err() != nil {
			p.mu.Unlock()
			return nil, nil, false, fmt.Errorf("acquire: %w", ErrClosed)
		}
		var idle, light *conn
		lightLoad := 0
		changed := p.changed
		kept := p.conns[:0]
		for _, pc := range p.conns {
			load, broken := pc.load()
			if broken {
				continue
			}
			kept = append(kept, pc)
			if load == 0 && idle == nil {
				idle = pc
			}
			if load < p.maxStreams && (light == nil || load < lightLoad) {
				light = pc
				lightLoad = load
			}
		}
		clear(p.conns[len(kept):])
		p.conns = kept
		chosen := light
		switch {
		case idle != nil:
			chosen = idle
		case len(p.conns)+p.dialing < p.maxConns:
			p.dialing++
			p.dials.Add(1)
			p.mu.Unlock()
			return p.acquireNew(ctx)
		}
		if chosen != nil {
			s, ok := chosen.reserve()
			load, _ := chosen.load()
			p.mu.Unlock()
			if ok {
				return chosen, s, load > 1, nil
			}
			continue
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, false, fmt.Errorf("acquire: %w", ctx.Err())
		case <-changed:
		}
	}
}

func (p *pool) acquireNew(ctx context.Context) (*conn, *stream, bool, error) {
	defer p.dials.Done()
	pc, err := p.dial(ctx)
	p.mu.Lock()
	p.dialing--
	if p.lifetime.Err() != nil {
		p.broadcastLocked()
		p.mu.Unlock()
		if pc != nil {
			_ = pc.nc.Close()
		}
		return nil, nil, false, fmt.Errorf("acquireNew: %w", ErrClosed)
	}
	if err != nil {
		p.broadcastLocked()
		p.mu.Unlock()
		return nil, nil, false, fmt.Errorf("acquireNew: %w", err)
	}
	s, ok := pc.reserve()
	if !ok {
		p.broadcastLocked()
		p.mu.Unlock()
		_ = pc.nc.Close()
		return nil, nil, false, fmt.Errorf("acquireNew: %w", ErrClosed)
	}
	p.addLocked(pc)
	p.broadcastLocked()
	p.mu.Unlock()
	return pc, s, false, nil
}

func (p *pool) openDedicated(ctx context.Context) (*conn, error) {
	p.mu.Lock()
	if p.lifetime.Err() != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("openDedicated: %w", ErrClosed)
	}
	p.dials.Add(1)
	p.mu.Unlock()
	defer p.dials.Done()
	pc, err := p.dial(ctx)
	p.mu.Lock()
	closed := p.lifetime.Err() != nil
	if err == nil && !closed {
		p.dedicated[pc] = struct{}{}
	}
	p.mu.Unlock()
	if closed {
		if pc != nil {
			_ = pc.nc.Close()
		}
		return nil, fmt.Errorf("openDedicated: %w", ErrClosed)
	}
	if err != nil {
		return nil, fmt.Errorf("openDedicated: %w", err)
	}
	return pc, nil
}

func (p *pool) releaseDedicated(pc *conn) {
	_ = pc.nc.Close()
	p.mu.Lock()
	delete(p.dedicated, pc)
	p.mu.Unlock()
}

func (p *pool) close() {
	p.mu.Lock()
	p.stop()
	conns := p.conns
	dedicated := p.dedicated
	p.conns = nil
	p.dedicated = nil
	p.broadcastLocked()
	p.mu.Unlock()
	for _, pc := range conns {
		pc.fail(fmt.Errorf("close: %w", ErrClosed))
	}
	for pc := range dedicated {
		pc.fail(fmt.Errorf("close: %w", ErrClosed))
	}
	p.dials.Wait()
	for _, pc := range conns {
		select {
		case <-pc.readerDone:
		case <-time.After(2 * time.Second):
		}
	}
}
