package cgdisplay

import (
	"io"
	"slices"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
)

// Cache reconciles invalidations only while a caller needs display data.
// A failed query retains pending work; it does not consume the only event.
type Cache struct {
	Changes            <-chan struct{}
	Query              func() ([]Display, error)
	Now                func() time.Time
	mu                 sync.Mutex
	data               []Display
	known              bool
	pending            bool
	next, timeFallback time.Time
	failures           uint
	err                error
}

func (c *Cache) Read() ([]Display, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
drain:
	for {
		select {
		case _, ok := <-c.Changes:
			if !ok {
				c.Changes = nil
				c.pending = true
				diagnostic.Default.Observe("display.cache.events", io.EOF, "reconcile on demand; use two-second fallback")
				break drain
			}
			c.pending = true
		default:
			break drain
		}
	}
	fallback := time.Minute
	if c.Changes == nil {
		fallback = 2 * time.Second
	}
	if c.known && !c.pending && now.Before(c.timeFallback) {
		return slices.Clone(c.data), c.err
	}
	if now.Before(c.next) {
		return slices.Clone(c.data), c.err
	}
	query := c.Query
	if query == nil {
		query = ActiveWithError
	}
	data, err := query()
	c.err = err
	diagnostic.Default.Observe("display.cache.query", err, "retain invalidation; retry with backoff")
	if err != nil {
		c.pending = true
		c.failures++
		c.next = now.Add(time.Second * time.Duration(1<<min(c.failures-1, 5)))
		return slices.Clone(c.data), err
	}
	c.data, c.known, c.pending, c.failures = data, true, false, 0
	c.next = time.Time{}
	c.timeFallback = now.Add(fallback)
	return slices.Clone(c.data), nil
}
