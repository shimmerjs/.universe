package reno

import (
	"os/exec"
	"reflect"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// sustainMod is a module whose drivers are a shell blocked on stdin. It
// exits when the pipe closes and not before, exactly as the real driver
// does, so the supervisor is tested against a real process without OBS.
func sustainMod(t *testing.T, webcam string) *Mod {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh to stand in for a driver")
	}
	old := sustainArgv
	sustainArgv = func(string, int) []string { return []string{"-c", "cat >/dev/null"} }
	t.Cleanup(func() { sustainArgv = old })

	m := &Mod{stateDir: t.TempDir(), exe: sh}
	m.fxLevels = map[string]int{}
	m.desired.Webcam = webcam
	t.Cleanup(func() { m.Close() })
	return m
}

// under runs a mutation the way the ctl and act paths do: everything named
// Locked is called with mu held, and the driver reaper counts on it.
func under(m *Mod, f func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f()
}

// driver reads a slot under the lock, since a reaper may be clearing one.
func driver(m *Mod, kind string) *sustainDriver {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sustain[kind]
}

// level reads one effect's magnitude under the lock.
func level(m *Mod, kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fxLevels[kind]
}

// One driver per effect, however many taps. Walking a level up five times
// must not leave five children driving the same camera at each other.
func TestSustainStartsOneDriverPerEffect(t *testing.T) {
	m := sustainMod(t, "br")
	under(m, func() {
		m.fxLevels[renocore.FXJitter] = 3
		m.touchSustainLocked(renocore.FXJitter)
	})
	first := driver(m, renocore.FXJitter)
	if first == nil {
		t.Fatal("a level above zero must start a driver")
	}
	// same level again: the running driver is already generating chunks at
	// it, so touching must leave it alone rather than churn a process
	under(m, func() { m.touchSustainLocked(renocore.FXJitter) })
	if driver(m, renocore.FXJitter) != first {
		t.Error("touch at an unchanged level restarted the driver")
	}
	// a different level IS a restart: the magnitude is baked into the
	// chunks the running one is already producing
	under(m, func() {
		m.fxLevels[renocore.FXJitter] = 7
		m.touchSustainLocked(renocore.FXJitter)
	})
	second := driver(m, renocore.FXJitter)
	if second == nil || second == first {
		t.Fatal("a level change must restart the driver")
	}
	if second.level != 7 {
		t.Errorf("driver level = %d, want 7", second.level)
	}
	waitGone(t, first)
}

// Zero is the off switch, and it has to reach the child: a level dropped on
// glass with a driver still shaking is the whole bug.
func TestSustainZeroStopsTheDriver(t *testing.T) {
	m := sustainMod(t, "br")
	under(m, func() {
		m.fxLevels[renocore.FXJitter] = 4
		m.touchSustainLocked(renocore.FXJitter)
	})
	d := driver(m, renocore.FXJitter)
	if d == nil {
		t.Fatal("no driver to stop")
	}
	under(m, func() {
		m.fxLevels[renocore.FXJitter] = 0
		m.touchSustainLocked(renocore.FXJitter)
	})
	if driver(m, renocore.FXJitter) != nil {
		t.Error("zero must clear the driver slot")
	}
	waitGone(t, d)
}

// Nothing to shake, nothing to drive. Without this the driver would fail
// against a missing cam item the moment it dialled, every time.
func TestSustainNeedsACamera(t *testing.T) {
	for _, webcam := range []string{"", "off"} {
		m := sustainMod(t, webcam)
		under(m, func() {
			m.fxLevels[renocore.FXJitter] = 5
			m.touchSustainLocked(renocore.FXJitter)
		})
		if driver(m, renocore.FXJitter) != nil {
			t.Errorf("webcam %q must not start a driver", webcam)
		}
	}
	// and turning the camera off while one runs takes it down with it
	m := sustainMod(t, "br")
	under(m, func() {
		m.fxLevels[renocore.FXJitter] = 5
		m.touchSustainLocked(renocore.FXJitter)
	})
	d := driver(m, renocore.FXJitter)
	if d == nil {
		t.Fatal("no driver to take down")
	}
	under(m, func() {
		m.desired.Webcam = "off"
		m.touchEverySustainLocked()
	})
	if driver(m, renocore.FXJitter) != nil {
		t.Error("the camera going away must stop the effects on it")
	}
	waitGone(t, d)
}

