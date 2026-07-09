// Shared module-row renderer: the native panel and the home chrome regions
// map module.Data through the same primitives, differing only in styles.
package dock

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// rowStyles is one panel's row vocabulary: the module style names plus the
// gauge fill/track pair, the spark heat ramp, and the optional gauge
// gradient stops (palette-derived; nil = flat fill).
type rowStyles struct {
	fg, dim, accent, warn lipgloss.Style
	highlight             lipgloss.Style
	gaugeFill, gaugeTrack lipgloss.Style
	// heat is the spark/heat-bucket ramp, indexed by heatBucket
	heat [3]lipgloss.Style
	// gaugeStops, when set, shade gauge fills cool -> hot across the bar
	// extent instead of the flat gaugeFill
	gaugeStops []color.Color
}

func dividerLine(cols int, ss rowStyles) string {
	return ss.dim.Render(strings.Repeat("-", max(min(cols-2, 40), 1)))
}

func (ss rowStyles) styleFor(s string) lipgloss.Style {
	switch s {
	case module.StyleDim:
		return ss.dim
	case module.StyleAccent:
		return ss.accent
	case module.StyleWarn:
		return ss.warn
	case module.StyleHighlight:
		return ss.highlight
	}
	return ss.fg
}

// spanStyle resolves one span's style; StyleTitle derives from the row's
// base style (bold), so a title span tracks the row's tone. A span Ident
// is identity data: it sets the hue, StyleTitle keeps bold, and the row's
// live/stale tone modulates ON TOP as bold-vs-faint so liveness still
// reads over the hue.
func (ss rowStyles) spanStyle(r module.Row, s module.Span) lipgloss.Style {
	if s.Ident != "" {
		st := lipgloss.NewStyle().Foreground(identityColor(s.Ident, nil))
		if s.Style == module.StyleTitle {
			st = st.Bold(r.Style != module.StyleDim)
		}
		if r.Style == module.StyleDim {
			st = st.Faint(true)
		}
		return st
	}
	if s.Style == module.StyleTitle {
		return ss.styleFor(r.Style).Bold(true)
	}
	return ss.styleFor(s.Style)
}

// renderRows maps module rows to lines; acts[i] is the argv behind line i
// (nil = not a button). MinHeight rows keep their act across every line they
// occupy. Uncapped: callers slice to their row budget.
func renderRows(d module.Data, cols int, ss rowStyles) (lines []string, acts [][]string) {
	barW := min(cols/3, 40)
	for _, r := range d.Rows {
		var line string
		switch r.Kind {
		case module.RowDivider:
			line = dividerLine(cols, ss)
		case module.RowKV:
			line = ss.dim.Render(" "+r.Key+"  ") + ss.styleFor(r.Style).Render(r.Value)
		case module.RowGauge:
			line = ss.dim.Render(fmt.Sprintf(" %-6s ", r.Key)) +
				gaugeBar(r.Frac, barW, ss) +
				ss.styleFor(r.Style).Render("  "+r.Value)
		case module.RowSeries:
			line = seriesLine(r, cols, ss)
		case module.RowSpans:
			var b strings.Builder
			b.WriteString(" ")
			for _, s := range r.Spans {
				b.WriteString(ss.spanStyle(r, s).Render(s.Text))
			}
			line = b.String()
			if cols > 0 {
				line = fitCell(line, cols)
			}
		case module.RowResource:
			// one renderer for every resource cluster (heat is
			// data-not-style, so it rides into the classic panel too)
			line = resourceLine(r, cols, ss)
		default:
			line = ss.styleFor(r.Style).Render(" " + r.Text)
		}
		lines = append(lines, line)
		acts = append(acts, r.Act)
		for extra := 1; extra < r.MinHeight; extra++ {
			lines = append(lines, "")
			acts = append(acts, r.Act)
		}
	}
	return lines, acts
}

// gaugeBar is a w-cell fill bar; frac arrives unclamped off the wire. With
// palette-derived gradient stops each fill cell takes the ramp tone at its
// bar position, so a filling gauge heats toward the hot stop (per-cell SGRs,
// bounded by the gauge width caps); without stops the fill is flat.
func gaugeBar(frac float64, w int, ss rowStyles) string {
	f := int(min(max(frac, 0), 1)*float64(w) + 0.5)
	track := ss.gaugeTrack.Render(strings.Repeat(" ", w-f))
	if len(ss.gaugeStops) == 0 || f < 1 {
		return ss.gaugeFill.Render(strings.Repeat(" ", f)) + track
	}
	ramp := lipgloss.Blend1D(w, ss.gaugeStops...)
	var b strings.Builder
	for i := range f {
		b.WriteString(lipgloss.NewStyle().Background(ramp[i]).Render(" "))
	}
	b.WriteString(track)
	return b.String()
}
