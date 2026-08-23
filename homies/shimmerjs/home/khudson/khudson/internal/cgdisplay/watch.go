package cgdisplay

/*
void cgdisplayWatchRun(void);
void cgdisplayWatchStop(void);
*/
import "C"

import (
	"context"
	"errors"
	"runtime"
	"sync"
)

// The CoreGraphics reconfiguration callback is process-global state, so one
// watcher at a time: a second registration would fan the same notifications
// into two channels and neither could be unregistered independently.
var (
	watchMu    sync.Mutex
	watchCh    chan struct{}
	watchReady chan struct{}
)

//export cgdisplayWatchReady
func cgdisplayWatchReady() {
	watchMu.Lock()
	ch := watchReady
	watchReady = nil
	watchMu.Unlock()
	if ch != nil {
		close(ch)
	}
}

//export cgdisplayChanged
func cgdisplayChanged() {
	watchMu.Lock()
	ch := watchCh
	watchMu.Unlock()
	if ch == nil {
		return
	}
	// Coalesce. One unplug fires the callback once per affected display and
	// a consumer re-reads the whole arrangement anyway, so a already-pending
	// signal covers everything that has happened since.
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Watch reports display arrangement changes -- add, remove, move, mode and
// desktop-shape -- on the returned channel, until ctx ends. Sends are
// coalesced into a depth-1 buffer: a receive means "the arrangement changed,
// re-read it", never how many times or which display.
//
// Registration is complete before Watch returns, so a caller that reads the
// arrangement afterwards cannot miss a change landing in between.
//
// One watcher per process; a second concurrent call is an error.
func Watch(ctx context.Context) (<-chan struct{}, error) {
	watchMu.Lock()
	if watchCh != nil {
		watchMu.Unlock()
		return nil, errors.New("cgdisplay: watch already running")
	}
	ch := make(chan struct{}, 1)
	ready := make(chan struct{})
	watchCh, watchReady = ch, ready
	watchMu.Unlock()

	go func() {
		// CoreGraphics delivers the callback to the run loop of the
		// registering thread, so that thread must stay put for the watch's
		// lifetime rather than be recycled by the Go scheduler.
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		C.cgdisplayWatchRun()
	}()

	select {
	case <-ready:
	case <-ctx.Done():
		C.cgdisplayWatchStop()
		watchMu.Lock()
		watchCh, watchReady = nil, nil
		watchMu.Unlock()
		return nil, ctx.Err()
	}

	go func() {
		<-ctx.Done()
		C.cgdisplayWatchStop()
		watchMu.Lock()
		watchCh = nil
		watchMu.Unlock()
	}()
	return ch, nil
}
