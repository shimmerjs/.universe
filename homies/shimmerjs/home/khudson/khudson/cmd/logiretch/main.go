// Command logiretch is the kuiboard-analog for the MX Master 4: a
// standalone terminal UI over logiretch.sock showing the mouse's
// asserted mapping state (DPI, SmartShift, wheel modes, haptic level,
// 1B04 remaps -- each with the device's readback) and lighting controls
// as they physically activate. Live activation rides the standard mouse
// reports the daemon already observes on the shared vendor handle:
// nothing is diverted, so what firmware never reports (the gesture
// button's custom actions, ring semantics) renders with a dim 6a tag
// instead of pretending. Runs in any terminal with no dock, no bus, no
// kitty dependency; control is ctrl+c only -- the mouse IS the input
// device, so its clicks arrive as highlights, not commands.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

// logiRedial is the quiet reconnect cadence, matched to the bus's
// logiLoop; a daemonless machine just shows "socket down".
const logiRedial = 2 * time.Second

// litFor is how long an activated control stays highlighted after its
// last event; decayEvery re-renders while anything is lit.
const (
	litFor     = 350 * time.Millisecond
	decayEvery = 120 * time.Millisecond
)

// wireLine is the superset of every logiretch.sock line kind (battery /
// state / input), discriminated by Kind -- the same decode-tolerant
// shape the bus uses.
type wireLine struct {
	T        int64           `json:"t"`
	Kind     string          `json:"kind"`
	SoC      int             `json:"soc"`
	Charging bool            `json:"charging"`
	Conn     bool            `json:"connected"`
	DevIdx   int             `json:"devIdx"`
	Features int             `json:"features"`
	Settings map[string]wSet `json:"settings"`
	Remaps   []wRemap        `json:"remaps"`
	Takeover string          `json:"takeover"`
	Pending  string          `json:"pending"`
	Held     []string        `json:"held"`
	Pressed  []string        `json:"pressed"`
	Released []string        `json:"released"`
	Wheel    int             `json:"wheel"`
	Pan      int             `json:"pan"`
	DX       int             `json:"dx"`
	DY       int             `json:"dy"`
}

type wSet struct {
	Want     string `json:"want"`
	Readback string `json:"readback"`
	Error    string `json:"error"`
}

type wRemap struct {
	CID    string `json:"cid"`
	Action string `json:"action"`
	Target string `json:"target"`
	Flags  string `json:"flags"`
}

// sockDown is the synthetic line the reader emits when the socket drops.
const sockDown = "sock-down"

type model struct {
	lines <-chan wireLine
	w, h  int
	now   time.Time

	sock    bool      // logiretch.sock connected
	state   *wireLine // latest kind:state
	battery *wireLine // latest kind:battery
	batAt   time.Time

	held map[string]bool      // buttons currently down
	lit  map[string]time.Time // control -> last activity
	// wheelDir/panDir carry the last flush's sign for the arrow glyphs.
	wheelDir, panDir int
	decayArmed       bool
}

type lineMsg wireLine
type decayMsg time.Time

func waitLine(ch <-chan wireLine) tea.Cmd {
	return func() tea.Msg { return lineMsg(<-ch) }
}

func (m *model) Init() tea.Cmd { return waitLine(m.lines) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case lineMsg:
		m.apply(wireLine(msg))
		cmds := []tea.Cmd{waitLine(m.lines)}
		if !m.decayArmed && len(m.lit) > 0 {
			m.decayArmed = true
			cmds = append(cmds, decayTick())
		}
		return m, tea.Batch(cmds...)
	case decayMsg:
		m.now = time.Time(msg)
		m.expire()
		if len(m.lit) > 0 {
			return m, decayTick()
		}
		m.decayArmed = false
	}
	return m, nil
}

func decayTick() tea.Cmd {
	return tea.Tick(decayEvery, func(t time.Time) tea.Msg { return decayMsg(t) })
}

