// Command kuiboard is a standalone terminal UI for your Moonlander keyboard:
// a thin imperative shell over the kbview functional core. It runs in any
// terminal (a kitty split/panel, tmux, a plain tty) with no dock, no bus,
// and no kitty dependency -- the couch-mode keyboard panel. It dials the
// HID daemon's keys.sock directly for live highlights (the same feed the
// khudson bus consumes) and loads the static board from the Keymapp DB, so
// it works offline and lights keys when touchd is streaming.
//
// Runtime dependency: the HID daemon (touchd) must be serving keys.sock for
// live highlights; the static board renders regardless. Control is mouse +
// ctrl+c only -- never letter keys -- because the Moonlander IS the input
// device, so its presses arrive as live highlights, not panel commands.
package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/kbview"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/keymappdb"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// keysRedial is the quiet reconnect cadence; touchd absent (board unplugged,
// daemon down) is a normal state, matched to the bus's keyLoop.
const keysRedial = 2 * time.Second

type model struct {
	board  *keyboard.Board
	err    string
	layer  int
	w, h   int
	hits   []kbview.Hit // last render's tap targets, for mouse hit-testing
	events <-chan proto.KeyEvent
}

// keyEvMsg carries one decoded key event from the reader goroutine.
type keyEvMsg proto.KeyEvent

// waitEvent blocks on the next key event and delivers it as a tea.Msg,
// re-armed after each one.
func waitEvent(ch <-chan proto.KeyEvent) tea.Cmd {
	return func() tea.Msg { return keyEvMsg(<-ch) }
}

func (m *model) Init() tea.Cmd { return waitEvent(m.events) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// ctrl+c only: the Moonlander is the input device, so binding letter
		// keys would quit the panel when the user types (the presses also
		// arrive as live highlights via keys.sock).
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.MouseClickMsg:
		for _, hit := range m.hits {
			a := hit.Area
			if msg.X >= a.X && msg.X < a.X+a.W && msg.Y >= a.Y && msg.Y < a.Y+a.H {
				switch hit.Kind {
				case kbview.HitLayerJump:
					m.layer = hit.Layer
				case kbview.HitOryx:
					openURL(hit.URL)
				}
				break // first-match, like the dock hit table
			}
		}
	case keyEvMsg:
		ev := proto.KeyEvent(msg)
		m.layer, _ = kbview.ApplyKey(m.board, m.layer, &ev)
		return m, waitEvent(m.events)
	}
	return m, nil
}

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if m.w < 3 || m.h < 2 {
		v.SetContent("kuiboard")
		return v
	}
	th := theme()
	interior := kbview.Rect{X: 1, Y: 1, W: m.w - 2, H: m.h - 2}
	body, hits := kbview.Body(m.board, m.err, m.layer, interior, "", "", keymappdb.ErrNoRevision.Error(), th)
	m.hits = hits
	title := "keyboard"
	if m.board != nil && m.board.Title != "" {
		title = "keyboard: " + m.board.Title
	}
	v.SetContent(kbview.TitledBox(title, body, m.w, m.h, kbview.LayerEdge(m.board, m.layer, th), th))
	return v
}

// theme is kuiboard's flat, palette-less capability set: no background tint,
// no identity hues (v1 renders the same board the dock golden pins). A later
// revision can supply a Background + Hue for layer tinting.
func theme() kbview.Theme {
	return kbview.Theme{
		FG:  lipgloss.NewStyle(),
		Dim: lipgloss.NewStyle().Foreground(lipgloss.BrightBlack),
	}
}

// loadBoard reads the static board from the Keymapp DB (offline; the sync
// hint renders when it is missing/empty).
func loadBoard() (*keyboard.Board, string) {
	path, err := keymappdb.DefaultPath()
	if err != nil {
		return nil, err.Error()
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err.Error()
	}
	rev, err := keymappdb.Active(path)
	if err != nil {
		return nil, err.Error()
	}
	b := keyboard.FromRevision(rev)
	if b == nil || len(b.Layers) == 0 {
		return b, "layout has no layers"
	}
	return b, ""
}

// readKeys dials the HID daemon's keys.sock and streams decoded events to ch,
// reconnecting forever; a lost source emits a synthetic clear so no highlight
// sticks (the bus keyLoop contract).
func readKeys(ctx context.Context, sock string, ch chan<- proto.KeyEvent) {
	for ctx.Err() == nil {
		conn, err := net.Dial("unix", sock)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(keysRedial):
			}
			continue
		}
		dec := json.NewDecoder(conn)
		for {
			var ev proto.KeyEvent
			if err := dec.Decode(&ev); err != nil {
				break
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				conn.Close()
				return
			}
		}
		conn.Close()
		select {
		case ch <- proto.KeyEvent{Kind: proto.KeyEventClear}:
		case <-ctx.Done():
			return
		}
	}
}

// openURL hands a URL to LaunchServices (the oryx link); failures surface in
// open's own UI, not ours.
func openURL(u string) {
	cmd := exec.Command("/usr/bin/open", u)
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

func main() {
	board, errStr := loadBoard()
	p, err := paths.Resolve()
	if err != nil {
		os.Exit(1)
	}
	ch := make(chan proto.KeyEvent, 16)
	go readKeys(context.Background(), p.KeysSocket(), ch)
	m := &model{board: board, err: errStr, events: ch}
	if _, err := tea.NewProgram(m).Run(); err != nil {
		os.Exit(1)
	}
}
