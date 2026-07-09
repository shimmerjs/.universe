// Keyboard layout kind: the user's Moonlander layout, one layer at a time
// with a tap-to-cycle layer selector, plus the live-highlight overlay. The
// static board renders offline (built once from the Keymapp DB, Oryx cache
// fallback) so the view works unplugged; when touchd's Moonlander reader is
// streaming, TypeKey broadcasts light pressed keys on the same render and
// follow the active layer (design doctrine: static view = static only,
// live view = static + HID overlay, JOINed at this renderer).
//
// Render v2 is minimal -- no box per key. Each key is one padded legend
// cell (tap line + dim hold line), rows separated by whitespace, the two
// halves mirrored around a center gap. The thumb cluster is physical: the
// wide "piano" key raised one row ABOVE the 3-key arc, each cluster against
// the center gap with the wide key over the arc end nearest the main block.
// Pressed keys render as reverse-video blocks.
//
// One layer at a time (not all-at-once): 4-8 layers of per-key tap+hold
// legends cannot legibly coexist at 196x24. The selector strip names every
// layer, the active one accented and identity-tinted; a tap in the region
// cycles to the next layer, a tap on a named selector button jumps to it.
package dock

import (
	"image/color"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/keymappdb"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

const (
	// kbKeyWMax/kbKeyWMin bound the adaptive key cell width; the gaps are
	// fixed columns
	kbKeyWMax = 11
	kbKeyWMin = 4
	kbColGap  = 1 // columns between key cells within a half
	kbHalfGap = 5 // columns between the two halves
)

// kbFullLines is the full render's body line count: selector + blank +
// 5 main rows x 2 (tap+hold) + blank + 4 thumb lines. A region interior
// shorter than this auto-engages the compact render (tap legends only,
// thumb cluster folded to one line): selector + 5 main + 1 thumb = 7 lines.
const kbFullLines = 17

// ensureBoard loads the static board once. Since kb-live sits on the default
// home layout the first attempt fires at dock startup, so a MISSING store
// must not latch: stay unlatched behind a cheap stat and adopt a later
// Keymapp sync without a dock restart -- otherwise the "open Keymapp" hint
// is unfulfillable. A store that exists but fails to load still latches
// (no per-frame re-parse of a corrupt DB). Never fatal.
func (m *model) ensureBoard() {
	if m.kbLoaded {
		return
	}
	path, err := keymappdb.DefaultPath()
	if err != nil {
		m.kbLoaded = true
		m.kbErr = err.Error()
		return
	}
	if _, err := os.Stat(path); err != nil {
		m.kbErr = err.Error()
		return
	}
	m.kbLoaded = true
	m.kbErr = ""
	rev, err := keymappdb.Active(path)
	if err != nil {
		m.kbErr = err.Error()
		return
	}
	m.kbBoard = keyboard.FromRevision(rev)
	if m.kbBoard == nil || len(m.kbBoard.Layers) == 0 {
		m.kbErr = "layout has no layers"
	}
}

// handleKeyMsg folds one TypeKey broadcast into the keyboard view state:
// press/release toggles the held overlay (matrix coords resolved through
// the geometry bridge), a layer event retargets the rendered layer so the
// glass follows the board, and clear (keys source lost) drops every
// highlight so nothing sticks. The first event loads the static board
// (lazy-once); only a failed load drops events -- there is nothing to
// highlight then.
//
// Wiring (dock.go handleBusMsg): case proto.TypeKey: m.handleKeyMsg(msg)
func (m *model) handleKeyMsg(msg proto.Msg) {
	m.ensureBoard()
	ev := msg.Key
	if ev == nil || m.kbBoard == nil {
		return
	}
	changed := false
	switch ev.Kind {
	case proto.KeyEventKey:
		slot := keyboard.SlotAt(ev.Row, ev.Col)
		if slot < 0 {
			return
		}
		switch {
		case ev.Pressed:
			if m.kbBoard.Held == nil {
				m.kbBoard.Held = make(map[int]bool)
			}
			m.kbBoard.Held[slot] = true
			changed = true
		case m.kbBoard.Held[slot]:
			delete(m.kbBoard.Held, slot)
			changed = true
		}
	case proto.KeyEventLayer:
		if ev.Layer >= 0 && ev.Layer < len(m.kbBoard.Layers) && ev.Layer != m.kbLayer {
			m.kbLayer = ev.Layer
			changed = true
		}
	case proto.KeyEventClear:
		if len(m.kbBoard.Held) > 0 {
			m.kbBoard.Held = nil
			changed = true
		}
	}
	// the keyboard body is memoized in homeCache; only a visible keyboard
	// surface -- the keyboard layout or an active kb-live region -- needs the
	// re-render (a layout switch resets the cache anyway). With kb-live
	// visible every press/release recomposes the whole home frame; per-region
	// caching is the escape hatch if that ever measures too hot.
	if changed && (m.layoutKind() == "keyboard" || m.kbLiveVisible()) {
		m.homeCache.ok = false
	}
}

// kbLiveVisible reports whether the active layout places a kb-live widget.
// Module-keyed (matching the chromeRenderers dispatch): TypeKey carries no
// widget id, so invalidateHome's id match does not apply.
func (m *model) kbLiveVisible() bool {
	if m.cfg == nil {
		return false
	}
	l, ok := m.cfg.Layouts[m.layout]
	if !ok {
		return false
	}
	for _, r := range l.Regions {
		if m.cfg.Widgets[r.Widget].Render.Module == "kb-live" {
			return true
		}
	}
	return false
}

// renderKeyboard draws the fullscreen keyboard view: a titled box holding
// the layer selector strip and the active layer's key grid. Rebuilds the hit
// table (selector buttons + a whole-interior cycle target).
func (m *model) renderKeyboard(bodyH int) string {
	m.resetHits()
	interior := rect{1, 1, m.width - 2, bodyH - 2}
	body := m.kbRegionBody(interior, "", m.kbTexture())

	title := "keyboard"
	if m.kbBoard != nil && m.kbBoard.Title != "" {
		title = "keyboard: " + m.kbBoard.Title
	}
	if m.kbErr != "" || m.kbBoard == nil || len(m.kbBoard.Layers) == 0 {
		m.hits = append(m.hits, hitRegion{area: interior, do: consumeTap})
	} else {
		// a tap anywhere in the interior (outside the selector buttons, which
		// were appended first and win because the hit table is first-match)
		// cycles to the next layer
		m.hits = append(m.hits, m.kbCycleHit(interior))
	}
	return kbTitledBox(title, body, m.width, bodyH, m.kbLayerEdge(m.kbLayer))
}

// kbRegionBody is the rect-scoped render core shared by the fullscreen view
// and the kb-live region widget: the selector strip and the active layer grid
// laid out for rr, or the sync hint when the store is empty/unreadable. It
// appends ONLY the selector-button hits (offset to rr); hosts own resetHits,
// the layer-cycle hit, and the error-branch consumeTap. mode is "full",
// "compact", or "" (auto: compact when rr.h cannot hold the full render);
// texture names the fill texture (config-vetted; "" or "none" = plain fill).
func (m *model) kbRegionBody(rr rect, mode, texture string) []string {
	m.ensureBoard()
	if m.kbErr != "" || m.kbBoard == nil || len(m.kbBoard.Layers) == 0 {
		// empty / unreadable DB: a dim, calm hint, never a crash
		msg := " open Keymapp and connect your board to sync the layout"
		if m.kbErr != "" && m.kbErr != keymappdb.ErrNoRevision.Error() {
			msg = " keymapp db: " + m.kbErr
		}
		return []string{"", chromeDim.Render(msg)}
	}
	if m.kbLayer >= len(m.kbBoard.Layers) || m.kbLayer < 0 {
		m.kbLayer = 0
	}
	compact := mode == "compact" || (mode != "full" && rr.h < kbFullLines)
	chip := m.kbLayerChip(m.kbLayer)
	fill := kbFill(chip, texture)
	lines := make([]string, 0, rr.h)
	if compact {
		lines = append(lines, m.kbSelector(rr, chip))
	} else {
		lines = append(lines, m.kbSelector(rr, chip), "")
	}
	grid := kbLayerGrid(m.kbBoard.Layers[m.kbLayer], rr.w, m.kbBoard.Held, compact, chip, fill, len(lines))
	lines = append(lines, grid...)
	// flood every interior cell: right-pad each line to the full region
	// width AND fill the rows below the board, all in the layer fill
	// (fixedBlock would pad with BARE spaces and leave untinted margins;
	// the fill is the whole point)
	for i, l := range lines {
		if w := lipgloss.Width(l); w < rr.w {
			lines[i] = l + fill(w, i, rr.w-w)
		}
	}
	gridLines := len(lines)
	for len(lines) < rr.h {
		lines = append(lines, fill(0, len(lines), rr.w))
	}
	m.kbOryxOverlay(lines, rr, gridLines, chip, fill)
	return lines
}

// kbCycleHit is the tap-to-next-layer target both keyboard hosts append after
// the core's selector hits; only valid with a loaded, non-empty board.
func (m *model) kbCycleHit(area rect) hitRegion {
	n := len(m.kbBoard.Layers)
	return hitRegion{area: area, do: func(int, int) {
		m.kbLayer = (m.kbLayer + 1) % n
		m.homeCache.ok = false
	}}
}

// renderKBLive draws the keyboard as a home-region chrome widget: the same
// selector strip + layer grid in a titled box at rr, compact when the region
// is too short for the full render (params.mode overrides auto both ways).
// The layer-cycle hit covers ONLY the selector row -- the board area is
// display glass here, not a cycle target -- and the whole region consumes any
// other tap (appended last; the hit table is first-match). No resetHits:
// region renderers never reset.
func (m *model) renderKBLive(w config.Widget, rr rect) string {
	interior := rect{rr.x + 1, rr.y + 1, rr.w - 2, rr.h - 2}
	mode, _ := w.Render.Params["mode"].(string)
	texture, _ := w.Render.Params["texture"].(string)
	body := m.kbRegionBody(interior, mode, texture)
	title := w.Title
	if m.kbBoard != nil && m.kbBoard.Title != "" {
		title = "keyboard: " + m.kbBoard.Title
	}
	if m.kbErr == "" && m.kbBoard != nil && len(m.kbBoard.Layers) > 0 {
		m.hits = append(m.hits, m.kbCycleHit(rect{interior.x, interior.y, interior.w, 1}))
	}
	box := kbTitledBox(title, body, rr.w, rr.h, m.kbLayerEdge(m.kbLayer))
	m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
	return box
}

// kbSelector renders one line of layer-name buttons, the active one accented
// and identity-tinted; each name is a tap target that jumps to that layer.
func (m *model) kbSelector(box rect, chip color.Color) string {
	var b strings.Builder
	x := box.x
	for i, l := range m.kbBoard.Layers {
		label := " " + l.Title + " "
		style := chromeDim
		if i == m.kbLayer {
			// active layer: identity hue (data-not-style) + bold
			style = lipgloss.NewStyle().Foreground(identityHue(l.Title)).Bold(true)
		}
		if chip != nil {
			style = style.Background(chip)
		}
		b.WriteString(style.Render(label))
		w := lipgloss.Width(label)
		// clip hits to the box: the painted line truncates at box.w
		// (fitCell below), so buttons past the edge must not leave
		// invisible tap targets on narrow regions
		if x >= box.x+box.w {
			break
		}
		idx := i
		m.hits = append(m.hits, hitRegion{area: rect{x, box.y, min(w, box.x+box.w-x), 1}, do: func(int, int) {
			m.kbLayer = idx
			m.homeCache.ok = false
		}})
		x += w
	}
	return fitCell(b.String(), box.w)
}

// kbOryxLabel is the bottom-border link text.
const kbOryxLabel = "oryx"

// oryxURL addresses the synced revision at the active layer in the ZSA web
// configurator, or "" when the board (or its Oryx identity) is absent --
// boards built from a pre-slug Keymapp DB carry no layout hash.
func (m *model) oryxURL() string {
	b := m.kbBoard
	if b == nil || b.LayoutID == "" || b.Geometry == "" {
		return ""
	}
	rev := b.RevisionID
	if rev == "" {
		rev = "latest"
	}
	return "https://configure.zsa.io/" + url.PathEscape(b.Geometry) +
		"/layouts/" + url.PathEscape(b.LayoutID) +
		"/" + url.PathEscape(rev) +
		"/" + strconv.Itoa(m.kbLayer)
}

// openWithMacOS hands a URL to LaunchServices; the reap goroutine only
// collects the child (open's own failures surface in its UI, not ours).
func openWithMacOS(u string) {
	cmd := exec.Command("/usr/bin/open", u)
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

// kbChipBlend is how far the key-chip fill pulls from the theme background
// toward the active layer's identity hue: barely off the background -- the
// border color is the layer signal, the fill is only a texture canvas.
const kbChipBlend = 0.06

// kbLayerChip is the active layer's fill background color: identity hue
// blended nearly all the way back to the theme background, so the board
// carries the layer's tint the way the selector's active label does
// (identity is data-not-style). Nil for the BASE layer -- the fill is an
// off-base-layer indicator, not decoration -- and nil
// until a palette broadcast arrives, so a palette-less dock (and every
// bare test model) renders exactly as before.
func (m *model) kbLayerChip(idx int) color.Color {
	if idx == 0 {
		return nil
	}
	if idx < 0 || m.kbBoard == nil || idx >= len(m.kbBoard.Layers) {
		return nil
	}
	bg, ok := m.palette.color("background")
	if !ok {
		return nil
	}
	return blendToward(bg, identityHue(m.kbBoard.Layers[idx].Title), kbChipBlend)
}

// kbTitledBox frames the keyboard with the SAME square chrome glyphs as
// every other widget; the layer signal rides ENTIRELY on the border
// COLOR -- identity hue off-base, chrome dim on base -- so the frame
// never changes shape, only color. Border cells carry no background (NO
// OVERFLOWS ON FILLS); the faint fill stays interior as the texture
// canvas. Base output is byte-identical to renderTitledBox.
func kbTitledBox(title string, body []string, w, h int, edge color.Color) string {
	if w < 3 || h < 2 {
		return blankBlock(w, h)
	}
	frame := chromeDim
	if edge != nil {
		frame = lipgloss.NewStyle().Foreground(edge)
	}
	bd := lipgloss.NormalBorder()
	innerW := w - 2
	t := title
	if t != "" {
		t = " " + t + " "
	}
	t = fitCell(t, innerW-1)
	lines := make([]string, 0, h)
	lines = append(lines, frame.Render(bd.TopLeft+bd.Top)+chromeFG.Render(t)+
		frame.Render(strings.Repeat(bd.Top, max(innerW-1-lipgloss.Width(t), 0))+bd.TopRight))
	if h > 2 {
		left, right := frame.Render(bd.Left), frame.Render(bd.Right)
		for c := range strings.SplitSeq(fixedBlock(body, innerW, h-2), "\n") {
			lines = append(lines, left+c+right)
		}
	}
	lines = append(lines, frame.Render(bd.BottomLeft+strings.Repeat(bd.Bottom, innerW)+bd.BottomRight))
	return strings.Join(lines, "\n")
}

// kbOryxPad is how many fill cells sit between the oryx link and the
// right interior edge.
const kbOryxPad = 2

// kbOryxOverlay paints the oryx link over the LAST body row -- inside the
// widget, right-aligned, off both the selector row (it must not read as a
// layer button) and the border (a tag there breaks the frame line) -- and
// appends its hit. Only a flood row may carry it: gridLines counts the
// content rows, so when the board reaches the region bottom the link is
// dropped rather than drawn over legends.
func (m *model) kbOryxOverlay(lines []string, rr rect, gridLines int, chip color.Color, fill kbFillFunc) {
	if m.oryxURL() == "" || gridLines >= rr.h || len(lines) != rr.h {
		return
	}
	tag := " " + kbOryxLabel + " "
	lead := rr.w - kbOryxPad - len(tag)
	if lead <= 0 {
		return
	}
	style := chromeDim.Underline(true)
	if chip != nil {
		style = style.Background(chip)
	}
	y := rr.h - 1
	lines[y] = fill(0, y, lead) + style.Render(tag) + fill(lead+len(tag), y, kbOryxPad)
	m.hits = append(m.hits, hitRegion{
		area: rect{rr.x + lead, rr.y + y, len(tag), 1},
		do: func(int, int) {
			if m.openURL != nil {
				m.openURL(m.oryxURL())
			}
		},
	})
}

// kbTexture is the kb-live texture param: the fullscreen keyboard kind has
// no params channel (Layout carries none), so it resolves from the config's
// kb-live widget module-keyed (the kbLiveVisible precedent), in stable widget
// order. "" or "none" means the plain fill.
func (m *model) kbTexture() string {
	if m.cfg == nil {
		return ""
	}
	for _, id := range slices.Sorted(maps.Keys(m.cfg.Widgets)) {
		if w := m.cfg.Widgets[id]; w.Render.Module == "kb-live" {
			s, _ := w.Render.Params["texture"].(string)
			return s
		}
	}
	return ""
}

// Per-recipe foreground lift off the chip: the grids deliberately fade
// subtler than the speckle family.
const (
	kbSpeckleLift = 0.10
	kbGridLift    = 0.06
)

// kbSpeckleGlyphs maps each speckle-lattice recipe to its glyph. The nerd
// font entries are PUA codepoints -- width-gated per fill by kbSafeGlyph.
var kbSpeckleGlyphs = map[string]string{
	"dots":         "\u00b7",     // middle dot
	"oct-dot":      "\uf444",     // octicons primitive-dot
	"circle-small": "\U000F09DE", // md circle-small
	"dots-column":  "\U000F01D9", // md dots-vertical
	"grabber":      "\uf45a",     // octicons grabber
	"dots-grid":    "\U000F15FC", // md dots-grid
	"crosshair":    "\ue621",     // seti crosshair
}

// kbSafeGlyph width-gates a texture glyph: PUA codepoints are the exact
// hazard class the fitCell double-measure convention exists for, so unless
// BOTH x/ansi and lipgloss agree on 1 cell the glyph falls back to a plain
// space -- fail-safe to the plain fill, never a misaligned row.
func kbSafeGlyph(g string) string {
	if ansi.StringWidth(g) != 1 || lipgloss.Width(g) != 1 {
		return " "
	}
	return g
}

// kbTexCellFn resolves params.texture ("<recipe>" or "<recipe>:<density>",
// density sparse|dense, bare = normal; the vocabulary config vet enforces)
// into the per-cell glyph painter and its foreground lift. The grammar is
// parsed HERE, once per kbFill -- never per cell. A cell returning "" is a
// plain fill cell (the kbFill run-grouper treats "" as bg fill). nil means
// no texture: "", "none", and -- fail-safe -- anything vet would reject.
// Painters are deterministic per absolute coordinate, so frames are stable
// across renders and continuous across rows.
func kbTexCellFn(texture string) (cell func(x, y int) string, lift float64) {
	recipe, density, _ := strings.Cut(texture, ":")
	switch recipe {
	case "dot-grid":
		gx, gy := 3, 2
		switch density {
		case "sparse":
			gx, gy = 5, 3
		case "dense":
			gx, gy = 2, 2
		}
		g := kbSafeGlyph("\u00b7")
		return func(x, y int) string {
			if x%gx == 0 && y%gy == 0 {
				return g
			}
			return ""
		}, kbGridLift
	case "line-grid":
		ry, rx := 3, 6
		switch density {
		case "sparse":
			ry, rx = 4, 8
		case "dense":
			ry, rx = 2, 4
		}
		return func(x, y int) string {
			if y%ry == 0 && x%2 == 1 {
				return "-"
			}
			if x%rx == 0 && y%ry != 0 {
				return "|"
			}
			return ""
		}, kbGridLift
	}
	g, ok := kbSpeckleGlyphs[recipe]
	if !ok {
		return nil, 0
	}
	// speckle lattice, shared shape: cell on when x % m == (y % 2) * k
	// (normal is byte-compatible with the v1 dots: x%4 == (y%2)*2)
	m, k := 4, 2
	switch density {
	case "sparse":
		m, k = 6, 3
	case "dense":
		m, k = 2, 1
	}
	g = kbSafeGlyph(g)
	return func(x, y int) string {
		if x%m == (y%2)*k {
			return g
		}
		return ""
	}, kbSpeckleLift
}

// kbFillFunc paints n fill cells whose leftmost sits at absolute body cell
// (x, y).
type kbFillFunc func(x, y, n int) string

// kbFill builds the fill painter for the active layer: plain spaces while
// chip is nil WHATEVER the texture param (the fill is the texture's canvas;
// the fullscreen golden pins the palette-less render), the flat chip fill
// for "none", else the texture glyph per whitespace cell -- fg the chip
// lifted the recipe's step over bg chip, runs grouped to bound SGR churn
// (the spark precedent).
func kbFill(chip color.Color, texture string) kbFillFunc {
	if chip == nil {
		return func(_, _, n int) string {
			if n <= 0 {
				return ""
			}
			return strings.Repeat(" ", n)
		}
	}
	bg := lipgloss.NewStyle().Background(chip)
	cell, lift := kbTexCellFn(texture)
	if cell == nil {
		return func(_, _, n int) string {
			if n <= 0 {
				return ""
			}
			return bg.Render(strings.Repeat(" ", n))
		}
	}
	tex := lipgloss.NewStyle().Background(chip).Foreground(lipgloss.Lighten(chip, lift))
	return func(x, y, n int) string {
		if n <= 0 {
			return ""
		}
		var b strings.Builder
		for i := 0; i < n; {
			g := cell(x+i, y)
			j := i + 1
			if g == "" {
				for j < n && cell(x+j, y) == "" {
					j++
				}
				b.WriteString(bg.Render(strings.Repeat(" ", j-i)))
			} else {
				var run strings.Builder
				run.WriteString(g)
				for j < n {
					ng := cell(x+j, y)
					if ng == "" {
						break
					}
					run.WriteString(ng)
					j++
				}
				b.WriteString(tex.Render(run.String()))
			}
			i = j
		}
		return b.String()
	}
}

// kbLayerEdge is the filled frame's border hue: the layer's identity hue
// at full saturation (the fill is the same hue blended nearly to the
// background; the edge is its bold highlight). Nil exactly when the fill
// is nil (base layer, no palette).
func (m *model) kbLayerEdge(idx int) color.Color {
	if m.kbLayerChip(idx) == nil {
		return nil
	}
	return identityHue(m.kbBoard.Layers[idx].Title)
}

// kbKeyW picks the key cell width that fills cols: per half MainCols cells
// plus their gaps, plus the center gap. Clamped so a small dock degrades to
// cropped legends instead of panicking and a huge one does not balloon.
func kbKeyW(cols int) int {
	w := (cols - kbHalfGap - 2*(keyboard.MainCols-1)*kbColGap) / (2 * keyboard.MainCols)
	return max(kbKeyWMin, min(kbKeyWMax, w))
}

// kbLayerGrid renders one layer's 72 keys as the two Moonlander halves:
// 5 main rows of legend cells (each a tap line over a dim hold line), then
// the physical thumb cluster. held marks pressed slots. compact drops the
// hold lines (tap legends only) and folds each thumb cluster onto the main
// rows' trailing line: 6 lines instead of 15. chip is the active layer's
// fill background (nil = none): key cells AND every gap/pad cell carry
// it, so the ENTIRE board reads as one tinted surface. fill paints the
// non-key cells at absolute body coordinates (textures are per-cell); y0 is
// the grid's first body line, so texture rows stay continuous with the
// selector above and the flood below.
func kbLayerGrid(layer keyboard.Layer, cols int, held map[int]bool, compact bool, chip color.Color, fill kbFillFunc, y0 int) []string {
	// bucket keys by half/row and thumb, remembering each key's slot index
	// (= its position in Keys) for the held lookup
	type placed struct {
		key  keyboard.PlacedKey
		slot int
	}
	var leftMain, rightMain [5][]placed
	var leftThumb, rightThumb []placed
	for i, k := range layer.Keys {
		p := placed{key: k, slot: i}
		switch {
		case k.Slot.Thumb && k.Slot.Half == keyboard.Left:
			leftThumb = append(leftThumb, p)
		case k.Slot.Thumb && k.Slot.Half == keyboard.Right:
			rightThumb = append(rightThumb, p)
		case k.Slot.Half == keyboard.Left:
			leftMain[k.Slot.Row] = append(leftMain[k.Slot.Row], p)
		default:
			rightMain[k.Slot.Row] = append(rightMain[k.Slot.Row], p)
		}
	}

	kw := kbKeyW(cols)
	halfW := keyboard.MainCols*kw + (keyboard.MainCols-1)*kbColGap
	pad := max(0, (cols-(halfW*2+kbHalfGap))/2)
	xL, xR := pad, pad+halfW+kbHalfGap

	// row lays out one half-row of up to MainCols cells at x0 on the tap
	// and hold lines yt/yh; short rows anchor at the outer edge like the
	// physical board (left half left-aligned, right half right-aligned --
	// the missing keys are the inner ones), missing cells filled at their
	// own coordinates
	row := func(cells []placed, left bool, x0, yt, yh int) (tap, hold string) {
		blanks := keyboard.MainCols - len(cells)
		var tb, hb strings.Builder
		for j := range keyboard.MainCols {
			xj := x0 + j*(kw+kbColGap)
			if j > 0 {
				tb.WriteString(fill(xj-kbColGap, yt, kbColGap))
				hb.WriteString(fill(xj-kbColGap, yh, kbColGap))
			}
			i := j
			if !left {
				i = j - blanks
			}
			if i < 0 || i >= len(cells) {
				tb.WriteString(fill(xj, yt, kw))
				hb.WriteString(fill(xj, yh, kw))
				continue
			}
			ct, ch := kbKeyCell(cells[i].key, kw, held[cells[i].slot], chip)
			tb.WriteString(ct)
			hb.WriteString(ch)
		}
		return tb.String(), hb.String()
	}

	join := func(l, r string, y int) string {
		return fill(0, y, pad) + l + fill(pad+halfW, y, kbHalfGap) + r
	}

	var lines []string
	y := y0
	for r := range 5 {
		if compact {
			lt, _ := row(leftMain[r], true, xL, y, y)
			rt, _ := row(rightMain[r], false, xR, y, y)
			lines = append(lines, join(lt, rt, y))
			y++
			continue
		}
		lt, lh := row(leftMain[r], true, xL, y, y+1)
		rt, rh := row(rightMain[r], false, xR, y, y+1)
		lines = append(lines, join(lt, rt, y), join(lh, rh, y+1))
		y += 2
	}

	// short halves pad in the FILL, not bare spaces: fitBlock's raw pad
	// leaves dead untinted rectangles inside the thumb rows
	fit := func(s string, x0, y int) string {
		if w := lipgloss.Width(s); w < halfW {
			return s + fill(x0+w, y, halfW-w)
		}
		return fitBlock(s, halfW)
	}

	// cell is one thumb slot: the rendered key, or a fill blank at its
	// own coordinate
	cell := func(s string, x, y int) string {
		if s == "" {
			return fill(x, y, kw)
		}
		return s
	}

	if compact {
		// thumb clusters folded to ONE line: wide key beside the arc, each
		// cluster against the center gap with the wide key adjacent to the
		// arc end it sits over in the full render
		flat := func(keys []placed, left bool, x0 int) string {
			wide := ""
			var arc []string
			for _, p := range keys {
				t, _ := kbKeyCell(p.key, kw, held[p.slot], chip)
				if p.key.Slot.ThumbIdx == 0 {
					wide = t
				} else {
					arc = append(arc, t)
				}
			}
			// element order against the gap: left = wide then arc, right
			// mirrored = arc then wide
			elems := make([]string, 4)
			offW := 0
			if left {
				offW = max(0, halfW-(4*kw+3*kbColGap))
				elems[0] = wide
				copy(elems[1:], arc)
			} else {
				copy(elems[:3], arc)
				elems[3] = wide
			}
			var b strings.Builder
			b.WriteString(fill(x0, y, offW))
			for i, e := range elems {
				xi := x0 + offW + i*(kw+kbColGap)
				if i > 0 {
					b.WriteString(fill(xi-kbColGap, y, kbColGap))
				}
				b.WriteString(cell(e, xi, y))
			}
			return b.String()
		}
		return append(lines, join(fit(flat(leftThumb, true, xL), xL, y), fit(flat(rightThumb, false, xR), xR, y), y))
	}

	// thumb clusters: ThumbIdx 0 is the wide piano key, raised one key row
	// ABOVE the 3-key arc (idx 1-3); each cluster sits against the center
	// gap, the wide key over the arc end nearest the main block. y is the
	// wide tap line; hold and the arc pair follow it.
	thumb := func(keys []placed, left bool, x0, y int) (wideTap, wideHold, arcTap, arcHold string) {
		wt, wh := "", ""
		var arcT, arcH []string
		for _, p := range keys {
			t, h := kbKeyCell(p.key, kw, held[p.slot], chip)
			if p.key.Slot.ThumbIdx == 0 {
				wt, wh = t, h
			} else {
				arcT = append(arcT, t)
				arcH = append(arcH, h)
			}
		}
		arcW := 3*kw + 2*kbColGap
		// left: arc right-aligned against the gap, wide raised above
		// arc[0]; right mirrored: arc left-aligned, wide above arc[2]
		wideOff, arcOff := 2*(kw+kbColGap), 0
		if left {
			wideOff = max(0, halfW-arcW)
			arcOff = wideOff
		}
		wideLine := func(s string, y int) string {
			return fill(x0, y, wideOff) + cell(s, x0+wideOff, y)
		}
		arcLine := func(cells []string, y int) string {
			var b strings.Builder
			b.WriteString(fill(x0, y, arcOff))
			for j := range 3 {
				xj := x0 + arcOff + j*(kw+kbColGap)
				if j > 0 {
					b.WriteString(fill(xj-kbColGap, y, kbColGap))
				}
				if j < len(cells) {
					b.WriteString(cell(cells[j], xj, y))
				} else {
					b.WriteString(fill(xj, y, kw))
				}
			}
			return b.String()
		}
		return wideLine(wt, y), wideLine(wh, y+1), arcLine(arcT, y+2), arcLine(arcH, y+3)
	}

	lwt, lwh, lat, lah := thumb(leftThumb, true, xL, y+1)
	rwt, rwh, rat, rah := thumb(rightThumb, false, xR, y+1)
	lines = append(lines, "",
		join(fit(lwt, xL, y+1), fit(rwt, xR, y+1), y+1),
		join(fit(lwh, xL, y+2), fit(rwh, xR, y+2), y+2),
		join(fit(lat, xL, y+3), fit(rat, xR, y+3), y+3),
		join(fit(lah, xL, y+4), fit(rah, xR, y+4), y+4),
	)
	return lines
}

// kbKeyCell renders one key as its two cell lines, each exactly w wide: the
// tap legend (layer-switch keys identity-tinted by target layer) and the
// dim hold hint, both over the active layer's chip fill (nil = bare). A
// held key's tap line renders as a plain reverse-video block -- no chip, so
// presses stay loud over the tinted board.
func kbKeyCell(k keyboard.PlacedKey, w int, held bool, chip color.Color) (tap, hold string) {
	style := chromeFG
	if k.TapLayer >= 0 || k.HoldLayer >= 0 {
		layer := k.TapLayer
		if layer < 0 {
			layer = k.HoldLayer
		}
		// tint by target layer so every switch to layer N shares a hue
		style = lipgloss.NewStyle().Foreground(identityHue("L" + strconv.Itoa(layer)))
	}
	holdStyle := chromeDim
	if held {
		style = style.Reverse(true).Bold(true)
	} else if chip != nil {
		style = style.Background(chip)
		holdStyle = holdStyle.Background(chip)
	}
	t := fitCell(k.Tap, w)
	if t == "" && held {
		t = "*" // a held blank key still shows a block
	}
	tap = style.Render(padCenter(t, w))
	if k.Hold != "" {
		hold = holdStyle.Render(padCenter(fitCell(k.Hold, w), w))
	} else if chip != nil && !held {
		// an empty hold slot still carries the chip so the key reads as
		// one two-row chip, not a floating legend
		hold = lipgloss.NewStyle().Background(chip).Render(strings.Repeat(" ", w))
	} else {
		hold = strings.Repeat(" ", w)
	}
	return tap, hold
}

// padCenter pads plain text to exactly w cells, centered, so a style
// covering it colors the whole cell (the reverse-video block).
func padCenter(s string, w int) string {
	pw := lipgloss.Width(s)
	if pw >= w {
		return s
	}
	lp := (w - pw) / 2
	return strings.Repeat(" ", lp) + s + strings.Repeat(" ", w-pw-lp)
}

// fitBlock pads or crops a single line to exactly w cells.
func fitBlock(s string, w int) string {
	t := fitCell(s, w)
	if pw := lipgloss.Width(t); pw < w {
		t += "\x1b[m" + strings.Repeat(" ", w-pw)
	}
	return t
}
