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
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/flash"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/flashjob"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/kbview"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
	"github.com/shimmerjs/khudson/khudson/internal/keyfeed"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/taskgroup"
	"github.com/shimmerjs/khudson/khudson/internal/visibility"
)

// serialTTL bounds the loader's ioreg poll; short because kuiboard is the
// interactive host that should adopt a flash promptly.
const serialTTL = 3 * time.Second

// armWindow is how long a first tap keeps the flash armed for its confirm
// tap before quietly disarming (the board going DFU mid-typing is
// disruptive; a stray tap must not trigger it).
const armWindow = 5 * time.Second

type model struct {
	tasks         *taskgroup.Group
	keys          kbview.PendingKeys
	ctx           context.Context
	jobs          flashjob.Service
	jobGeneration uint64
	visibility    visibility.Tracker
	lastView      tea.View

	watch  *keyboard.Watcher
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
	// means the independent firmware worker is pending or running.
	// notice is a transient done/error line, tap to clear.
	armed     time.Time
	armedRev  string
	flashing  bool
	flashLine string
	flashStep flash.Step
	notice    string
}

// keyEvMsg carries one decoded key event from the reader goroutine.
type keyEvMsg proto.KeyEvent

type jobStatusMsg struct {
	status     flashjob.Status
	err        error
	generation uint64
}
type jobTickMsg struct{ generation uint64 }

// armTimeoutMsg quietly disarms a confirm window that lapsed.
type armTimeoutMsg struct{}

// waitEvent blocks on the next key event and delivers it as a tea.Msg,
// re-armed after each one.
func waitEvent(ch <-chan proto.KeyEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return tea.Quit()
		}
		return keyEvMsg(ev)
	}
}

