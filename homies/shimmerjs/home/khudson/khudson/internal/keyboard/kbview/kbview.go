// Package kbview is the functional core of the Moonlander keyboard viewer:
// pure render + a pure event fold over a keyboard.Board, with no dependency
// on the dock model, the bus, or kitty. An imperative shell (the khudson
// dock, a standalone terminal client, any host) owns I/O -- loading the
// board, reading the keys feed, owning the terminal -- and drives this core:
// feed key events through ApplyKey, call Body to render a region, and map
// the returned Hits onto the host's own tap table. Theme carries the few
// host capabilities the render needs (chrome styles, the theme background,
// the identity-hue function) so the core stays presentation-pure.
package kbview

import (
	"image/color"
	"net/url"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// Rect is a host-agnostic cell rectangle; the shell translates it to its own
// geometry type when wiring hits.
type Rect struct{ X, Y, W, H int }

// HitKind tags what a returned Hit means so the shell dispatches it without
// the core reaching into host state.
type HitKind int

const (
	HitLayerJump HitKind = iota // jump to Layer
	HitOryx                     // open URL
)

// Hit is a tap target the core produced; the shell maps it into its own tap
// table and owns the side effect (layer state, opening a URL, cache invalidation).
type Hit struct {
	Area  Rect
	Kind  HitKind
	Layer int    // HitLayerJump
	URL   string // HitOryx
}

// Theme is the host capability set the render needs. FG/Dim are the chrome
// styles; Background is the theme background (nil when no palette has been
// broadcast -- the core then renders the flat palette-less board the golden
// pins); Accent is the house accent the tab band tints toward (nil = the
// neutral lift); Hue maps an identity key to a stable hue.
type Theme struct {
	FG         lipgloss.Style
	Dim        lipgloss.Style
	Background color.Color
	Accent     color.Color
	Hue        func(string) color.Color
}

func (th Theme) hue(key string) color.Color {
	if th.Hue == nil {
		return nil
	}
	return th.Hue(key)
}

const (
	keyWMax = 11
	keyWMin = 4
	colGap  = 1
	halfGap = 5
	// fullLines is the full render's grid line count; a region shorter than
	// this auto-engages the compact render. The tab bar is not part of the
	// grid -- it rides the box's bottom row (TitledBox bar).
	fullLines = 14
	chipBlend = 0.06
	oryxLabel = "oryx"
	// bandShift is the tab bar band's distance off the body tone: toward
	// the host accent when the theme carries one (the strip band's family),
	// else the neutral lift.
	bandShift       = 0.35
	bandAccentBlend = 0.22
	speckLift       = 0.10
	gridLift        = 0.06
)

// ApplyKey folds one key event into the board+layer view state, mutating
// board.Held for press/release/clear and returning the (possibly new) layer
// and whether anything changed. Pure over its inputs -- no host state, no
// I/O. The caller supplies a non-nil board (a nil board is a no-op).
func ApplyKey(board *keyboard.Board, layer int, ev *proto.KeyEvent) (newLayer int, changed bool) {
	if board == nil || ev == nil {
		return layer, false
	}
	switch ev.Kind {
	case proto.KeyEventKey:
		slot := keyboard.SlotAt(ev.Row, ev.Col)
		if slot < 0 {
			return layer, false
		}
		switch {
		case ev.Pressed:
			if board.Held == nil {
				board.Held = make(map[int]bool)
			}
			board.Held[slot] = true
			return layer, true
		case board.Held[slot]:
			delete(board.Held, slot)
			return layer, true
		}
	case proto.KeyEventLayer:
		if ev.Layer >= 0 && ev.Layer < len(board.Layers) && ev.Layer != layer {
			return ev.Layer, true
		}
	case proto.KeyEventClear:
		if len(board.Held) > 0 {
			board.Held = nil
			return layer, true
		}
	}
	return layer, false
}

// Empty reports whether the board is missing/unreadable/layerless, so the
// host knows to render the sync hint and skip the layer-cycle hit.
func Empty(board *keyboard.Board, err string) bool {
	return err != "" || board == nil || len(board.Layers) == 0
}

// Body renders the keyboard grid core shared by every host: the active
// layer laid out for a w x h region, or the plug-in hint when the board is
// empty/unreadable. The layer tab bar is NOT part of the body -- it is the
// box's cap row (Bar). mode is "full", "compact", or "" (auto: compact
// when h cannot hold the full grid). texture names the fill texture ("" or
// "none" = plain). noBoardErr is the sentinel error string that should NOT
// show as a hard load error (it degrades to the calm plug-in hint).
func Body(board *keyboard.Board, err string, layer, w, h int, mode, texture, noBoardErr string, th Theme) []string {
	if Empty(board, err) {
		msg := " plug in the board to fetch its layout from oryx"
		if err != "" && err != noBoardErr {
			msg = " board: " + err
		}
		return []string{"", th.Dim.Render(msg)}
	}
	if layer >= len(board.Layers) || layer < 0 {
		layer = 0
	}
	compact := mode == "compact" || (mode != "full" && h < fullLines)
	chip := LayerChip(board, layer, th)
	lines, fill := layerGrid(board.Layers[layer], w, board.Held, compact, chip, texture, th)
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw < w {
			lines[i] = l + fill(lw, i, w-lw)
		}
	}
	for len(lines) < h {
		lines = append(lines, fill(0, len(lines), w))
	}
	if len(lines) > h {
		// a region shorter than even the compact grid crops rather than
		// overflowing a host that joins without a fixed block
		lines = lines[:max(h, 0)]
	}
	return lines
}

