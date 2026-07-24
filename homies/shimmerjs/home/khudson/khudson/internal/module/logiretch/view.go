package logiretch

import (
	"fmt"
	"sort"

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

// artLines is the ASCII MX Master 4: a top-down silhouette with the
// two main buttons and wheel up top, the thumb cluster (thumbwheel,
// back/forward, gesture button) on the left flank, and the palm swell
// below. Controls light through ctl() as events arrive.
func (m *Mod) artLines() [][]module.Span {
	wheel := "|"
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
	motion := dimSpan("   . . .")
	if m.isLit("motion") {
		motion = module.Span{Text: "   ~ ~ ~", Style: module.StyleHighlight}
	}

	return [][]module.Span{
		{plain("      .-\"\"-.")},
		{plain("     /"), m.ctl("left", "L"), plain(" | "), m.ctl("right", "R"), plain(`\`)},
		{plain("    |   "), m.ctl("wheel", wheel), plain("   |")},
		{plain("    | ("), m.ctl("middle", "o"), plain(")   |")},
		{m.ctl("thumb", thumb), plain("---|       |")},
		{m.ctl("back", "<"), plain(" "), m.ctl("forward", ">"), plain(" |       |    "), m.ctl("btn6", "*")},
		{plain("[ "), m.ctl("gesture", "G"), plain(" ] |       |")},
		{plain("    |       |")},
		{plain("     \\     /")},
		{plain("      '.__.'"), motion},
	}
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