func (m *model) Init() tea.Cmd {
	if m.watch != nil {
		m.watch.Visible(true)
		return tea.Batch(m.visibility.Init(), waitEvent(m.events), m.waitBoard(), m.inspectJob())
	}
	return tea.Batch(m.visibility.Init(), waitEvent(m.events))
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	changed, cmd := m.visibility.Update(msg)
	if changed && m.watch != nil {
		m.watch.Visible(!m.visibility.Hidden())
		m.jobGeneration++
		if !m.visibility.Hidden() {
			return m, m.inspectJob()
		}
	}
	if changed && m.visibility.Hidden() {
		m.armed, m.armedRev = time.Time{}, ""
	}
	if cmd != nil {
		return m, cmd
	}

	switch msg := msg.(type) {
	case keyboard.Observation:
		if m.watch.Current(msg.Generation) {
			m.adoptBoard(msg.State)
			if msg.LatestRevision != "" {
				m.latestRev, m.latestTitle = msg.LatestRevision, msg.LatestTitle
			}
		}
		return m, m.waitBoard()
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case tea.MouseClickMsg:
		if m.visibility.Hidden() {
			return m, nil
		}
		for _, hit := range m.hits {
			a := hit.Area
			if msg.X >= a.X && msg.X < a.X+a.W && msg.Y >= a.Y && msg.Y < a.Y+a.H {
				switch hit.Kind {
				case kbview.HitLayerJump:
					m.layer = hit.Layer
				case kbview.HitOryx:
					m.openURL(hit.URL)
				case hitFlashRow:
					return m, m.tapFlashRow()
				}
				break // first-match, like the dock hit table
			}
		}
	case keyEvMsg:
		ev := proto.KeyEvent(msg)
		m.layer, _ = m.keys.Apply(m.board, m.layer, &ev)
		return m, waitEvent(m.events)
	case armTimeoutMsg:
		if !m.armed.IsZero() && time.Since(m.armed) >= armWindow {
			m.armed, m.armedRev = time.Time{}, ""
		}
	case jobTickMsg:
		if msg.generation == m.jobGeneration && !m.visibility.Hidden() {
			return m, m.inspectJob()
		}
	case jobStatusMsg:
		if msg.generation != m.jobGeneration {
			return m, nil
		}
		if msg.err != nil {
			m.notice = "firmware: " + msg.err.Error()
			m.flashing = false
			gen := m.jobGeneration
			return m, tea.Tick(3*time.Second, func(time.Time) tea.Msg { return jobTickMsg{gen} })
		}
		wasFlashing := m.flashing
		m.flashing = msg.status.State == actionjournal.Pending || msg.status.State == actionjournal.Running
		m.flashStep, m.flashLine = msg.status.Step, msg.status.Info
		if m.flashing {
			gen := m.jobGeneration
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg { return jobTickMsg{gen} })
		}
		if msg.status.State == actionjournal.Succeeded && wasFlashing {
			m.notice = "deployed " + msg.status.Target
			if m.watch != nil {
				m.watch.Refresh()
			}
		}
		if msg.status.State == actionjournal.Uncertain {
			m.notice = "firmware outcome needs review: kuiboard --flash-status"
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

func (m *model) startFlashCmd(target string) tea.Cmd {
	m.flashing = true
	m.flashLine = "starting independent firmware worker"
	m.flashStep = flash.StepResolve
	gen := m.jobGeneration
	return m.jobCommand(func(ctx context.Context) tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, err := m.jobs.Submit(ctx, target, spawnFlashWorker)
		if err != nil {
			return jobStatusMsg{err: err, generation: gen}
		}
		status, err := m.jobs.Inspect(ctx)
		return jobStatusMsg{status: status, err: err, generation: gen}
	})
}
func (m *model) inspectJob() tea.Cmd {
	gen := m.jobGeneration
	return m.jobCommand(func(ctx context.Context) tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status, err := m.jobs.Inspect(ctx)
		diagnostic.Default.Observe("firmware.inspect", err, "retain job; retry inspection")
		return jobStatusMsg{status: status, err: err, generation: gen}
	})
}
func spawnFlashWorker(id string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	cmd := exec.Command(exe, "--flash-worker", id)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	if err = cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
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
	if m.visibility.Hidden() {
		return m.lastView
	}
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if m.w < 3 || m.h < 2 {
		v.SetContent("kuiboard")
		return v
	}
	th := theme()
	m.hits = nil

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
	m.lastView = v
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

func (m *model) adoptBoard(st keyboard.LoadState) {
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
	m.layer = m.keys.Replay(m.board, m.layer)
	m.err = ""
	if m.layer >= len(st.Board.Layers) {
		m.layer = 0
	}
}

// readKeys dials the HID daemon's keys.sock and streams decoded events to ch,
// reconnecting forever; a lost source emits a synthetic clear so no highlight
// sticks (the bus keyLoop contract).
func readKeys(ctx context.Context, sock string, ch chan<- proto.KeyEvent) {
	(keyfeed.Reader{}).Run(ctx, sock, func(ev proto.KeyEvent) {
		select {
		case ch <- ev:
		case <-ctx.Done():
		}
	})
}

func (m *model) openURL(u string) {
	go m.tasks.Do(func(ctx context.Context) {
		cmd := exec.CommandContext(ctx, "/usr/bin/open", u)
		cmd.WaitDelay = time.Second
		diagnostic.Default.Observe("keyboard.url.open", cmd.Run(), "report launch failure")
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "kuiboard:", err)
		os.Exit(1)
	}
}

func run() error {
	p, err := paths.Resolve()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("kuiboard", flag.ContinueOnError)
	worker := fs.String("flash-worker", "", "run an accepted firmware operation")
	status := fs.Bool("flash-status", false, "inspect the latest firmware operation")
	discard := fs.String("discard-flash", "", "discard a reviewed uncertain firmware operation")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	jobs := flashjob.Service{Dir: filepath.Join(p.Dir, "components", "keyboard", "firmware")}
	if *status {
		v, err := jobs.Inspect(context.Background())
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(v)
	}
	if *discard != "" {
		return jobs.Discard(context.Background(), *discard)
	}
	logName := "diagnostics.jsonl"
	if *worker != "" {
		logName = "firmware-worker.jsonl"
	}
	logger, err := diagnostic.OpenFile(filepath.Join(p.Dir, "components", "keyboard", logName), 1<<20)
	if err != nil {
		return err
	}
	previous := log.Writer()
	log.SetOutput(logger)
	defer func() { log.SetOutput(previous); logger.Close() }()
	if *worker != "" {
		return jobs.Run(context.Background(), *worker, func(ctx context.Context, target string, emit func(flash.Event)) (*generations.Record, error) {
			return (&flash.Runner{Target: target, Emit: emit}).Run(ctx)
		})
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	ch := make(chan proto.KeyEvent, 16)
	done := make(chan struct{})
	go func() { defer close(done); defer close(ch); readKeys(ctx, p.KeysSocket(), ch) }()
	defer func() { cancel(); <-done }()
	tasks := taskgroup.New(ctx)
	defer tasks.Close()
	m := &model{
		tasks: tasks,
		ctx:   ctx, jobs: jobs,
		loader: &keyboard.Loader{Poller: &usbserial.Poller{TTL: serialTTL}},
		check:  &keyboard.UpdateCheck{},
		events: ch,
	}
	m.watch = keyboard.Watch(ctx, m.loader, m.check)
	defer m.watch.Close()
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	diagnostic.Default.Observe("terminal.visibility.restore", m.visibility.Restore(os.Stdout), "restore previous terminal mode")
	return err
}

func (m *model) waitBoard() tea.Cmd {
	return func() tea.Msg {
		v, ok := <-m.watch.Updates()
		if !ok {
			return nil
		}
		return v
	}
}

func (m *model) jobCommand(fn func(context.Context) tea.Msg) tea.Cmd {
	return func() tea.Msg {
		var result tea.Msg
		m.tasks.Do(func(ctx context.Context) { result = fn(ctx) })
		return result
	}
}
