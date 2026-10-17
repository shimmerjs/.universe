package keyboard

import (
	"context"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
)

type Observation struct {
	State                       LoadState
	LatestRevision, LatestTitle string
	Generation                  uint64
}

// Watcher serializes USB/cache/update reads off the UI thread. One visible
// lifetime owns its fetches; hiding cancels it and discards late observations.
type Watcher struct {
	loader     *Loader
	check      *UpdateCheck
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	readCtx    context.Context
	readCancel context.CancelFunc
	visible    bool
	generation uint64
	wake       chan struct{}
	updates    chan Observation
	done       chan struct{}
}

func Watch(ctx context.Context, loader *Loader, check *UpdateCheck) *Watcher {
	ctx, cancel := context.WithCancel(ctx)
	w := &Watcher{ctx: ctx, cancel: cancel, loader: loader, check: check, wake: make(chan struct{}, 1), updates: make(chan Observation, 1), done: make(chan struct{})}
	go w.run()
	return w
}
func (w *Watcher) Updates() <-chan Observation { return w.updates }
func (w *Watcher) Visible(visible bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.visible == visible || w.ctx.Err() != nil {
		return
	}
	w.visible = visible
	w.generation++
	if w.readCancel != nil {
		w.readCancel()
	}
	if visible {
		w.readCtx, w.readCancel = context.WithCancel(w.ctx)
	} else {
		w.loader.Suspend()
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *Watcher) Current(generation uint64) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.visible && generation == w.generation && w.ctx.Err() == nil
}
func (w *Watcher) Close() { w.cancel(); <-w.done }
func (w *Watcher) Refresh() {
	w.loader.Invalidate()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}
func (w *Watcher) run() {
	defer close(w.done)
	defer close(w.updates)
	defer w.loader.Close()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	var tick <-chan time.Time
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.wake:
		case <-tick:
		}
		w.mu.Lock()
		visible, ctx, generation := w.visible, w.readCtx, w.generation
		w.mu.Unlock()
		if !visible {
			timer.Stop()
			tick = nil
			continue
		}
		state := w.loader.Load(ctx)
		observation := Observation{State: state, Generation: generation}
		if w.check != nil && state.Present {
			rev, title, err := w.check.Get(ctx, state.Identity.LayoutID)
			diagnostic.Default.Observe("keyboard.update.fetch", err, "retain previous revision; retry at provider cadence")
			if err == nil {
				observation.LatestRevision, observation.LatestTitle = rev, title
			}
		}
		if w.Current(generation) {
			select {
			case <-w.updates:
			default:
			}
			select {
			case w.updates <- observation:
			case <-w.ctx.Done():
				return
			}
		}
		timer.Reset(3 * time.Second)
		tick = timer.C
	}
}
