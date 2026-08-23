package reno

import (
	"testing"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

func jitterMod(t *testing.T, level int, webcam string) *Mod {
	t.Helper()
	m := &Mod{stateDir: t.TempDir(), exe: "/inst/khudson"}
	m.fxLevels = map[string]int{renocore.FXJitter: level}
	m.desired.Webcam = webcam
	return m
}

// The driver keeps firing while the level is up and retires the moment it
// is not. A jitter is a held state now, not a one-shot, so "stop" has to be
// a decision the driver reaches on its own -- nothing sends it a stop.
func TestJitterCheckFiresWhileTheLevelIsUp(t *testing.T) {
	m := jitterMod(t, 4, "br")
	level, action := m.jitterCheckLocked()
	if action != jitterFire || level != 4 {
		t.Errorf("level 4 with a cam = %d/%v, want 4/fire", level, action)
	}

	// zero is the off switch: the last chunk already ended on the base pose
	m.fxLevels[renocore.FXJitter] = 0
	if _, action := m.jitterCheckLocked(); action != jitterStop {
		t.Errorf("level 0 must stop the driver, got %v", action)
	}

	// a level past the top is state nobody should be able to write; refuse
	// to shake at it rather than clamping silently
	m.fxLevels[renocore.FXJitter] = renocore.FXMaxLevel + 1
	if _, action := m.jitterCheckLocked(); action != jitterStop {
		t.Errorf("an out-of-range level must stop the driver, got %v", action)
	}
}

// Nothing to shake, nothing to fire. Without this the driver would spend a
// failing chunk against a missing cam item every few seconds, forever.
func TestJitterCheckStopsWithoutACam(t *testing.T) {
	for _, webcam := range []string{"", "off"} {
		m := jitterMod(t, 5, webcam)
		if _, action := m.jitterCheckLocked(); action != jitterStop {
			t.Errorf("webcam %q must stop the driver, got %v", webcam, action)
		}
	}
}

// One driver, however many taps. Walking the level up five times must not
// leave five goroutines re-firing the same shake at each other.
func TestTouchJitterStartsOneDriver(t *testing.T) {
	m := jitterMod(t, 0, "br")
	// level 0: nothing to drive, so no goroutine is spawned
	m.touchJitterLocked()
	if m.jitterWatch {
		t.Error("a zero level must not start a driver")
	}
	// claim the flag by hand rather than racing a real goroutine: the point
	// under test is that touch is a no-op once one is running
	m.fxLevels[renocore.FXJitter] = 3
	m.jitterWatch = true
	m.touchJitterLocked()
	if !m.jitterWatch {
		t.Error("touch must leave a running driver alone")
	}
}

// An in-flight chunk notes the level it was fired at. That note must not be
// adopted back over a tap that just turned the shake off, or the effect
// would switch itself on again a beat after being dismissed.
func TestJitterChunkNoteDoesNotRevivePriorLevel(t *testing.T) {
	m := jitterMod(t, 0, "br")
	m.jitterWatch = true // a chunk fired at the old level is still in flight
	m.adoptFXLevelLocked("jitter 7")
	if lvl := m.fxLevels[renocore.FXJitter]; lvl != 0 {
		t.Errorf("a chunk echo revived the shake: level = %d, want 0", lvl)
	}

	// with no driver running the same note IS news -- a hand-run
	// `khudson reno obs cam jitter 7` -- and it takes effect
	m.jitterWatch = false
	m.adoptFXLevelLocked("jitter 7")
	if lvl := m.fxLevels[renocore.FXJitter]; lvl != 7 {
		t.Errorf("a hand-run jitter must be adopted: level = %d, want 7", lvl)
	}
}
