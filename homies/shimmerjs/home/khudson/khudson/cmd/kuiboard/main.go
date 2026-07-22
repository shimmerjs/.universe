// Command kuiboard is a standalone terminal UI for your Moonlander keyboard:
// a thin imperative shell over the kbview functional core. It runs in any
// terminal (a kitty split/panel, tmux, a plain tty) with no dock, no bus,
// and no kitty dependency -- the couch-mode keyboard panel. It dials the
// HID daemon's keys.sock directly for live highlights (the same feed the
// khudson bus consumes) and resolves the static board keymapp-free: the USB
// serial names the deployed revision, the payload comes from the local
// caches or Oryx, so it works offline and lights keys when touchd is
// streaming.
//
// kuiboard also owns the flash loop: it polls Oryx for a newer revision of
// the deployed layout, and a two-tap arm/confirm on the status row drives
// the flash orchestrator (download, zapp, RESET tap, serial-verified
// generation record).
//
// Runtime dependency: the HID daemon (touchd) must be serving keys.sock for
// live highlights; the static board renders regardless. Control is mouse +
// ctrl+c only -- never letter keys -- because the Moonlander IS the input
// device, so its presses arrive as live highlights, not panel commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/flash"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/kbview"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// keysRedial is the quiet reconnect cadence; touchd absent (board unplugged,
// daemon down) is a normal state, matched to the bus's keyLoop.
const keysRedial = 2 * time.Second

// serialTTL bounds the loader's ioreg poll; short because kuiboard is the
// interactive host that should adopt a flash promptly.
const serialTTL = 3 * time.Second

// updatePoll is the update-check cadence; the UpdateCheck's own TTL (5m)
// dedups the network behind it.
const updatePoll = time.Minute

// armWindow is how long a first tap keeps the flash armed for its confirm
// tap before quietly disarming (the board going DFU mid-typing is
// disruptive; a stray tap must not trigger it).
const armWindow = 5 * time.Second

type model struct {
	loader *keyboard.Loader
	check  *keyboard.UpdateCheck

	board  *keyboard.Board
	err    string
	layer  int
	w, h   int
	hits   []kbview.Hit // last render's tap targets, for mouse hit-testing
	events <-chan proto.KeyEvent

	// identity/present mirror the last load state (the deployed revision).
	identity usbserial.Identity
	present  bool

	// latestRev/latestTitle are the newest known Oryx revision; the update
	// chip shows when latestRev differs from the deployed revision.
	latestRev   string
	latestTitle string

	// armed non-zero means the first tap landed and the confirm window is
	// open; armedRev pins the revision that tap named, so a poll landing
	// mid-arm can neither swap nor erase the confirmed target. flashing
	// means the orchestrator goroutine is running and flashMsgs is live.
	// notice is a transient done/error line, tap to clear.
	armed     time.Time
	armedRev  string
	flashing  bool
	flashMsgs chan tea.Msg
	flashLine string
	flashStep flash.Step
	notice    string
}

// keyEvMsg carries one decoded key event from the reader goroutine.
type keyEvMsg proto.KeyEvent

// flashEvMsg is one orchestrator progress event.
type flashEvMsg flash.Event

// flashDoneMsg is the orchestrator's terminal result; rec is the written
// generation record (nil on error), so the done notice names the revision
// that actually deployed, not the chip's possibly-newer latest.
type flashDoneMsg struct {
	rec *generations.Record
	err error
}

// updateTickMsg re-runs the update check; updateMsg carries its result.
// ok=false (a failed poll) keeps the last known update on glass -- one
// transient failure must not erase a known-available revision.
type updateTickMsg struct{}
type updateMsg struct {
	rev, title string
	ok         bool
}

// armTimeoutMsg quietly disarms a confirm window that lapsed.
type armTimeoutMsg struct{}

// waitEvent blocks on the next key event and delivers it as a tea.Msg,
// re-armed after each one.
func waitEvent(ch <-chan proto.KeyEvent) tea.Cmd {
	return func() tea.Msg { return keyEvMsg(<-ch) }
}

