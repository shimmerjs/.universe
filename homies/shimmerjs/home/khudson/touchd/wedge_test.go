package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type wedgeFixture struct {
	w    *wedgeWatch
	seen *atomic.Int64
	idle time.Duration
	now  time.Time
}

func newWedgeFixture() *wedgeFixture {
	r := newInputRate()
	f := &wedgeFixture{seen: r.source("moonlander"), now: time.Unix(1700000000, 0)}
	f.w = &wedgeWatch{rate: r, idle: func() time.Duration { return f.idle }, console: func() string { return "" }}
	return f
}

// step advances one poll, optionally delivering n hid reports first.
func (f *wedgeFixture) step(n int) string {
	for range n {
		f.seen.Add(1)
	}
	f.now = f.now.Add(wedgePoll)
	return f.w.tick(f.now)
}

// Neither half of the signal means anything alone: a quiet keyboard and an
// idle pipeline is just nobody typing, and reports with a live pipeline is
// just typing.
func TestWedgeNeedsBothHalves(t *testing.T) {
	f := newWedgeFixture()

	// nobody home: pipeline idle for ages, no reports
	f.idle = time.Minute
	for range 5 {
		if line := f.step(0); line != "" {
			t.Fatalf("idle machine flagged a wedge: %q", line)
		}
	}
	// typing, and the pipeline is delivering it
	f.idle = 0
	for range 5 {
		if line := f.step(3); line != "" {
			t.Fatalf("healthy typing flagged a wedge: %q", line)
		}
	}
}

// Reports arriving while the pipeline insists it has been idle for far
// longer is the wedge, and it fires once, not every poll.
func TestWedgeFiresOnceOnStall(t *testing.T) {
	f := newWedgeFixture()
	f.idle = 0
	f.step(3) // establish that the hardware is sending

	f.idle = 10 * time.Second // the pipeline stopped delivering
	line := f.step(3)
	if !strings.Contains(line, "WEDGE:") || !strings.Contains(line, "stalled") {
		t.Fatalf("onset line = %q", line)
	}
	for range 4 {
		if l := f.step(3); l != "" {
			t.Errorf("wedge re-announced itself: %q", l)
		}
	}
}

// It clears when the PIPELINE delivers again, not when the reports stop.
// Clearing on silence would call the wedge over the moment you gave up
// typing at a dead machine -- exactly when it is still happening.
func TestWedgeClearsOnDeliveryNotOnSilence(t *testing.T) {
	f := newWedgeFixture()
	f.idle = 0
	f.step(3)
	f.idle = 10 * time.Second
	if line := f.step(3); !strings.Contains(line, "WEDGE:") {
		t.Fatalf("expected onset, got %q", line)
	}

	// you stop typing at the dead machine; still wedged, still silent
	for range 10 {
		f.idle += wedgePoll
		if l := f.step(0); l != "" {
			t.Fatalf("silence was mistaken for recovery: %q", l)
		}
	}
	// the pipeline delivers something again
	f.idle = 0
	line := f.step(1)
	if !strings.Contains(line, "IDLE DISAGREEMENT ENDED") {
		t.Fatalf("clear line = %q", line)
	}
	// and it does not keep announcing that either
	if l := f.step(1); l != "" {
		t.Errorf("clear re-announced itself: %q", l)
	}
}

// Interval logging must not change the cumulative activity signal.
func TestWedgeSurvivesACounterDrain(t *testing.T) {
	f := newWedgeFixture()
	f.idle = 0
	f.step(5)
	f.w.rate.drain() // the interval log consumed its delta

	// let the last real reports age out, so anything that fires below is
	// the drain's doing and not leftover freshness
	for range 3 {
		f.step(0)
	}
	f.idle = 10 * time.Second
	if l := f.step(0); l != "" {
		t.Errorf("a drain alone flagged a wedge: %q", l)
	}
	// Reports after a drain still count as activity.
	if line := f.step(2); !strings.Contains(line, "WEDGE:") {
		t.Errorf("reports after a drain must still count: %q", line)
	}
}

// Preserve the console field as evidence without treating absence as proof.
func TestWedgeOnsetReportsSecureInputHolder(t *testing.T) {
	held := `+-o Root  <class IORegistryEntry>
    "IOConsoleUsers" = ({"kCGSSessionOnConsoleKey"=Yes,"kCGSSessionSecureInputPID"=4242,"kCGSSessionUserNameKey"="shimmerjs"})`
	free := `+-o Root  <class IORegistryEntry>
    "IOConsoleUsers" = ({"kCGSSessionOnConsoleKey"=Yes,"kCGSSessionUserNameKey"="shimmerjs"})`

	f := newWedgeFixture()
	f.w.console = func() string { return held }
	f.idle = 0
	f.step(3)
	f.idle = 10 * time.Second
	if line := f.step(3); !strings.Contains(line, "secure input field names pid 4242") {
		t.Errorf("onset must name the secure-input holder: %q", line)
	}

	// and says so plainly when nobody holds it, so the absence is recorded
	// as an observation rather than read as a missing field
	g := newWedgeFixture()
	g.w.console = func() string { return free }
	g.idle = 0
	g.step(3)
	g.idle = 10 * time.Second
	if line := g.step(3); !strings.Contains(line, "secure input: holder not observed in console snapshot") {
		t.Errorf("onset must record the absence: %q", line)
	}
}

func TestDiagnosticProbeBoundsOutput(t *testing.T) {
	_, err := probeOutput(context.Background(), "/bin/sh", "-c", "while :; do printf '0123456789012345678901234567890123456789012345678901234567890123456789'; done")
	if !errors.Is(err, errProbeOutputLimit) {
		t.Fatalf("output bound: %v", err)
	}
}
func TestDiagnosticProbeHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeOutput(ctx, "/bin/sh", "-c", "exit 0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
