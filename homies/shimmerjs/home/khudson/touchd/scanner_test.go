package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sstallion/go-hid"
)

// fakeHID swaps the enumerate seam for a test-controlled device set so
// scanner behavior is verifiable without hardware (or hid.Init).
type fakeHID struct {
	calls atomic.Int64
	mu    sync.Mutex
	devs  []hid.DeviceInfo
}

func installFakeHID(t *testing.T) *fakeHID {
	t.Helper()
	f := &fakeHID{}
	old := hidEnumerate
	hidEnumerate = func(vid, pid uint16, fn hid.EnumFunc) error {
		f.calls.Add(1)
		f.mu.Lock()
		devs := append([]hid.DeviceInfo(nil), f.devs...)
		f.mu.Unlock()
		for i := range devs {
			if err := fn(&devs[i]); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() { hidEnumerate = old })
	return f
}

func (f *fakeHID) set(devs ...hid.DeviceInfo) {
	f.mu.Lock()
	f.devs = devs
	f.mu.Unlock()
}

func matchDevice(m Match, path string) hid.DeviceInfo {
	return hid.DeviceInfo{Path: path, VendorID: m.VID, ProductID: m.PID, UsagePage: m.UsagePage, Usage: m.Usage}
}

func parkedCount(sc *scanner) int {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	n := 0
	for _, st := range sc.states {
		if st.ch != nil {
			n++
		}
	}
	return n
}

func waitParked(t *testing.T, sc *scanner, want int) {
	t.Helper()
	for i := 0; parkedCount(sc) != want; i++ {
		if i > 400 {
			t.Fatalf("parked waiters = %d, want %d", parkedCount(sc), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func recvPath(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("await did not deliver")
		return ""
	}
}

// park starts an AwaitDevice waiter and returns its delivery channel.
func park(t *testing.T, sc *scanner, m Match) <-chan string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		p, err := sc.await(context.Background(), m)
		if err == nil {
			ch <- p
		}
	}()
	return ch
}

// Two concurrent waiters must share the scan: every tick costs exactly one
// enumerate no matter how many modules are waiting (the point of the central
// scanner -- today's independent loops paid one each).
func TestScannerSharedEnumerate(t *testing.T) {
	f := installFakeHID(t)
	sc := newScanner()
	chA := park(t, sc, edgeDigitizerMatch)
	chB := park(t, sc, moonMatch)
	waitParked(t, sc, 2)

	t0 := time.Now()
	sc.tick(t0)
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("tick with two waiters ran %d enumerates, want 1", got)
	}
	sc.tick(t0.Add(reconnectMin))
	if got := f.calls.Load(); got != 2 {
		t.Fatalf("two ticks ran %d enumerates, want 2", got)
	}
	// both ramps advanced on the shared scans: next attempt 1s after tick 2
	if next, ok := sc.nextDue(); !ok || !next.Equal(t0.Add(3*reconnectMin)) {
		t.Fatalf("nextDue = %v ok=%v, want %v", next, ok, t0.Add(3*reconnectMin))
	}

	f.set(matchDevice(edgeDigitizerMatch, "p-digi"), matchDevice(moonMatch, "p-moon"))
	sc.tick(t0.Add(3 * reconnectMin))
	if got := f.calls.Load(); got != 3 {
		t.Fatalf("three ticks ran %d enumerates, want 3", got)
	}
	if p := recvPath(t, chA); p != "p-digi" {
		t.Fatalf("digitizer waiter got %q", p)
	}
	if p := recvPath(t, chB); p != "p-moon" {
		t.Fatalf("moonlander waiter got %q", p)
	}
}

// The absent ramp doubles from reconnectMin and pins at absentCap, the
// worst-case reattach latency (same schedule TestReopenBackoff pins on the
// raw type, exercised here through scheduled scanner ticks).
func TestScannerAbsentRampCapsAt30s(t *testing.T) {
	installFakeHID(t)
	sc := newScanner()
	park(t, sc, moonMatch)
	waitParked(t, sc, 1)

	now := time.Now()
	want := reconnectMin
	for i := range 12 {
		sc.tick(now)
		next, ok := sc.nextDue()
		if !ok || next.Sub(now) != want {
			t.Fatalf("tick %d: next attempt in %s, want %s", i, next.Sub(now), want)
		}
		now = next
		want = min(want*2, absentCap)
	}
	sc.tick(now)
	next, _ := sc.nextDue()
	if next.Sub(now) != absentCap {
		t.Fatalf("chronic absence waits %s, want absent cap %s", next.Sub(now), absentCap)
	}
}

// A failed open after delivery is the seized class: it ramps to
// reconnectCap, and class flips (device unplugged, or an absent device
// appearing seized) reset to the floor -- the same transitions the old
// per-loop backoff had.
func TestScannerSeizedClassAndFlip(t *testing.T) {
	f := installFakeHID(t)
	sc := newScanner()

	// a few absent failures first, so the seized flip below proves the reset
	ch := park(t, sc, moonMatch)
	waitParked(t, sc, 1)
	now := time.Now()
	for range 4 {
		sc.tick(now)
		next, _ := sc.nextDue()
		now = next
	}

	// device appears: the scheduled tick delivers
	f.set(matchDevice(moonMatch, "p"))
	sc.tick(now)
	if p := recvPath(t, ch); p != "p" {
		t.Fatalf("delivered %q", p)
	}

	// open fails: absent -> seized flips the ramp back to the floor, then
	// chronic failure doubles to reconnectCap
	want := reconnectMin
	for i := range 12 {
		sc.reportOpenAt("p", errors.New("exclusive access and device already open"), now)
		ch = park(t, sc, moonMatch)
		waitParked(t, sc, 1)
		next, ok := sc.nextDue()
		if !ok || next.Sub(now) != want {
			t.Fatalf("seized attempt %d: wait %s, want %s", i, next.Sub(now), want)
		}
		// seized waiters are NOT delivered early: a tick before the
		// scheduled attempt must leave the waiter parked
		sc.tick(now)
		if parkedCount(sc) != 1 {
			t.Fatalf("seized attempt %d: early tick delivered ahead of schedule", i)
		}
		now = next
		sc.tick(now)
		if p := recvPath(t, ch); p != "p" {
			t.Fatalf("seized attempt %d: delivered %q", i, p)
		}
		want = min(want*2, reconnectCap)
	}

	// device disappears mid-seize: seized -> absent flips back to the floor
	sc.reportOpenAt("p", errors.New("still seized"), now)
	f.set()
	park(t, sc, moonMatch)
	waitParked(t, sc, 1)
	next, _ := sc.nextDue()
	now = next
	sc.tick(now)
	next, _ = sc.nextDue()
	if next.Sub(now) != reconnectMin {
		t.Fatalf("flip to absent waits %s, want %s", next.Sub(now), reconnectMin)
	}
}

// A successful open resets the ramp -- but only once the module keeps the
// device: bouncing straight back into AwaitDevice (a failed pairing init)
// counts as a seized-class failure instead, so a chronic init failure backs
// off rather than tight-looping.
func TestScannerOpenResetAndRapidReentry(t *testing.T) {
	f := installFakeHID(t)
	f.set(matchDevice(moonMatch, "p"))
	sc := newScanner()

	ch := park(t, sc, moonMatch)
	waitParked(t, sc, 1)
	now := time.Now()
	sc.tick(now)
	recvPath(t, ch)

	// open succeeded but the module bounced back immediately: seized class
	sc.reportOpen("p", nil)
	ch = park(t, sc, moonMatch)
	waitParked(t, sc, 1)
	next, ok := sc.nextDue()
	if !ok || next.Sub(now) < reconnectMin/2 {
		t.Fatalf("rapid re-entry got an immediate retry (due in %s); want a backoff wait", next.Sub(now))
	}
	sc.tick(next)
	recvPath(t, ch)

	// open succeeded and a real session ran (handedAt pushed into the past):
	// the ramp resets and the next await is served immediately
	sc.reportOpen("p", nil)
	sc.mu.Lock()
	sc.states[moonMatch].handedAt = time.Now().Add(-time.Minute)
	sc.mu.Unlock()
	ch = park(t, sc, moonMatch)
	waitParked(t, sc, 1)
	next, ok = sc.nextDue()
	if !ok || next.After(time.Now()) {
		t.Fatalf("post-session re-await scheduled at %v, want immediate", next)
	}
	sc.tick(time.Now())
	recvPath(t, ch)
}

// An absent-class waiter is delivered as soon as any tick sees the device --
// its cap is a poll bound, not an open-rate limit -- which is what lets a
// future IOKit arrival wake cut reattach latency without module changes.
func TestScannerAbsentDeliversEarly(t *testing.T) {
	f := installFakeHID(t)
	sc := newScanner()
	ch := park(t, sc, moonMatch)
	waitParked(t, sc, 1)

	now := time.Now()
	for range 8 {
		sc.tick(now)
		now, _ = sc.nextDue()
	}
	// due is far in the future; an off-schedule tick (another waiter, a
	// wake) that sees the device must deliver anyway
	f.set(matchDevice(moonMatch, "p"))
	sc.tick(now.Add(-time.Millisecond))
	if p := recvPath(t, ch); p != "p" {
		t.Fatalf("delivered %q", p)
	}
}

// The run loop is fully idle with no parked waiters and services arrivals
// end to end when woken.
func TestScannerRunLoopDelivers(t *testing.T) {
	f := installFakeHID(t)
	sc := newScanner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sc.run(ctx)

	time.Sleep(20 * time.Millisecond)
	if got := f.calls.Load(); got != 0 {
		t.Fatalf("idle scanner ran %d enumerates, want 0", got)
	}

	f.set(matchDevice(moonMatch, "p"))
	ch := park(t, sc, moonMatch)
	if p := recvPath(t, ch); p != "p" {
		t.Fatalf("delivered %q", p)
	}
}
