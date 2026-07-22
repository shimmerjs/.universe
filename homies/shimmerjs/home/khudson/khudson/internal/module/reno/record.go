package reno

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// actRecord is the recorder view's one-tap control: with nothing live it
// arms an ad hoc run and fires the record verb; while recording it fires
// the stop verb. The ad hoc path is the register's "press record, drive
// the screen manually" case -- no script anywhere.
const actRecord = "reno:record"

// ArmResult is the record-arm ctl payload the record verb consumes.
type ArmResult struct {
	Run       string `json:"run"`
	Directory string `json:"directory"`
	Scene     string `json:"scene,omitempty"`
}

// armRecordingLocked journals recording intent WRITE-AHEAD of any OBS
// call, creating an ad hoc run when nothing is live. Idempotent while
// the intent is unproven (the glass act arms in-process and the exec'd
// verb re-arms over ctl -- both must land on one intent). Caller holds
// mu.
func (m *Mod) armRecordingLocked() (ArmResult, error) {
	if m.runsDir == "" {
		return ArmResult{}, errors.New("reno record: no state root")
	}
	if m.run == nil || !m.run.Live() {
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
		m.run.Recording = rec
	case rec.Active():
		return ArmResult{}, fmt.Errorf("reno record: run %s is already recording", m.run.ID)
	case !rec.StoppedAt.IsZero():
		return ArmResult{}, fmt.Errorf("reno record: run %s already recorded (one clip per run; stop and re-record ad hoc)", m.run.ID)
	}
	m.journal()
	if m.journalErr != "" {
		// intent must land before OBS ever starts: a recording the
		// journal cannot own is the crash-recovery hole
		return ArmResult{}, fmt.Errorf("reno record: journal: %s", m.journalErr)
	}
	return ArmResult{Run: m.run.ID, Directory: rec.Directory, Scene: rec.Scene}, nil
}

// noteRecordingLocked applies a record verb's outcome note. Accepted for
// the current run even after it finalized (the stop note races the run
// stop). Caller holds mu.
func (m *Mod) noteRecordingLocked(args []string) error {
	if len(args) == 0 {
		return errors.New("reno record-note: need started|stopped|failed")
	}
	if m.run == nil || m.run.Recording == nil {
		return errors.New("reno record-note: no armed recording")
	}
	rec := m.run.Recording
	switch args[0] {
	case "started":
		rec.StartedAt = m.now()
		if len(args) > 1 {
			rec.ObsVersion = args[1]
		}
		rec.Failed = ""
	case "stopped":
		rec.StoppedAt = m.now()
		if len(args) > 1 {
			rec.Output = args[1]
		}
	case "failed":
		rec.Failed = strings.Join(args[1:], " ")
	default:
		return fmt.Errorf("reno record-note: unknown note %q", args[0])
	}
	m.journal()
	if m.journalErr != "" {
		return fmt.Errorf("reno record-note: journal: %s", m.journalErr)
	}
	return nil
}

// recordEffect is the exec'd record verb; obsEffect the transport ops.
// Every head is the exe identity (PATH flips mid-switch).
func (m *Mod) recordEffect() module.Effect {
	return module.Effect{
		Key:  m.run.ID + "/record",
		Argv: []string{m.exe, "reno", "record", "--run", m.run.ID},
	}
}

func (m *Mod) obsStopEffect() module.Effect {
	return module.Effect{
		Key:  m.run.ID + "/record-stop",
		Argv: []string{m.exe, "reno", "obs", "stop", "--run", m.run.ID},
	}
}

// handleRecordActLocked is the actRecord transition: arm+record, or stop
// a live recording. Caller holds mu.
func (m *Mod) handleRecordActLocked() []module.Effect {
	if m.run != nil && m.run.Recording.Active() {
		return []module.Effect{m.obsStopEffect()}
	}
	if _, err := m.armRecordingLocked(); err != nil {
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
