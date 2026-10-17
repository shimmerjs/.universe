// Package taskgroup owns cancellable host commands, including commands delivered
// after a terminal has exited.
package taskgroup

import (
	"context"
	"sync"
)

type Group struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func New(ctx context.Context) *Group {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{ctx: ctx, cancel: cancel}
}
func (g *Group) Do(fn func(context.Context)) bool {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return false
	}
	g.wg.Add(1)
	g.mu.Unlock()
	defer g.wg.Done()
	fn(g.ctx)
	return true
}
func (g *Group) Close() { g.mu.Lock(); g.closed = true; g.cancel(); g.mu.Unlock(); g.wg.Wait() }
