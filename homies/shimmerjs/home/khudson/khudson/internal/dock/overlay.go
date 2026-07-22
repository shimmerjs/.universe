// The modal popover: a long-press context menu composited over the frame
// (View, dock.go) on the P0-pinned Canvas/GraphemeWidth path. Menus arrive
// as DATA (module.Row.Menu) -- the dock imports zero module impls -- and
// item taps exec through sendRowAct like any published row act. Hit
// resolution is khudson-owned cell rects computed at build time: lipgloss
// Compositor.Hit returns only the top-most layer id and cannot resolve
// items.
package dock

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// overlayItemH is one menu item's touch band: a single tight list row
// (~30px on glass). The long-press -> LIFT -> tap idiom still applies;
// the tighter target trades fat-finger margin for a compact menu.
const overlayItemH = 1

// weld names how the popover attaches to the long-pressed rail tile: the
// box anchors ON the tile's facing border column, its roof and floor lines
// continue the tile's own border (junction glyphs where the box outgrows
// the tile), and the shared wall stays OPEN across the tile's label band --
// the button opens straight into the menu, and the two read as one shape.
const (
	weldNone = iota
	weldRight
	weldLeft
)

// confirmPrefix relabels an armed destructive item; the box budgets its
// width at build time so arming never moves the geometry.
const confirmPrefix = "confirm "

// overlayState is the open popover; nil on the model means closed. box is
// rendered ONCE on open, on selection/confirm change, and when the bloom
// window closes -- never per tick; anchor is the clamped box rect the item
// rects derive from.
type overlayState struct {
	anchor  rect
	box     string
	items   []menuItem
	confirm *pendingConfirm
	weld    int // weldNone/weldRight/weldLeft: tile-attached border joins

	// openedAt drives the bloom: the frame renders accent for the menu's
	// first tapFlashFor, then settles to chrome (one rebuild, off the
	// flash tick). originKey is the element whose long-press opened the
	// menu; a fired item flashes it as the menu closes.
	openedAt  time.Time
	originKey string

	// info marks a display-only popover (the resources bloom): no items to
	// fire, and a second tap inside doubleTapWindow of openedWall converts
	// to the monitor layout. openedWall is wall-clock (the model clock is
	// frozen in golden tests and too coarse for a tap window).
	info       bool
	openedWall time.Time

	// widget routes the fired argv (sendRowAct). The box renders untitled:
	// the welded origin tile IS the menu's caption.
	widget string
}

// overlayBloomStyle is the plain-item label tone: accent while the menu
// blooms (the open cue), chrome fg once settled. The frame never carries
// it -- the box border stays in the tiles' dim tone so the welded pair
// reads as one consistent border.
func (m *model) overlayBloomStyle(o *overlayState) lipgloss.Style {
	if m.now.Sub(o.openedAt) < tapFlashFor {
		if ac, ok := m.palette.color("color5"); ok {
			return lipgloss.NewStyle().Foreground(ac)
		}
		return chromeAccent
	}
	return chromeFG
}

// overlayFillStyle is the modal body's raised backdrop: the theme's dark
// block (color0) behind every interior cell so the popover reads elevated
// above the base layer; no palette means no fill (indexed fallback).
func (m *model) overlayFillStyle() lipgloss.Style {
	if c, ok := m.palette.color("color0"); ok {
		return lipgloss.NewStyle().Background(c)
	}
	return lipgloss.NewStyle()
}

// menuItem is one tappable menu entry; area is its absolute cell rect,
// computed when the box is built. section groups items (module.Act.Section):
// the box draws a dotted rule where it changes and textures named sections.
type menuItem struct {
	label       string
	argv        []string
	area        rect
	destructive bool
	section     string
}

// pendingConfirm is the armed destructive item: item indexes items, area is
// the Confirm rect an explicit second tap must land in before the exec.
// armedAt gates the exec behind confirmArmDelay so a fat-finger double-tap
// cannot arm and fire in one bounce (the 2s bus debounce keys the exec argv,
// not the arm step).
type pendingConfirm struct {
	item    int
	area    rect
	armedAt time.Time
}

// confirmArmDelay is the minimum arm-to-confirm interval; taps on the
// Confirm rect inside it are consumed without firing.
const confirmArmDelay = 250 * time.Millisecond

// menuOpener is the hitRegion longPress slot for a row menu: nil when the
// row carries none, so menu-less regions stay transparent to long-presses.
func (m *model) menuOpener(widget string, menu []module.Act) func(int, int) {
	if len(menu) == 0 {
		return nil
	}
	return func(x, y int) { m.openOverlay(widget, menu, x, y) }
}

// anyMenu reports whether any rendered line carries a menu.
func anyMenu(menus [][]module.Act) bool {
	for _, mn := range menus {
		if len(mn) > 0 {
			return true
		}
	}
	return false
}