// waitFlash blocks on the next orchestrator message, re-armed until done.
func waitFlash(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		waitEvent(m.events),
		func() tea.Msg { return updateTickMsg{} },
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// ctrl+c only: the Moonlander is the input device, so binding letter
		// keys would quit the panel when the user types (the presses also
		// arrive as live highlights via keys.sock). Quitting mid-flash
		// closes zapp's output pipes and leaves it to its own write-error
		// handling -- not guaranteed safe mid-DFU-write -- so prefer letting
		// the flash finish; the generation record is lost either way.
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
				case hitFlashRow:
					return m, m.tapFlashRow()
				}
				break // first-match, like the dock hit table
			}
		}
	case keyEvMsg:
		ev := proto.KeyEvent(msg)
		m.layer, _ = kbview.ApplyKey(m.board, m.layer, &ev)
		return m, waitEvent(m.events)
	case updateTickMsg:
		return m, tea.Batch(m.checkUpdateCmd(), tea.Tick(updatePoll, func(time.Time) tea.Msg { return updateTickMsg{} }))
	case updateMsg:
		if msg.ok {
			m.latestRev, m.latestTitle = msg.rev, msg.title
		}
	case armTimeoutMsg:
		if !m.armed.IsZero() && time.Since(m.armed) >= armWindow {
			m.armed, m.armedRev = time.Time{}, ""
		}
	case flashEvMsg:
		m.flashStep, m.flashLine = msg.Step, msg.Info
		return m, waitFlash(m.flashMsgs)
	case flashDoneMsg:
		m.flashing = false
		m.flashMsgs = nil
		switch {
		case msg.err == nil:
			m.notice = "deployed " + msg.rec.RevisionID
			m.loader.Invalidate()
		case errors.Is(msg.err, flash.ErrAlreadyDeployed):
			m.notice = "already up to date"
		default:
			m.notice = "flash failed: " + msg.err.Error()
		}
	}
	return m, nil
}

// hitFlashRow is kuiboard's own hit kind for the status row; kbview's enum
// is not extended -- the row is host chrome, not core view.
const hitFlashRow = kbview.HitKind(-1)

// tapFlashRow advances the status-row state machine: clear a notice, confirm
// an armed flash, or open the confirm window.
func (m *model) tapFlashRow() tea.Cmd {
	switch {
	case m.flashing:
		return nil // the row is a progress line mid-flash, not a control
	case m.notice != "":
		m.notice = ""
	case !m.armed.IsZero():
		target := m.armedRev
		m.armed, m.armedRev = time.Time{}, ""
		return m.startFlashCmd(target)
	case m.updateAvailable():
		m.armed, m.armedRev = time.Now(), m.latestRev
		return tea.Tick(armWindow, func(time.Time) tea.Msg { return armTimeoutMsg{} })
	}
	return nil
}

// updateAvailable reports a known-newer revision for the deployed layout.
func (m *model) updateAvailable() bool {
	return m.present && !m.flashing && m.latestRev != "" && m.latestRev != m.identity.RevisionID
}

// checkUpdateCmd polls Oryx for the latest revision off the render path.
// Skipped while the board is absent (no layout id to ask about).
func (m *model) checkUpdateCmd() tea.Cmd {
	if !m.present || m.identity.LayoutID == "" {
		return nil
	}
	layoutID := m.identity.LayoutID
	check := m.check
	return func() tea.Msg {
		rev, title, err := check.Get(context.Background(), layoutID)
		if err != nil {
			return updateMsg{} // offline: keep the last known chip, retry next tick
		}
		return updateMsg{rev: rev, title: title, ok: true}
	}
}

