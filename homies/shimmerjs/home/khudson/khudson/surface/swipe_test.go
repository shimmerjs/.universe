package surface

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

// Vertical swipes: the region the stroke STARTED in owns the direction,
// with the layout's own target as the fallback. homeTestConfig's regions at
// 196x24 place claude-hud across the top: {20,0,164,10}.
func verticalSwipe(m *model, dir string, startCol, startRow int) {
	m.handleBusMsg(proto.Msg{Type: proto.TypeGesture, Gesture: &proto.Gesture{
		Kind: proto.GestureSwipe, Dir: dir, Cells: 8,
		StartCol: startCol, StartRow: startRow,
	}})
}

// bindRegionSwipe rebinds one of home's regions by widget id and renders so
// the frame's swipe table exists (gestures resolve against the last frame).
func bindRegionSwipe(t *testing.T, m *model, widget, up, down string) {
	t.Helper()
	l := m.cfg.Layouts["home"]
	regs := make([]config.Region, len(l.Regions))
	copy(regs, l.Regions)
	found := false
	for i := range regs {
		if regs[i].Widget == widget {
			regs[i].SwipeUp, regs[i].SwipeDown = up, down
			found = true
		}
	}
	if !found {
		t.Fatalf("fixture has no %q region", widget)
	}
	l.Regions = regs
	m.cfg.Layouts["home"] = l
	m.renderHome(m.height - stripH)
}

func TestSwipeUpOnRegionNavigatesThatRegion(t *testing.T) {
	m := newHomeModel(196, 24)
	bindRegionSwipe(t, m, "claude-hud", "hub", "")

	// starting outside the bound region: nothing bound, nothing happens
	verticalSwipe(m, "up", 5, 5)
	if m.layout != "home" {
		t.Fatalf("swipe outside the region navigated: %q", m.layout)
	}
	if m.xition != nil {
		t.Fatalf("unbound swipe opened a wash over %v", m.xition.area)
	}

	verticalSwipe(m, "up", 100, 5)
	if m.layout != "hub" {
		t.Fatalf("region swipe up: layout = %q, want hub", m.layout)
	}
	// the wash covers the region that changed, not the whole body
	if m.xition == nil {
		t.Fatal("region swipe opened no wash")
	}
	if want := (rect{20, 0, 164, 10}); m.xition.area != want {
		t.Errorf("wash area = %+v, want %+v (the region's rect)", m.xition.area, want)
	}
}

// A region binding only one direction leaves the other to the layout: the
// column does not swallow the whole vertical axis.
func TestSwipeRegionFallsBackToLayoutTarget(t *testing.T) {
	m := newHomeModel(196, 24)
	l := m.cfg.Layouts["home"]
	l.SwipeDown = "hub"
	m.cfg.Layouts["home"] = l
	bindRegionSwipe(t, m, "claude-hud", "hub", "")

	verticalSwipe(m, "down", 100, 5) // inside the up-only region
	if m.layout != "hub" {
		t.Fatalf("layout fallback: layout = %q, want hub", m.layout)
	}
	if m.xition == nil {
		t.Fatal("layout swipe opened no wash")
	}
	// a layout-level swipe changes everything under the strip
	if want := (rect{0, 0, 196, 23}); m.xition.area != want {
		t.Errorf("wash area = %+v, want the whole body %+v", m.xition.area, want)
	}
}

// The swipe table rides homeCache: a cache-hit frame must resolve swipes
// against the regions actually on glass.
func TestSwipeTableSurvivesHomeCache(t *testing.T) {
	m := newHomeModel(196, 24)
	bindRegionSwipe(t, m, "claude-hud", "hub", "")
	m.View() // composes and memoizes the frame
	if !m.homeCache.ok {
		t.Fatal("home frame not memoized")
	}
	m.regionSwipes = nil
	m.View() // cache hit: restores both frame tables
	if len(m.regionSwipes) != 1 {
		t.Fatalf("swipe table after cache hit = %d entries, want 1", len(m.regionSwipes))
	}
	verticalSwipe(m, "up", 100, 5)
	if m.layout != "hub" {
		t.Fatalf("cached-frame region swipe: layout = %q, want hub", m.layout)
	}
}

// A swipe before the first frame has no table to resolve against; it must
// fall back, not panic.
func TestSwipeBeforeFirstFrame(t *testing.T) {
	m := newHomeModel(196, 24)
	l := m.cfg.Layouts["home"]
	l.SwipeUp = "hub"
	m.cfg.Layouts["home"] = l
	verticalSwipe(m, "up", 100, 5)
	if m.layout != "hub" {
		t.Fatalf("pre-frame swipe: layout = %q, want hub", m.layout)
	}
}

// A thumb arriving at a column edge lands a cell or two off the region it
// meant. Without a near-miss pass the stroke fell through to a layout with
// no vertical binding and did nothing at all, which reads as a broken
// gesture rather than a missed one.
func TestSwipeStartNearMissStillHitsRegion(t *testing.T) {
	m := newHomeModel(196, 24)
	bindRegionSwipe(t, m, "claude-hud", "hub", "") // region rect {20,0,164,10}

	// four columns short of the region's left edge
	verticalSwipe(m, "up", 16, 5)
	if m.layout != "hub" {
		t.Fatalf("near-miss swipe: layout = %q, want hub", m.layout)
	}
	// the wash still covers the region that changed, not the slop rect
	if want := (rect{20, 0, 164, 10}); m.xition == nil || m.xition.area != want {
		t.Errorf("wash area = %+v, want the region's own rect %+v", m.xition, want)
	}

	// one row under its bottom edge
	m2 := newHomeModel(196, 24)
	bindRegionSwipe(t, m2, "claude-hud", "hub", "")
	verticalSwipe(m2, "up", 100, 11)
	if m2.layout != "hub" {
		t.Fatalf("near-miss below the region: layout = %q, want hub", m2.layout)
	}

	// slop is a near miss, not a free-for-all: the far side of the glass
	// still resolves to the layout
	m3 := newHomeModel(196, 24)
	bindRegionSwipe(t, m3, "claude-hud", "hub", "")
	verticalSwipe(m3, "up", 2, 20)
	if m3.layout != "home" {
		t.Fatalf("far swipe navigated: %q", m3.layout)
	}
}

// The near-miss pass must not overrule a region that deliberately leaves
// one direction to the layout: a start INSIDE a bound region is resolved
// by that region alone.
func TestSwipeNearMissRespectsExactMatch(t *testing.T) {
	m := newHomeModel(196, 24)
	l := m.cfg.Layouts["home"]
	l.SwipeDown = "hub"
	m.cfg.Layouts["home"] = l
	bindRegionSwipe(t, m, "claude-hud", "keyboard", "")

	verticalSwipe(m, "down", 100, 5) // inside the up-only region
	if m.layout != "hub" {
		t.Fatalf("layout fallback: layout = %q, want hub", m.layout)
	}
	if want := (rect{0, 0, 196, 23}); m.xition == nil || m.xition.area != want {
		t.Errorf("wash area = %+v, want the whole body %+v", m.xition, want)
	}
}