// Bar renders the tab-bar row at the view's FULL width w: layer tabs flush
// against the left edge on a contrasting band, the active tab carrying the
// body's own background so it reads as the view notching through the bar
// (a bubbletea tab strip), and the oryx link as a block button flush
// against the right edge. A non-empty note renders dim on the band,
// right-aligned before the button (a host without a title row parks the
// board title there). The band caps a view edge in place of a border
// (TitledBox bar / Panel). Hits are bar-local (Y always 0, X from the
// view's left edge); the host offsets them. Callers gate on Empty.
func Bar(board *keyboard.Board, layer, w int, note string, th Theme) (string, []Hit) {
	if layer >= len(board.Layers) || layer < 0 {
		layer = 0
	}
	chip := LayerChip(board, layer, th)
	if chip == nil {
		// base layer under a palette: the body tone IS the theme background,
		// so the notch read survives (nil stays nil palette-less)
		chip = th.Background
	}
	band, tab, active, button := barStyles(board, layer, chip, th)
	tag := " " + oryxLabel + " "
	tagW := lipgloss.Width(tag)
	u := OryxURL(board, layer)
	btnX := w - tagW
	if u == "" {
		btnX = w
	}
	var b strings.Builder
	var hits []Hit
	x := 0
	for i, l := range board.Layers {
		label := " " + l.Title + " "
		lw := lipgloss.Width(label)
		if x+lw > btnX-1 {
			break
		}
		st := tab
		if i == layer {
			st = active
		}
		b.WriteString(st.Render(label))
		hits = append(hits, Hit{Kind: HitLayerJump, Layer: i, Area: Rect{x, 0, lw, 1}})
		x += lw
	}
	if note != "" {
		note = " " + note + " "
		if nw := lipgloss.Width(note); x+nw <= btnX {
			b.WriteString(band.Render(strings.Repeat(" ", btnX-nw-x)))
			b.WriteString(tab.Render(note))
			x = btnX
		}
	}
	if btnX > x {
		b.WriteString(band.Render(strings.Repeat(" ", btnX-x)))
	}
	if u != "" && btnX >= x {
		b.WriteString(button.Render(tag))
		hits = append(hits, Hit{Kind: HitOryx, URL: u, Area: Rect{btnX, 0, tagW, 1}})
	}
	return b.String(), hits
}