// apply folds one wire line into the view state.
func (m *model) apply(l wireLine) {
	m.now = time.Now()
	switch l.Kind {
	case sockDown:
		m.sock = false
		// held state can never clear from a dead feed: drop it
		m.held = map[string]bool{}
	case "battery":
		m.sock = true
		b := l
		m.battery = &b
		m.batAt = m.now
	case "state":
		m.sock = true
		s := l
		m.state = &s
	case "input":
		m.sock = true
		for _, b := range l.Pressed {
			m.held[b] = true
			m.lit[b] = m.now
		}
		for _, b := range l.Released {
			delete(m.held, b)
			m.lit[b] = m.now
		}
		if l.Wheel != 0 {
			m.lit["wheel"] = m.now
			m.wheelDir = sign(l.Wheel)
		}
		if l.Pan != 0 {
			m.lit["thumb"] = m.now
			m.panDir = sign(l.Pan)
		}
		if l.DX != 0 || l.DY != 0 {
			m.lit["motion"] = m.now
		}
	}
}

// expire drops highlights past their window (held buttons stay lit).
func (m *model) expire() {
	for k, at := range m.lit {
		if m.held[k] {
			continue
		}
		if m.now.Sub(at) >= litFor {
			delete(m.lit, k)
		}
	}
}

func sign(v int) int {
	if v < 0 {
		return -1
	}
	return 1
}

// styles: flat and palette-less like kuiboard v1.
var (
	dim  = lipgloss.NewStyle().Foreground(lipgloss.BrightBlack)
	fg   = lipgloss.NewStyle()
	hot  = lipgloss.NewStyle().Reverse(true).Bold(true)
	warn = lipgloss.NewStyle().Foreground(lipgloss.Red)
)

// cell renders one control label, lit while active or held.
func (m *model) cell(key, label string) string {
	if _, on := m.lit[key]; on || m.held[key] {
		return hot.Render(label)
	}
	return fg.Render(label)
}

// mouseArt is the left column: an ASCII MX Master 4, controls lit by
// activity. Rebuilt every frame; geometry is fixed (the art is small).
func (m *model) mouseArt() []string {
	wheel := "wheel"
	if _, on := m.lit["wheel"]; on {
		if m.wheelDir > 0 {
			wheel = "wheel v"
		} else {
			wheel = "wheel ^"
		}
	}
	thumb := "thumb ==="
	if _, on := m.lit["thumb"]; on {
		if m.panDir > 0 {
			thumb = "thumb ==>"
		} else {
			thumb = "thumb <=="
		}
	}
	rows := []string{
		"+------+-------+------+",
		"| " + m.cell("left", "LEFT") + " | " + m.cell("wheel", fmt.Sprintf("%-5s", wheel)) + " | " + m.cell("right", "RGHT") + " |",
		"|      | " + m.cell("middle", "  mid") + " |      |",
		"+------+-------+------+",
		"| " + m.cell("thumb", fmt.Sprintf("%-9s", thumb)) + "         |",
		"| " + m.cell("forward", "[fwd]") + " " + m.cell("back", "[back]") + "       |",
		"| " + m.cell("btn6", "(btn6)") + " " + m.cell("btn7", "(btn7)") + " " + dim.Render("6a?") + "  |",
		"+---------------------+",
		"  " + m.cell("motion", "~ motion ~"),
	}
	return rows
}

// settingsOrder pins the mapping panel's row order.
var settingsOrder = []string{"dpi", "smartShift", "hiresWheel", "thumbwheel", "haptic"}

