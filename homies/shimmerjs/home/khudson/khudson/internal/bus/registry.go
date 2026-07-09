package bus

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
)

// UserVarWidget is the kitty user var binding widgets to their hidden
// windows (`@ ls` alone cannot tell two identical argvs apart).
const UserVarWidget = "khudson_widget"

// Registry holds per-widget runtime state, keyed by widget id.
type Registry struct {
	mu      sync.Mutex
	widgets map[string]*WidgetState
}

// WidgetState is one widget's runtime state. WindowID/Cols/Rows/Snapshot/
// Native/Acts/PolledAt are written under bindMu because other goroutines
// (the input worker, the dock greeting, ctl status) read them; Widget is
// immutable after construction.
type WidgetState struct {
	Widget config.Widget

	bindMu sync.Mutex
	// exec (scraped) runtime
	WindowID int // substrate kitty window hosting it; 0 = not materialized
	// Cols/Rows is the widget's panel region in cells (set from the layout
	// before Ensure); zero leaves the hidden window at kitty's default size.
	Cols, Rows int
	Snapshot   []byte // last get-text --ansi capture
	Native     []byte // last marshaled module.Data (native widgets)
	// Acts is every row-act argv from the last successful native poll;
	// handleRowAct's allowlist reads it. Error polls keep the previous set.
	Acts     [][]string
	PolledAt time.Time // when Snapshot last refreshed; staleness reads it
}

// Binding snapshots the window binding for cross-goroutine readers (the
// input worker); scheduler-side readers may keep reading the fields directly
// under the busy discipline.
func (st *WidgetState) Binding() (windowID, cols, rows int) {
	st.bindMu.Lock()
	defer st.bindMu.Unlock()
	return st.WindowID, st.Cols, st.Rows
}

func (st *WidgetState) setWindowID(id int) {
	st.bindMu.Lock()
	st.WindowID = id
	st.bindMu.Unlock()
}

func (st *WidgetState) setSize(cols, rows int) {
	st.bindMu.Lock()
	st.Cols, st.Rows = cols, rows
	st.bindMu.Unlock()
}

func (st *WidgetState) setSnapshot(b []byte) {
	st.bindMu.Lock()
	st.Snapshot = b
	st.PolledAt = time.Now()
	st.bindMu.Unlock()
}

// polled snapshots PolledAt for cross-goroutine readers (ctl status); the
// scheduler's staleness check reads through it too.
func (st *WidgetState) polled() time.Time {
	st.bindMu.Lock()
	defer st.bindMu.Unlock()
	return st.PolledAt
}

func (st *WidgetState) setNative(b []byte) {
	st.bindMu.Lock()
	st.Native = b
	st.bindMu.Unlock()
}

func (st *WidgetState) setActs(acts [][]string) {
	st.bindMu.Lock()
	st.Acts = acts
	st.bindMu.Unlock()
}

// acts snapshots the last-poll row acts for cross-goroutine readers (the
// input worker's row-act allowlist); the argv slices are shared read-only.
func (st *WidgetState) acts() [][]string {
	st.bindMu.Lock()
	defer st.bindMu.Unlock()
	return slices.Clone(st.Acts)
}

// cached snapshots the replayable render state for greeting a reconnected
// dock; copies because the caller encodes outside bindMu. polledAt rides
// along so the greeting can mark an aged snapshot stale.
func (st *WidgetState) cached() (snapshot, native []byte, cols, rows int, polledAt time.Time) {
	st.bindMu.Lock()
	defer st.bindMu.Unlock()
	return bytes.Clone(st.Snapshot), bytes.Clone(st.Native), st.Cols, st.Rows, st.PolledAt
}

// NewRegistry builds runtime state for every widget in cfg.
func NewRegistry(cfg *config.Config) *Registry {
	r := &Registry{widgets: make(map[string]*WidgetState, len(cfg.Widgets))}
	for id, w := range cfg.Widgets {
		r.widgets[id] = &WidgetState{Widget: w}
	}
	return r
}

// Get returns the state for id.
func (r *Registry) Get(id string) (*WidgetState, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.widgets[id]
	return st, ok
}

// IDs returns all widget ids, unordered.
func (r *Registry) IDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.widgets))
	for id := range r.widgets {
		ids = append(ids, id)
	}
	return ids
}

