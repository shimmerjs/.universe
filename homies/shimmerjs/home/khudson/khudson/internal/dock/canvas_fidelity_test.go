package dock

// P0 overlay gate: routing the real dock frame through lipgloss.Canvas --
// the overlay compositor candidate, which hard-pins ansi.GraphemeWidth
// (lipgloss/v2 canvas.go) -- must be cell-identical to today's plain
// View().Content path. GraphemeWidth is also what bubbletea's renderer
// flips to when kitty reports mode 2027, and what the dock's own layout
// math uses (lipgloss.Width == ansi.StringWidth == grapheme width at
// x/ansi v0.11.7), so the binding diff runs under a GraphemeWidth
// reference buffer; the WcWidth default is diffed informationally to name
// which method diverges. Byte equality is unattainable -- Canvas.Render
// re-serializes SGR per cell run (ultraviolet renderLine) -- so both
// sides parse into cell buffers and compare per cell.

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
)

// canvasRefBuffer parses a styled frame into a 196x24 reference cell
// buffer under the given width method (only Canvas pins GraphemeWidth;
// NewScreenBuffer defaults to WcWidth, so set it explicitly).
func canvasRefBuffer(content string, method ansi.Method) uv.ScreenBuffer {
	buf := uv.NewScreenBuffer(196, 24)
	buf.Method = method
	uv.NewStyledString(content).Draw(buf, buf.Bounds())
	return buf
}

// canvasCellDiff compares two parsed frames cell-for-cell -- content,
// width, SGR, link -- naming every differing cell through report (t.Errorf
// for the binding method, t.Logf for the informational one); skip masks
// cells out of scope. Returns the differing positions for region
// attribution.
func canvasCellDiff(want, got uv.ScreenBuffer, skip func(x, y int) bool, report func(string, ...any)) []uv.Position {
	var diffs []uv.Position
	for y := range 24 {
		for x := range 196 {
			if skip != nil && skip(x, y) {
				continue
			}
			w, g := want.CellAt(x, y), got.CellAt(x, y)
			if w.Equal(g) {
				continue
			}
			diffs = append(diffs, uv.Pos(x, y))
			report("cell row %d col %d: want %q w%d sgr %q link %q params %q, got %q w%d sgr %q link %q params %q",
				y, x, w.Content, w.Width, w.Style.Diff(&uv.Style{}), w.Link.URL, w.Link.Params,
				g.Content, g.Width, g.Style.Diff(&uv.Style{}), g.Link.URL, g.Link.Params)
		}
	}
	return diffs
}

// canvasStripOnly reports whether every diff sits in the strip band (rows
// 22-23): the body-region-only fallback question on FAIL.
func canvasStripOnly(diffs []uv.Position) bool {
	for _, p := range diffs {
		if p.Y < 24-stripH {
			return false
		}
	}
	return true
}

// canvasDivergentGlyph picks a glyph the two width methods disagree on. At
// x/ansi v0.11.7 ansi.StringWidth and lipgloss.Width are the same grapheme
// measurer and cannot disagree with each other, so the live fitCell-class
// divergence is GraphemeWidth vs WcWidth -- VS16 emoji presentation and
// multi-rune clusters are that class.
func canvasDivergentGlyph(t *testing.T) string {
	t.Helper()
	for _, g := range []string{
		"\u2764\ufe0f",         // heart + VS16
		"\u26a0\ufe0f",         // warning sign + VS16
		"\U0001F44D\U0001F3FD", // thumbs up + skin tone
		"\U0001F1FA\U0001F1F8", // regional indicator pair
	} {
		if ansi.StringWidth(g) != ansi.StringWidthWc(g) {
			return g
		}
	}
	t.Fatal("no candidate glyph diverges between GraphemeWidth and WcWidth")
	return ""
}

