// Swipe transition: a low-fps static wash over the area a swipe changes, so
// a layout swap reads as a transition instead of a jump-cut. The new content
// lands mid-run (the bus TypeLayout broadcast is what flips the layout), so
// the wash masks the seam rather than sequencing around it.
package surface

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

const (
	// xitionFrames is the wash's length and xitionFrame its cadence: 5
	// frames at 80ms, ~12fps for 400ms. Coarse on purpose -- the dock's
	// steady state is a 1 s clock, and static gains nothing from a smoother
	// frame budget.
	xitionFrames = 5
	xitionFrame  = 80 * time.Millisecond

	// Fill at the first and last frame: the wash starts near-solid and
	// thins, so the swap surfaces out of settling static. Opaque
	// throughout -- lipgloss layers have no transparency, so a skipped
	// cell is a blank, not a peek at the frame below.
	xitionFillFirst = 0.85
	xitionFillLast  = 0.12
)

// noiseGlyphs is the static's vocabulary, light to dark: the shade blocks.
// kitty draws them procedurally, so they stay crisp at any cell size (the
// pressBorder precedent).
var noiseGlyphs = [3]string{"░", "▒", "▓"}

// xition is the live wash: the area it covers, the frames it has drawn, and
// the generation of the startXition that opened it. Bounded by construction
// -- the tick chain ends at xitionFrames, so the dock gains no steady-state
// timer, and per-frame cost is the area's cells.
type xition struct {
	area  rect
	frame int
	gen   int
}

// startXition opens the wash over area. A swipe landing mid-wash retargets
// it rather than queueing (the last stroke is the one on glass), and the new
// generation retires the running chain so two chains never advance one wash
// at double speed (the busGen convention). The armed mark is drained by
// Update into the tick chain, the flashArmed idiom.
func (m *model) startXition(area rect) {
	if area.w <= 0 || area.h <= 0 {
		return
	}
	m.xitionGen++
	m.xition = &xition{area: area, gen: m.xitionGen}
	m.xitionArmed = true
}

// advance steps the wash one frame and reports whether it is still live.
func (m *model) advanceXition() bool {
	if m.xition == nil {
		return false
	}
	m.xition.frame++
	if m.xition.frame >= xitionFrames {
		m.xition = nil
		return false
	}
	return true
}

// render draws the wash: an area-sized block of static, cells filled at the
// frame's density and shaded by the same per-cell mix. Deterministic in
// (frame, col, row) -- no PRNG state on the model, and a test renders the
// exact frame the glass draws.
func (x *xition) render() string {
	fill := xitionFill(x.frame)
	lines := make([]string, 0, x.area.h)
	var b strings.Builder
	for row := range x.area.h {
		b.Reset()
		for col := range x.area.w {
			h := noiseHash(x.frame, col, row)
			if float64(h&0xffff)/0x10000 >= fill {
				b.WriteString(" ")
				continue
			}
			// the shade picks off a different slice of the mix than the
			// fill test, so density and weight thin independently
			g := noiseGlyphs[(h>>16)%uint32(len(noiseGlyphs))]
			style := chromeDim
			if g == noiseGlyphs[2] {
				style = chromeFG
			}
			b.WriteString(style.Render(g))
		}
		lines = append(lines, b.String())
	}
	return fixedBlock(lines, x.area.w, x.area.h)
}

// layer positions the wash over the composed frame; nil-safe so View can
// call it unconditionally.
func (x *xition) layer(z int) *lipgloss.Layer {
	if x == nil {
		return nil
	}
	return lipgloss.NewLayer(x.render()).X(x.area.x).Y(x.area.y).Z(z)
}

// xitionFill is the share of cells the wash fills at frame: a linear thin
// from xitionFillFirst to xitionFillLast across the run.
func xitionFill(frame int) float64 {
	if xitionFrames < 2 {
		return xitionFillLast
	}
	t := float64(min(max(frame, 0), xitionFrames-1)) / float64(xitionFrames-1)
	return xitionFillFirst + (xitionFillLast-xitionFillFirst)*t
}

// noiseHash mixes one cell coordinate and frame into a scrambled word
// (splitmix32's finalizer over a per-axis odd-prime spread). Cheap, no
// state, and the frame term makes consecutive frames independent -- static
// shimmers where a fade would just dim.
func noiseHash(frame, col, row int) uint32 {
	h := uint32(col)*0x9e3779b1 + uint32(row)*0x85ebca6b + uint32(frame)*0xc2b2ae35
	h ^= h >> 16
	h *= 0x7feb352d
	h ^= h >> 15
	h *= 0x846ca68b
	h ^= h >> 16
	return h
}
