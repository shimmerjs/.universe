package reno

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// actRecord is the recorder view's one-tap control: with nothing live it
// arms an ad hoc run and fires the record verb; while recording it fires
// the stop verb. The ad hoc path is the register's "press record, drive
// the screen manually" case -- no script anywhere.
const actRecord = "reno:record"

// RecordOpen is the record-arm ctl payload the record verb consumes.
type RecordOpen struct {
	Run       string                 `json:"run"`
	Directory string                 `json:"directory"`
	Scene     string                 `json:"scene,omitempty"`
	Config    *renocore.RecordConfig `json:"config,omitempty"`
}

// wantRun rejects a verb note aimed at a different run than the module
// holds: a stale effect from a previous run must never arm or annotate
// the current one. Empty means unscoped (hand-run verbs).
func (m *Mod) wantRun(runID string) error {
	if runID == "" || (m.run != nil && m.run.ID == runID) {
		return nil
	}
	return fmt.Errorf("reno: run %s is not current", runID)
}

// openRecordingLocked journals recording intent WRITE-AHEAD of any OBS
// call, creating an ad hoc run when nothing is live. Idempotent while
// the intent is unproven (the glass act arms in-process and the exec'd
// verb re-arms over ctl -- both must land on one intent). Caller holds
// mu.
func (m *Mod) openRecordingLocked(runID string) (RecordOpen, error) {
	if m.runsDir == "" {
		return RecordOpen{}, errors.New("reno record: no state root")
	}
	if err := m.wantRun(runID); err != nil {
		return RecordOpen{}, err
	}
	prevRun, prevRec := m.run, (*renocore.RecordingState)(nil)
	if m.run != nil {
		prevRec = m.run.Recording
	}
	if m.run == nil || !m.run.Live() {
		if runID != "" {
			return RecordOpen{}, fmt.Errorf("reno record: run %s already finished", runID)
		}
		now := m.now()
		m.run = &renocore.Run{
			Version:   1,
			ID:        renocore.NewRunID(now),
			Demo:      renocore.AdhocName,
			StartedAt: now,
			Cursor:    renocore.CursorArmed,
		}
		m.stale = ""
	}
	rec := m.run.Recording
	switch {
	case rec == nil:
		rec = &renocore.RecordingState{IntentAt: m.now(), Directory: m.workspace()}
		if m.staged != nil && m.run.Demo == m.staged.Name {
			rec.Scene = m.staged.Scene
		}
		// the one-core config: the script's for a scripted run, the
		// pending ad hoc knobs otherwise -- journaled write-ahead with
		// the rest of the intent
		rec.Config = m.openConfigLocked()
		m.run.Recording = rec
	case rec.Active():
		return RecordOpen{}, fmt.Errorf("reno record: run %s is already recording", m.run.ID)
	case rec.Failed != "" && rec.StartedAt.IsZero():
		// a failed start is re-armable: swap in a FRESH intent (never
		// mutate in place -- a journal failure below must be able to
		// restore the failed state, arc-2 review #9) and re-snapshot the
		// config so a fixed pending config actually applies on the retry
		// (#8: the stale broken config was unrecoverable)
		fresh := *rec
		fresh.Failed = ""
		fresh.IntentAt = m.now()
		fresh.Config = m.openConfigLocked()
		rec = &fresh
		m.run.Recording = rec
	case !rec.StoppedAt.IsZero():
		return RecordOpen{}, fmt.Errorf("reno record: run %s already recorded (one clip per run; stop and re-record ad hoc)", m.run.ID)
	}
	m.journal()
	if m.journalErr != "" {
		// intent must land before OBS ever starts: a recording the
		// journal cannot own is the crash-recovery hole. Roll the
		// in-memory state back to what the journal still holds -- a
		// phantom ad hoc run or an unjournaled intent left behind would
		// let later taps act on state recovery cannot see (#9, #10).
		m.run = prevRun
		if m.run != nil {
			m.run.Recording = prevRec
		}
		return RecordOpen{}, fmt.Errorf("reno record: journal: %s", m.journalErr)
	}
	return RecordOpen{Run: m.run.ID, Directory: rec.Directory, Scene: rec.Scene, Config: rec.Config}, nil
}