// Close is the bus's clean shutdown. A driver is a child started outside
// the effect executor, so this is the only thing that reaps one -- and a
// shake outliving the bus that started it is the arm-watchdog failure one
// layer down.
func TestCloseStopsEveryDriver(t *testing.T) {
	m := sustainMod(t, "br")
	under(m, func() {
		m.fxLevels[renocore.FXJitter] = 3
		m.fxLevels[renocore.FXLava] = 6
		m.touchEverySustainLocked()
	})
	jitter, lava := driver(m, renocore.FXJitter), driver(m, renocore.FXLava)
	if jitter == nil || lava == nil {
		t.Fatal("want one driver each for the shake and the lamp")
	}
	if err := m.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if driver(m, renocore.FXJitter) != nil || driver(m, renocore.FXLava) != nil {
		t.Error("close left drivers behind")
	}
	waitGone(t, jitter)
	waitGone(t, lava)
}

// A level change on a sustained effect fires NO one-shot. The driver owns
// the whole effect, home included, so an effect sent alongside it would be
// a second author of the same camera.
func TestSustainLevelBumpFiresNoOneShot(t *testing.T) {
	m := sustainMod(t, "br")
	under(m, func() {
		if fx, ok := m.bumpFXLevelLocked(renocore.FXJitter, true); ok {
			t.Errorf("a sustained bump emitted %+v, want nothing to fire", fx)
		}
		// a settled effect still fires its one-shot, so the two paths stay
		// distinguishable
		if _, ok := m.bumpFXLevelLocked(renocore.FXPurple, true); !ok {
			t.Error("purple is a setting, not a driver: it must still fire")
		}
	})
	if lvl := level(m, renocore.FXJitter); lvl != 1 {
		t.Errorf("the level still has to move: %d", lvl)
	}
	if driver(m, renocore.FXJitter) == nil {
		t.Error("the bump must start the driver instead")
	}
}

// A driver's own note is an echo of a level this module already owns.
// Adopting it loses the race against turning the effect off: the tap sets
// zero and stops the driver, the driver's note lands afterwards, and the
// level it carries would put the effect straight back up.
func TestDriverNoteDoesNotRevivePriorLevel(t *testing.T) {
	m := sustainMod(t, "br")
	under(m, func() {
		m.fxLevels[renocore.FXJitter] = 3
		m.touchSustainLocked(renocore.FXJitter)
		m.fxLevels[renocore.FXJitter] = 0
		m.adoptFXLevelLocked("jitter 7")
	})
	if lvl := level(m, renocore.FXJitter); lvl != 0 {
		t.Errorf("a driver echo revived the shake: level = %d, want 0", lvl)
	}

	// with no driver running the same note IS news -- a hand-run
	// `khudson reno obs cam jitter 7` -- and it takes effect
	m2 := sustainMod(t, "br")
	under(m2, func() { m2.adoptFXLevelLocked("jitter 7") })
	if lvl := level(m2, renocore.FXJitter); lvl != 7 {
		t.Errorf("a hand-run jitter must be adopted: level = %d, want 7", lvl)
	}
	if driver(m2, renocore.FXJitter) == nil {
		t.Error("a hand-run jitter must also be picked up and driven")
	}
}

// A note at the level already showing still revives a dead driver. The
// scripted step is exactly this case: it runs no span of its own on the
// strength of being driven from here, so a note that changed no number and
// started nothing would be a step that did nothing at all.
func TestNoteAtTheSameLevelRevivesADeadDriver(t *testing.T) {
	m := sustainMod(t, "br")
	m.fxLevels[renocore.FXJitter] = 8
	under(m, func() { m.adoptFXLevelLocked("jitter 8") })
	if driver(m, renocore.FXJitter) == nil {
		t.Error("a level with no driver must get one, level change or not")
	}
	if lvl := level(m, renocore.FXJitter); lvl != 8 {
		t.Errorf("level = %d, want 8", lvl)
	}
}