// Full-frame round-trip fidelity: the real fixtures (strip band with the
// PUA nerd glyphs, keyboard body with textures, plus an injected
// divergent-width strip label) compose through the real Canvas API and
// must come back cell-identical under GraphemeWidth. On FAIL the per-cell
// diff is the deliverable.
func TestCanvasComposeFidelity(t *testing.T) {
	div := canvasDivergentGlyph(t)
	t.Logf("divergent glyph %q: grapheme width %d, wc width %d",
		div, ansi.StringWidth(div), ansi.StringWidthWc(div))
	frames := []struct {
		name  string
		model func(t *testing.T) *model
		carry []string // glyphs the frame must put on glass (width-gate permitting)
	}{
		{"strip", func(t *testing.T) *model { return stripModel() },
			[]string{homeGlyph, stripCollapseGlyph, cupOffGlyph}},
		{"strip-divergent-label", func(t *testing.T) *model {
			m := stripModel()
			m.cfg.Strip.Entries = append(m.cfg.Strip.Entries,
				config.StripEntry{Label: "w" + div, Target: "sys"})
			return m
		}, []string{div}},
		{"kb-oct-dot", func(t *testing.T) *model { return kbTexModel(t, "oct-dot") },
			kbTexGlyphs["oct-dot"]},
		{"kb-dots-grid", func(t *testing.T) *model { return kbTexModel(t, "dots-grid") },
			kbTexGlyphs["dots-grid"]},
		{"kb-dots", func(t *testing.T) *model { return kbTexModel(t, "dots") },
			kbTexGlyphs["dots"]},
	}
	for _, f := range frames {
		t.Run(f.name, func(t *testing.T) {
			m := f.model(t)
			base := m.View().Content
			if lines := strings.Count(base, "\n") + 1; lines != 24 {
				t.Fatalf("frame lines = %d, want 24", lines)
			}
			if kbGlyphSafe(f.carry) || f.name == "strip-divergent-label" {
				for _, g := range f.carry {
					if !strings.Contains(base, g) {
						t.Fatalf("frame does not carry %q; the fidelity claim would be vacuous", g)
					}
				}
			}

			out := lipgloss.NewCanvas(196, 24).Compose(lipgloss.NewLayer(base)).Render()

			want := canvasRefBuffer(base, ansi.GraphemeWidth)
			got := canvasRefBuffer(out, ansi.GraphemeWidth)
			if diffs := canvasCellDiff(want, got, nil, t.Errorf); len(diffs) > 0 {
				t.Errorf("%d cells diverge under GraphemeWidth (mode-2027 glass); strip-region-only: %v",
					len(diffs), canvasStripOnly(diffs))
			}

			wwWant := canvasRefBuffer(base, ansi.WcWidth)
			wwGot := canvasRefBuffer(out, ansi.WcWidth)
			if diffs := canvasCellDiff(wwWant, wwGot, nil, t.Logf); len(diffs) > 0 {
				t.Logf("%d cells diverge under the WcWidth default (pre-2027 glass only); strip-region-only: %v",
					len(diffs), canvasStripOnly(diffs))
			}
		})
	}
}

// An opaque renderTitledBox layered at an interior anchor perturbs nothing
// outside its rectangle: every outside cell stays identical to the plain
// path's cells. Positioned layering goes through lipgloss.NewCompositor --
// in v2.0.4 Layer.X/Y offsets are honored only by the Compositor's
// flatten/draw (Canvas.Compose passes the whole canvas bounds to a bare
// Layer, which draws its content at the origin and clears the area first).
func TestCanvasBoxLeavesBaseUntouched(t *testing.T) {
	m := stripModel()
	base := m.View().Content

	const bx, by, bw, bh = 60, 5, 40, 9
	box := renderTitledBox("t", []string{"alpha", "beta", "gamma"}, bw, bh)
	// explicit z: equal-z draw order under slices.SortFunc is unspecified
	comp := lipgloss.NewCompositor(
		lipgloss.NewLayer(base),
		lipgloss.NewLayer(box).X(bx).Y(by).Z(1),
	)
	out := lipgloss.NewCanvas(196, 24).Compose(comp).Render()

	want := canvasRefBuffer(base, ansi.GraphemeWidth)
	got := canvasRefBuffer(out, ansi.GraphemeWidth)

	// the layer must actually land (an overlay no-op would pass the
	// outside diff vacuously): the box corner sits at the anchor
	if c := got.CellAt(bx, by); c == nil || c.Content != lipgloss.NormalBorder().TopLeft {
		t.Fatalf("box corner cell = %+v, want %q at col %d row %d",
			c, lipgloss.NormalBorder().TopLeft, bx, by)
	}

	inBox := func(x, y int) bool { return x >= bx && x < bx+bw && y >= by && y < by+bh }
	if diffs := canvasCellDiff(want, got, inBox, t.Errorf); len(diffs) > 0 {
		t.Errorf("%d cells outside the box rect perturbed by the overlay layer", len(diffs))
	}
}
