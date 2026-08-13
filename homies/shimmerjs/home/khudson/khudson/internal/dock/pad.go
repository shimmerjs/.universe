// Control-pad renderer: labeled bands of compact buttons, the hardware
// mixer idiom. Every CHOICE is its own button -- each mic, each display,
// each webcam preset -- because a popover on a touch surface is a menu
// you have to aim at twice. Modules publish RowPad rows (what the buttons
// are, which set they belong to, which are lit); this file owns the band
// geometry, the tones, and the press feedback.
package dock

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

const (
	// padTileMinW / padTileMaxW bound a button: wide enough for a thumb,
	// narrow enough that a six-choice band still fits one row.
	padTileMinW = 9
	padTileMaxW = 22
	// padTilePad is the horizontal breathing room around a label, on top
	// of the two border cells.
	padTilePad = 2
	// padBandMinH is a section band: one label row plus a button that can
	// hold a frame and its text.
	padBandMinH = 4
	// padTileMaxH keeps a sparse pad from inflating three buttons into
	// wall-sized slabs.
	padTileMaxH = 5
)

// padBand is one labeled row of buttons.
type padBand struct {
	label string
	tiles []module.Row
}

// splitPadBands groups consecutive pad rows by section, preserving module
// order. Rows with no section join the band in progress.
func splitPadBands(tiles []module.Row) []padBand {
	var bands []padBand
	for _, t := range tiles {
		if n := len(bands); n > 0 && (t.Section == "" || t.Section == bands[n-1].label) {
			bands[n-1].tiles = append(bands[n-1].tiles, t)
			continue
		}
		bands = append(bands, padBand{label: t.Section, tiles: []module.Row{t}})
	}
	return bands
}

// renderPad draws header lines, then one band per section. Tiles with an
// Act fire it; tiles with only a Menu open it welded to the tile, so every
// button answers a plain tap.
func (m *model) renderPad(id string, header []string, tiles []module.Row, rr rect) string {
	lines := make([]string, 0, rr.h)
	for _, h := range header {
		lines = append(lines, fitCellPad(h, rr.w))
	}
	gridH := rr.h - len(lines)
	bands := splitPadBands(tiles)
	if len(bands) == 0 || gridH < padBandMinH || rr.w < padTileMinW {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return fixedBlock(lines, rr.w, rr.h)
	}
	// bands that cannot hold a frame are not buttons: drop the tail into
	// the last band's overflow rather than drawing rubble
	if maxBands := gridH / padBandMinH; len(bands) > maxBands {
		bands = bands[:maxBands]
	}

	bandH := make([]int, len(bands))
	for i := range bandH {
		bandH[i] = gridH / len(bands)
	}
	for i := range gridH % len(bands) {
		bandH[i]++
	}

	// one key size for the whole pad: a deck reads as a grid, and bands
	// that size themselves independently read as unrelated widgets
	w := padTileMinW
	for _, t := range tiles {
		w = max(w, lipgloss.Width(t.Text)+2+padTilePad)
	}
	w = min(w, padTileMaxW, rr.w)
	perRow := max(rr.w/w, 1)

	y := rr.y + len(lines)
	for bi, b := range bands {
		// the section label rides its own dim row; the buttons take the rest.
		// y tracks the row the next appended line lands on, so it steps over
		// the label here -- a tile rect that started at the label row drifted
		// one row per band off the buttons it was supposed to be
		lines = append(lines, fitCellPad(chromeDim.Render(" "+strings.ToUpper(b.label)), rr.w))
		y++
		tileH := min(bandH[bi]-1, padTileMaxH)
		rowsUsed := 1 + tileH

		x, drawn := rr.x, 0
		var cells [][]string
		flush := func() {
			for j := range tileH {
				var sb strings.Builder
				for _, c := range cells {
					sb.WriteString(c[j])
				}
				lines = append(lines, sb.String())
			}
			cells, x = nil, rr.x
		}
		for _, t := range b.tiles {
			if drawn == perRow {
				// a band wider than the glass wraps, but only into the
				// vertical room its own section was given
				if rowsUsed+tileH > bandH[bi] {
					break
				}
				flush()
				y += tileH
				rowsUsed += tileH
				drawn = 0
			}
			area := rect{x, y, w, tileH}
			key := id + ":pad:" + t.Key
			border, label := m.padTones(t)
			if m.flashLive(key) {
				if bd, l, ok := m.pressStyles(); ok {
					border, label = bd, l
				} else {
					label = m.tapStyle(label)
				}
			}
			cells = append(cells, padTile(t, w, tileH, border, label))
			m.registerPadHit(id, t, area, key)
			x += w
			drawn++
		}
		flush()
		y += tileH
		// pad the band out so the next section starts where it was budgeted
		for ; rowsUsed < bandH[bi]; rowsUsed++ {
			lines = append(lines, "")
			y++
		}
	}
	// the pad owns every cell it drew; taps in the remainder die here
	m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
	return fixedBlock(lines, rr.w, rr.h)
}

