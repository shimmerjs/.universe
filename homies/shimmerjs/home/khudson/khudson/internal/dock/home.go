// Home layout: chrome (rail, tray) + content regions composed from config
// (layout ALGORITHM here, layout CONTENT in CUE). All-native: one khudson dock
// process renders everything; no scrape, no substrate.
package dock

import (
	"encoding/json"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// Chrome styles are ANSI-16 / default fg-bg (style deferral: the kitty theme
// is the theme). These are the base vocabulary AND the palette-nil fallback
// for the derived accents (style.go).
var (
	chromeFG        = lipgloss.NewStyle()
	chromeDim       = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	chromeAccent    = lipgloss.NewStyle().Foreground(lipgloss.Green)
	chromeWarn      = lipgloss.NewStyle().Foreground(lipgloss.Yellow)
	chromeHighlight = lipgloss.NewStyle().Foreground(lipgloss.Cyan).Bold(true)

	// gauge fill/track in the same ANSI-16 vocabulary; resource clusters use
	// them in every panel (gauge heat is data-not-style)
	gaugeFill16  = lipgloss.NewStyle().Background(lipgloss.Green)
	gaugeTrack16 = lipgloss.NewStyle().Background(lipgloss.BrightBlack)
)

// Series heat is data-not-style: green/yellow/red buckets, still theme-mapped.
var heatStyles = [3]lipgloss.Style{
	lipgloss.NewStyle().Foreground(lipgloss.Green),
	lipgloss.NewStyle().Foreground(lipgloss.Yellow),
	lipgloss.NewStyle().Foreground(lipgloss.Red),
}

// chromeRowStyles is the indexed-ANSI row vocabulary: the pre-broadcast
// default and the per-tone fallback newRowStyles derives from.
var chromeRowStyles = rowStyles{
	fg: chromeFG, dim: chromeDim, accent: chromeAccent, warn: chromeWarn,
	highlight: chromeHighlight,
	gaugeFill: gaugeFill16, gaugeTrack: gaugeTrack16,
	heat: heatStyles,
}

const (
	homeTileH    = 3  // tray buttons are 3 lines tall
	railTileH    = 3  // rail buttons: border + label + border, ~90px touch
	railTileW    = 11 // rail tiles cap at this width: more columns, not wider tiles
	trayFlashFor = 2 * time.Second
	// tapFlashFor is the tap-feedback window; one registry (m.trayFlash)
	// serves both flashes, tap keys namespaced ("tab:kb", "rail:safari",
	// "icon:home", "icon:chevron", "cup:0"), soon keys bare entry labels.
	tapFlashFor = 250 * time.Millisecond
)

// flashWindow is a flash key's lifetime: namespaced tap keys flash
// tapFlashFor, bare "soon" labels trayFlashFor.
func flashWindow(key string) time.Duration {
	if strings.Contains(key, ":") {
		return tapFlashFor
	}
	return trayFlashFor
}

// flash syncs the model clock to the tap's wall time before stamping:
// m.now otherwise lags up to 1 s behind the tap, and a stale stamp lets
// the next 1 s tick expire a late-interval tap after a fraction of its
// window. The jump can advance the marching attention border a frame on
// tap -- accepted. A tap flash also marks flashArmed for Update to drain
// into the one-shot expiry tick -- the 1 s clock alone would hold a
// 250 ms flash on glass for up to a second.
func (m *model) flash(key string) {
	if m.trayFlash == nil {
		m.trayFlash = make(map[string]time.Time)
	}
	m.now = time.Now()
	m.trayFlash[key] = m.now
	if flashWindow(key) == tapFlashFor {
		m.flashArmed = true
	}
	m.homeCache.ok = false
}

// flashLive reports whether key's flash is still unexpired.
func (m *model) flashLive(key string) bool {
	at, ok := m.trayFlash[key]
	return ok && m.now.Sub(at) < flashWindow(key)
}

// tapStyle is base with the tap-press treatment: an accent-filled chip
// (theme accent behind background-toned text) while the flash is live --
// unmistakably a pressed control. The old treatment lifted the theme
// background a hair under the label, which on glass read as TEXT
// SELECTION, twice reported (worse over a control whose action was
// broken: highlight, no effect). Background/foreground only -- zero
// geometry change; bright reverse as the indexed fallback.
func (m *model) tapStyle(base lipgloss.Style) lipgloss.Style {
	if ac, ok := m.palette.color("color2"); ok {
		if bg, ok := m.palette.color("background"); ok {
			return base.Foreground(bg).Background(ac).Bold(true)
		}
	}
	return base.Foreground(lipgloss.BrightWhite).Bold(true).Reverse(true)
}

// homeCache is the memoized home frame; ok=false means rebuild.
type homeCache struct {
	body string
	hits []hitRegion
	ok   bool
}

// trayFlashLive reports whether any flash -- "soon" stub or tap feedback --
// is still unexpired, purging expired entries as it scans (nothing else
// ever deletes them).
func (m *model) trayFlashLive() bool {
	live := false
	for key, at := range m.trayFlash {
		if m.now.Sub(at) < flashWindow(key) {
			live = true
		} else {
			delete(m.trayFlash, key)
		}
	}
	return live
}

// invalidateHome drops the cached home body when a widget placed by the
// active layout changes; off-screen updates keep the cache.
func (m *model) invalidateHome(id string) {
	if !m.homeCache.ok {
		return
	}
	l, ok := m.cfg.Layouts[m.layout]
	if !ok {
		return
	}
	for _, r := range l.Regions {
		if r.Widget == id {
			m.homeCache.ok = false
			return
		}
	}
}

// renderHome composes the home layout: peel each region off its declared
// edge in config order, split the remainder among fill regions. No outer
// frame -- the region borders are the chrome; the base box bought nothing
// at the glass edge. Rebuilds the hit table as it places regions.
func (m *model) renderHome(bodyH int) string {
	m.resetHits()
	l := m.cfg.Layouts[m.layout]
	inner := m.renderRegions(l.Regions, rect{0, 0, m.width, bodyH})
	return fixedBlock(strings.Split(inner, "\n"), m.width, bodyH)
}

// renderRegions peels regs[0] off its edge of box and recurses on the rest;
// a fill region hands the whole remainder to renderFills (config.check
// guarantees fills trail every edged region). Sizes clamp to the box so a
// small dock degrades instead of panicking.
func (m *model) renderRegions(regs []config.Region, box rect) string {
	if box.w <= 0 || box.h <= 0 {
		return ""
	}
	if len(regs) == 0 {
		return blankBlock(box.w, box.h)
	}
	r := regs[0]
	if r.Edge == "fill" {
		return m.renderFills(regs, box)
	}
	size := r.Size
	switch r.Edge {
	case "left", "right":
		size = min(size, box.w)
		rr := rect{box.x, box.y, size, box.h}
		rest := rect{box.x + size, box.y, box.w - size, box.h}
		if r.Edge == "right" {
			rr.x = box.x + box.w - size
			rest.x = box.x
		}
		block := m.renderRegion(r, rr)
		remainder := m.renderRegions(regs[1:], rest)
		if remainder == "" {
			return block
		}
		if r.Edge == "right" {
			return lipgloss.JoinHorizontal(lipgloss.Top, remainder, block)
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, block, remainder)
	case "top", "bottom":
		size = min(size, box.h)
		rr := rect{box.x, box.y, box.w, size}
		rest := rect{box.x, box.y + size, box.w, box.h - size}
		if r.Edge == "bottom" {
			rr.y = box.y + box.h - size
			rest.y = box.y
		}
		block := m.renderRegion(r, rr)
		remainder := m.renderRegions(regs[1:], rest)
		if remainder == "" {
			return block
		}
		if r.Edge == "bottom" {
			return lipgloss.JoinVertical(lipgloss.Left, remainder, block)
		}
		return lipgloss.JoinVertical(lipgloss.Left, block, remainder)
	}
	return m.renderRegions(regs[1:], box)
}

// renderFills splits box among the trailing fill regions evenly left to
// right; the leftmost fills absorb the remainder columns.
func (m *model) renderFills(regs []config.Region, box rect) string {
	base, extra := box.w/len(regs), box.w%len(regs)
	blocks := make([]string, 0, len(regs))
	x := box.x
	for i, r := range regs {
		w := base
		if i < extra {
			w++
		}
		if w <= 0 {
			continue
		}
		blocks = append(blocks, m.renderRegion(r, rect{x, box.y, w, box.h}))
		x += w
	}
	if len(blocks) == 0 {
		return blankBlock(box.w, box.h)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
}

// chromeRenderers maps a chrome widget's module to the dock-side renderer
// that owns its region frame.
var chromeRenderers = map[string]func(*model, config.Widget, rect) string{
	"dock-mirror": (*model).renderRail,
	"nav-tray":    (*model).renderTray,
	"resources":   (*model).renderResources,
	"kb-live":     (*model).renderKBLive,
}

// renderRegion draws one region at rr. A module with a registered dock-side
// renderer always draws its own frame, whatever the widget's chrome flag; a
// chrome widget whose module has no registered renderer is a loud warn box,
// never a silent titled region. Everything else gets a titled border around
// its module rows.
func (m *model) renderRegion(r config.Region, rr rect) string {
	w := m.cfg.Widgets[r.Widget]
	if render, ok := chromeRenderers[w.Render.Module]; ok {
		return render(m, w, rr)
	}
	if w.Chrome || w.Render.Kind == "chrome" {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return renderTitledBox(w.Title,
			[]string{chromeWarn.Render(" no chrome renderer: " + w.Render.Module)}, rr.w, rr.h)
	}
	return m.renderHomeWidget(w, rr)
}

// renderHomeWidget is a titled region: widget title on the border, module
// rows inside, poll errors loud. The region consumes every tap that lands in
// it; a tap on a content row with an act fires it, a long-press on a row
// with a menu opens the popover anchored at the press.
func (m *model) renderHomeWidget(w config.Widget, rr rect) string {
	content := rect{rr.x + 1, rr.y + 1, rr.w - 2, rr.h - 2}
	title := w.Title
	var lines []string
	var acts [][]string
	var menus [][]module.Act
	if e, ok := m.widgetErr[w.ID]; ok {
		lines = []string{chromeWarn.Render(" " + w.Render.Module + ": " + e)}
	} else if d, ok := m.widgetData[w.ID]; ok {
		// a module Data.Title carries live info (e.g. "claude 2/4"); the
		// config title is only the pre-data fallback
		if d.Title != "" {
			title = d.Title
		}
		lines, acts, menus = renderChromeRows(d, content.w, content.h, m.rowStyles())
	} else {
		lines = []string{chromeDim.Render(" ...")}
	}
	if m.widgetStale[w.ID] {
		// bold-vs-faint liveness idiom (rows.go): the frame is outdated
		title += chromeDim.Faint(true).Render(" stale")
	}
	var lp func(int, int)
	if anyMenu(menus) {
		lp = func(x, y int) {
			if !content.contains(x, y) {
				return
			}
			if row := y - content.y; row < len(menus) && len(menus[row]) > 0 {
				m.openOverlay(w.ID, "", menus[row], x, y)
			}
		}
	}
	m.hits = append(m.hits, hitRegion{area: rr, do: func(x, y int) {
		if !content.contains(x, y) {
			return
		}
		if row := y - content.y; row < len(acts) && len(acts[row]) > 0 {
			m.sendRowAct(w.ID, acts[row])
		}
	}, longPress: lp})
	if m.widgetData[w.ID].Attention {
		return renderAttentionBox(title, lines, rr.w, rr.h, m.attentionRamp(), m.attentionTick())
	}
	return renderTitledBox(title, lines, rr.w, rr.h)
}

// attentionTick is the marching border's phase: one step per dock tick
// (tickMsg advances m.now 1/s; no extra timer -- a slow crawl).
func (m *model) attentionTick() int {
	return int(m.now.Unix())
}

// attentionLive reports whether any widget placed by the active layout
// carries Data.Attention; while it does, the frame is clock-driven and View
// bypasses the home cache (the trayFlash precedent).
func (m *model) attentionLive() bool {
	if m.cfg == nil {
		return false
	}
	l, ok := m.cfg.Layouts[m.layout]
	if !ok {
		return false
	}
	for _, r := range l.Regions {
		if m.widgetData[r.Widget].Attention {
			return true
		}
	}
	return false
}

// renderRail draws the dock rail: a width-scaled grid of snug BORDERED
// buttons (border, label, border -- no padding rows or columns beyond the
// frame; borderless tiles read as floating labels on
// glass), one per actionable row (rows with an Act) from the dock-mirror
// module in row order -- running apps, then the minimized-window section as
// dim tiles -- tap = the row's act. Identity hue colors the running label;
// the minimized tier stays dim. Columns are the fewest that keep tiles at
// most railTileW wide, so a wide region grows columns instead of stretching
// tiles. Dim text rows without an Act (module degrade notes) list under the
// grid. When the tiles outgrow the grid the tail truncates into a dim "+N"
// cell, never silently.
func (m *model) renderRail(w config.Widget, rr rect) string {
	if e, bad := m.widgetErr[w.ID]; bad {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return fixedBlock([]string{chromeWarn.Render(" " + e)}, rr.w, rr.h)
	}
	var apps []module.Row
	var notes []string
	if d, ok := m.widgetData[w.ID]; ok {
		for _, r := range d.Rows {
			switch {
			case len(r.Act) > 0:
				apps = append(apps, r)
			case r.Kind == module.RowText && r.Text != "":
				// module degrade notes (e.g. the minimized sweep needing
				// accessibility): dim lines under the grid, loud not silent
				notes = append(notes, r.Text)
			}
		}
	}
	nicks := railNicknames(w.Render.Params)
	colors := railColors(w.Render.Params)
	cols := max((rr.w+railTileW+1)/(railTileW+1), 1)
	bw := (rr.w - (cols - 1)) / cols
	capacity := max((rr.h-len(notes))/railTileH, 0) * cols
	if bw < 4 || capacity < 1 {
		// region too small for tiles: a dim name list, never a panic
		lines := make([]string, 0, len(apps)+len(notes))
		for _, r := range apps {
			lines = append(lines, chromeDim.Render(fitCell(" "+railName(r, nicks), rr.w)))
		}
		for _, n := range notes {
			lines = append(lines, chromeDim.Render(fitCell(" "+n, rr.w)))
		}
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return fixedBlock(lines, rr.w, rr.h)
	}
	shown, overflow := apps, 0
	if len(shown) > capacity {
		shown = shown[:capacity-1]
		overflow = len(apps) - len(shown)
	}
	lpad := max((rr.w-(bw*cols+cols-1))/2, 0)
	cells := make([][]string, 0, len(shown)+1)
	for i, r := range shown {
		name := railName(r, nicks)
		// running tiles carry the app's identity hue on the name; the
		// minimized tier stays fully dim -- ANSI-16 has no faded hue
		// variant and the tier distinction outranks identity there
		label := chromeDim
		if r.Style != module.StyleDim {
			label = lipgloss.NewStyle().Foreground(railIdentity(r, name, colors))
		}
		// flash keys on the rail index, not the display name: two tiles
		// sharing a name must not flash together
		key := "rail:" + strconv.Itoa(i)
		if m.flashLive(key) {
			label = m.tapStyle(label)
		}
		cells = append(cells, railTile(name, bw, label))
		m.hits = append(m.hits, hitRegion{
			area: rect{rr.x + lpad + (i%cols)*(bw+1), rr.y + (i/cols)*railTileH, bw, railTileH},
			do: func(int, int) {
				m.flash(key)
				m.sendRowAct(w.ID, r.Act)
			},
			longPress: m.menuOpener(w.ID, name, r.Menu),
		})
	}
	if overflow > 0 {
		cells = append(cells, railTile(fmt.Sprintf("+%d", overflow), bw, chromeDim))
	}
	pad := strings.Repeat(" ", lpad)
	blank := strings.Split(blankBlock(bw, railTileH), "\n")
	var lines []string
	for i := 0; i < len(cells); i += cols {
		band := cells[i:min(i+cols, len(cells))]
		for j := range railTileH {
			var b strings.Builder
			b.WriteString(pad)
			for c := range cols {
				if c > 0 {
					b.WriteString(" ")
				}
				if c < len(band) {
					b.WriteString(band[c][j])
				} else {
					b.WriteString(blank[j])
				}
			}
			lines = append(lines, b.String())
		}
	}
	for _, n := range notes {
		lines = append(lines, chromeDim.Render(fitCell(" "+n, rr.w)))
	}
	if len(lines) == 0 {
		lines = []string{chromeDim.Render(" ...")}
	}
	m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
	return fixedBlock(lines, rr.w, rr.h)
}

// railName is the button text: the app name through the widget's nickname
// map, then lowercased.
func railName(r module.Row, nicks map[string]string) string {
	name := r.Text
	if name == "" {
		name = r.Key
	}
	if n, ok := nicks[name]; ok {
		name = n
	}
	return strings.ToLower(name)
}

// railNicknames reads params.nicknames: app name -> display nickname.
func railNicknames(params map[string]any) map[string]string {
	raw, _ := params["nicknames"].(map[string]any)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// railColors reads params.colors: app name -> identity color override.
func railColors(params map[string]any) map[string]string {
	raw, _ := params["colors"].(map[string]any)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// railIdentity is a running app's identity hue: a params.colors override
// (raw app name first, then the display name) beats the stable hash of
// the nicknamed display name -- nicknames apply before hashing so
// "chrome" and "Google Chrome" share a hue.
func railIdentity(r module.Row, display string, colors map[string]string) color.Color {
	for _, k := range []string{r.Text, r.Key, display} {
		if c, ok := colors[k]; ok && c != "" {
			return parseIdentColor(c)
		}
	}
	return identityHue(display)
}

// borderedTile is one rounded-border button box: the label fitCell-ed and
// centered, the border in the given tone. lipgloss v2 Width/Height are
// frame-inclusive, so the box is exactly w x h cells; w < 4 degrades to a
// blank block.
func borderedTile(label string, w, h int, border, style lipgloss.Style) []string {
	if w < 4 {
		return strings.Split(blankBlock(w, h), "\n")
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(border.GetForeground()).
		Width(w).
		Height(h).
		Align(lipgloss.Center, lipgloss.Center)
	return strings.Split(box.Render(style.Render(fitCell(label, w-2))), "\n")
}

// railTile is one grid button: a snug rounded-border box, the name
// fitCell-ed and centered on the middle row in the caller's tone (identity
// hue for running apps, dim for the minimized tier and the overflow cell).
// The border IS the button read; the only padding is the frame itself.
// fixedBlock re-asserts the geometry per the ambiguous-width convention.
func railTile(name string, w int, label lipgloss.Style) []string {
	return strings.Split(
		fixedBlock(borderedTile(name, w, railTileH, chromeDim, label), w, railTileH),
		"\n")
}

// trayEntry is one nav-tray button from config params.entries.
type trayEntry struct {
	label, target string
}

func trayEntries(params map[string]any) []trayEntry {
	raw, _ := params["entries"].([]any)
	out := make([]trayEntry, 0, len(raw))
	for _, e := range raw {
		f, ok := e.(map[string]any)
		if !ok {
			continue
		}
		label, _ := f["label"].(string)
		target, _ := f["target"].(string)
		if label == "" {
			label = target
		}
		if label == "" {
			continue
		}
		out = append(out, trayEntry{label: label, target: target})
	}
	return out
}

// trayEntriesFor memoizes trayEntries per widget: parsed once per config,
// not per frame (resetLayout drops the memo on config/layout changes).
func (m *model) trayEntriesFor(w config.Widget) []trayEntry {
	if e, ok := m.trayCache[w.ID]; ok {
		return e
	}
	if m.trayCache == nil {
		m.trayCache = make(map[string][]trayEntry)
	}
	e := trayEntries(w.Render.Params)
	m.trayCache[w.ID] = e
	return e
}

// Cup glyphs (nerd font material design icons, the schema's glyph
// vocabulary): filled while the caffeinate assertion is held, outline when
// off. Config params.toggles may override per entry.
const (
	cupOnGlyph  = "\U000F0176" // nf-md-coffee
	cupOffGlyph = "\U000F06CA" // nf-md-coffee_outline
)

// Battery glyphs (nerd font material design icons, the cup-glyph
// convention): the strip battery readout picks one by state-of-charge bucket,
// swapping to the charging glyph while wired. batUnknownGlyph is the
// no-data placeholder so the always-present cell never renders blank.
const (
	batEmptyGlyph        = "\U000F008E" // nf-md-battery_outline
	batQuarterGlyph      = "\U000F007B" // nf-md-battery_20
	batHalfGlyph         = "\U000F007E" // nf-md-battery_50
	batThreeQuarterGlyph = "\U000F0080" // nf-md-battery_70
	batFullGlyph         = "\U000F0079" // nf-md-battery
	batChargingGlyph     = "\U000F0084" // nf-md-battery_charging
	batUnknownGlyph      = "\U000F0091" // nf-md-battery_unknown
)

// trayToggle is one state-toggle button from nav-tray params.toggles; kind
// names the bus state it reflects ("caffeinate" is the only kind).
type trayToggle struct {
	kind, on, off string
}

// trayToggles reads params.toggles: [{kind, on?, off?}]. Unset glyphs take
// the cup defaults; unknown kinds are kept (rendered dead) so a config ahead
// of the binary is visible, never silent.
func trayToggles(params map[string]any) []trayToggle {
	raw, _ := params["toggles"].([]any)
	out := make([]trayToggle, 0, len(raw))
	for _, e := range raw {
		f, ok := e.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := f["kind"].(string)
		if kind == "" {
			continue
		}
		tg := trayToggle{kind: kind, on: cupOnGlyph, off: cupOffGlyph}
		if s, ok := f["on"].(string); ok && s != "" {
			tg.on = s
		}
		if s, ok := f["off"].(string); ok && s != "" {
			tg.off = s
		}
		out = append(out, tg)
	}
	return out
}

// renderTray draws the nav tray: one 3-line button per config entry stacked
// from the top, the current layout accented, recently-stubbed entries
// flashing "soon"; config toggles (the caffeinate cup) pin to the bottom of
// the region, filled glyph + accent while on, outline in the plain entry
// tone while off. When the region is short, toggles keep their bands and
// entries truncate -- the cup must stay reachable.
func (m *model) renderTray(w config.Widget, rr rect) string {
	entries := m.trayEntriesFor(w)
	toggles := trayToggles(w.Render.Params)
	bands := rr.h / homeTileH
	if len(toggles) > bands {
		toggles = toggles[:max(bands, 0)]
	}
	if maxE := bands - len(toggles); len(entries) > maxE {
		entries = entries[:max(maxE, 0)]
	}
	var lines []string
	for i, e := range entries {
		label := e.label
		flash := false
		if m.flashLive(e.label) {
			label, flash = "soon", true
		}
		lines = append(lines, trayButton(label, e.target == m.layout, flash, rr.w)...)
		m.hits = append(m.hits, hitRegion{
			area: rect{rr.x, rr.y + i*homeTileH, rr.w, homeTileH},
			do:   func(int, int) { m.trayActivate(e.target, e.label) },
		})
	}
	if len(entries) == 0 && len(toggles) == 0 {
		lines = []string{chromeDim.Render(" no entries")}
	}
	// pad so the toggle band sits flush with the region bottom
	if pad := rr.h - len(lines) - len(toggles)*homeTileH; pad > 0 {
		lines = append(lines, strings.Split(blankBlock(rr.w, pad), "\n")...)
	}
	for i, tg := range toggles {
		on := tg.kind == "caffeinate" && m.caffeinate == "on"
		glyph := tg.off
		if on {
			glyph = tg.on
		}
		label := glyph
		if tg.kind != "caffeinate" {
			// unknown kind: LOOK dead -- dim glyph, loud no-op tap (config
			// ahead of the binary stays visible, never healthy)
			label = chromeDim.Render(glyph)
		}
		lines = append(lines, trayButton(label, on, false, rr.w)...)
		y := rr.y + rr.h - (len(toggles)-i)*homeTileH
		m.hits = append(m.hits, hitRegion{
			area: rect{rr.x, y, rr.w, homeTileH},
			do: func(int, int) {
				if tg.kind == "caffeinate" {
					m.sendCaffeinateToggle()
				} else {
					m.lastGst = "toggle " + tg.kind + ": unknown kind"
				}
			},
		})
	}
	m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
	return fixedBlock(lines, rr.w, rr.h)
}

// sendCaffeinateToggle routes the cup tap through the bus like tray nav
// (TypeCtl, no resp -- the TypeCaffeinate broadcast is the ack and the
// re-render trigger); degraded state is loud when the bus is absent.
func (m *model) sendCaffeinateToggle() {
	if m.bus != busConnected || m.busConn == nil {
		m.lastGst = "caffeinate: bus absent"
		return
	}
	enc := json.NewEncoder(m.busConn)
	_ = enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "caffeinate", Arg: "toggle"})
	m.lastGst = "caffeinate: toggle"
}

// trayButton is one 3-line nav button with a lipgloss-centered label
// (frame-inclusive box construction: the style yields exactly w x homeTileH
// cells).
func trayButton(label string, current, flash bool, w int) []string {
	border, style := chromeDim, chromeFG
	if current {
		border, style = chromeAccent, chromeAccent
	}
	if flash {
		style = chromeWarn
	}
	return borderedTile(label, w, homeTileH, border, style)
}

// renderChromeRows maps module rows to lines via the shared row renderer in
// the given vocabulary, capped to the region's row budget. A degenerate
// region (interior shorter than the frame) caps at zero rows, never a
// negative slice.
func renderChromeRows(d module.Data, cols, rows int, ss rowStyles) (lines []string, acts [][]string, menus [][]module.Act) {
	lines, acts, menus = renderRows(d, cols, ss)
	if rows = max(rows, 0); len(lines) > rows {
		lines, acts, menus = lines[:rows], acts[:rows], menus[:rows]
	}
	return lines, acts, menus
}

// seriesLine is a series row: dim key, braille history padded to a stable
// width, current value in human units.
func seriesLine(r module.Row, cols int, ss rowStyles) string {
	prefix := fmt.Sprintf(" %-6s ", r.Key)
	sparkW := cols - lipgloss.Width(prefix) - lipgloss.Width(r.Value) - 1
	if sparkW < 1 {
		return ss.dim.Render(" "+r.Key+"  ") + ss.styleFor(r.Style).Render(r.Value)
	}
	return ss.dim.Render(prefix) + spark(r.Series, sparkW, ss.heat) + " " +
		ss.styleFor(r.Style).Render(r.Value)
}

// resourceLine is a resource cluster row: dim fixed-width label, current
// gauge, braille history, current value. Gauge and spark widths derive from
// cols alone (never the value), so every resource at one region width shares
// the same segment layout -- cpu, mem, and disk volumes render identically.
func resourceLine(r module.Row, cols int, ss rowStyles) string {
	prefix := fmt.Sprintf(" %-6s ", ansi.Cut(r.Key, 0, 6))
	avail := cols - lipgloss.Width(prefix)
	barW := min(max(avail/5, 4), 16)
	sparkW := min(max(avail*2/5, 8), module.MaxSeries)
	if avail-barW-sparkW-2 < lipgloss.Width(r.Value) {
		return ss.dim.Render(" "+r.Key+"  ") + ss.styleFor(r.Style).Render(r.Value)
	}
	return ss.dim.Render(prefix) + gaugeBar(r.Frac, barW, ss) + " " +
		spark(r.Series, sparkW, ss.heat) + " " + ss.styleFor(r.Style).Render(r.Value)
}

// spark renders the newest width samples as a braille bar chart, one cell
// per sample, colored by heat bucket (runs grouped to bound SGR churn).
func spark(samples []float64, width int, heat [3]lipgloss.Style) string {
	show := samples
	if len(show) > width {
		show = show[len(show)-width:]
	}
	var b strings.Builder
	for start := 0; start < len(show); {
		bkt := heatBucket(show[start])
		end := start + 1
		for end < len(show) && heatBucket(show[end]) == bkt {
			end++
		}
		runes := make([]rune, 0, end-start)
		for _, v := range show[start:end] {
			runes = append(runes, brailleRune(v))
		}
		b.WriteString(heat[bkt].Render(string(runes)))
		start = end
	}
	out := b.String()
	if pad := width - lipgloss.Width(out); pad > 0 {
		out += strings.Repeat(" ", pad)
	}
	return out
}

// heatBucket: <0.6 green, <0.85 yellow, else red.
func heatBucket(v float64) int {
	switch {
	case v < 0.6:
		return 0
	case v < 0.85:
		return 1
	}
	return 2
}

// brailleRune fills both dot columns bottom-up, 4 levels per cell.
func brailleRune(v float64) rune {
	v = min(max(v, 0), 1)
	lvl := min(1+int(v*4), 4)
	masks := [4]int{0xc0, 0x24, 0x12, 0x09}
	bits := 0
	for i := range lvl {
		bits |= masks[i]
	}
	return rune(0x2800 + bits)
}

// blankBlock is a w x h space block (empty peel remainders).
func blankBlock(w, h int) string {
	if w <= 0 || h <= 0 {
		return ""
	}
	line := strings.Repeat(" ", w)
	rows := make([]string, h)
	for i := range rows {
		rows[i] = line
	}
	return strings.Join(rows, "\n")
}

// fitCell crops s (ansi.Cut) until BOTH x/ansi and lipgloss agree it fits in
// w cells (they disagree on ambiguous-width glyphs; a wider measure would
// make pad/repeat counts negative and panic).
func fitCell(s string, w int) string {
	t := ansi.Cut(s, 0, w)
	for lipgloss.Width(t) > w && w > 0 {
		w--
		t = ansi.Cut(s, 0, w)
	}
	return t
}

// fitCellPad crops s to at most w cells (fitCell) and pads with bare spaces
// to exactly w, measured per the ambiguous-width convention.
func fitCellPad(s string, w int) string {
	t := fitCell(s, w)
	if pad := w - lipgloss.Width(t); pad > 0 {
		t += strings.Repeat(" ", pad)
	}
	return t
}

// fixedBlock pads/crops lines to exactly w x h so lipgloss joins keep the
// guillotine geometry exact.
func fixedBlock(lines []string, w, h int) string {
	out := make([]string, h)
	for i := range out {
		var l string
		if i < len(lines) {
			l = lines[i]
		}
		t := ansi.Cut(l, 0, w)
		if tw := lipgloss.Width(t); tw < w {
			t += "\x1b[m" + strings.Repeat(" ", w-tw)
		}
		out[i] = t
	}
	return strings.Join(out, "\n")
}

// renderTitledBox frames content lines with a 1-cell rounded border, the
// title embedded in the top edge; the body is fixedBlock-normalized to the
// interior.
func renderTitledBox(title string, body []string, w, h int) string {
	if w < 3 || h < 2 {
		return blankBlock(w, h)
	}
	// square, not rounded: frames sit flush with region fills (a bg fills
	// the whole cell; rounded corners leak tinted negative space), and all
	// non-button borders share one style
	bd := lipgloss.NormalBorder()
	innerW := w - 2
	t := title
	if t != "" {
		t = " " + t + " "
	}
	t = fitCell(t, innerW-1)
	lines := make([]string, 0, h)
	lines = append(lines, chromeDim.Render(bd.TopLeft+bd.Top)+chromeFG.Render(t)+
		chromeDim.Render(strings.Repeat(bd.Top, max(innerW-1-lipgloss.Width(t), 0))+bd.TopRight))
	if h > 2 {
		left, right := chromeDim.Render(bd.Left), chromeDim.Render(bd.Right)
		for c := range strings.SplitSeq(fixedBlock(body, innerW, h-2), "\n") {
			lines = append(lines, left+c+right)
		}
	}
	lines = append(lines, chromeDim.Render(bd.BottomLeft+strings.Repeat(bd.Bottom, innerW)+bd.BottomRight))
	return strings.Join(lines, "\n")
}

// renderAttentionBox is renderTitledBox with the frame marching: each border
// cell's foreground comes from ramp indexed by (perimeterPosition + tick) %
// len(ramp), positions walking clockwise from the top-left corner, so the
// warn tone crawls the frame one cell per tick. A nil ramp (no palette)
// alternates chromeWarn/chromeDim on the same phase. Body and geometry are
// renderTitledBox's exactly; the title keeps its chrome tone, its cells
// advancing the perimeter so the crawl stays continuous around it.
func renderAttentionBox(title string, body []string, w, h int, ramp []color.Color, tick int) string {
	if w < 3 || h < 2 {
		return blankBlock(w, h)
	}
	at := func(pos int) lipgloss.Style {
		if len(ramp) == 0 {
			if wrapIdx(pos+tick, 2) == 0 {
				return chromeWarn
			}
			return chromeDim
		}
		return lipgloss.NewStyle().Foreground(ramp[wrapIdx(pos+tick, len(ramp))])
	}
	bd := lipgloss.NormalBorder()
	innerW := w - 2
	t := title
	if t != "" {
		t = " " + t + " "
	}
	t = fitCell(t, innerW-1)
	tw := lipgloss.Width(t)
	lines := make([]string, 0, h)
	// perimeter positions clockwise: top row x, right side (w-1)+y, bottom
	// row (w-1)+(h-1)+(w-1-x), left side 2*(w-1)+(h-1)+(h-1-y)
	var top strings.Builder
	top.WriteString(at(0).Render(bd.TopLeft))
	top.WriteString(at(1).Render(bd.Top))
	top.WriteString(chromeFG.Render(t))
	for x := 2 + tw; x < w-1; x++ {
		top.WriteString(at(x).Render(bd.Top))
	}
	top.WriteString(at(w - 1).Render(bd.TopRight))
	lines = append(lines, top.String())
	if h > 2 {
		for i, c := range strings.Split(fixedBlock(body, innerW, h-2), "\n") {
			y := i + 1
			left := at(2*(w-1) + (h - 1) + (h - 1 - y)).Render(bd.Left)
			right := at(w - 1 + y).Render(bd.Right)
			lines = append(lines, left+c+right)
		}
	}
	var bot strings.Builder
	base := (w - 1) + (h - 1)
	bot.WriteString(at(base + w - 1).Render(bd.BottomLeft))
	for x := 1; x < w-1; x++ {
		bot.WriteString(at(base + w - 1 - x).Render(bd.Bottom))
	}
	bot.WriteString(at(base).Render(bd.BottomRight))
	lines = append(lines, bot.String())
	return strings.Join(lines, "\n")
}

// wrapIdx is the non-negative modulo: tick phases must not index negative on
// a zero clock.
func wrapIdx(a, n int) int {
	return ((a % n) + n) % n
}

// trayActivate navigates to the target layout when it exists (bus-routed,
// dock-local as the bus-absent fallback); otherwise flashes "soon" on the
// entry (stub targets are config-declared).
func (m *model) trayActivate(target, label string) {
	if _, ok := m.cfg.Layouts[target]; ok {
		m.navigateTo(target)
		return
	}
	m.flash(label)
	m.lastGst = "nav: " + label + " (soon)"
}

// sendRowAct forwards a tapped row's argv to the bus (acts run on the bus
// host); degraded state is loud when the bus is absent.
func (m *model) sendRowAct(widget string, argv []string) {
	if m.bus != busConnected || m.busConn == nil {
		m.lastGst = "row: bus absent"
		return
	}
	enc := json.NewEncoder(m.busConn)
	_ = enc.Encode(proto.Msg{Type: proto.TypeRowAct, Widget: widget, Argv: argv})
	m.lastGst = "row: " + strings.Join(argv, " ")
}