// openOverlay builds the popover for a row menu: geometry, item rects, and
// the box string are all computed here (and on confirm changes), never per
// tick. A press that rode a rail tile (overlayOriginTile) welds the box to
// the tile's right border, top-aligned -- mirrored to the left border when
// the frame is short on the right -- so the menu extends the button;
// otherwise the box clamps into the frame at the long-press cell.
func (m *model) openOverlay(widget string, menu []module.Act, x, y int) {
	tile := m.overlayOriginTile
	m.overlayOriginTile = rect{}
	items := make([]menuItem, 0, len(menu))
	labelW := 0
	for _, a := range menu {
		if len(a.Argv) == 0 {
			continue
		}
		label := a.Label
		if label == "" {
			label = strings.Join(a.Argv, " ")
		}
		w := lipgloss.Width(label)
		if a.Destructive {
			// budget the armed relabel now: geometry never moves on confirm
			w = lipgloss.Width(confirmPrefix + label)
		}
		labelW = max(labelW, w)
		items = append(items, menuItem{
			label: label, argv: a.Argv, destructive: a.Destructive,
			section: a.Section,
		})
	}
	if len(items) == 0 {
		return
	}
	boxW := min(labelW+4, m.width) // a space either side + the frame columns
	// a clamped box renders only whole rows: rects for items the chrome
	// truncates would alias trailing rows to invisible items. Rows = one
	// per item + a dotted rule per section change, plus the frame rows.
	rows, kept := 0, 0
	for i := range items {
		need := overlayItemH
		if i > 0 && items[i].section != items[i-1].section {
			need++
		}
		if rows+need+2 > m.height {
			break
		}
		rows += need
		kept++
	}
	if kept < 1 {
		return
	}
	items = items[:kept]
	boxH := rows + 2
	bx, by, weld := 0, 0, weldNone
	switch {
	case tile.h == railTileH && tile.x+tile.w-1+boxW <= m.width && tile.y+max(boxH, railTileH) <= m.height:
		// welded menus pad to tile height so the whole tile wall opens
		// into the raised surface
		boxH = max(boxH, railTileH)
		bx, by, weld = tile.x+tile.w-1, tile.y, weldRight
	case tile.h == railTileH && tile.x+1-boxW >= 0 && tile.y+max(boxH, railTileH) <= m.height:
		boxH = max(boxH, railTileH)
		bx, by, weld = tile.x+1-boxW, tile.y, weldLeft
	default:
		bx = min(max(x, 0), max(m.width-boxW, 0))
		by = min(max(y, 0), max(m.height-boxH, 0))
	}
	o := &overlayState{
		anchor: rect{bx, by, boxW, boxH},
		items:  items,
		weld:   weld,
		// openedAt rides the model clock, never time.Now(): resetting m.now
		// here would stomp frozen-clock tests, and the flash tick that
		// settles the bloom advances m.now past the window regardless of lag
		openedAt:  m.now,
		originKey: m.overlayOriginKey,
		widget:    widget,
	}
	yy := by + 1 // items start under the roof row
	for i := range o.items {
		if i > 0 && o.items[i].section != o.items[i-1].section {
			yy++
		}
		o.items[i].area = rect{bx, yy, boxW, overlayItemH}
		yy += overlayItemH
	}
	// bloom: the box opens accent-framed; flashArmed schedules the one-shot
	// tick that settles it to chrome (the same expiry redraw taps use)
	m.flashArmed = true
	o.box = o.render(m.overlayBloomStyle(o), m.overlayFillStyle())
	m.overlay = o
}

// render composes the popover as a raised BORDERED panel: the frame in the
// tiles' own dim tone (a welded box's border must read as ONE line with
// the tile's -- no tone seam) on the fill backdrop, one tight fill-backed
// row per item, bloom toning the plain labels while the menu opens --
// destructive items in the warn tone, the armed confirm target loud
// (reverse warn). A section change draws a dotted rule, and rows in a
// named section carry a speckled trailing pad (the textured minimized
// field). A welded box continues the tile's roof and floor lines and
// leaves the shared wall open across the tile's label band, so the button
// opens into the menu and the pair reads as one bordered shape.
func (o *overlayState) render(bloom, fill lipgloss.Style) string {
	w, h := o.anchor.w, o.anchor.h
	wi := max(w-2, 0)
	frame := chromeDim
	sep := chromeDim
	// GetBackground yields NoColor, not nil, when unset
	if bg := fill.GetBackground(); !isNoColor(bg) {
		frame = frame.Background(bg)
		sep = sep.Background(bg)
	}

	// interior rows: items and rules, then blank fill (welded boxes pad to
	// tile height so the surface stays solid)
	interior := make([]string, 0, max(h-2, 0))
	for i, it := range o.items {
		if i > 0 && it.section != o.items[i-1].section {
			interior = append(interior, sep.Render(strings.Repeat("┄", wi)))
		}
		label, style := it.label, bloom
		if it.destructive {
			style = chromeWarn
		}
		if bg := fill.GetBackground(); !isNoColor(bg) {
			style = style.Background(bg)
		}
		armed := o.confirm != nil && o.confirm.item == i
		if armed {
			// reverse swaps fg/bg itself: the fill must not ride under it
			label, style = confirmPrefix+label, chromeWarn.Bold(true).Reverse(true)
		}
		if it.section != "" && !armed {
			interior = append(interior, style.Render(fitCell(" "+label, wi))+
				sectionPad(lipgloss.Width(fitCell(" "+label, wi)), wi, sep))
		} else {
			interior = append(interior, style.Render(fitCellPad(" "+label, wi)))
		}
	}
	for len(interior) < h-2 {
		interior = append(interior, fill.Render(strings.Repeat(" ", wi)))
	}
	return o.frameBox(interior, frame)
}

