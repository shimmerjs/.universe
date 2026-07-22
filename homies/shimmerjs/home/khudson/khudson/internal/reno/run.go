package reno

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// runVersion stamps every journal file; foreign versions are treated as
// parse garbage on recovery (hookspool discipline).
const runVersion = 1

// sweepHorizon is how long finalized run journals linger before the
// boot-time sweep prunes them. Live (unfinalized) runs are never swept.
const sweepHorizon = 30 * 24 * time.Hour

// CursorArmed is the pre-show cursor: the run exists, timers are idle,
// and no step effects have fired. The first advance enters step 0 and
// starts the stopwatch.
const CursorArmed = -1

// Run is one demo run's journaled state, written on every transition
// (small, O(1)) so a bus crash mid-demo loses nothing that matters.
type Run struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	Demo       string    `json:"demo"`
	ScriptPath string    `json:"scriptPath,omitempty"` // "" = ad hoc
	ScriptHash string    `json:"scriptHash,omitempty"`
	StartedAt  time.Time `json:"startedAt"`
	// ShowStartedAt stamps the first advance out of armed: the elapsed
	// talk timer runs from here, not from arming the run.
	ShowStartedAt time.Time    `json:"showStartedAt,omitzero"`
	StoppedAt     time.Time    `json:"stoppedAt,omitzero"`
	Cursor        int          `json:"cursor"`
	Steps         []StepActual `json:"steps,omitempty"`
	// Recording is nil until intent lands (write-ahead of StartRecord).
	Recording *RecordingState `json:"recording,omitempty"`
}

// RecordingState journals a run's OBS recording lifecycle. IntentAt is
// WRITE-AHEAD: it lands before StartRecord ever fires, so a crash
// between the two recovers as "intended but unproven" instead of a
// recording nobody owns; StartedAt/ObsVersion arrive from the verb's
// post-start note, Output from the stop note -- the deterministic clip
// association. Failed carries the arm-or-start failure for reconcile.
type RecordingState struct {
	IntentAt   time.Time `json:"intentAt,omitzero"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	StoppedAt  time.Time `json:"stoppedAt,omitzero"`
	Directory  string    `json:"directory,omitempty"`
	Scene      string    `json:"scene,omitempty"`
	Output     string    `json:"output,omitempty"`
	ObsVersion string    `json:"obsVersion,omitempty"`
	Failed     string    `json:"failed,omitempty"`
}

// Active reports a recording believed live: started, not stopped.
func (r *RecordingState) Active() bool {
	return r != nil && !r.StartedAt.IsZero() && r.StoppedAt.IsZero()
}

// StepActual accumulates one step's real time across visits (prev/goto
// legally re-enter a step).
type StepActual struct {
	Visits int           `json:"visits,omitempty"`
	Total  time.Duration `json:"total,omitempty"`
	// EnteredAt is the live visit's entry stamp; zero when the step is
	// not current.
	EnteredAt time.Time `json:"enteredAt,omitzero"`
}

// Live reports whether the run is still open.
func (r *Run) Live() bool { return r.StoppedAt.IsZero() }

// NewRunID is a UTC compact stamp plus a short random suffix: sortable,
// filesystem-safe, collision-proof enough for one machine.
func NewRunID(now time.Time) string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		// timestamps alone still key one run per second; degrade quietly
		return now.UTC().Format("20060102-150405")
	}
	return now.UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// WriteRun journals r atomically (tmp+rename, pid-suffixed tmp, 0600).
// Callers surface errors loudly: a demo whose journal cannot land must
// say so, not pretend (deliberate delta from claudeverb's silent logf).
func WriteRun(dir string, r *Run) error {
	if dir == "" {
		return errors.New("reno run journal: no state dir")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.tmp", r.ID, os.Getpid()))
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, r.ID+".json"))
}

// ReadRun loads one journal file; foreign versions error.
func ReadRun(path string) (*Run, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Run
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	if r.Version != runVersion {
		return nil, fmt.Errorf("run journal version %d (want %d)", r.Version, runVersion)
	}
	return &r, nil
}

// RecoverLatest returns the newest live run in dir, or nil when none. A
// missing dir is a fresh install, not an error; unreadable files are
// skipped (returned in errs for loud logging, never fatal -- a corrupt
// journal must not brick the bus).
func RecoverLatest(dir string) (run *Run, errs []error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []error{err}
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || !e.Type().IsRegular() {
			continue
		}
		r, err := ReadRun(filepath.Join(dir, e.Name()))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if !r.Live() {
			continue
		}
		if run == nil || r.StartedAt.After(run.StartedAt) {
			run = r
		}
	}
	return run, errs
}

// SweepRuns prunes finalized journals older than the horizon. One-shot
// boot work (constant-cost invariant: never per-tick).
func SweepRuns(dir string, now time.Time) {
	if dir == "" {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		r, err := ReadRun(path)
		if err != nil {
			// parse garbage ages out by mtime like hookspool's foreign versions
			if info, statErr := os.Stat(path); statErr == nil && now.Sub(info.ModTime()) > sweepHorizon {
				os.Remove(path)
			}
			continue
		}
		if !r.Live() && now.Sub(r.StoppedAt) > sweepHorizon {
			os.Remove(path)
		}
	}
}
