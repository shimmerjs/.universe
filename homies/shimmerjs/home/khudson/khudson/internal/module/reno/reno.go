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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
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

	// desired is the ad hoc recording posture: mutated by the
	// record-config ctl and the recorder view's menu acts, driven into
	// OBS on every change, and snapshotted into the run journal at arm.
	// It is DESIRED state, not a queued submission -- OBS holds the
	// actual state and applyEffectsLocked reconciles the two, so the
	// arm-time apply is a convergence check rather than first contact.
	// Scripted runs take their script's config instead: a leftover ad
	// hoc knob must never hijack a show.
	desired renocore.RecordConfig
	// inventory is the last obs probe's device enumeration (button
	// source); the module never dials OBS itself.
	inventory *renocore.ObsInventory
	// local is the machine's display set as of the last recorder poll,
	// enumerated OUTSIDE mu. It is what tells the pad an inventory has
	// gone wrong by identity rather than by age.
	local []cgdisplay.Display
	// notes is the ad hoc speaker-notes body (notes without a script),
	// with the path it was read from for the status row.
	notes     string
	notesPath string

	// live-apply latch: a config change drives the reno scene immediately
	// so the pad is a mixer, not a form you submit. applyLive marks an
	// apply in flight, applyDirty a change that landed while it ran, and
	// applyGen keys each emission uniquely (the probe precedent -- a retry
	// after a failure must never be debounce-eaten). applyNote is the
	// last outcome for the pad readout.
	applyLive  bool
	applyAt    time.Time
	applyDirty bool
	applyGen   uint64
	applyNote  string
	applyFail  bool

	// fx one-shots keep their own generation and readout: they never
	// touch the apply latch, so a tapped effect cannot strand a queued
	// config change.
	fxGen  uint64
	fxNote string
	fxFail bool
	// fxLevels holds each level effect's magnitude, keyed by FX kind. It
	// is the only record of it anywhere: the filters these drive report
	// their settings but never which step of khudson's scale produced
	// them, so this map IS the level, which is what earns the tile the
	// right to show one.
	fxLevels map[string]int
	// fxParked holds the levels set aside while the camera is away. An
	// effect with nothing to run on is off -- the filter is switched off and
	// the tiles read off, because there is no tint on a camera that is not
	// there -- and this is what puts them back when the camera returns.
	// Deliberately not persisted: a bus that restarted while the camera was
	// off comes back with the effects off, which is what the pad says.
	fxParked map[string]int

	// standby is phase one of recording: OBS up, scene matching. It gets
	// its own latch and readout because the whole point of splitting the
	// phases is that this one is separately runnable and separately
	// reportable -- welded to record, its cost was invisible.
	armLive bool
	armAt   time.Time
	armGen  uint64
	armNote string
	armFail bool
	// armDeadline is when an untouched arm gets released out from under
	// itself; armWatch says a watchdog goroutine is already counting down.
	armDeadline time.Time
	armWatch    bool
	// sustain holds the running driver for each sustained effect, keyed by
	// FX kind (internal/module/reno/sustain.go). A key present means a
	// child is out there driving that effect right now; the level it drives
	// at lives in fxLevels like every other level FX.
	sustain map[string]*sustainDriver

	// winState is OBS's own window: "" unknown, "shown", "hidden". Unlike
	// an OBS filter this reads back (kAXHidden), so the tile is allowed to
	// show its state instead of being a button that lies.
	winState string
	winLive  bool
	winAt    time.Time
	winGen   uint64
	winFail  bool

	// vcamState is OBS's virtual camera output: "" unknown, "on", "off".
	// Nothing here ever starts it -- macOS does, the moment any app opens
	// the OBS Virtual Camera device -- so it is the one thing that can hold
	// the camera with no other tile accounting for it. GetVirtualCamStatus
	// reads back, so like winState it is allowed on glass.
	vcamState string
	vcamLive  bool
	vcamAt    time.Time
	vcamGen   uint64
	vcamFail  bool

	// recPhase names what the record verb is doing between the tap and the
	// first frame. Pure readout, never journal state: the run's recording
	// truth stays with the record notes.
	recPhase string

	stateDir string // <state>/reno; "" on bare test buses
	runsDir  string // "" on bare test buses: journal writes error loudly
	exe      string // os.Executable for TCC-identity argvs (focus-app)
	now      func() time.Time
	lastAct  time.Time
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
		m.stateDir = filepath.Join(p.Dir, "reno")
		m.runsDir = filepath.Join(m.stateDir, "runs")
	}
	m.recover()
	m.recoverInventory()
	m.recoverConfig()
	m.recoverFXLevels()
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
	return m.handleCfgActLocked(argv)
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
	if m.run.Demo != m.staged.Name {
		// an AD HOC run with a script merely staged: walking the staged
		// steps would fire its machine actions under a run that never
		// vetted them and keeps no step accounting (arc-2 review #1)
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
		if _, err := m.openRecordingLocked(""); err == nil {
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
	case "cam":
		return append(append([]string{m.exe, "reno", "obs", "cam"}, c.Args...), corr...)
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
	if !m.finishRunLocked() {
		return nil
	}
	effects := []module.Effect{module.Boundary()}
	// ending the session puts the rig down. Stopping a take deliberately
	// does not -- the devices stay held so the next take is instant -- so
	// this is the only path that turns the camera light back off.
	if m.armed() {
		m.armLive, m.armNote, m.armFail = false, "", false
		effects = append(effects, module.Effect{
			Key:  fmt.Sprintf("%s/release/%d", m.run.ID, m.now().UnixNano()),
			Argv: []string{m.exe, "reno", "obs", "release"},
		})
	}
	if m.stopWorthyLocked() {
		effects = append(effects, m.obsStopEffect())
	}
	return effects
}

// finishRunLocked closes the live run's books -- the open step's clock,
// the stop stamp, the journal write -- and reports whether there was one
// to close. Caller holds mu.
func (m *Mod) finishRunLocked() bool {
	if m.run == nil || !m.run.Live() {
		return false
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
	return true
}

// stopWorthyLocked reports a take OBS may still be rolling: any recording
// intent not yet stopped and not known-failed-before-start. An UNPROVEN
// start (intent journaled, started note lost or the verb still in flight)
// counts, or OBS records with no owner (arc-2 review #7/#12). Caller holds
// mu.
func (m *Mod) stopWorthyLocked() bool {
	if m.run == nil {
		return false
	}
	rec := m.run.Recording
	return rec != nil && rec.StoppedAt.IsZero() && (rec.Failed == "" || !rec.StartedAt.IsZero())
}

// QuitRequest is the quit-begin ctl reply: what the quit verb still has to
// put down once the module has closed the run. Run names the run a
// record-stop note belongs to ("" when none was live), and Recording marks
// a take OBS may still be rolling.
type QuitRequest struct {
	Run       string `json:"run,omitempty"`
	Recording bool   `json:"recording,omitempty"`
}

// quitLocked closes the run for a quit and hands the verb the cleanup.
// Unlike stopLocked it emits NO release and NO record-stop effect: the
// quit verb performs both itself over the one connection it is about to
// close, and a second pair of children racing it would arrive at an OBS
// that had already gone. The boundary still fires -- queued step actions
// must not execute into an OBS that is quitting. Caller holds mu.
func (m *Mod) quitLocked() (QuitRequest, []module.Effect) {
	req := QuitRequest{Recording: m.stopWorthyLocked()}
	if m.run != nil {
		req.Run = m.run.ID
	}
	var effects []module.Effect
	if m.finishRunLocked() {
		effects = []module.Effect{module.Boundary()}
	}
	// the rig is going down whether or not a run was live: this is the
	// path that exists for the hand-driven session with no script at all
	m.armLive, m.armNote, m.armFail = false, "", false
	return req, effects
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
	// Any verb is someone using the rig, so it pushes the idle deadline out;
	// registered after the unlock defer so it runs first, and placed here so
	// the arm-note that arms the rig is the pass that starts the watchdog.
	defer m.touchArmLocked()
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
	case "quit-begin":
		req, effects := m.quitLocked()
		return req, effects, nil
	case "status":
		return m.statusLocked(), nil, nil
	case "record-open":
		runID := ""
		if len(args) > 1 {
			runID = args[1]
		}
		arm, err := m.openRecordingLocked(runID)
		return arm, nil, err
	case "record-note":
		if len(args) < 2 {
			return nil, nil, errors.New("reno record-note: need a run id")
		}
		return nil, nil, m.noteRecordingLocked(args[1], args[2:])
	case "record-config":
		// the CLI seam applies live exactly like a pad tap: one core, so
		// `khudson reno config` and the tiles cannot drift
		if err := m.setRecordConfigLocked(args[1:]); err != nil {
			return nil, nil, err
		}
		return nil, m.applyEffectsLocked(), nil
	case "apply-begin":
		return ApplyRequest{Config: m.beginApplyLocked()}, nil, nil
	case "apply-note":
		effects, err := m.noteApplyLocked(args[1:])
		return nil, effects, err
	case "fx-note":
		return nil, nil, m.noteFXLocked(args[1:])
	case "arm-note":
		effects, err := m.noteArmLocked(args[1:])
		return nil, effects, err
	case "window-note":
		return nil, nil, m.noteWindowLocked(args[1:])
	case "vcam-note":
		return nil, nil, m.noteVCamLocked(args[1:])
	case "record-phase":
		// pure readout: what the record verb is doing between the tap and
		// the first frame. Never journal state -- the record notes own that
		m.recPhase = strings.Join(args[1:], " ")
		return nil, nil, nil
	case "obs-inventory":
		if len(args) != 2 {
			return nil, nil, errors.New("reno obs-inventory: need one JSON payload")
		}
		var inv renocore.ObsInventory
		if err := json.Unmarshal([]byte(args[1]), &inv); err != nil {
			return nil, nil, fmt.Errorf("reno obs-inventory: %w", err)
		}
		m.inventory = &inv
		m.saveInventoryLocked()
		return nil, nil, nil
	case "notes":
		return nil, nil, m.setNotesLocked(args[1:])
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
	// Config is the pending ad hoc recording config's summary.
	Config string `json:"config,omitempty"`
	// Notes is the loaded scriptless notes path.
	Notes string `json:"notes,omitempty"`
	// Inventory summarizes the last obs probe.
	Inventory string `json:"inventory,omitempty"`
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
	st := Status{Stale: m.stale, Notes: m.notesPath}
	if !m.desired.Empty() {
		st.Config = m.desired.Summary()
	}
	if inv := m.inventory; inv != nil {
		st.Inventory = fmt.Sprintf("obs %s: %d displays, %d mics, %d cams (%s old)",
			inv.ObsVersion, len(inv.Screens), len(inv.Mics), len(inv.Cams),
			m.now().Sub(inv.At).Round(time.Second))
	}
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
	if view != "recorder" {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.presenterData(), nil
	}
	// enumerate BEFORE taking mu: a display-server call under the module
	// lock would put every ctl and act behind it, and only the recorder
	// pad has anything to say about displays
	local := localDisplays()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.local = local
	// a moved display is repaired here rather than reported: the fix needs
	// no OBS, so the pad has no business asking for a tap it could make
	// itself. Costs nothing on the ordinary tick -- it returns immediately
	// unless the enumeration actually disagrees.
	m.healDisplaysLocked()
	return m.recorderData(), nil
}

// fmtAge renders an age as ONE coarse unit. A pad value is read at arm's
// length off glass, so "2h" carries the whole meaning and "2h13m4.2s"
// costs a squint to say the same thing.
func fmtAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
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
