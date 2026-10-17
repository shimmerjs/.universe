package logiretch

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shimmerjs/khudson/khudson/provider"
)

// Row keys the dock's chrome renderer keys off (the reno precedent): the
// generic titled-box path still renders them tolerably.
const (
	KeyArt     = "art"     // one RowSpans line of the mouse diagram
	KeyMap     = "map"     // one mapping/setting row
	KeyStatus  = "status"  // link + battery footer
	KeyControl = "control" // a lit-control label span carries this ident
)

// settingsOrder pins the mapping rows.
var settingsOrder = []string{"dpi", "smartShift", "hiresWheel", "thumbwheel", "haptic"}

// render builds the widget view model. Caller holds mu.
func (m *Mod) render() module.Data {
	d := module.Data{Title: "logiretch"}
	for _, line := range m.artLines() {
		d.Rows = append(d.Rows, module.Row{Kind: module.RowSpans, Key: KeyArt, Spans: line})
	}
	d.Rows = append(d.Rows, module.Row{Kind: module.RowDivider})
	d.Rows = append(d.Rows, m.mapRows()...)
	d.Rows = append(d.Rows, module.Row{Kind: module.RowDivider}, m.statusRow())
	return d
}

// lit reports whether a control is currently highlighted (active window
// or held). Caller holds mu.
func (m *Mod) isLit(key string) bool {
	if _, ok := m.lit[key]; ok {
		return true
	}
	return m.held[key]
}

// ctl is a control cell as a styled span: highlighted when active.
func (m *Mod) ctl(key, label string) module.Span {
	if m.isLit(key) {
		return module.Span{Text: label, Style: module.StyleHighlight}
	}
	return module.Span{Text: label}
}

func plain(s string) module.Span { return module.Span{Text: s} }
func dimSpan(s string) module.Span {
	return module.Span{Text: s, Style: module.StyleDim}
}

// artW is the art block's fixed cell width. Rows composite into a fixed
// buffer, so a hand-edited silhouette cannot drift a column and leave the
// right edge ragged (the top-down art it replaced was 12 cells on some
// rows and 13 on others).
const artW = 62

// calloutW is the label column ahead of each leader: right-aligned names
// so the leaders line up into one gutter.
const calloutW = 10

// Drawing glyphs, escaped so this file stays ASCII. kitty renders the
// block and box-drawing families procedurally, so they stay crisp at any
// cell size.
const (
	glyphFill   = "\u2588" // full block: the shell body
	glyphLeader = "\u2500" // light horizontal: callout leaders
)

// artShell is the MX Master in profile, one {left, right} column pair per
// row: a long low wedge, not an oval -- a thin nose almost on the desk at
// the front (left), the surface climbing to the palm crest at the back,
// a steep tail, and a flat base. Reshape it by moving numbers.
var artShell = [...][2]int{
	{38, 52},
	{35, 55},
	{32, 57},
	{29, 58},
	{26, 59},
	{24, 60},
	{22, 60},
	{21, 60},
	{20, 60},
	{20, 60}, // the base: flat on the desk, and no control sits on it
}