// frameBox wraps prebuilt interior lines (already fill-padded to the
// interior width) in the box frame; the roof/floor rows and per-row frame
// columns come from frameEnds.
func (o *overlayState) frameBox(interior []string, frame lipgloss.Style) string {
	w, h := o.anchor.w, o.anchor.h
	wi := max(w-2, 0)
	lines := make([]string, 0, h)
	l, r := o.frameEnds(0, h)
	lines = append(lines, frame.Render(l+strings.Repeat("─", wi)+r))
	for i, row := range interior {
		l, r = o.frameEnds(i+1, h)
		lines = append(lines, frame.Render(l)+row+frame.Render(r))
	}
	l, r = o.frameEnds(h-1, h)
	lines = append(lines, frame.Render(l+strings.Repeat("─", wi)+r))
	return strings.Join(lines, "\n")
}

// frameEnds is the box's frame column pair for one row: a plain border on a
// free-standing box; on a welded box the shared wall opens across the
// tile's label band, the roof/floor lines run straight into the tile's own
// border, and the tile's floor turns down the box wall when the box
// outgrows the tile.
func (o *overlayState) frameEnds(row, h int) (string, string) {
	last := h - 1
	switch o.weld {
	case weldRight: // tile to the LEFT: the left column is the shared wall
		switch {
		case row == 0:
			return "─", "┐" // the tile's roofline continues into the box roof
		case row == last && h == railTileH:
			return "─", "┘" // floorline continues: one tile-height shape
		case row == last:
			return "└", "┘"
		case row == railTileH-1:
			return "┐", "│" // the tile's floor turns down the box wall
		case row < railTileH-1:
			return " ", "│" // open band: the tile's label row
		default:
			return "│", "│"
		}
	case weldLeft: // mirrored: the right column is the shared wall
		switch {
		case row == 0:
			return "┌", "─"
		case row == last && h == railTileH:
			return "└", "─"
		case row == last:
			return "└", "┘"
		case row == railTileH-1:
			return "│", "┌"
		case row < railTileH-1:
			return "│", " "
		default:
			return "│", "│"
		}
	}
	switch row {
	case 0:
		return "┌", "┐"
	case last:
		return "└", "┘"
	}
	return "│", "│"
}

// sectionPad is a textured trailing pad: a dim dot every third cell on the
// fill, the sectioned rows' speckled field.
func sectionPad(used, w int, sep lipgloss.Style) string {
	var b strings.Builder
	for i := used; i < w; i++ {
		if i%3 == 2 {
			b.WriteString("·")
		} else {
			b.WriteString(" ")
		}
	}
	return sep.Render(b.String())
}

// fireOverlayItem execs a menu item and closes the menu, flashing the
// element whose long-press opened it -- the fired feedback lands on the
// origin as the popover disappears.
func (m *model) fireOverlayItem(o *overlayState, it menuItem) {
	m.sendRowAct(o.widget, it.argv)
	if o.originKey != "" {
		m.flash(o.originKey)
	}
	m.overlay = nil
}

// overlayTap is the modal gate's dispatcher (resolveTap consumes the tap in
// every case, so base hits never fire through an open menu):
//
//	(a) a tap on an item rect fires it -- or arms the confirm on a
//	    destructive item, which execs only on a second explicit tap on the
//	    Confirm rect (the 2s bus debounce is amplification protection, not
//	    intent confirmation);
//	(b) a tap inside the box but on no item (border/title/padding) stays
//	    open -- fat-finger near-misses must not dismiss;
//	(c) a tap outside the box dismisses.
func (m *model) overlayTap(x, y int) {
	o := m.overlay
	for i := range o.items {
		it := o.items[i]
		if !it.area.contains(x, y) {
			continue
		}
		if it.destructive {
			if o.confirm != nil && o.confirm.item == i && o.confirm.area.contains(x, y) {
				if time.Since(o.confirm.armedAt) < confirmArmDelay {
					return
				}
				m.fireOverlayItem(o, it)
				return
			}
			o.confirm = &pendingConfirm{item: i, area: it.area, armedAt: time.Now()}
			o.box = o.render(m.overlayBloomStyle(o), m.overlayFillStyle())
			return
		}
		m.fireOverlayItem(o, it)
		return
	}
	if o.anchor.contains(x, y) {
		// an info bloom's quick second tap converts to the monitor layout
		// (the window measures from the FIRST tap; a faster second tap
		// converts pre-bloom via tapResources and never sees this path)
		if o.info && time.Since(o.openedWall) < doubleTapWindow {
			m.overlay = nil
			m.convertToMonitor()
		}
		return
	}
	m.overlay = nil
}