// barStyles is the tab bar's style set. With a body tone the band shifts
// off it -- toward the host accent when the theme carries one (matching
// the strip band's family), with full-contrast foreground labels; the
// neutral lift keeps body-toned labels. The active tab carries the body
// tone as its background (the view notching through); palette-less hosts
// get an indexed band -- plain labels on bright black so the inactive
// layers stay readable -- with the active tab a bold reverse block and the
// button a plain one.
func barStyles(board *keyboard.Board, layer int, chip color.Color, th Theme) (band, tab, active, button lipgloss.Style) {
	hue := lipgloss.NewStyle().Foreground(th.hue(board.Layers[layer].Title)).Bold(true)
	if chip != nil {
		if th.Accent != nil {
			bandBg := blend(chip, th.Accent, bandAccentBlend)
			return lipgloss.NewStyle().Background(bandBg), th.FG.Background(bandBg),
				hue.Background(chip), th.FG.Background(liftTone(bandBg, 0.2)).Bold(true)
		}
		bandBg := liftTone(chip, bandShift)
		return lipgloss.NewStyle().Background(bandBg), th.FG.Foreground(chip).Background(bandBg),
			hue.Background(chip), th.FG.Foreground(chip).Background(liftTone(bandBg, 0.2)).Bold(true)
	}
	return lipgloss.NewStyle().Background(lipgloss.BrightBlack),
		th.FG.Background(lipgloss.BrightBlack),
		hue.Reverse(true), th.FG.Reverse(true)
}

// OryxURL addresses the synced revision at the active layer in the ZSA web
// configurator, or "" when the board (or its Oryx identity) is absent.
func OryxURL(board *keyboard.Board, layer int) string {
	if board == nil || board.LayoutID == "" || board.Geometry == "" {
		return ""
	}
	rev := board.RevisionID
	if rev == "" {
		rev = "latest"
	}
	return "https://configure.zsa.io/" + url.PathEscape(board.Geometry) +
		"/layouts/" + url.PathEscape(board.LayoutID) +
		"/" + url.PathEscape(rev) +
		"/" + strconv.Itoa(layer)
}

// TitledBox frames the keyboard with square chrome glyphs; the layer signal
// rides on the border color (edge, nil = dim chrome). Byte-identical to
// the dock's renderTitledBox.
func TitledBox(title string, body []string, w, h int, edge color.Color, th Theme) string {
	if w < 3 || h < 2 {
		return blankBlock(w, h)
	}
	frame := th.Dim
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
	lines = append(lines, frame.Render(bd.TopLeft+bd.Top)+th.FG.Render(t)+
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

// headerLift is the panel header band's distance off the body tone.
const headerLift = 0.12

// Hairline is the panel separator glyph: a left-eighth block reads as a
// thin rule hugging the boundary between abutting panels; the full bar is
// the fallback when a measurer disagrees on its width. Exported so the
// dock's side panels share the vocabulary.
func Hairline() string {
	if g := "▏"; ansi.StringWidth(g) == 1 && lipgloss.Width(g) == 1 {
		return g
	}
	return "│"
}

// Panel is the side-border panel chrome for full-height hosts: the bar --
// when present -- IS the header, capping the panel's TOP edge-to-edge (the
// interactive band replaces the title row; hosts park the title in the
// bar's note). Without a bar the title rides a lifted header band instead
// of a top border. The body renders full-bleed below, a hairline separator
// marks the left edge only when a neighbor abuts (leftEdge -- context
// decides), and there is no bottom border. edge tints the title and
// separator (the layer signal; nil = dim separator, plain title).
func Panel(title string, body []string, w, h int, bar string, leftEdge bool, edge color.Color, th Theme) string {
	if w < 2 || h < 2 {
		return blankBlock(w, h)
	}
	frame := th.Dim
	if edge != nil {
		frame = lipgloss.NewStyle().Foreground(edge)
	}
	inset := 0
	left := ""
	if leftEdge {
		inset = 1
		left = frame.Render(Hairline())
	}
	innerW := w - inset
	lines := make([]string, 0, h)
	if bar != "" {
		lines = append(lines, bar)
	} else {
		ts := th.FG
		if edge != nil {
			// no frame to tint: the layer signal rides the title text
			ts = frame
		}
		pad := lipgloss.NewStyle()
		headLeft := left
		if th.Background != nil {
			hb := liftTone(th.Background, headerLift)
			ts = ts.Background(hb)
			pad = pad.Background(hb)
			if leftEdge {
				headLeft = frame.Background(hb).Render(Hairline())
			}
		}
		t := fitCell(" "+title, innerW)
		lines = append(lines, headLeft+ts.Render(t)+pad.Render(strings.Repeat(" ", max(innerW-lipgloss.Width(t), 0))))
	}
	for c := range strings.SplitSeq(fixedBlock(body, innerW, h-1), "\n") {
		lines = append(lines, left+c)
	}
	return strings.Join(lines, "\n")
}

// LayerChip is the active layer's fill background: identity hue blended
// nearly to the theme background. Nil for the base layer and until a
// background is known (palette-less render).
func LayerChip(board *keyboard.Board, idx int, th Theme) color.Color {
	if idx <= 0 || board == nil || idx >= len(board.Layers) {
		return nil
	}
	if th.Background == nil {
		return nil
	}
	return blend(th.Background, th.hue(board.Layers[idx].Title), chipBlend)
}

// LayerEdge is the filled frame's border hue: the layer's identity hue at
// full saturation. Nil exactly when LayerChip is nil.
func LayerEdge(board *keyboard.Board, idx int, th Theme) color.Color {
	if LayerChip(board, idx, th) == nil {
		return nil
	}
	return th.hue(board.Layers[idx].Title)
}

var speckleGlyphs = map[string]string{
	"dots":         "\u00b7",
	"oct-dot":      "\uf444",
	"circle-small": "\U000F09DE",
	"dots-column":  "\U000F01D9",
	"grabber":      "\uf45a",
	"dots-grid":    "\U000F15FC",
	"crosshair":    "\ue621",
}

func safeGlyph(g string) string {
	if ansi.StringWidth(g) != 1 || lipgloss.Width(g) != 1 {
		return " "
	}
	return g
}

// TexCellFn resolves a texture recipe into its per-cell glyph painter and
// foreground lift; cell is nil for an empty/unknown recipe. Exported so a
// host can validate its texture vocabulary against the core.
func TexCellFn(texture string) (cell func(x, y int) string, lift float64) {
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
		g := safeGlyph("\u00b7")
		return func(x, y int) string {
			if x%gx == 0 && y%gy == 0 {
				return g
			}
			return ""
		}, gridLift
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
		}, gridLift
	}
	g, ok := speckleGlyphs[recipe]
	if !ok {
		return nil, 0
	}
	m, k := 4, 2
	switch density {
	case "sparse":
		m, k = 6, 3
	case "dense":
		m, k = 2, 1
	}
	g = safeGlyph(g)
	return func(x, y int) string {
		if x%m == (y%2)*k {
			return g
		}
		return ""
	}, speckLift
}

