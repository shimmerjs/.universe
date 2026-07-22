// Resources chrome renderer: the vitals card. Every leading RowResource
// renders one identical row -- plaintext word label, capped dot-grid
// rolling history (the braille spark), tone-ramped percent (PctHeat when
// the module overrides the ramp), raw x/y pair (RawX ramps by RawHeat
// toward the metric's danger bound; RawY, separator included, renders
// neutral so the bound column scans flat). The divider and process rows the
// composer still emits stay OFF glass: they feed the tap bloom, and a
// second tap inside doubleTapWindow converts to the monitor layout (btop).
package dock

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// vitalsSparkCap bounds the card's dot-grid width: the rolling window
// keeps its newest-samples semantics, just shorter, so the card runs about
// two thirds of the old full-region sprawl. The bloom carries the
// full-width history.
const vitalsSparkCap = 10

func (m *model) renderResources(w config.Widget, rr rect) string {
	if e, bad := m.widgetErr[w.ID]; bad {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return renderTitledBox(w.Title,
			[]string{chromeWarn.Render(" " + w.Render.Module + ": " + e)}, rr.w, rr.h)
	}
	d, ok := m.widgetData[w.ID]
	if !ok {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return renderTitledBox(w.Title, []string{chromeDim.Render(" ...")}, rr.w, rr.h)
	}
	m.hits = append(m.hits, hitRegion{area: rr, do: func(int, int) {
		m.tapResources(d, rr)
	}})
	return fixedBlock(vitalsLines(d, rr.w, m.rowStyles()), rr.w, rr.h)
}

// pendingBloom is the armed-but-not-open bloom: the first card tap records
// it, bloomDelay later it opens for a single tap, and a second tap inside
// the delay converts to the monitor layout with the bloom never flashing
// up mid-double-tap.
type pendingBloom struct {
	d  module.Data
	rr rect
	at time.Time
}

// bloomDelay is the single-tap open debounce: long enough for a double
// tap's second press to land first, short enough to read as instant.
const bloomDelay = 200 * time.Millisecond

// tapResources is the card's tap: convert on a fast second tap, else arm
// the deferred bloom (Update turns the armed mark into bloomTick).
func (m *model) tapResources(d module.Data, rr rect) {
	if m.resPending != nil {
		m.resPending = nil
		m.convertToMonitor()
		return
	}
	m.resPending = &pendingBloom{d: d, rr: rr, at: time.Now()}
	m.resPendingArmed = true
}

// convertToMonitor jumps to the fullscreen btop layout (shared by the
// pre-bloom double tap and the in-bloom conversion tap).
func (m *model) convertToMonitor() {
	if _, ok := m.cfg.Layouts[monitorLayout]; ok {
		m.navigateTo(monitorLayout)
	} else {
		m.lastGst = "monitor: no layout"
	}
}

// openPendingBloom opens an armed bloom whose debounce lapsed; the bloom's
// conversion window still measures from the FIRST tap.
func (m *model) openPendingBloom() {
	p := m.resPending
	if p == nil || time.Since(p.at) < bloomDelay {
		return
	}
	m.resPending = nil
	m.openResourcesBloom(p.d, p.rr)
	if o := m.overlay; o != nil {
		o.openedWall = p.at
	}
}

// cardRows splits the pre-divider prefix the card owns -- the composer's
// leading resource rows plus any dim degrade note standing in for a failed
// part (loud, never swallowed) -- from the divider-onward process table,
// which is bloom-only.
func cardRows(d module.Data) (lead, rest []module.Row) {
	for i, r := range d.Rows {
		if r.Kind == module.RowDivider {
			return d.Rows[:i], d.Rows[i:]
		}
	}
	return d.Rows, nil
}

// rawCols is the raw pair's shared sub-column geometry: X right-aligns in
// xW, the one-cell separator slot carries "/" (or space for disk's
// suffix-style pair), Y left-aligns in yW -- so the numerators, the
// slashes, and the bounds each form their own scannable column.
type rawCols struct{ xW, yW int }

func (rc rawCols) width() int {
	if rc.xW == 0 && rc.yW == 0 {
		return 0
	}
	return rc.xW + 1 + rc.yW
}