// Supervisor materializes exec widgets as minimized windows in the khudson
// kitty instance and retires them per keepAlive/idleKill.
type Supervisor interface {
	// Ensure makes the widget's window exist and records its id.
	Ensure(ctx context.Context, st *WidgetState) error
	// Resize re-sizes an existing window to cols x rows cells and records
	// the new size on st.
	Resize(ctx context.Context, st *WidgetState, cols, rows int) error
	// Release tears the window down (idleKill fired, or shutdown).
	Release(ctx context.Context, st *WidgetState) error
	// Adopt rebinds windows found in an ls tree by user var after a bus
	// restart (crash model: topology from CUE + @ ls + user vars).
	Adopt(tree []rc.OSWindow, reg *Registry) int
	// LS returns the substrate window tree; adoption and binding
	// verification go through the seam so fakes can exercise them.
	LS() ([]rc.OSWindow, error)
}

// Scraper owns the get-text cadence for exec widgets. Implementations must
// keep at most one capture in flight per widget.
type Scraper interface {
	Poll(st *WidgetState, sink func(id string, snapshot []byte, err error))
}

// Injector delivers input into scraped windows: raw SGR mouse reports and
// semantic keys (both land verbatim on the child PTY).
type Injector interface {
	SendSGR(match string, button, x, y int, release bool) error
	SendKey(match string, keys ...string) error
}

type rcInjector struct {
	rc *rc.Client
}

// NewInjector returns the hud-kitty input injector.
func NewInjector(client *rc.Client) Injector {
	return &rcInjector{rc: client}
}

func (i *rcInjector) SendSGR(match string, button, x, y int, release bool) error {
	return i.rc.SendBytes(match, rc.SGRMouse(button, x, y, release))
}

func (i *rcInjector) SendKey(match string, keys ...string) error {
	return i.rc.SendKey(match, keys...)
}

// rcSupervisor is the hud-kitty supervisor.
type rcSupervisor struct {
	rc *rc.Client
}

// NewSupervisor returns the hud-kitty supervisor.
func NewSupervisor(client *rc.Client) Supervisor {
	return &rcSupervisor{rc: client}
}

func (s *rcSupervisor) Ensure(ctx context.Context, st *WidgetState) error {
	if st.Widget.Render.Kind != "exec" {
		return nil
	}
	// Binding, not the bare field: the async adopt goroutine can be
	// rebinding concurrently
	if id, _, _ := st.Binding(); id != 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := s.rc.Launch(rc.LaunchOpts{
		Args: st.Widget.Render.Argv,
		Type: "os-window",
		// kitty has no hidden os-window state; minimized keeps
		// the substrate off screen while the screen model updates, and the
		// off-screen position covers panel instances, whose windows cannot
		// miniaturize and would otherwise land visible on the desktop
		OSWindowState:    "minimized",
		OSWindowPosition: "-20000x-20000",
		KeepFocus:        true,
		Var:              []string{UserVarWidget + "=" + st.Widget.ID},
	})
	if err != nil {
		return fmt.Errorf("supervisor: launch %s: %w", st.Widget.ID, err)
	}
	if id == 0 {
		return fmt.Errorf("supervisor: launch %s: kitty returned no window id", st.Widget.ID)
	}
	return finishEnsure(s.rc, st, id)
}

// ensureOps is the post-launch window surface finishEnsure needs; an
// interface so the rollback path is testable without a live kitty.
type ensureOps interface {
	HideOSWindow(match string) error
	ResizeOSWindow(match string, widthCells, heightCells int) error
	CloseWindow(match string) error
}

// finishEnsure binds a freshly launched window and applies hide + initial
// size; on failure the window is best-effort closed and the binding rolled
// back so backoff + the materialize branch retry cleanly instead of keeping
// a visible half-configured window.
func finishEnsure(ops ensureOps, st *WidgetState, id int) error {
	st.setWindowID(id)
	match := fmt.Sprintf("id:%d", id)
	// hide is the real invisibility (orderOut: no Dock tile, no genie
	// animation); minimized-at-creation only covers the gap before it
	if err := ops.HideOSWindow(match); err != nil {
		_ = ops.CloseWindow(match)
		st.setWindowID(0)
		return fmt.Errorf("supervisor: hide %s: %w", st.Widget.ID, err)
	}
	_, cols, rows := st.Binding()
	if cols > 0 && rows > 0 {
		if err := ops.ResizeOSWindow(match, cols, rows); err != nil {
			_ = ops.CloseWindow(match)
			st.setWindowID(0)
			return fmt.Errorf("supervisor: resize %s: %w", st.Widget.ID, err)
		}
	}
	return nil
}

