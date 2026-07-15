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

// overlayItemH is one menu item's touch band, the rail/tray tile height
// (~90px on glass): the recognizer swallows all motion in stateHeld, so
// the idiom is long-press -> LIFT -> a separate tap, and slide-to-select
// is impossible -- targets must be fat enough for the second touch.
const overlayItemH = 3

// confirmPrefix relabels an armed destructive item; the box budgets its
// width at build time so arming never moves the geometry.
const confirmPrefix = "confirm "

// overlayState is the open popover; nil on the model means closed. box is
// rendered ONCE on open and on selection/confirm change, never per tick;
// anchor is the clamped box rect the item rects derive from.
type overlayState struct {
	anchor  rect
	box     string
	items   []menuItem
	sel     int
	confirm *pendingConfirm

	// widget + title route the fired argv (sendRowAct) and caption the box.
	widget string
	title  string
}

// menuItem is one tappable menu entry; area is its absolute cell rect,
// computed when the box is built.
type menuItem struct {
	label       string
	kind        string
	argv        []string
	area        rect
	destructive bool
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
func (m *model) menuOpener(widget, title string, menu []module.Act) func(int, int) {
	if len(menu) == 0 {
		return nil
	}
	return func(x, y int) { m.openOverlay(widget, title, menu, x, y) }
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

// openOverlay builds the popover for a row menu anchored at the long-press
// cell: geometry, item rects, and the box string are all computed here (and
// on confirm changes), never per tick. The box clamps into the frame so an
// edge-adjacent anchor stays fully on glass.
func (m *model) openOverlay(widget, title string, menu []module.Act, x, y int) {
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
			label: label, kind: "act", argv: a.Argv, destructive: a.Destructive,
		})
	}
	if len(items) == 0 {
		return
	}
	boxW := min(labelW+2+2, m.width) // a space either side + the frame
	boxH := min(len(items)*overlayItemH+2, m.height)
	// a frame-clamped box renders only whole item bands: rects for items the
	// chrome truncates would alias the bottom border to an invisible item
	if fit := (boxH - 2) / overlayItemH; fit < len(items) {
		if fit < 1 {
			return
		}
		items = items[:fit]
		boxH = fit*overlayItemH + 2
	}
	bx := min(max(x, 0), max(m.width-boxW, 0))
	by := min(max(y, 0), max(m.height-boxH, 0))
	o := &overlayState{
		anchor: rect{bx, by, boxW, boxH},
		items:  items,
		sel:    -1,
		widget: widget,
		title:  title,
	}
	for i := range o.items {
		o.items[i].area = rect{bx + 1, by + 1 + i*overlayItemH, boxW - 2, overlayItemH}
	}
	o.box = o.render()
	m.overlay = o
}

// render composes the popover chrome: renderTitledBox around one
// overlayItemH band per item, the label on the band's middle row --
// destructive items in the warn tone, the armed confirm target loud
// (reverse warn). Opaque by construction: the box interior is real cells.
func (o *overlayState) render() string {
	innerW := o.anchor.w - 2
	lines := make([]string, 0, len(o.items)*overlayItemH)
	for i, it := range o.items {
		label, style := it.label, chromeFG
		if it.destructive {
			style = chromeWarn
		}
		if o.confirm != nil && o.confirm.item == i {
			label, style = confirmPrefix+label, chromeWarn.Bold(true).Reverse(true)
		}
		for row := range overlayItemH {
			if row == overlayItemH/2 {
				lines = append(lines, style.Render(fitCellPad(" "+label, innerW)))
			} else {
				lines = append(lines, "")
			}
		}
	}
	return renderTitledBox(o.title, lines, o.anchor.w, o.anchor.h)
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
				m.sendRowAct(o.widget, it.argv)
				m.overlay = nil
				return
			}
			o.sel = i
			o.confirm = &pendingConfirm{item: i, area: it.area, armedAt: time.Now()}
			o.box = o.render()
			return
		}
		m.sendRowAct(o.widget, it.argv)
		m.overlay = nil
		return
	}
	if o.anchor.contains(x, y) {
		return
	}
	m.overlay = nil
}
