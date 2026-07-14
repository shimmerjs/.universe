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
// pins); Hue maps an identity key to a stable hue.
type Theme struct {
	FG         lipgloss.Style
	Dim        lipgloss.Style
	Background color.Color
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
	// FullLines is the full render's body line count; a region shorter than
	// this auto-engages the compact render. Exported so a host can size.
	FullLines = 15
	chipBlend = 0.06
	oryxLabel = "oryx"
	OryxPad   = 2
	speckLift = 0.10
	gridLift  = 0.06
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

// LayerCount reports the board's layer count (0 for a nil/empty board).
func LayerCount(board *keyboard.Board) int {
	if board == nil {
		return 0
	}
	return len(board.Layers)
}

// Empty reports whether the board is missing/unreadable/layerless, so the
// host knows to render the sync hint and skip the layer-cycle hit.
func Empty(board *keyboard.Board, err string) bool {
	return err != "" || board == nil || len(board.Layers) == 0
}

// Body renders the keyboard region core shared by every host: the selector
// strip plus the active layer grid laid out for rr, or the sync hint when
// the board is empty/unreadable. It returns the body lines and the tap hits
// it produced (selector jumps, offset to rr, and the oryx link) -- the host
// owns resetting its table, the layer-cycle hit, and any error-branch
// consume. mode is "full", "compact", or "" (auto: compact when rr.H cannot
// hold the full render). texture names the fill texture ("" or "none" = plain).
// noRevisionErr is the sentinel error string that should NOT show as a hard
// db error (it degrades to the calm sync hint).
func Body(board *keyboard.Board, err string, layer int, rr Rect, mode, texture, noRevisionErr string, th Theme) (lines []string, hits []Hit) {
	if Empty(board, err) {
		msg := " open Keymapp and connect your board to sync the layout"
		if err != "" && err != noRevisionErr {
			msg = " keymapp db: " + err
		}
		return []string{"", th.Dim.Render(msg)}, nil
	}
	if layer >= len(board.Layers) || layer < 0 {
		layer = 0
	}
	compact := mode == "compact" || (mode != "full" && rr.H < FullLines)
	chip := LayerChip(board, layer, th)
	fill := makeFill(chip, texture)
	lines = make([]string, 0, rr.H)
	sel, selHits := selector(board, layer, rr, chip, th)
	hits = append(hits, selHits...)
	if compact {
		lines = append(lines, sel)
	} else {
		lines = append(lines, sel, "")
	}
	grid := layerGrid(board.Layers[layer], rr.W, board.Held, compact, chip, fill, len(lines), th)
	lines = append(lines, grid...)
	for i, l := range lines {
		if w := lipgloss.Width(l); w < rr.W {
			lines[i] = l + fill(w, i, rr.W-w)
		}
	}
	gridLines := len(lines)
	for len(lines) < rr.H {
		lines = append(lines, fill(0, len(lines), rr.W))
	}
	if h, ok := oryxOverlay(board, layer, lines, rr, gridLines, chip, fill, th); ok {
		hits = append(hits, h)
	}
	return lines, hits
}

// selector renders one line of layer-name buttons, the active one accented
// and identity-tinted, and returns the per-button jump hits (offset to box).
func selector(board *keyboard.Board, layer int, box Rect, chip color.Color, th Theme) (string, []Hit) {
	var b strings.Builder
	var hits []Hit
	x := box.X
	for i, l := range board.Layers {
		label := " " + l.Title + " "
		style := th.Dim
		if i == layer {
			style = lipgloss.NewStyle().Foreground(th.hue(l.Title)).Bold(true)
		}
		if chip != nil {
			style = style.Background(chip)
		}
		b.WriteString(style.Render(label))
		w := lipgloss.Width(label)
		if x >= box.X+box.W {
			break
		}
		hits = append(hits, Hit{Kind: HitLayerJump, Layer: i, Area: Rect{x, box.Y, min(w, box.X+box.W-x), 1}})
		x += w
	}
	return fitCell(b.String(), box.W), hits
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

// oryxOverlay paints the oryx link over the last body row and returns its hit;
// ok=false when there is no room / no url (nothing drawn).
func oryxOverlay(board *keyboard.Board, layer int, lines []string, rr Rect, gridLines int, chip color.Color, fill fillFunc, th Theme) (Hit, bool) {
	u := OryxURL(board, layer)
	if u == "" || gridLines >= rr.H || len(lines) != rr.H {
		return Hit{}, false
	}
	tag := " " + oryxLabel + " "
	lead := rr.W - OryxPad - len(tag)
	if lead <= 0 {
		return Hit{}, false
	}
	style := th.Dim.Underline(true)
	if chip != nil {
		style = style.Background(chip)
	}
	y := rr.H - 1
	lines[y] = fill(0, y, lead) + style.Render(tag) + fill(lead+len(tag), y, OryxPad)
	return Hit{Kind: HitOryx, URL: u, Area: Rect{rr.X + lead, rr.Y + y, len(tag), 1}}, true
}

// TitledBox frames the keyboard with square chrome glyphs; the layer signal
// rides on the border color (edge, nil = dim chrome). Base output is
// byte-identical to the dock's renderTitledBox.
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

func keyW(cols int) int {
	w := (cols - halfGap - 2*(keyboard.MainCols-1)*colGap) / (2 * keyboard.MainCols)
	return max(keyWMin, min(keyWMax, w))
}

func layerGrid(layer keyboard.Layer, cols int, held map[int]bool, compact bool, chip color.Color, fill fillFunc, y0 int, th Theme) []string {
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
	y := y0
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

	cluster := func(keys []placed, left bool, x0, yt, yh int) (tap, hold string) {
		var wt, wh string
		var arcT, arcH []string
		for _, p := range keys {
			t, h := keyCell(p.key, kw, held[p.slot], chip, th)
			if p.key.Slot.ThumbIdx == 0 {
				wt, wh = t, h
			} else {
				arcT = append(arcT, t)
				arcH = append(arcH, h)
			}
		}
		offW := 0
		if left {
			offW = max(0, halfW-(4*kw+3*colGap))
		}
		order := func(wide string, arc []string) []string {
			elems := make([]string, 4)
			if left {
				copy(elems[:3], arc)
				elems[3] = wide
			} else {
				elems[0] = wide
				copy(elems[1:], arc)
			}
			return elems
		}
		line := func(elems []string, y int) string {
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
		return line(order(wt, arcT), yt), line(order(wh, arcH), yh)
	}

	if compact {
		lt, _ := cluster(leftThumb, true, xL, y, y)
		rt, _ := cluster(rightThumb, false, xR, y, y)
		return append(lines, join(fit(lt, xL, y), fit(rt, xR, y), y))
	}

	lt, lh := cluster(leftThumb, true, xL, y+1, y+2)
	rt, rh := cluster(rightThumb, false, xR, y+1, y+2)
	lines = append(lines,
		fill(0, y, cols),
		join(fit(lt, xL, y+1), fit(rt, xR, y+1), y+1),
		join(fit(lh, xL, y+2), fit(rh, xR, y+2), y+2),
	)
	return lines
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
	holdStyle := th.Dim
	if held {
		style = style.Reverse(true).Bold(true)
	} else if chip != nil {
		style = style.Background(chip)
		holdStyle = holdStyle.Background(chip)
	}
	t := fitCell(k.Tap, w)
	if t == "" && held {
		t = "*"
	}
	tap = style.Render(padCenter(t, w))
	if k.Hold != "" {
		hold = holdStyle.Render(padCenter(fitCell(k.Hold, w), w))
	} else if chip != nil && !held {
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