// splitRaw separates the pair's separator from RawY ("/12" -> "/", "12";
// " free" -> " ", "free"); the separator is one ASCII cell by contract.
func splitRaw(r module.Row) (x, sep, y string) {
	if r.RawY == "" {
		return r.RawX, "", ""
	}
	return r.RawX, r.RawY[:1], r.RawY[1:]
}

// vitalsLines renders one card row per resource with the raw sub-columns
// aligned across rows; a part's degrade note renders as its dim text row.
func vitalsLines(d module.Data, cols int, ss rowStyles) []string {
	lead, _ := cardRows(d)
	var rc rawCols
	for _, r := range lead {
		if r.Kind != module.RowResource {
			continue
		}
		x, _, y := splitRaw(r)
		rc.xW = max(rc.xW, lipgloss.Width(x))
		rc.yW = max(rc.yW, lipgloss.Width(y))
	}
	lines := make([]string, 0, len(lead))
	for _, r := range lead {
		switch r.Kind {
		case module.RowResource:
			lines = append(lines, vitalsLine(r, cols, rc, ss))
		case module.RowText:
			lines = append(lines, ss.styleFor(r.Style).Render(fitCell(" "+r.Text, cols)))
		}
	}
	return lines
}

// pctCell formats a resource fraction for the fixed percent column.
func pctCell(frac float64) string {
	return fmt.Sprintf("%3.0f%%", min(max(frac, 0), 1)*100)
}

// pctHeat is the percent's ramp fraction: PctHeat when the module set one
// (disk shows used% but heats by free space), Frac otherwise.
func pctHeat(r module.Row) float64 {
	if r.PctHeat > 0 {
		return r.PctHeat
	}
	return r.Frac
}

// metricName is the card label beside the glyph: the root volume reads
// "disk" (a bare "/" was unscannable); other volumes keep their path.
func metricName(name string) string {
	if name == "/" {
		return "disk"
	}
	return name
}

// vitalsLine is one card row: plaintext word label in the plain fg (the
// glyph experiment lost -- words scan, and dim was too quiet), capped
// dot-grid history, tone-ramped percent, sub-column-aligned raw pair.
func vitalsLine(r module.Row, cols int, rc rawCols, ss rowStyles) string {
	name, _ := splitKeyHint(r.Key)
	prefix := fmt.Sprintf(" %-4s ", metricName(name))
	pct := pctCell(r.Frac)
	sparkW := min(cols-lipgloss.Width(prefix)-1-lipgloss.Width(pct)-2-rc.width()-1, vitalsSparkCap)
	if sparkW < 1 {
		// too narrow for the grid: name, percent, nothing else
		return ss.fg.Render(" "+metricName(name)+" ") + textHeat(pctHeat(r), ss).Render(pct)
	}
	x, sep, y := splitRaw(r)
	xPad := strings.Repeat(" ", max(rc.xW-lipgloss.Width(x), 0))
	if sep == "" && rc.width() > 0 {
		sep = " "
	}
	tail := xPad + textHeat(r.RawHeat, ss).Render(x) +
		ss.dim.Render(fmt.Sprintf("%s%-*s", sep, rc.yW, y))
	return ss.fg.Render(prefix) + spark(r.Series, sparkW, ss.heat) + " " +
		textHeat(pctHeat(r), ss).Render(pct) + "  " + tail
}

// textHeat is the number ramp: neutral fg at rest, then the spark ramp's
// hotter buckets, so digits and dot grids heat together.
func textHeat(v float64, ss rowStyles) lipgloss.Style {
	if b := heatBucket(v); b > 0 {
		return ss.heat[b]
	}
	return ss.fg
}

// doubleTapWindow is the bloom's conversion window: a second tap inside it
// jumps to the monitor layout; later in-box taps are ordinary holds.
const doubleTapWindow = 400 * time.Millisecond

// monitorLayout hosts the fullscreen btop scrape the bloom converts to.
const monitorLayout = "monitor"

