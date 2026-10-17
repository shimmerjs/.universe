// Package component hosts a native component independently of the khudson bus.
// Providers and the integrated dock share the data, rendering, and action contracts.
package component

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/ownerlock"
	"github.com/shimmerjs/khudson/khudson/internal/visibility"
	"github.com/shimmerjs/khudson/khudson/provider"
	"github.com/shimmerjs/khudson/khudson/surface"
)

type Provider = module.Module
type Data = module.Data
type Row = module.Row
type Widget = config.Widget
type Render = config.Render
type View = surface.Component
type Action = surface.ComponentAction

func NewView(widget Widget) *View { return surface.NewComponent(widget) }

type Options struct {
	Provider Provider
	Widget   Widget
	Interval time.Duration
	Timeout  time.Duration
	// StateDir owns the log and, when Actions is true, the action journal.
	StateDir string
	Actions  bool
	Execute  func(context.Context, []string) error
}

// Run owns provider cancellation, journal access, and subprocess reaping. Action
// hosts use one writer per StateDir; another instance must choose a different dir.
func Run(ctx context.Context, opts Options) error {
	if opts.Provider == nil || opts.StateDir == "" {
		return fmt.Errorf("component requires a provider and state directory")
	}
	if opts.Widget.ID == "" {
		opts.Widget.ID = opts.Provider.Name()
	}
	if opts.Widget.Render.Kind != "native" || opts.Widget.Render.Module != opts.Provider.Name() {
		return fmt.Errorf("component widget must name its native provider")
	}
	if _, ok := opts.Provider.(module.EffectHandler); ok {
		return fmt.Errorf("effectful jobs require their shared service owner")
	}
	logger, err := diagnostic.OpenFile(filepath.Join(opts.StateDir, "diagnostics.jsonl"), 1<<20)
	if err != nil {
		return err
	}
	previous := log.Writer()
	log.SetOutput(logger)
	defer func() { log.SetOutput(previous); logger.Close() }()
	var journal *actionjournal.Store
	if opts.Actions {
		lock, err := ownerlock.Acquire(filepath.Join(opts.StateDir, "actions.lock"))
		if err != nil {
			return err
		}
		defer lock.Close()
		journal, err = actionjournal.Open(filepath.Join(opts.StateDir, "actions.sqlite"))
		if err != nil {
			return err
		}
		defer journal.Close()
		if err = journal.Recover(ctx); err != nil {
			return err
		}
	}
	e := startEngine(ctx, opts, journal)
	defer e.close()
	m := &terminal{view: NewView(opts.Widget), engine: e}
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	diagnostic.Default.Observe("terminal.visibility.restore", m.visibility.Restore(os.Stdout), "restore previous terminal mode")
	return err
}

type terminal struct {
	visibility visibility.Tracker
	view       *View
	engine     *engine
}
type submission struct {
	id  string
	err error
}

func (m *terminal) Init() tea.Cmd { return tea.Batch(m.view.Init(), m.wait(), m.visibility.Init()) }
func (m *terminal) wait() tea.Cmd {
	return func() tea.Msg {
		u, ok := <-m.engine.updates
		if !ok {
			return tea.Quit()
		}
		return u
	}
}
func (m *terminal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	changed, cmd := m.visibility.Update(msg)
	cmds = append(cmds, cmd)
	if changed {
		m.engine.setVisible(!m.visibility.Hidden())
	}
	switch v := msg.(type) {
	case update:
		m.view.Data(v.data, v.err)
		m.view.Status(v.status)
		cmds = append(cmds, m.wait())
	case submission:
		if v.err != nil {
			m.view.Status("action refused: " + v.err.Error())
		}
	default:
		cmds = append(cmds, m.view.Update(msg))
	}
	for _, action := range m.view.Actions() {
		cmds = append(cmds, func() tea.Msg { id, err := m.engine.submit(action); return submission{id, err} })
	}
	return m, tea.Batch(cmds...)
}
func (m *terminal) View() tea.View { return m.view.View() }

func execute(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty action")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	return cmd.Run()
}