// noteRecordingLocked applies a record verb's outcome note to the run it
// names. The current run takes it in memory (even after finalizing --
// the stop note races the run stop); a note for an OLDER run reconciles
// straight into that run's journal file, so a new take can never absorb
// the previous take's output path. Caller holds mu.
func (m *Mod) noteRecordingLocked(runID string, args []string) error {
	if len(args) == 0 {
		return errors.New("reno record-note: need started|stopped|failed")
	}
	if m.run != nil && (runID == "" || m.run.ID == runID) {
		if m.run.Recording == nil {
			return errors.New("reno record-note: no armed recording")
		}
		if runID == "" && !m.run.Recording.StoppedAt.IsZero() {
			// an unscoped note binds to whichever run is current AT
			// ARRIVAL; over a closed recording that can write run A's
			// clip into run B (arc-2 review #14) -- demand the run id
			return fmt.Errorf("reno record-note: run %s's recording is closed; name the run the note belongs to", m.run.ID)
		}
		if err := applyRecordNote(m.run.Recording, args, m.now()); err != nil {
			return err
		}
		m.journal()
		if m.journalErr != "" {
			return fmt.Errorf("reno record-note: journal: %s", m.journalErr)
		}
		return nil
	}
	if runID == "" {
		return errors.New("reno record-note: no run")
	}
	// reconcile path: the named run is no longer current
	path := filepath.Join(m.runsDir, runID+".json")
	run, err := renocore.ReadRun(path)
	if err != nil {
		return fmt.Errorf("reno record-note: run %s: %w", runID, err)
	}
	if run.Recording == nil {
		return fmt.Errorf("reno record-note: run %s never armed a recording", runID)
	}
	if err := applyRecordNote(run.Recording, args, m.now()); err != nil {
		return err
	}
	return renocore.WriteRun(m.runsDir, run)
}

func applyRecordNote(rec *renocore.RecordingState, args []string, now time.Time) error {
	switch args[0] {
	case "started":
		rec.StartedAt = now
		if len(args) > 1 {
			rec.ObsVersion = args[1]
		}
		rec.Failed = ""
	case "stopped":
		rec.StoppedAt = now
		if len(args) > 1 {
			rec.Output = args[1]
		}
	case "failed":
		rec.Failed = strings.Join(args[1:], " ")
	default:
		return fmt.Errorf("reno record-note: unknown note %q", args[0])
	}
	return nil
}

// recordEffect is the exec'd record verb; obsEffect the transport ops.
// Every head is the exe identity (PATH flips mid-switch). The key is
// per-ARM (IntentAt-stamped), not per-run: a re-arm after a failed
// start is a fresh attempt the 2s debounce must never eat (arc-2
// review #11); true tap double-fires still collapse because both taps
// resolve one arm.
func (m *Mod) recordEffect() module.Effect {
	key := m.run.ID + "/record"
	if rec := m.run.Recording; rec != nil {
		key = fmt.Sprintf("%s/record/%d", m.run.ID, rec.IntentAt.UnixNano())
	}
	return module.Effect{
		Key:  key,
		Argv: []string{m.exe, "reno", "record", "--run", m.run.ID},
	}
}

// obsStopEffect is Final (survives run boundaries -- it IS the cleanup
// a boundary must not cancel) and uniquely keyed per emission so a
// retry after a failed stop is never debounce-eaten.
func (m *Mod) obsStopEffect() module.Effect {
	return module.Effect{
		Key:   fmt.Sprintf("%s/record-stop/%d", m.run.ID, m.now().UnixNano()),
		Argv:  []string{m.exe, "reno", "obs", "stop", "--run", m.run.ID},
		Final: true,
	}
}

// handleRecordActLocked is the actRecord transition: arm+record, or stop
// a live recording. Caller holds mu.
func (m *Mod) handleRecordActLocked() []module.Effect {
	if m.run != nil && m.run.Recording.Active() {
		return []module.Effect{m.obsStopEffect()}
	}
	if _, err := m.openRecordingLocked(""); err != nil {
		// surface on glass through the status row; no effect fires
		m.journalErr = err.Error()
		return nil
	}
	return []module.Effect{m.recordEffect()}
}

// workspace is where the live run's clips land: OBS's record directory
// is pointed here at record start (SetRecordDirectory, capability-
// probed). Caller holds mu.
func (m *Mod) workspace() string {
	if m.runsDir == "" || m.run == nil {
		return "(no state root)"
	}
	root := filepath.Dir(m.runsDir) // <state>/reno
	return filepath.Join(root, "workspace", m.run.Demo, m.run.ID)
}