// startFlashCmd launches the orchestrator goroutine and starts draining its
// messages. target is the revision the confirm prompt named -- an update
// poll landing mid-arm must not swap the deploy under the user's tap. The
// run gets context.Background(), not the program lifetime: see the ctrl+c
// note in Update.
func (m *model) startFlashCmd(target string) tea.Cmd {
	msgs := make(chan tea.Msg, 32)
	m.flashing = true
	m.flashMsgs = msgs
	m.flashLine = "starting"
	m.flashStep = flash.StepResolve
	go func() {
		r := &flash.Runner{
			Target: target,
			Emit:   func(e flash.Event) { msgs <- flashEvMsg(e) },
		}
		rec, err := r.Run(context.Background())
		msgs <- flashDoneMsg{rec: rec, err: err}
	}()
	return waitFlash(msgs)
}

// statusRow renders kuiboard's own bottom row: flash progress, the confirm
// prompt, the update chip, or a transient notice. Empty when idle.
func (m *model) statusRow(th kbview.Theme) string {
	switch {
	case m.flashing:
		line := m.flashLine
		if m.flashStep == flash.StepZapp {
			return th.FG.Render(" TAP RESET ") + th.Dim.Render(line)
		}
		return th.Dim.Render(" flashing: " + line)
	case m.notice != "":
		return th.Dim.Render(" " + m.notice + " -- tap to dismiss")
	case !m.armed.IsZero():
		return th.FG.Render(" flash " + m.armedRev + "? tap again to confirm")
	case m.updateAvailable():
		return th.Dim.Render(" update available: " + m.latestRev + " (" + m.latestTitle + ") -- tap to flash")
	}
	return ""
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
	m.hits = nil
	m.refreshBoard()

	status := m.statusRow(th)
	bodyBottom := m.h
	if status != "" {
		bodyBottom = m.h - 1
	}

	var content string
	if kbview.Empty(m.board, m.err) {
		body := kbview.Body(m.board, m.err, m.layer, m.w-2, bodyBottom-2, "", "", keyboard.ErrNoBoard.Error(), th)
		content = kbview.TitledBox("keyboard", body, m.w, bodyBottom, nil, th)
	} else {
		// the bar rides the TOP row (tabs left, board title + oryx right) with
		// a spacer row under it; the grid fills the rest full-bleed -- the
		// terminal window is the frame
		bar, hits := kbview.Bar(m.board, m.layer, m.w, m.board.Title, th)
		m.hits = hits
		body := kbview.Body(m.board, m.err, m.layer, m.w, bodyBottom-2, "", "", keyboard.ErrNoBoard.Error(), th)
		content = bar + "\n\n" + strings.Join(body, "\n")
	}
	if status != "" {
		content += "\n" + status
		m.hits = append(m.hits, kbview.Hit{
			Kind: hitFlashRow,
			Area: kbview.Rect{X: 0, Y: m.h - 1, W: m.w, H: 1},
		})
	}
	v.SetContent(content)
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

// refreshBoard folds the loader state into the view: the board follows the
// deployed revision (a flash mid-session is adopted on the next serial
// poll); a resolve that yields nothing keeps the board on glass.
func (m *model) refreshBoard() {
	st := m.loader.Load(context.Background())
	m.identity, m.present = st.Identity, st.Present
	if st.Board == nil || len(st.Board.Layers) == 0 {
		if m.board == nil {
			m.err = st.Err
		}
		return
	}
	if st.Board == m.board {
		return
	}
	m.board = st.Board
	m.err = ""
	if m.layer >= len(st.Board.Layers) {
		m.layer = 0
	}
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
	p, err := paths.Resolve()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kuiboard:", err)
		os.Exit(1)
	}
	ch := make(chan proto.KeyEvent, 16)
	go readKeys(context.Background(), p.KeysSocket(), ch)
	m := &model{
		loader: &keyboard.Loader{Poller: &usbserial.Poller{TTL: serialTTL}},
		check:  &keyboard.UpdateCheck{},
		events: ch,
	}
	m.refreshBoard()
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "kuiboard:", err)
		os.Exit(1)
	}
}
