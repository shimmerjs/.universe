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
	// narrow enough that a six-choice band still fits one row. padTileMaxW
	// IS the key size on any glass wide enough for it -- see renderPad.
	padTileMinW = 9
	padTileMaxW = 22
	// padTileFloorH is the least a button can be: a frame around one
	// interior line, which is enough for a label with its value folded on.
	// It is also the DEFAULT -- a band grows past it only by asking
	// (MinHeight), never because the grid had rows to spare.
	padTileFloorH = 3
	// padGutterPad is the space around a section label in the gutter.
	padGutterPad = 2
)

// padBand is one labeled row of buttons.
type padBand struct {
	label string
	tiles []module.Row
}

// ask is the tile height the band wants: the tallest MinHeight any of its
// tiles declares, or the floor. Spare grid rows never grow a band past it,
// for two reasons. A band stretched into room it has nothing to put in gets a
// dead row inside every one of its tiles, which reads as a column of buttons
// whose readouts all failed; and a height that depended on the grid would
// reflow the whole pad under a thumb the moment a header alarm appeared.
// Leftover rows collect at the bottom, where they read as space.
func (b padBand) ask() int {
	h := padTileFloorH
	for _, t := range b.tiles {
		h = max(h, t.MinHeight)
	}
	return h
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

// budgetBands hands gridH out as one tile height per band. rows[i] is how
// many tile rows band i wraps to, so a band that wraps pays its height that
// many times over.
//
// Every band gets a frame before any band gets a second row. An even split
// cannot do that: five even shares of a grid two alarm lines shorter did not
// go, and the last band came off the pad silently. Asks are paid after, in
// publication order, so the transport carries weight and the choice bands
// take the remainder.
func budgetBands(bands []padBand, rows []int, gridH int) []int {
	h := make([]int, len(bands))
	rem := gridH
	for i := range bands {
		h[i] = padTileFloorH
		rem -= rows[i] * padTileFloorH
	}
	for i, b := range bands {
		// each extra row costs one per row the band wraps to
		for h[i] < b.ask() && rem >= rows[i] {
			h[i]++
			rem -= rows[i]
		}
	}
	return h
}

// renderPad draws header lines, then one band per section. Tiles with an
// Act fire it; tiles with only a Menu open it welded to the tile, so every
// button answers a plain tap.
//
// Section labels ride a left gutter rather than a row of their own: the pad
// is far wider than its buttons need, and rows are the scarce axis -- five
// label rows come out of the same twenty-one the buttons are competing for.
func (m *model) renderPad(id string, header []string, tiles []module.Row, rr rect) string {
	lines := make([]string, 0, rr.h)
	for _, h := range header {
		lines = append(lines, fitCellPad(h, rr.w))
	}
	gridH := rr.h - len(lines)
	bands := splitPadBands(tiles)
	if len(bands) == 0 || gridH < padTileFloorH || rr.w < padTileMinW {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return fixedBlock(lines, rr.w, rr.h)
	}
	// the gutter sizes to the widest label it has to carry, and yields
	// entirely when the glass is too narrow to spare it -- a button that
	// fits beats a section name
	gutterW := 0
	for _, b := range bands {
		if b.label != "" {
			gutterW = max(gutterW, lipgloss.Width(b.label)+padGutterPad)
		}
	}
	if rr.w-gutterW < padTileMinW {
		gutterW = 0
	}

	// One key size for the whole pad, and it comes from the GLASS, never
	// from what the tiles happen to say. A width derived from the longest
	// label resized every button on the pad the moment any band's content
	// changed -- probing devices, which only adds tiles to the choice bands,
	// pushed the transport keys from 19 cells to 22 under the thumb that had
	// just pressed one. A mixer's keys do not change size when you plug
	// something in.
	gridW := rr.w - gutterW
	w := min(padTileMaxW, gridW)
	perRow := max(gridW/w, 1)

	// a band wider than the glass wraps, and every extra row it wraps to is
	// another band's worth of grid. Price that in before anything is budgeted
	// -- a six-display screens band is two rows deep, not one.
	rows := make([]int, len(bands))
	for i, b := range bands {
		rows[i] = (len(b.tiles) + perRow - 1) / perRow
	}
	// bands that cannot hold a frame are not buttons: drop the tail rather
	// than draw rubble. With every band framed before any band is fattened
	// this only bites on glass far shorter than the Edge's.
	framed := 0
	for _, r := range rows {
		framed += r * padTileFloorH
	}
	for len(bands) > 0 && framed > gridH {
		framed -= rows[len(rows)-1] * padTileFloorH
		bands, rows = bands[:len(bands)-1], rows[:len(rows)-1]
	}
	if len(bands) == 0 {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return fixedBlock(lines, rr.w, rr.h)
	}

	bandH := budgetBands(bands, rows, gridH)
	y := rr.y + len(lines)
	for bi, b := range bands {
		tileH, rowsUsed := bandH[bi], 0
		bandTotal := rows[bi] * tileH
		x, drawn := rr.x+gutterW, 0
		var cells [][]string
		flush := func() {
			for j := range tileH {
				// the label rides the tile's MIDDLE row, not its first: row
				// zero is the top border, so a label there reads as sitting
				// above the band rather than naming it
				gut := strings.Repeat(" ", gutterW)
				if gutterW > 0 && rowsUsed == 0 && j == tileH/2 {
					gut = fitCellPad(chromeDim.Render(" "+strings.ToUpper(b.label)), gutterW)
				}
				sb := strings.Builder{}
				sb.WriteString(gut)
				for _, c := range cells {
					sb.WriteString(c[j])
				}
				lines = append(lines, sb.String())
			}
			cells, x = nil, rr.x+gutterW
			rowsUsed += tileH
		}
		for _, t := range b.tiles {
			if drawn == perRow {
				// a band wider than the glass wraps, but only into the
				// vertical room its own section was given
				if rowsUsed+2*tileH > bandTotal {
					break
				}
				flush()
				y += tileH
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
		for ; rowsUsed < bandTotal; rowsUsed++ {
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
	// a menu-less tile falls back to its act on a hold. The press light goes
	// up at contact either way, so a hold that resolved to nothing read as a
	// button that armed and then dropped the touch -- and the recognizer is
	// silent on a menu-less held release, so nothing later corrected it.
	hold := open
	if hold == nil && len(t.Act) > 0 {
		hold = do
	}
	m.hits = append(m.hits, hitRegion{
		area: area, do: do, longPress: hold,
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
// w x h. How the glyph lands depends on the height the tile was given -- its
// own line on a tall tile, joined to the label on a short one. The pressed
// treatment is the rail button's -- fill background plus the edge-hugging
// ring -- so a pad press feels like every other button on this glass.
func padTile(t module.Row, w, h int, border, label lipgloss.Style) []string {
	if w < padTileMinW || h < padTileFloorH {
		return strings.Split(blankBlock(w, h), "\n")
	}
	inner, interior := w-2, h-2
	text := t.Text
	// What the tile is owed: a label, a state line under it when it has a
	// value, and the glyph on a line of its own only if both still fit. The
	// value is state the pad is REPORTING; the glyph is decoration over a
	// label that already names the button. So when the interior fits two of
	// the three, the glyph joins the label and the value keeps its line --
	// the pad's loudest alarm is also the one with the longest value, and it
	// was the readout that vanished.
	want := 1
	if t.Value != "" {
		want++
	}
	var body []string
	switch {
	case t.Glyph == "":
	case interior > want:
		// Room for the icon to BE the button rather than prefix it: glyph on
		// its own line, name under it, state under that. A band earns this by
		// asking for the height (MinHeight), which on the recorder is the
		// transport's -- a record circle you aim a thumb at.
		body = append(body, label.Bold(true).Render(fitCell(t.Glyph, inner)))
	case inner > 2:
		// Crop the label, never drop the icon. A band reads as a strip of
		// icons, and the one tile whose generated label ran a cell long
		// losing its own reads as a different kind of button.
		text = t.Glyph + " " + fitCell(text, inner-2)
	}
	body = append(body, label.Bold(true).Render(fitCell(text, inner)))
	if t.Value != "" {
		if interior > len(body) {
			body = append(body, chromeDim.Render(fitCell(t.Value, inner)))
		} else {
			// One interior line for both, so fold the value onto the label,
			// pushed right so the band still scans as a column of labels. A
			// value that silently never renders is a readout lying by
			// omission: every state this pad reports (standing by, the device
			// list's age, a clip's name) lives in Value, so the label is what
			// gives way. A value with no room left for any label at all is
			// still dropped -- a tile crushed down to a state with no name on
			// it is not a button any more.
			if keep := inner - 1 - lipgloss.Width(t.Value); keep >= 1 {
				lab := fitCell(text, keep)
				gap := inner - lipgloss.Width(lab) - lipgloss.Width(t.Value)
				body[len(body)-1] = label.Bold(true).Render(lab) +
					strings.Repeat(" ", gap) + chromeDim.Render(t.Value)
			}
		}
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
