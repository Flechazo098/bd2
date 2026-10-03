// Package lifecycle coordinates graceful server replacement. It never moves
// live request or battle memory between processes: draining stops admission,
// lets committed requests finish, and requires clients to create a new session.
package lifecycle

import (
	"context"
	"sync"
)

type Gate struct {
	mu       sync.Mutex
	ready    bool
	inFlight int
	drained  chan struct{}
}

func NewGate() *Gate {
	return &Gate{ready: true, drained: make(chan struct{})}
}

func (g *Gate) Ready() bool {
	if g == nil {
		return true
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ready
}

// BeginRequest admits one request while ready. The returned completion
// callback must be invoked exactly once.
func (g *Gate) BeginRequest() (func(), bool) {
	if g == nil {
		return func() {}, true
	}
	g.mu.Lock()
	if !g.ready {
		g.mu.Unlock()
		return nil, false
	}
	g.inFlight++
	g.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.inFlight--
			if !g.ready && g.inFlight == 0 {
				select {
				case <-g.drained:
				default:
					close(g.drained)
				}
			}
			g.mu.Unlock()
		})
	}, true
}

func (g *Gate) Drain() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.ready {
		g.ready = false
		if g.inFlight == 0 {
			close(g.drained)
		}
	}
	g.mu.Unlock()
}

func (g *Gate) Wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	select {
	case <-g.drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
