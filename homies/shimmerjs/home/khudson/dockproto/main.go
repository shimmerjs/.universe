// khudson dock proto: the first thing on the glass. A static-but-alive skeleton
// of the dock layout (tile grid + active panel + status strip) to prove panel
// pinning, cell budget, and touch-as-mouse taps end to end. Not the real dock:
// no bus, no config, no widgets -- tap a tile, watch it activate.
//
// Launch on the Edge:
//
//	kitten panel --detach --edge=center --output-name "XENEON EDGE" ./dockproto
package main

import (
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	bg     = lipgloss.Color("#232a2e")
	fg     = lipgloss.Color("#d3c6aa")
	dim    = lipgloss.Color("#859289")
	green  = lipgloss.Color("#a7c080")
	border = lipgloss.Color("#475258")

	tileStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border).
			Foreground(dim).
			Align(lipgloss.Center, lipgloss.Center)
	tileActiveStyle = tileStyle.
			BorderForeground(green).
			Foreground(green).
			Bold(true)
	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(border)
	stripStyle = lipgloss.NewStyle().
			Foreground(dim)
	brandStyle = lipgloss.NewStyle().
			Foreground(green).
			Bold(true)
)

var tiles = []string{"sys", "prs", "claude", "kitty", "chat", "keys", "demo", "dim"}

type tickMsg time.Time

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

type model struct {
	width, height int
	active        int
	now           time.Time
	taps          int
	tileRects     []rect
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) Init() tea.Cmd { return tick() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.MouseClickMsg:
		m.taps++
		for i, r := range m.tileRects {
			if r.contains(msg.X, msg.Y) {
				m.active = i
			}
		}
	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()
	}
	return m, nil
}

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if m.width == 0 {
		v.SetContent("...")
		return v
	}

	stripH := 1
	bodyH := m.height - stripH

	// dock: 2 columns x 4 rows of tiles
	tileW, tileH := 14, (bodyH-1)/4
	if tileH < 3 {
		tileH = 3
	}
	dockW := tileW * 2

	m.tileRects = m.tileRects[:0]
	rows := make([]string, 0, 4)
	for r := range 4 {
		l, ri := r*2, r*2+1
		left := m.renderTile(l, tileW, tileH)
		right := m.renderTile(ri, tileW, tileH)
		m.tileRects = append(m.tileRects,
			rect{0, r * tileH, tileW, tileH},
			rect{tileW, r * tileH, tileW, tileH},
		)
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, left, right))
	}
	dock := lipgloss.JoinVertical(lipgloss.Left, rows...)

	panelW := m.width - dockW - 1
	panelBody := lipgloss.JoinVertical(lipgloss.Center,
		"",
		brandStyle.Render(tiles[m.active]),
		"",
		lipgloss.NewStyle().Foreground(fg).Render("widget panel -- coming soon"),
		"",
		lipgloss.NewStyle().Foreground(dim).Render(m.now.Format("15:04:05")),
		lipgloss.NewStyle().Foreground(dim).Render(fmt.Sprintf("taps seen: %d", m.taps)),
	)
	panel := panelStyle.
		Width(panelW-2).
		Height(bodyH-2).
		Align(lipgloss.Center, lipgloss.Center).
		Render(panelBody)

	body := lipgloss.JoinHorizontal(lipgloss.Top, dock, " ", panel)

	strip := lipgloss.JoinHorizontal(lipgloss.Top,
		brandStyle.Render(" khudson "),
		stripStyle.Render("| "+tiles[m.active]+" | tap tiles | q quits | "),
		stripStyle.Render(m.now.Format("mon 15:04")),
	)

	v.SetContent(lipgloss.JoinVertical(lipgloss.Left, body, strip))
	v.BackgroundColor = bg
	return v
}

func (m *model) renderTile(i, w, h int) string {
	s := tileStyle
	if i == m.active {
		s = tileActiveStyle
	}
	return s.Width(w - 2).Height(h - 2).Render(tiles[i])
}

func main() {
	m := &model{now: time.Now()}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
