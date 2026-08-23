package cgdisplay

import (
	"context"
	"os"
	"testing"
	"time"
)

// Live tests against the host's real display configuration, env-gated like
// the ax suite: with KHUDSON_CGDISPLAY unset every test self-skips, so
// buildGoModule's doCheck never needs a GUI session.

func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("KHUDSON_CGDISPLAY") == "" {
		t.Skip("KHUDSON_CGDISPLAY not set")
	}
}

// Registration reaches CFRunLoopRun and stop unwinds it. This is the half of
// the watcher that needs no display to move: Watch only returns once the C
// side has registered the callback and signalled ready, so a return proves
// the locked thread parked on a live run loop rather than falling straight
// through it (CFRunLoopRun exits immediately on a loop with no input source).
func TestWatchRegistersAndStopsLive(t *testing.T) {
	liveGate(t)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if ch == nil {
		t.Fatal("Watch returned a nil channel")
	}
	// a second watcher must be refused while the first holds the callback
	if _, err := Watch(context.Background()); err == nil {
		cancel()
		t.Fatal("second Watch succeeded; the CG callback is process-global")
	}
	cancel()
	// the stop path must release the registration so a later Watch works
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ctx2, cancel2 := context.WithCancel(context.Background())
		if _, err := Watch(ctx2); err == nil {
			cancel2()
			return
		}
		cancel2()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("watch registration never released after cancel")
}

// The other half: a real reconfiguration reaches the channel. Needs a human
// to change the arrangement (unplug a display, or change its resolution in
// System Settings) inside the window.
func TestWatchFiresOnReconfigurationLive(t *testing.T) {
	liveGate(t)
	if os.Getenv("KHUDSON_CGDISPLAY_HOTPLUG") == "" {
		t.Skip("KHUDSON_CGDISPLAY_HOTPLUG not set (needs a manual display change)")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := Watch(ctx)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	before := Active()
	t.Logf("watching %d display(s); change the arrangement now", len(before))
	select {
	case <-ch:
		t.Logf("reconfiguration seen; now %d display(s)", len(Active()))
	case <-time.After(60 * time.Second):
		t.Fatal("no reconfiguration signal within 60s")
	}
}