// mappingRows is the right column: the asserted state with readbacks,
// remaps, and honesty rows (pending retries, takeover disposition).
func (m *model) mappingRows() []string {
	if m.state == nil {
		return []string{dim.Render("no state line yet (daemon up?)")}
	}
	s := m.state
	if !s.Conn {
		return []string{warn.Render("mouse disconnected"), dim.Render("(napping or unpaired; state resumes on reconnect)")}
	}
	var rows []string
	rows = append(rows, dim.Render(fmt.Sprintf("devIdx 0x%02X, %d features", s.DevIdx, s.Features)))
	for _, name := range settingsOrder {
		st, ok := s.Settings[name]
		if !ok {
			rows = append(rows, fmt.Sprintf("%-11s %s", name, dim.Render("(not configured)")))
			continue
		}
		line := fmt.Sprintf("%-11s %s", name, st.Want)
		switch {
		case st.Error != "":
			line += " " + warn.Render("! "+st.Error)
		case st.Readback != "":
			line += dim.Render(" (dev: " + st.Readback + ")")
		}
		rows = append(rows, line)
	}
	// any settings outside the pinned order still render (schema drift
	// must be visible, not dropped)
	var extra []string
	for name := range s.Settings {
		if !slices.Contains(settingsOrder, name) {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		rows = append(rows, fmt.Sprintf("%-11s %s", name, s.Settings[name].Want))
	}
	if s.Takeover != "" {
		rows = append(rows, fmt.Sprintf("%-11s %s", "takeover", s.Takeover))
	}
	for _, r := range s.Remaps {
		line := fmt.Sprintf("%-11s %s %s", "remap", r.CID, r.Action)
		if r.Target != "" {
			line += " -> " + r.Target
		}
		if r.Flags != "" {
			line += dim.Render(" [" + r.Flags + "]")
		}
		rows = append(rows, line)
	}
	if s.Pending != "" {
		rows = append(rows, warn.Render(fmt.Sprintf("%-11s %s (retrying)", "pending", s.Pending)))
	}
	return rows
}

// statusRow is the bottom line: socket, link, battery with age.
func (m *model) statusRow() string {
	parts := []string{}
	if !m.sock {
		parts = append(parts, warn.Render("logiretch.sock down"))
	} else if m.state != nil && !m.state.Conn {
		parts = append(parts, dim.Render("link: down"))
	} else {
		parts = append(parts, "link: up")
	}
	if m.battery != nil {
		b := fmt.Sprintf("battery %d%%", m.battery.SoC)
		if m.battery.Charging {
			b += " charging"
		}
		if age := time.Since(m.batAt).Round(time.Second); age > 10*time.Minute {
			// last-known-value + age, never an error: the staleness IS the
			// alarm (design doc battery cadence note)
			b += dim.Render(fmt.Sprintf(" (%s old)", age))
		}
		parts = append(parts, b)
	}
	return " " + strings.Join(parts, "   ")
}

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	if m.w < 30 || m.h < 6 {
		v.SetContent("logiretch")
		return v
	}
	title := fg.Bold(true).Render(" logiretch") + dim.Render(" -- MX Master 4")
	art := m.mouseArt()
	maps := m.mappingRows()
	body := make([]string, 0, max(len(art), len(maps))+3)
	body = append(body, title, "")
	const artW = 26
	for i := 0; i < max(len(art), len(maps)); i++ {
		left, right := "", ""
		if i < len(art) {
			left = art[i]
		}
		if i < len(maps) {
			right = maps[i]
		}
		pad := max(artW-lipgloss.Width(left), 0)
		body = append(body, "  "+left+strings.Repeat(" ", pad)+right)
	}
	for len(body) < m.h-1 {
		body = append(body, "")
	}
	body = body[:m.h-1]
	body = append(body, m.statusRow())
	v.SetContent(strings.Join(body, "\n"))
	return v
}

// readLines dials logiretch.sock and streams decoded lines, reconnecting
// forever; a lost socket emits a synthetic sock-down marker so stale
// state reads as stale (the kuiboard readKeys contract).
func readLines(ctx context.Context, sock string, ch chan<- wireLine) {
	for ctx.Err() == nil {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(logiRedial):
			}
			continue
		}
		dec := json.NewDecoder(conn)
		for {
			var l wireLine
			if err := dec.Decode(&l); err != nil {
				break
			}
			select {
			case ch <- l:
			case <-ctx.Done():
				conn.Close()
				return
			}
		}
		conn.Close()
		select {
		case ch <- wireLine{Kind: sockDown}:
		case <-ctx.Done():
			return
		}
	}
}

func main() {
	p, err := paths.Resolve()
	if err != nil {
		fmt.Fprintln(os.Stderr, "logiretch:", err)
		os.Exit(1)
	}
	ch := make(chan wireLine, 32)
	go readLines(context.Background(), p.LogiSocket(), ch)
	m := &model{
		lines: ch,
		held:  map[string]bool{},
		lit:   map[string]time.Time{},
		now:   time.Now(),
	}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "logiretch:", err)
		os.Exit(1)
	}
}
