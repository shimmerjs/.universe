package dock

// Single-finger swipe swaps strip tabs (multi-finger is hardware-
// refuted): horizontal swipes cycle the ring [home, defined entries],
// stubs skipped, Alt pairs one slot, overlay open swallows the swipe.

import (
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

func swipe(m *model, dir string) {
	m.handleBusMsg(proto.Msg{Type: proto.TypeGesture, Gesture: &proto.Gesture{
		Kind: proto.GestureSwipe, Dir: dir, Cells: 8,
	}})
}

// stripModel's ring is [home, clod->hub]: kb and sys are stubs.
func TestSwipeCyclesTabs(t *testing.T) {
	m := stripModel()
	if m.layout != "home" {
		t.Fatalf("fixture layout = %q", m.layout)
	}
	swipe(m, "left")
	if m.layout != "hub" {
		t.Fatalf("swipe left: layout = %q, want hub (stubs skipped)", m.layout)
	}
	swipe(m, "left") // ring of 2 wraps back
	if m.layout != "home" {
		t.Fatalf("swipe left wrap: layout = %q", m.layout)
	}
	swipe(m, "right") // prev from home wraps to the tail
	if m.layout != "hub" {
		t.Fatalf("swipe right: layout = %q", m.layout)
	}
}

func TestSwipeVerticalAndOverlayIgnored(t *testing.T) {
	m := stripModel()
	swipe(m, "up")
	swipe(m, "down")
	if m.layout != "home" {
		t.Fatalf("vertical swipe navigated: %q", m.layout)
	}
	m.overlay = &overlayState{}
	swipe(m, "left")
	if m.layout != "home" {
		t.Fatalf("overlay swipe navigated: %q", m.layout)
	}
}

// An Alt pair is one slot: active on either side, cycling moves past it
// rather than bouncing between its two faces.
func TestSwipeAltPairOneSlot(t *testing.T) {
	m := stripModel()
	m.cfg.Layouts["reno-rec"] = config.Layout{Kind: "home", Regions: []config.Region{
		{Widget: "cpumem", Edge: "fill"},
	}}
	m.cfg.Strip.Entries = append(m.cfg.Strip.Entries,
		config.StripEntry{Label: "reno", Target: "hub", Alt: "reno-rec"})
	// entries now: kb(stub), clod->hub, sys(stub), reno(hub/reno-rec).
	// hub appears twice as a target; slot matching takes the first hit.
	m.layout = "reno-rec" // the Alt side of the reno tab
	swipe(m, "left")      // wraps past the tail to home
	if m.layout != "home" {
		t.Fatalf("alt-side swipe: layout = %q, want home", m.layout)
	}
}

// A layout that is not on the strip swipes nowhere: jumping to an
// arbitrary slot mid-glass is worse than a no-op.
func TestSwipeOffStripNoops(t *testing.T) {
	m := stripModel()
	m.cfg.Layouts["detached"] = config.Layout{Kind: "home", Regions: []config.Region{
		{Widget: "cpumem", Edge: "fill"},
	}}
	m.layout = "detached"
	swipe(m, "left")
	if m.layout != "detached" {
		t.Fatalf("off-strip swipe navigated: %q", m.layout)
	}
}