// artLines is the ASCII MX Master 4 seen from the LEFT -- the flank a side
// view actually shows -- as a filled silhouette with its controls punched
// into the shell and a leader out to each name. Rows run front-to-back
// bottom-to-top, so the controls sit roughly where they do on the real
// mouse: main buttons at the nose, thumbwheel low and forward, the flank
// cluster behind them, the wheel back near the crest. Wide and short by
// design, to sit under the keyboard render later. A lit control lights its
// LABEL too, so the name reads at a glance instead of only the glyph.
//
// The two main buttons are stacked rather than in true profile: a real
// side view hides the far button entirely, and a mouse viewer that cannot
// show a right-click is worse than one that cheats the perspective.
func (m *Mod) artLines() [][]module.Span {
	wheel := "O"
	if m.isLit("wheel") {
		if m.wheelDir > 0 {
			wheel = "v"
		} else {
			wheel = "^"
		}
	}
	thumb := "="
	if m.isLit("thumb") {
		if m.panDir > 0 {
			thumb = ">"
		} else {
			thumb = "<"
		}
	}

	rows := make([][]module.Span, len(artShell))
	for r, span := range artShell {
		buf := newArtBuf()
		buf.put(span[0], module.Span{
			Text:  strings.Repeat(glyphFill, span[1]-span[0]+1),
			Style: module.StyleDim,
		})
		rows[r] = buf.spans()
	}
	// front (low rows) to back (high rows): nose buttons, then the flank
	// cluster, then the wheel near the crest
	m.place(rows, 8, "left", "left", "L")
	m.place(rows, 7, "right", "right", "R")
	m.place(rows, 6, "thumb", "thumb", thumb)
	m.placeCluster(rows, 5, m.isLit("back") || m.isLit("forward"), "back/fwd",
		m.ctl("back", "<"), m.ctl("forward", ">"))
	m.place(rows, 4, "gesture", "gesture", "G")
	m.place(rows, 3, "btn6", "btn6", m.btn6Glyph())
	// middle-click IS the wheel press: it sits directly under the wheel
	m.place(rows, 2, "middle", "middle", "M")
	m.place(rows, 1, "wheel", "wheel", wheel)
	return append(rows, m.motionRow())
}

// place punches one labeled control into the shell and runs its leader
// back to the label gutter, stopping at the shell edge -- a leader that
// crossed the body would read as a line drawn ON the mouse.
func (m *Mod) place(rows [][]module.Span, r int, key, label, glyph string) {
	m.placeCluster(rows, r, m.isLit(key), label, m.ctl(key, glyph))
}

func (m *Mod) placeCluster(rows [][]module.Span, r int, lit bool, label string, ctls ...module.Span) {
	left := artShell[r][0]
	name := module.Span{Text: fmt.Sprintf("%*s", calloutW, label), Style: module.StyleDim}
	if lit {
		name.Style = module.StyleHighlight
	}
	buf := newArtBuf()
	buf.under(rows[r])
	buf.put(0, name)
	buf.put(calloutW+1, module.Span{
		Text:  strings.Repeat(glyphLeader, max(left-calloutW-2, 0)),
		Style: module.StyleDim,
	})
	// blank the shell around the glyph so the control reads as a key set
	// into the surface rather than a letter floating on it
	buf.put(left+1, plain(strings.Repeat(" ", len(ctls)+2)))
	for i, c := range ctls {
		buf.put(left+2+i, c)
	}
	rows[r] = buf.spans()
}

// artBuf composites one row cell by cell: last write wins, so callers
// paint back to front and never count spaces.
type artBuf struct {
	runes  []rune
	styles []string
}

func newArtBuf() *artBuf {
	b := &artBuf{runes: make([]rune, artW), styles: make([]string, artW)}
	for i := range b.runes {
		b.runes[i] = ' '
	}
	return b
}

func (b *artBuf) put(col int, s module.Span) {
	for i, r := range []rune(s.Text) {
		if c := col + i; c >= 0 && c < artW {
			b.runes[c], b.styles[c] = r, s.Style
		}
	}
}

// under paints spans onto the buffer, blanks included: it seeds a row.
func (b *artBuf) under(spans []module.Span) {
	col := 0
	for _, s := range spans {
		for _, r := range []rune(s.Text) {
			if col < artW {
				b.runes[col], b.styles[col] = r, s.Style
			}
			col++
		}
	}
}

// spans coalesces the buffer into styled runs. Every row is exactly artW
// cells; padding, not drawing, is what keeps the block rectangular.
func (b *artBuf) spans() []module.Span {
	var out []module.Span
	start := 0
	for i := 1; i <= artW; i++ {
		if i == artW || b.styles[i] != b.styles[start] {
			out = append(out, module.Span{
				Text: string(b.runes[start:i]), Style: b.styles[start]})
			start = i
		}
	}
	return out
}

// btn6Glyph is the lone body-mounted button, drawn inside the shell: a
// bare dot at rest so it does not compete with the flank cluster, a star
// while held.
func (m *Mod) btn6Glyph() string {
	if m.isLit("btn6") {
		return "*"
	}
	return "."
}

