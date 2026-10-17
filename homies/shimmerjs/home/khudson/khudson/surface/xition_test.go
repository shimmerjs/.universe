package surface

// The swipe wash: bounded frame count, exact geometry, deterministic
// static, and a tick chain that stops on its own.

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// filledCells counts the static's drawn cells in a rendered wash frame.
func filledCells(t *testing.T, block string, wantW, wantH int) int {
	t.Helper()
	lines := strings.Split(block, "\n")
	if len(lines) != wantH {
		t.Fatalf("wash lines = %d, want %d", len(lines), wantH)
	}
	n := 0
	for i, l := range lines {
		if w := lipgloss.Width(l); w != wantW {
			t.Fatalf("wash line %d width = %d, want %d", i, w, wantW)
		}
		for _, r := range ansi.Strip(l) {
			if r != ' ' {
				n++
			}
		}
	}
	return n
}

func TestXitionRenderGeometryAndThinning(t *testing.T) {
	x := &xition{area: rect{20, 0, 75, 23}}
	var counts []int
	for f := range xitionFrames {
		x.frame = f
		counts = append(counts, filledCells(t, x.render(), 75, 23))
	}
	// the wash thins across the run: densest first, sparsest last
	if counts[0] <= counts[len(counts)-1] {
		t.Fatalf("wash did not thin: %v", counts)
	}
	cells := 75 * 23
	// first frame near-solid, last frame sparse but not empty (static, not
	// a blank column)
	if got := float64(counts[0]) / float64(cells); got < 0.75 {
		t.Errorf("first frame fill = %.2f, want >= 0.75", got)
	}
	if counts[len(counts)-1] == 0 {
		t.Error("last frame is blank, not static")
	}
}

// Consecutive frames must differ: static shimmers where a plain fade would
// redraw the same cells dimmer.
func TestXitionFramesShimmer(t *testing.T) {
	x := &xition{area: rect{0, 0, 40, 6}}
	seen := map[string]int{}
	for f := range xitionFrames {
		x.frame = f
		seen[x.render()]++
	}
	if len(seen) != xitionFrames {
		t.Fatalf("distinct frames = %d, want %d", len(seen), xitionFrames)
	}
}

// Same frame, same cells: the wash carries no PRNG state, so a re-render
// (a redraw from an unrelated message) draws the identical frame.
func TestXitionRenderDeterministic(t *testing.T) {
	a := &xition{area: rect{3, 4, 20, 5}, frame: 2}
	b := &xition{area: rect{3, 4, 20, 5}, frame: 2}
	if a.render() != b.render() {
		t.Fatal("wash frame is not deterministic in (frame, col, row)")
	}
}

// The tick chain is bounded: xitionFrames advances end it, and the model is
// back to no wash with no further tick armed.
func TestXitionTickChainTerminates(t *testing.T) {
	m := newHomeModel(196, 24)
	m.startXition(rect{0, 0, 196, 23})
	if !m.drainXitionArmed() {
		t.Fatal("startXition did not arm the tick")
	}
	if m.drainXitionArmed() {
		t.Fatal("arm mark did not drain")
	}
	// frame 0 is on glass already; xitionFrames ticks draw the rest and
	// close the wash on the last one
	for i := 1; i <= xitionFrames; i++ {
		mm, cmd := m.Update(xitionTickMsg{gen: m.xitionGen, at: time.Now()})
		m = mm.(*model)
		if i < xitionFrames {
			if cmd == nil {
				t.Fatalf("frame %d: chain dropped mid-wash", i)
			}
			if m.xition == nil {
				t.Fatalf("frame %d: wash ended early", i)
			}
			continue
		}
		if cmd != nil {
			t.Error("last frame re-armed the chain")
		}
		if m.xition != nil {
			t.Errorf("wash still live after %d frames", xitionFrames)
		}
	}
	// a stray late tick after the wash ends is a no-op, not a panic
	if _, cmd := m.Update(xitionTickMsg{gen: m.xitionGen, at: time.Now()}); cmd != nil {
		t.Error("late tick re-armed the chain")
	}
}

// A swipe mid-wash retargets it and retires the running chain: the old
// chain's next tick must not advance the new wash (which would run it at
// double speed and cut it short).
func TestXitionRetargetRetiresOldChain(t *testing.T) {
	m := newHomeModel(196, 24)
	m.startXition(rect{0, 0, 196, 23})
	stale := m.xitionGen
	m.drainXitionArmed()

	m.startXition(rect{121, 0, 75, 23}) // second swipe, new area
	if m.xition.frame != 0 {
		t.Fatalf("retargeted wash started at frame %d, want 0", m.xition.frame)
	}
	if _, cmd := m.Update(xitionTickMsg{gen: stale, at: time.Now()}); cmd != nil {
		t.Error("stale chain re-armed itself")
	}
	if m.xition == nil || m.xition.frame != 0 {
		t.Fatalf("stale tick advanced the live wash: %+v", m.xition)
	}
	mm, cmd := m.Update(xitionTickMsg{gen: m.xitionGen, at: time.Now()})
	m = mm.(*model)
	if cmd == nil || m.xition == nil || m.xition.frame != 1 {
		t.Fatalf("live chain did not advance: %+v", m.xition)
	}
}

// A degenerate area (a region peeled to nothing) opens no wash.
func TestXitionIgnoresEmptyArea(t *testing.T) {
	m := newHomeModel(196, 24)
	m.startXition(rect{0, 0, 0, 23})
	if m.xition != nil || m.xitionArmed {
		t.Fatal("empty area opened a wash")
	}
}

// The wash covers its own rect and nothing else. Compared per CELL, not per
// line: the composited frame re-serializes SGR per run (canvasRefBuffer's
// premise), so raw rows differ everywhere even where the glass does not.
func TestXitionCoversItsRectOnly(t *testing.T) {
	m := newHomeModel(196, 24)
	m.widgetData["dock-rail"] = railData()
	before := canvasRefBuffer(m.View().Content, ansi.GraphemeWidth)
	area := rect{20, 0, 40, 4}
	m.startXition(area)
	after := canvasRefBuffer(m.View().Content, ansi.GraphemeWidth)

	washed := 0
	for y := range 24 {
		for x := range 196 {
			same := before.CellAt(x, y).Equal(after.CellAt(x, y))
			if area.contains(x, y) {
				if !same {
					washed++
				}
				continue
			}
			if !same {
				t.Fatalf("cell row %d col %d changed outside the wash rect", y, x)
			}
		}
	}
	// most of the rect is static on the first frame; the odd cell can match
	// what was under it
	if min := area.w * area.h * 3 / 4; washed < min {
		t.Errorf("washed cells = %d, want >= %d of %d", washed, min, area.w*area.h)
	}
}