// A sustained level does NOT survive a bus restart, and for the opposite
// reason to the ones that do. A pixelate comes back because its filter is
// still on the camera; a shake does not, because the driver died with the
// bus and put the pose back on the way out.
func TestSustainLevelsDoNotRecover(t *testing.T) {
	m := sustainMod(t, "br")
	m.fxLevels = map[string]int{
		renocore.FXJitter:   5,
		renocore.FXLava:     8,
		renocore.FXPixelate: 4,
	}
	m.writeState(levelsFile, m.fxLevels)

	next := &Mod{stateDir: m.stateDir, exe: m.exe}
	next.recoverFXLevels()
	if lvl := next.fxLevels[renocore.FXPixelate]; lvl != 4 {
		t.Errorf("pixelate must survive: %d", lvl)
	}
	for _, kind := range renocore.SustainFXKinds {
		if lvl := next.fxLevels[kind]; lvl != 0 {
			t.Errorf("%s came back at %d with nothing driving it", kind, lvl)
		}
	}
}

// A good arm re-asserts the levels khudson holds. The filter effects go out
// as one-shots -- OBS's copy of them left with the scene collection -- and a
// sustained one gets a driver only where nothing is already driving it.
func TestArmReassertsLevels(t *testing.T) {
	m := sustainMod(t, "br")
	m.fxLevels = map[string]int{
		renocore.FXPixelate: 4,
		renocore.FXPurple:   0,
		renocore.FXLava:     6,
	}
	var effects []module.Effect
	under(m, func() {
		var err error
		effects, err = m.noteArmLocked([]string{"ok", "obs 32.2.0"})
		if err != nil {
			t.Fatal(err)
		}
	})
	want := [][]string{{m.exe, "reno", "obs", "cam", "pixelate", "4"}}
	var got [][]string
	for _, e := range effects {
		got = append(got, e.Argv)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("arm effects = %v, want %v", got, want)
	}
	if driver(m, renocore.FXLava) == nil {
		t.Error("a lamp at 6 with no driver must get one on arm")
	}
	if driver(m, renocore.FXJitter) != nil {
		t.Error("a shake at 0 must not be started")
	}

	// the running lamp is left alone: a second arm must not restart a driver
	// that is already the authority on that camera
	d := driver(m, renocore.FXLava)
	under(m, func() {
		if _, err := m.noteArmLocked([]string{"ok", "obs 32.2.0"}); err != nil {
			t.Fatal(err)
		}
	})
	if driver(m, renocore.FXLava) != d {
		t.Error("re-arming restarted a healthy driver")
	}
}

// A failed or released arm asserts nothing: there is no OBS to assert into,
// and a driver started against one would die on its first chunk.
func TestFailedArmAssertsNothing(t *testing.T) {
	for _, outcome := range []string{"error", "released"} {
		t.Run(outcome, func(t *testing.T) {
			m := sustainMod(t, "br")
			m.fxLevels = map[string]int{renocore.FXPixelate: 4, renocore.FXLava: 6}
			var effects []module.Effect
			under(m, func() {
				var err error
				effects, err = m.noteArmLocked([]string{outcome, "obs cold"})
				if err != nil {
					t.Fatal(err)
				}
			})
			if len(effects) != 0 {
				t.Errorf("effects = %+v, want none", effects)
			}
			if driver(m, renocore.FXLava) != nil {
				t.Error("started a driver off a failed arm")
			}
		})
	}
}

// waitGone fails unless the child actually exited: the supervisor promising
// to stop a driver is worth nothing if the process is still out there.
func waitGone(t *testing.T, d *sustainDriver) {
	t.Helper()
	select {
	case <-d.done:
	case <-time.After(sustainStopGrace + 2*time.Second):
		t.Fatalf("driver pid %d never exited", d.cmd.Process.Pid)
	}
	if !d.cmd.ProcessState.Exited() {
		t.Errorf("driver pid %d was killed, not stopped: %v",
			d.cmd.Process.Pid, d.cmd.ProcessState)
	}
}