// motionRow is the trail under the mouse: the pointer's own liveness,
// which no button can show.
func (m *Mod) motionRow() []module.Span {
	dots := dimSpan("             . .  .   .")
	if m.isLit("motion") {
		dots = module.Span{Text: "             ~ ~  ~   ~", Style: module.StyleHighlight}
	}
	return padSpans([]module.Span{plain(strings.Repeat(" ", calloutW+5)), dots}, artW)
}

// padSpans right-pads a row to width cells. Art rows are hand-drawn, so
// the padding -- not the drawing -- is what guarantees a straight edge.
func padSpans(spans []module.Span, width int) []module.Span {
	n := 0
	for _, s := range spans {
		n += len([]rune(s.Text))
	}
	if n < width {
		spans = append(spans, plain(strings.Repeat(" ", width-n)))
	}
	return spans
}

// mapRows renders the asserted mapping state with device readbacks.
// Caller holds mu.
func (m *Mod) mapRows() []module.Row {
	if !m.sockUp {
		return []module.Row{{Kind: module.RowText, Key: KeyMap, Text: "logiretch.sock down (touchd running?)", Style: module.StyleWarn}}
	}
	if m.state == nil {
		return []module.Row{{Kind: module.RowText, Key: KeyMap, Text: "waiting for state...", Style: module.StyleDim}}
	}
	s := m.state
	if !s.Conn {
		return []module.Row{{Kind: module.RowText, Key: KeyMap, Text: "mouse disconnected (napping/unpaired)", Style: module.StyleDim}}
	}
	var rows []module.Row
	for _, name := range settingsOrder {
		st, ok := s.Settings[name]
		if !ok {
			rows = append(rows, module.Row{Kind: module.RowKV, Key: name, Value: "-", Style: module.StyleDim})
			continue
		}
		r := module.Row{Kind: module.RowKV, Key: name, Value: st.Want}
		switch {
		case st.Error != "":
			r.Value, r.Style = st.Want+" ! "+st.Error, module.StyleWarn
		case st.Readback != "":
			r.Value = st.Want + " (dev " + st.Readback + ")"
		}
		rows = append(rows, r)
	}
	// settings outside the pinned order still render (schema drift visible)
	var extra []string
	for name := range s.Settings {
		if !inOrder(name) {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		rows = append(rows, module.Row{Kind: module.RowKV, Key: name, Value: s.Settings[name].Want})
	}
	for _, r := range s.Remaps {
		v := r.CID + " " + r.Action
		if r.Target != "" {
			v += " -> " + r.Target
		}
		if r.Flags != "" {
			v += " (" + r.Flags + ")"
		}
		rows = append(rows, module.Row{Kind: module.RowKV, Key: "remap", Value: v, Style: module.StyleDim})
	}
	if s.Pending != "" {
		rows = append(rows, module.Row{Kind: module.RowKV, Key: "pending", Value: s.Pending + " (retrying)", Style: module.StyleWarn})
	}
	if s.Takeover != "" {
		rows = append(rows, module.Row{Kind: module.RowKV, Key: "takeover", Value: s.Takeover})
	}
	return rows
}

// statusRow is the link + battery footer. Caller holds mu.
func (m *Mod) statusRow() module.Row {
	var spans []module.Span
	switch {
	case !m.sockUp:
		spans = append(spans, module.Span{Text: "sock down", Style: module.StyleWarn})
	case m.state != nil && !m.state.Conn:
		spans = append(spans, dimSpan("link down"))
	default:
		spans = append(spans, plain("link up"))
	}
	if m.battery != nil {
		b := fmt.Sprintf("  bat %d%%", m.battery.SoC)
		if m.battery.Charging {
			b += "+"
		}
		st := ""
		if m.now().Sub(m.batAt) > batteryStaleAfter {
			// last-known + age, never an error: the age IS the staleness alarm
			st = fmt.Sprintf(" (%s old)", m.now().Sub(m.batAt).Round(1e9))
		}
		spans = append(spans, plain(b), dimSpan(st))
	}
	return module.Row{Kind: module.RowSpans, Key: KeyStatus, Spans: spans}
}

func inOrder(name string) bool {
	for _, n := range settingsOrder {
		if n == name {
			return true
		}
	}
	return false
}