func (s *rcSupervisor) Resize(ctx context.Context, st *WidgetState, cols, rows int) error {
	win, curCols, curRows := st.Binding()
	if win == 0 || (curCols == cols && curRows == rows) {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.rc.ResizeOSWindow(fmt.Sprintf("id:%d", win), cols, rows); err != nil {
		return fmt.Errorf("supervisor: resize %s: %w", st.Widget.ID, err)
	}
	st.setSize(cols, rows)
	return nil
}

func (s *rcSupervisor) Release(ctx context.Context, st *WidgetState) error {
	win, _, _ := st.Binding()
	if win == 0 {
		return nil
	}
	if err := s.rc.CloseWindow(fmt.Sprintf("id:%d", win)); err != nil {
		// the window may already be gone (widget process exited); confirm
		// against ls before treating the error as real
		if tree, lsErr := s.rc.LS(); lsErr != nil || windowExists(tree, win) {
			return fmt.Errorf("supervisor: close %s: %w", st.Widget.ID, err)
		}
	}
	st.setWindowID(0)
	return nil
}

func windowExists(tree []rc.OSWindow, id int) bool {
	for _, osw := range tree {
		for _, tab := range osw.Tabs {
			for _, w := range tab.Windows {
				if w.ID == id {
					return true
				}
			}
		}
	}
	return false
}

func (s *rcSupervisor) Adopt(tree []rc.OSWindow, reg *Registry) int {
	return adoptTree(tree, reg, s.rc.CloseWindow)
}

func (s *rcSupervisor) LS() ([]rc.OSWindow, error) {
	return s.rc.LS()
}

// adoptTree walks every window carrying the widget user var: unbound
// registry entries rebind, windows whose widget left the config are closed
// (orphan GC), and extra windows for an already-bound widget are closed
// (duplicate GC) so a reload cannot leak or double-materialize.
func adoptTree(tree []rc.OSWindow, reg *Registry, closeWin func(match string) error) int {
	adopted := 0
	for _, osw := range tree {
		for _, tab := range osw.Tabs {
			for _, w := range tab.Windows {
				id := w.UserVars[UserVarWidget]
				if id == "" {
					continue
				}
				st, ok := reg.Get(id)
				if !ok {
					log.Printf("khudson bus: adopt: closing orphan window %d (widget %q not in config)", w.ID, id)
					if err := closeWin(fmt.Sprintf("id:%d", w.ID)); err != nil {
						log.Printf("khudson bus: adopt: close window %d: %v", w.ID, err)
					}
					continue
				}
				bound, _, _ := st.Binding()
				switch {
				case bound == 0:
					st.setWindowID(w.ID)
					adopted++
				case bound != w.ID:
					log.Printf("khudson bus: adopt: closing duplicate window %d for widget %q (bound to %d)", w.ID, id, bound)
					if err := closeWin(fmt.Sprintf("id:%d", w.ID)); err != nil {
						log.Printf("khudson bus: adopt: close window %d: %v", w.ID, err)
					}
				}
			}
		}
	}
	return adopted
}

// rcScraper drives rc.TextPoller; the per-widget single-flight lives there.
type rcScraper struct {
	poller *rc.TextPoller
}

// NewScraper returns the get-text scraper for the hud kitty.
func NewScraper(client *rc.Client) Scraper {
	return &rcScraper{poller: rc.NewTextPoller(client)}
}

func (s *rcScraper) Poll(st *WidgetState, sink func(id string, snapshot []byte, err error)) {
	id := st.Widget.ID
	win, _, _ := st.Binding()
	if win == 0 {
		sink(id, nil, fmt.Errorf("scrape %s: no window", id))
		return
	}
	s.poller.Request(rc.GetTextOpts{
		Match: fmt.Sprintf("id:%d", win),
		ANSI:  true,
	}, func(text string, err error) {
		sink(id, []byte(text), err)
	})
}