// openResourcesBloom raises the detail the card gave up: per metric a
// header (name, window hint, percent, raw pair -- the disk row's free
// space spelled out) over a full-width dot-grid history, then the divider
// and the process table. Outside taps dismiss; a quick second tap inside
// converts to monitorLayout (overlayTap owns both).
func (m *model) openResourcesBloom(d module.Data, rr rect) {
	boxW := min(max(rr.w, 24), m.width)
	wi := boxW - 2
	if wi < 8 || m.height < 4 {
		return
	}
	ss := m.rowStyles()
	fill := m.overlayFillStyle()
	bg := fill.GetBackground()
	on := func(st lipgloss.Style) lipgloss.Style {
		if !isNoColor(bg) {
			return st.Background(bg)
		}
		return st
	}
	pad := func(s string) string {
		s = fitCell(s, wi) // a header can outgrow the clamped box; crop, never overflow the frame
		if p := wi - lipgloss.Width(s); p > 0 {
			s += fill.Render(strings.Repeat(" ", p))
		}
		return s
	}

	lead, rest := cardRows(d)
	var lines []string
	for _, r := range lead {
		if r.Kind == module.RowText {
			// a failed part's degrade note: loud in the bloom too
			lines = append(lines, pad(on(ss.styleFor(r.Style)).Render(" "+r.Text)))
			continue
		}
		name, hint := splitKeyHint(r.Key)
		hdr := on(ss.fg).Render(" " + metricName(name) + " ")
		if hint != "" {
			hdr += on(ss.dim).Render("(" + hint + ") ")
		}
		hdr += on(textHeat(pctHeat(r), ss)).Render(pctCell(r.Frac)) + fill.Render("  ") +
			on(textHeat(r.RawHeat, ss)).Render(r.RawX) + on(ss.dim).Render(r.RawY)
		lines = append(lines, pad(hdr))
		lines = append(lines, pad(fill.Render(" ")+spark(r.Series, wi-2, ss.heat)))
	}
	keyW, valW := kvColumns(rest)
	for _, r := range rest {
		switch r.Kind {
		case module.RowDivider:
			lines = append(lines, on(chromeDim).Render(strings.Repeat("┄", wi)))
		case module.RowKV:
			key := fitCell(r.Key, kvNameCap)
			kp := strings.Repeat(" ", max(keyW-lipgloss.Width(key), 0))
			lines = append(lines, pad(on(ss.dim).Render(" "+key)+fill.Render(kp+"  ")+
				on(ss.styleFor(r.Style)).Render(alignFields(r.Value, valW))))
		case module.RowText:
			lines = append(lines, pad(on(ss.styleFor(r.Style)).Render(" "+r.Text)))
		}
	}
	// clamp the box on glass, preferring the card's own origin
	h := len(lines) + 2
	if h > m.height {
		lines = lines[:max(m.height-2, 0)]
		h = len(lines) + 2
	}
	bx := min(max(rr.x, 0), max(m.width-boxW, 0))
	by := min(max(rr.y, 0), max(m.height-h, 0))
	o := &overlayState{
		anchor:     rect{bx, by, boxW, h},
		info:       true,
		openedAt:   m.now,
		openedWall: time.Now(),
		widget:     "resources",
	}
	o.box = o.frameBox(lines, on(chromeDim))
	m.overlay = o
}

// kvNameCap bounds the KV table's name column.
const kvNameCap = 24

// kvColumns measures the KV table: the name column width (capped at
// kvNameCap), and per-column widths of the space-split value fields, so the
// table aligns in the renderer while modules stay layout-blind.
func kvColumns(rows []module.Row) (keyW int, valW []int) {
	for _, r := range rows {
		if r.Kind != module.RowKV {
			continue
		}
		keyW = max(keyW, lipgloss.Width(fitCell(r.Key, kvNameCap)))
		for i, f := range strings.Fields(r.Value) {
			if i == len(valW) {
				valW = append(valW, 0)
			}
			valW[i] = max(valW[i], lipgloss.Width(f))
		}
	}
	return keyW, valW
}

// alignFields right-aligns each space-split field of v in its column width.
func alignFields(v string, valW []int) string {
	fields := strings.Fields(v)
	for i, f := range fields {
		if i < len(valW) {
			if p := valW[i] - lipgloss.Width(f); p > 0 {
				fields[i] = strings.Repeat(" ", p) + f
			}
		}
	}
	return strings.Join(fields, " ")
}

// splitKeyHint splits a resource Key into the metric name and the trailing
// window hint the module appends ("cpu 6h" -> "cpu", "6h"). Cut at the
// LAST space: a volume path may itself contain spaces.
func splitKeyHint(key string) (name, hint string) {
	if i := strings.LastIndex(key, " "); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}