// registerPadHit makes one button live. A tile carrying only a menu opens
// it on a plain tap (welded exactly where a long-press would put it): a
// pad has no discoverable long-press, so a menu-only button that ignored
// taps would be a dead button.
func (m *model) registerPadHit(id string, t module.Row, area rect, key string) {
	open := m.menuOpener(id, t.Menu)
	do := consumeTap
	switch {
	case len(t.Act) > 0:
		do = func(int, int) {
			m.flash(key)
			m.sendRowAct(id, t.Act)
		}
	case open != nil:
		do = func(px, py int) {
			m.flash(key)
			m.overlayOriginKey = key
			m.overlayOriginTile = area
			open(px, py)
		}
	}
	m.hits = append(m.hits, hitRegion{
		area: area, do: do, longPress: open,
		pressKey: key, weldTile: true,
	})
}

// padTones maps a tile's published style to its frame and label tones. The
// frame carries the state at arm's length -- warn for live, accent for
// engaged, dim for unavailable -- because on a touch surface the border is
// what you read before the text.
func (m *model) padTones(t module.Row) (border, label lipgloss.Style) {
	ss := m.rowStyles()
	switch t.Style {
	case module.StyleWarn:
		return ss.warn, ss.warn
	case module.StyleAccent:
		return ss.accent, ss.accent
	case module.StyleHighlight:
		return ss.highlight, ss.highlight
	case module.StyleDim:
		return chromeDim, ss.dim
	}
	return chromeDim, ss.fg
}

// padTile draws one button: the label centered in a frame sized to exactly
// w x h, with the glyph joined on when there is width for it. The pressed
// treatment is the rail button's -- fill background plus the edge-hugging
// ring -- so a pad press feels like every other button on this glass.
func padTile(t module.Row, w, h int, border, label lipgloss.Style) []string {
	if w < padTileMinW || h < 3 {
		return strings.Split(blankBlock(w, h), "\n")
	}
	inner := w - 2
	text := t.Text
	if t.Glyph != "" && lipgloss.Width(text)+2 <= inner {
		text = t.Glyph + " " + text
	}
	body := []string{label.Bold(true).Render(fitCell(text, inner))}
	if t.Value != "" && h >= 4 {
		body = append(body, chromeDim.Render(fitCell(t.Value, inner)))
	}
	// Width/Height are the OUTER extent here (borderedTile's convention),
	// so buttons butt against their neighbours and a band reads as one
	// strip of keys instead of a scatter of boxes
	box := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(border.GetForeground()).
		Width(w).
		Height(h).
		Align(lipgloss.Center, lipgloss.Center)
	if bg := border.GetBackground(); !isNoColor(bg) {
		box = box.Border(pressBorder).Background(bg).BorderBackground(bg)
	}
	return strings.Split(
		fixedBlock(strings.Split(box.Render(strings.Join(body, "\n")), "\n"), w, h),
		"\n")
}
