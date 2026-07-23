// Package reno is the demo presenter/recorder module: the step cursor,
// stopwatch, and action dispatch behind the reno strip tab. The module is
// the engine; machine actions exec as `khudson reno ...` verbs through
// the bus effect executor (module.EffectHandler), so a wedged subprocess
// can never stall the bus and the same verbs stay drivable from a shell.
// Script and run-journal machinery live in internal/reno; this package
// binds them to the bus contract.
package reno

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// Act argvs, deliberately not command names so a bus that misses the
// in-process dispatch fails loudly at exec (panel:fold precedent).
const (
	actNext       = "reno:next"
	actPrev       = "reno:prev"
	actGoto       = "reno:goto"        // + step id
	actGotoWindow = "reno:goto-window" // + -1|+1, shifts the goto menu window
	actStop       = "reno:stop"
)

// transitionGap suppresses double-fired transitions: a retap on a lagging
// panel must not double-advance the cursor mid-demo. In-process acts are
// never bus-debounced (deliberate, fold-toggle precedent), so the module
// owns this guard.
const transitionGap = 400 * time.Millisecond

// gotoWindow is how many steps the goto menu shows around the cursor;
// popovers keep only rows that fit (~22 on this glass) and discard the
// rest, so the module windows before publishing.
const gotoWindow = 12

// Mod is the reno module singleton. Cross-poll state is the contract's
// legal shape; everything below mu survives config reloads (module
// instances are built once per bus process).
type Mod struct {
	mu     sync.Mutex
	staged *renocore.Script
	run    *renocore.Run
	// stale carries a loud recovery problem (script moved/edited under a
	// live run); it renders as a warn row until the next load/start.
	stale string
	// journalErr is the last failed journal write -- the demo keeps
	// running (the show must go on) but the failure stays on glass.
	journalErr string
	gotoOff    int

	runsDir string // "" on bare test buses: journal writes error loudly
	exe     string // os.Executable for TCC-identity argvs (focus-app)
	now     func() time.Time
	lastAct time.Time
}

// New builds the singleton, recovering the newest live run journal (a
// bus crash mid-demo must not lose the cursor) and sweeping old
// finalized journals -- one-shot boot work, never per-tick.
func New() *Mod {
	m := &Mod{now: time.Now}
	if exe, err := os.Executable(); err == nil {
		m.exe = exe
	} else {
		m.exe = "khudson"
	}
	if p, err := paths.Resolve(); err == nil {
		m.runsDir = filepath.Join(p.Dir, "reno", "runs")
	}
	m.recover()
	renocore.SweepRuns(m.runsDir, m.now())
	return m
}

func (m *Mod) recover() {
	run, errs := renocore.RecoverLatest(m.runsDir)
	for _, err := range errs {
		log.Printf("khudson reno: run journal: %v", err)
	}
	if run == nil {
		return
	}
	m.run = run
	if run.ScriptPath == "" {
		return // ad hoc: nothing to restage
	}
	s, err := renocore.Load(run.ScriptPath)
	if err != nil {
		m.stale = fmt.Sprintf("recovered run %s but its script is unreadable: %v", run.ID, err)
		return
	}
	if s.Hash != run.ScriptHash {
		m.stale = fmt.Sprintf("recovered run %s but %s changed since (hash mismatch); stop and reload", run.ID, run.ScriptPath)
		return
	}
	m.staged = s
	log.Printf("khudson reno: recovered run %s (%s, step %d)", run.ID, run.Demo, run.Cursor)
}

// Name matches the schema's #Module vocabulary.
func (m *Mod) Name() string { return "reno" }

// Essential opts out of load shedding: demos run exactly when the
// machine is loaded.
func (m *Mod) Essential() {}