type fillFunc func(x, y, n int) string

func makeFill(chip color.Color, texture string) fillFunc {
	if chip == nil {
		return func(_, _, n int) string {
			if n <= 0 {
				return ""
			}
			return strings.Repeat(" ", n)
		}
	}
	bg := lipgloss.NewStyle().Background(chip)
	cell, lift := TexCellFn(texture)
	tex := lipgloss.NewStyle().Background(chip).Foreground(lipgloss.Lighten(chip, lift))
	return func(x, y, n int) string {
		return TexRun(cell, bg, tex, x, y, n)
	}
}

// TexRun renders n cells of textured surface starting at absolute cell
// (x, y): the cell painter's glyphs in tex, the gaps in blank, adjacent
// same-style cells batched into one SGR run. A nil painter is all blank.
// Exported beside TexCellFn so hosts painting their own surfaces (the
// dock strip band) share one run-batching implementation.
func TexRun(cell func(x, y int) string, blank, tex lipgloss.Style, x, y, n int) string {
	if n <= 0 {
		return ""
	}
	if cell == nil {
		return blank.Render(strings.Repeat(" ", n))
	}
	var b strings.Builder
	for i := 0; i < n; {
		g := cell(x+i, y)
		j := i + 1
		if g == "" {
			for j < n && cell(x+j, y) == "" {
				j++
			}
			b.WriteString(blank.Render(strings.Repeat(" ", j-i)))
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

func keyW(cols int) int {
	w := (cols - halfGap - 2*(keyboard.MainCols-1)*colGap) / (2 * keyboard.MainCols)
	return max(keyWMin, min(keyWMax, w))
}

// liftTone shifts c toward contrast: dark tones lighten, light tones
// darken, so a derived band/highlight stays visible on either theme.
func liftTone(c color.Color, t float64) color.Color {
	r, g, b, _ := c.RGBA()
	if (299*r+587*g+114*b)/1000 > 0x7fff {
		return lipgloss.Darken(c, t)
	}
	return lipgloss.Lighten(c, t)
}

func layerGrid(layer keyboard.Layer, cols int, held map[int]bool, compact bool, chip color.Color, texture string, th Theme) ([]string, fillFunc) {
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
	kw := keyW(cols)
	halfW := keyboard.MainCols*kw + (keyboard.MainCols-1)*colGap
	pad := max(0, (cols-(halfW*2+halfGap))/2)
	xL, xR := pad, pad+halfW+halfGap

	arcW := 3*kw + 2*colGap
	fill := makeFill(chip, texture)

	place := func(cells []placed, left bool) [keyboard.MainCols]*placed {
		var out [keyboard.MainCols]*placed
		off := 0
		if !left {
			off = keyboard.MainCols - len(cells)
		}
		for i := range cells {
			out[off+i] = &cells[i]
		}
		return out
	}

	row := func(slots [keyboard.MainCols]*placed, x0, yt, yh int) (tap, hold string) {
		var tb, hb strings.Builder
		for j := range keyboard.MainCols {
			xj := x0 + j*(kw+colGap)
			if j > 0 {
				tb.WriteString(fill(xj-colGap, yt, colGap))
				hb.WriteString(fill(xj-colGap, yh, colGap))
			}
			if slots[j] == nil {
				tb.WriteString(fill(xj, yt, kw))
				hb.WriteString(fill(xj, yh, kw))
				continue
			}
			ct, ch := keyCell(slots[j].key, kw, held[slots[j].slot], chip, th)
			tb.WriteString(ct)
			hb.WriteString(ch)
		}
		return tb.String(), hb.String()
	}

	join := func(l, r string, y int) string {
		return fill(0, y, pad) + l + fill(pad+halfW, y, halfGap) + r
	}

	var lines []string
	y := 0
	for r := range 5 {
		ls := place(leftMain[r], true)
		rs := place(rightMain[r], false)
		if compact {
			lt, _ := row(ls, xL, y, y)
			rt, _ := row(rs, xR, y, y)
			lines = append(lines, join(lt, rt, y))
			y++
			continue
		}
		lt, lh := row(ls, xL, y, y+1)
		rt, rh := row(rs, xR, y, y+1)
		lines = append(lines, join(lt, rt, y), join(lh, rh, y+1))
		y += 2
	}

	fit := func(s string, x0, y int) string {
		if w := lipgloss.Width(s); w < halfW {
			return s + fill(x0+w, y, halfW-w)
		}
		return fitBlock(s, halfW)
	}

	cell := func(s string, x, y int) string {
		if s == "" {
			return fill(x, y, kw)
		}
		return s
	}

	// cluster mirrors the physical thumb shape (QMK zsa/moonlander: left
	// wide key x=5 w=2 over the arc at x=5,6,7; right mirrored): the wide
	// 2u "piano" key on its own row directly under the main grid, hugging
	// the arc's grid-side edge, and the 3-key arc one row below with its
	// center-side key poking out past the piano key.
	pianoW := 2*kw + colGap
	cluster := func(keys []placed, left bool, x0, ypt, yph, yat, yah int) (pt, ph, at, ah string) {
		var wt, wh string
		var arcT, arcH []string
		for _, p := range keys {
			if p.key.Slot.ThumbIdx == 0 {
				wt, wh = keyCell(p.key, pianoW, held[p.slot], chip, th)
				continue
			}
			t, h := keyCell(p.key, kw, held[p.slot], chip, th)
			arcT = append(arcT, t)
			arcH = append(arcH, h)
		}
		offA := 0
		if left {
			offA = max(0, halfW-arcW)
		}
		offP := offA
		if !left {
			offP = offA + arcW - pianoW
		}
		arcLine := func(elems []string, y int) string {
			var b strings.Builder
			b.WriteString(fill(x0, y, offA))
			for i, e := range elems {
				xi := x0 + offA + i*(kw+colGap)
				if i > 0 {
					b.WriteString(fill(xi-colGap, y, colGap))
				}
				b.WriteString(cell(e, xi, y))
			}
			return b.String()
		}
		pianoLine := func(e string, y int) string {
			if e == "" {
				return fill(x0, y, offP+pianoW)
			}
			return fill(x0, y, offP) + e
		}
		return pianoLine(wt, ypt), pianoLine(wh, yph), arcLine(arcT, yat), arcLine(arcH, yah)
	}

	if compact {
		// compact folds the cluster to a single row -- [arc, wide] left half,
		// [wide, arc] right, the wide key at the center-side end -- because
		// compact's contract is minimal height, not physical shape.
		flat := func(keys []placed, left bool, x0, y int) string {
			var wide string
			var arc []string
			for _, p := range keys {
				t, _ := keyCell(p.key, kw, held[p.slot], chip, th)
				if p.key.Slot.ThumbIdx == 0 {
					wide = t
				} else {
					arc = append(arc, t)
				}
			}
			offW := 0
			if left {
				offW = max(0, halfW-(4*kw+3*colGap))
			}
			elems := make([]string, 4)
			if left {
				copy(elems[:3], arc)
				elems[3] = wide
			} else {
				elems[0] = wide
				copy(elems[1:], arc)
			}
			var b strings.Builder
			b.WriteString(fill(x0, y, offW))
			for i, e := range elems {
				xi := x0 + offW + i*(kw+colGap)
				if i > 0 {
					b.WriteString(fill(xi-colGap, y, colGap))
				}
				b.WriteString(cell(e, xi, y))
			}
			return b.String()
		}
		return append(lines, join(fit(flat(leftThumb, true, xL, y), xL, y),
			fit(flat(rightThumb, false, xR, y), xR, y), y)), fill
	}

	lpt, lph, lat, lah := cluster(leftThumb, true, xL, y, y+1, y+2, y+3)
	rpt, rph, rat, rah := cluster(rightThumb, false, xR, y, y+1, y+2, y+3)
	lines = append(lines,
		join(fit(lpt, xL, y), fit(rpt, xR, y), y),
		join(fit(lph, xL, y+1), fit(rph, xR, y+1), y+1),
		join(fit(lat, xL, y+2), fit(rat, xR, y+2), y+2),
		join(fit(lah, xL, y+3), fit(rah, xR, y+3), y+3),
	)
	return lines, fill
}

func keyCell(k keyboard.PlacedKey, w int, held bool, chip color.Color, th Theme) (tap, hold string) {
	style := th.FG
	if k.TapLayer >= 0 || k.HoldLayer >= 0 {
		layer := k.TapLayer
		if layer < 0 {
			layer = k.HoldLayer
		}
		style = lipgloss.NewStyle().Foreground(th.hue("L" + strconv.Itoa(layer)))
	}
	// the chip rides the HOLD half unconditionally: only the tap cell pops
	// on a press (glass-reported: gating both halves on !held punched a
	// bare-background hole under every held key on a filled layer)
	holdStyle := th.Dim
	if chip != nil {
		holdStyle = holdStyle.Background(chip)
	}
	if held {
		style = style.Reverse(true).Bold(true)
	} else if chip != nil {
		style = style.Background(chip)
	}
	t := fitCell(k.Tap, w)
	if t == "" && held {
		t = "*"
	}
	tap = style.Render(padCenter(t, w))
	if k.Hold != "" {
		hold = holdStyle.Render(padCenter(fitCell(k.Hold, w), w))
	} else if chip != nil {
		hold = lipgloss.NewStyle().Background(chip).Render(strings.Repeat(" ", w))
	} else {
		hold = strings.Repeat(" ", w)
	}
	return tap, hold
}

func padCenter(s string, w int) string {
	pw := lipgloss.Width(s)
	if pw >= w {
		return s
	}
	lp := (w - pw) / 2
	return strings.Repeat(" ", lp) + s + strings.Repeat(" ", w-pw-lp)
}

// --- width + color primitives (kept local so the core has no dock dependency;
// byte-identical to the dock originals the golden pins) ---

func fitCell(s string, w int) string {
	t := ansi.Cut(s, 0, w)
	for lipgloss.Width(t) > w && w > 0 {
		w--
		t = ansi.Cut(s, 0, w)
	}
	return t
}

func fitBlock(s string, w int) string {
	t := fitCell(s, w)
	if pw := lipgloss.Width(t); pw < w {
		t += "\x1b[m" + strings.Repeat(" ", w-pw)
	}
	return t
}

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

func blend(a, b color.Color, t float64) color.Color {
	const steps = 32
	ramp := lipgloss.Blend1D(steps, a, b)
	i := int(min(max(t, 0), 1)*float64(steps-1) + 0.5)
	return ramp[i]
}
