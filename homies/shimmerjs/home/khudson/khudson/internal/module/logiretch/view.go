package logiretch

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/module"
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

// artW is the art block's fixed cell width. Every row pads to it, so a
// hand-edited silhouette cannot drift a column and leave the right edge
// ragged (the top-down art it replaced was 12 cells on some rows and 13
// on others).
const artW = 41

// calloutW is the label column ahead of each leader: right-aligned names
// so the leaders line up into one gutter.
const calloutW = 10

// artLines is the ASCII MX Master 4 seen from the LEFT: the flank a side
// view actually shows -- thumbwheel, back/forward, gesture -- reads
// straight down the near edge, with leader lines out to named labels.
// The profile is wide and short by design so it can sit under the
// keyboard render later. Controls light through ctl() as events arrive;
// a lit control lights its label too, so the name is readable at a
// glance instead of only the glyph.
//
// The two main buttons are stacked rather than side by side: a true
// profile hides the far button entirely, and a mouse viewer that cannot
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

	// The shell, drawn right of the control column: the nose sits low at
	// the left where the buttons are, the surface climbs to the palm hump,
	// then the back wall drops and the base closes it. Every tail is 26
	// cells; the leading control cluster IS the near flank, so the shape
	// hangs off the callout column rather than repeating a left wall.
	rows := [][]module.Span{
		m.artRow("", "", "", "    _.-~~~~~~-._          "),
		m.artRow("", "", "", "  _.-~          ~-._      "),
		m.artRow("wheel", "wheel", wheel, "~                   ~-.   "),
		m.artRow("left", "left", "L", "                       \\  "),
		m.artRow("right", "right", "R", "                        | "),
		m.artRow("middle", "middle", "M", "                        | "),
		m.artRowSpans(m.isLit("thumb"), "thumb", []module.Span{m.ctl("thumb", thumb)},
			plain("            "), m.ctl("btn6", m.btn6Glyph()), plain("           | ")),
		// the only two-glyph control: back and forward light separately
		m.artRowSpans(m.isLit("back") || m.isLit("forward"), "back/fwd",
			[]module.Span{m.ctl("back", "<"), m.ctl("forward", ">")},
			plain("                       | ")),
		m.artRow("gesture", "gesture", "G", "                       /  "),
		m.artRow("", "", "", "~-.._______________..-~   "),
	}
	return append(rows, m.motionRow())
}

// artRow is one art line: an optional right-aligned label, its leader, the
// lit control glyph, then silhouette.
func (m *Mod) artRow(key, label, glyph, tail string) []module.Span {
	if label == "" {
		// +5, not +4: a bare row must also skip the control cell so every
		// tail starts in the same column
		return padSpans([]module.Span{
			plain(strings.Repeat(" ", calloutW+5)), plain(tail)}, artW)
	}
	return m.artRowSpans(m.isLit(key), label, []module.Span{m.ctl(key, glyph)}, plain(tail))
}

// artRowSpans is artRow for a control cluster wider than one glyph. lit
// drives the LABEL tone: the name has to read at a glance, not just the
// character. Pads to artW so the block stays rectangular whatever the
// caller drew.
func (m *Mod) artRowSpans(lit bool, label string, ctls []module.Span, tail ...module.Span) []module.Span {
	name := module.Span{Text: fmt.Sprintf("%*s", calloutW, label), Style: module.StyleDim}
	if lit {
		name.Style = module.StyleHighlight
	}
	spans := append([]module.Span{name, dimSpan(" --"), plain(" ")}, ctls...)
	return padSpans(append(spans, tail...), artW)
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
		rows = append(rows, module.Row{Kind: module.RowKV, Key: "remap", Value: v, Style: module.StyleDim})
	}
	if s.Pending != "" {
		rows = append(rows, module.Row{Kind: module.RowKV, Key: "pending", Value: s.Pending + " (retrying)", Style: module.StyleWarn})
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