// HandleActEffects is the effectful transition seam: one tap moves the
// cursor in-process and hands the entered step's machine actions to the
// bus effect executor. Safe concurrent with Poll (everything under mu).
func (m *Mod) HandleActEffects(argv []string) (bool, []module.Effect) {
	if len(argv) == 0 {
		return false, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch argv[0] {
	case actNext:
		if len(argv) != 1 {
			return false, nil
		}
		return true, m.transition(m.cursor()+1, true)
	case actPrev:
		if len(argv) != 1 {
			return false, nil
		}
		return true, m.transition(m.cursor()-1, true)
	case actGoto:
		if len(argv) != 2 || m.staged == nil {
			return false, nil
		}
		idx := m.staged.StepIndex(argv[1])
		if idx < 0 {
			return true, nil // stale menu against a reloaded script: ignore loudly enough via no-op
		}
		return true, m.transition(idx, true)
	case actGotoWindow:
		if len(argv) != 2 {
			return false, nil
		}
		d, err := strconv.Atoi(argv[1])
		if err != nil {
			return false, nil
		}
		m.gotoOff += d * gotoWindow
		return true, nil
	case actStop:
		if len(argv) != 1 {
			return false, nil
		}
		return true, m.stopLocked()
	case actRecord:
		if len(argv) != 1 {
			return false, nil
		}
		return true, m.handleRecordActLocked()
	}
	return false, nil
}

func (m *Mod) cursor() int {
	if m.run == nil {
		return renocore.CursorArmed
	}
	return m.run.Cursor
}

// transition moves the cursor to step `to` and returns the entered
// step's effects. viaGlass applies the retap gap: glass taps can double
// -fire (lagging panel, multiple docks resolving one gesture), while a
// ctl-driven transition is one deliberate invocation and must never be
// silently swallowed with an ok. Caller holds mu.
func (m *Mod) transition(to int, viaGlass bool) []module.Effect {
	if m.run == nil || !m.run.Live() || m.staged == nil || m.stale != "" {
		return nil
	}
	if to < 0 || to >= len(m.staged.Steps) {
		return nil // walking off either end is a no-op, not a crash on stage
	}
	now := m.now()
	if viaGlass && !m.lastAct.IsZero() && now.Sub(m.lastAct) < transitionGap {
		return nil // lagging-panel retap; the first tap already landed
	}
	m.lastAct = now

	if m.run.ShowStartedAt.IsZero() {
		m.run.ShowStartedAt = now
	}
	if cur := m.run.Cursor; cur >= 0 && cur < len(m.run.Steps) {
		sa := &m.run.Steps[cur]
		if !sa.EnteredAt.IsZero() {
			sa.Total += now.Sub(sa.EnteredAt)
			sa.EnteredAt = time.Time{}
		}
	}
	m.run.Cursor = to
	m.gotoOff = 0
	if to < len(m.run.Steps) {
		sa := &m.run.Steps[to]
		sa.Visits++
		sa.EnteredAt = now
	}
	m.journal()

	step := m.staged.Steps[to]
	visit := 0
	if to < len(m.run.Steps) {
		visit = m.run.Steps[to].Visits
	}
	effects := make([]module.Effect, 0, len(step.Commands)+1)
	rec := m.run.Recording
	if m.staged.Record && (rec == nil || (rec.Failed != "" && rec.StartedAt.IsZero())) {
		// recording rides the show: intent journals write-ahead here and
		// the record verb starts OBS before this step's actions run
		// (ordered executor: the chain is sequential). A failed start
		// re-arms so a transient OBS error does not kill the whole run's
		// recording.
		if _, err := m.armRecordingLocked(""); err == nil {
			effects = append(effects, m.recordEffect())
		} else {
			m.journalErr = err.Error()
			log.Printf("khudson reno: scripted record arm: %v", err)
		}
	}
	for i, c := range step.Commands {
		effects = append(effects, module.Effect{
			Key:  fmt.Sprintf("%s/%d/%d/%d", m.run.ID, to, visit, i),
			Argv: m.commandArgv(c, to, i),
		})
	}
	return effects
}

// commandArgv maps one registered script command onto its `khudson reno`
// verb argv with correlation flags. EVERY head is os.Executable(): the
// bus runs the fixed TCC-granted install while launchd PATH's first
// entry is the live profile symlink, which flips mid-switch BEFORE the
// bus restarts -- bare `khudson` would splice a new-binary verb into an
// old-binary run (the re-review's two-binary skew finding).
func (m *Mod) commandArgv(c renocore.Command, step, act int) []string {
	argv := []string{m.exe, "reno", c.Name}
	corr := []string{"--run", m.run.ID, "--step", strconv.Itoa(step), "--act", strconv.Itoa(act)}
	switch c.Name {
	case "scene":
		return append([]string{m.exe, "reno", "obs", "scene", c.Args[0]}, corr...)
	case "record":
		return append([]string{m.exe, "reno", "obs", c.Args[0]}, corr...)
	case "launch-session":
		// correlation flags must precede the -- tail
		argv = append(argv, c.Args[0])
		argv = append(argv, corr...)
		argv = append(argv, c.Args[1:]...)
	default:
		argv = append(argv, c.Args...)
		argv = append(argv, corr...)
	}
	return argv
}

// journal persists the run; failures surface on glass and in the log but
// never stop the show. Caller holds mu.
func (m *Mod) journal() {
	if m.run == nil {
		return
	}
	if err := renocore.WriteRun(m.runsDir, m.run); err != nil {
		m.journalErr = err.Error()
		log.Printf("khudson reno: journal: %v", err)
		return
	}
	m.journalErr = ""
}

// stopLocked finalizes the live run and returns the effect-stream
// boundary the bus executor needs: a stopped run's queued step actions
// must not execute into the next run.
func (m *Mod) stopLocked() []module.Effect {
	if m.run == nil || !m.run.Live() {
		return nil
	}
	now := m.now()
	if cur := m.run.Cursor; cur >= 0 && cur < len(m.run.Steps) {
		sa := &m.run.Steps[cur]
		if !sa.EnteredAt.IsZero() {
			sa.Total += now.Sub(sa.EnteredAt)
			sa.EnteredAt = time.Time{}
		}
	}
	m.run.StoppedAt = now
	m.stale = ""
	m.journal()
	effects := []module.Effect{module.Boundary()}
	if m.run.Recording.Active() {
		// the stop verb runs after the boundary cut and notes the output
		// path back into this (finalized) run's journal
		effects = append(effects, m.obsStopEffect())
	}
	return effects
}

// HandleCtl is the run-lifecycle control seam behind `khudson reno
// load|start|stop|status|next|prev|goto`: the bus's reno ctl verb calls
// it and execs returned effects through the same executor as tapped
// acts -- one dispatch site.
func (m *Mod) HandleCtl(args []string) (any, []module.Effect, error) {
	if len(args) == 0 {
		return nil, nil, errors.New("reno: need a subverb: load|start|stop|status|next|prev|goto")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch args[0] {
	case "load":
		effects, err := m.loadLocked(args[1:])
		return nil, effects, err
	case "start":
		effects, err := m.startLocked()
		return nil, effects, err
	case "stop":
		if m.run == nil || !m.run.Live() {
			return nil, nil, errors.New("reno stop: no live run")
		}
		return nil, m.stopLocked(), nil
	case "status":
		return m.statusLocked(), nil, nil
	case "record-arm":
		runID := ""
		if len(args) > 1 {
			runID = args[1]
		}
		arm, err := m.armRecordingLocked(runID)
		return arm, nil, err
	case "record-note":
		if len(args) < 2 {
			return nil, nil, errors.New("reno record-note: need a run id")
		}
		return nil, nil, m.noteRecordingLocked(args[1], args[2:])
	case "next":
		return nil, m.transition(m.cursor()+1, false), nil
	case "prev":
		return nil, m.transition(m.cursor()-1, false), nil
	case "goto":
		if len(args) != 2 || m.staged == nil {
			return nil, nil, errors.New("reno goto: need a step id of the loaded script")
		}
		idx := m.staged.StepIndex(args[1])
		if idx < 0 {
			return nil, nil, fmt.Errorf("reno goto: no step %q", args[1])
		}
		return nil, m.transition(idx, false), nil
	}
	return nil, nil, fmt.Errorf("reno: unknown subverb %q", args[0])
}

func (m *Mod) loadLocked(args []string) ([]module.Effect, error) {
	replace := false
	path := ""
	for _, a := range args {
		if a == "--replace" {
			replace = true
			continue
		}
		if path != "" {
			return nil, errors.New("reno load: one script path")
		}
		path = a
	}
	if path == "" {
		return nil, errors.New("reno load: need a script path")
	}
	// vet BEFORE touching the live run: a --replace with a broken script
	// must leave the current run untouched instead of stopping it and
	// then failing to stage the replacement
	s, err := renocore.Load(path)
	if err != nil {
		return nil, err
	}
	var effects []module.Effect
	if m.run != nil && m.run.Live() {
		if !replace {
			return nil, fmt.Errorf("reno load: run %s is live; stop it or load --replace", m.run.ID)
		}
		effects = m.stopLocked()
	}
	m.staged = s
	m.stale = ""
	m.gotoOff = 0
	return effects, nil
}

func (m *Mod) startLocked() ([]module.Effect, error) {
	if m.staged == nil {
		return nil, errors.New("reno start: no script loaded")
	}
	if m.run != nil && m.run.Live() {
		return nil, fmt.Errorf("reno start: run %s already live; stop it first", m.run.ID)
	}
	now := m.now()
	m.run = &renocore.Run{
		Version:    1,
		ID:         renocore.NewRunID(now),
		Demo:       m.staged.Name,
		ScriptPath: m.staged.Path,
		ScriptHash: m.staged.Hash,
		StartedAt:  now,
		Cursor:     renocore.CursorArmed,
		Steps:      make([]renocore.StepActual, len(m.staged.Steps)),
	}
	m.stale = ""
	m.lastAct = time.Time{}
	m.journal()
	if m.journalErr != "" {
		// arming must be honest: a run that cannot journal is refused,
		// because recovery after a crash would silently lose it
		m.run = nil
		return nil, fmt.Errorf("reno start: journal: %s", m.journalErr)
	}
	// a fresh run opens with a boundary: anything still queued from a
	// previous run dies before this run's first step can fire
	return []module.Effect{module.Boundary()}, nil
}

// Status is the reno ctl status payload.
type Status struct {
	Script *ScriptStatus `json:"script,omitempty"`
	Run    *RunStatus    `json:"run,omitempty"`
	Stale  string        `json:"stale,omitempty"`
}

type ScriptStatus struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Steps int    `json:"steps"`
}

type RunStatus struct {
	ID      string `json:"id"`
	Demo    string `json:"demo"`
	Live    bool   `json:"live"`
	Cursor  int    `json:"cursor"`
	Step    string `json:"step,omitempty"`
	Elapsed string `json:"elapsed,omitempty"`
}

func (m *Mod) statusLocked() Status {
	st := Status{Stale: m.stale}
	if m.staged != nil {
		st.Script = &ScriptStatus{Name: m.staged.Name, Path: m.staged.Path, Hash: m.staged.Hash, Steps: len(m.staged.Steps)}
	}
	if m.run != nil {
		rs := &RunStatus{ID: m.run.ID, Demo: m.run.Demo, Live: m.run.Live(), Cursor: m.run.Cursor}
		if m.staged != nil && m.run.Cursor >= 0 && m.run.Cursor < len(m.staged.Steps) {
			rs.Step = m.staged.Steps[m.run.Cursor].ID
		}
		if !m.run.ShowStartedAt.IsZero() {
			end := m.now()
			if !m.run.Live() {
				end = m.run.StoppedAt
			}
			rs.Elapsed = fmtClock(end.Sub(m.run.ShowStartedAt))
		}
		st.Run = rs
	}
	return st
}

// Poll renders the view named by params ("presenter" | "recorder") from
// in-memory state -- no I/O, O(steps-window), honors nothing from ctx
// because there is nothing to wait on.
func (m *Mod) Poll(_ context.Context, params map[string]any) (module.Data, error) {
	view, _ := params["view"].(string)
	m.mu.Lock()
	defer m.mu.Unlock()
	if view == "recorder" {
		return m.recorderData(), nil
	}
	return m.presenterData(), nil
}

func fmtClock(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	d = d.Round(time.Second)
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	return fmt.Sprintf("%d:%02d", mins, secs)
}
