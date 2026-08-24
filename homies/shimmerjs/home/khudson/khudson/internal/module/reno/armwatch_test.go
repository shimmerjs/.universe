package reno

import (
	"testing"
	"time"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

func armedMod(t *testing.T, now time.Time) *Mod {
	t.Helper()
	m := New()
	m.now = func() time.Time { return now }
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.noteArmLocked([]string{"ok", "obs 32.2.0, screen X, webcam bl"}); err != nil {
		t.Fatal(err)
	}
	m.armDeadline = now.Add(armIdleMax)
	return m
}

// The leak this closes: an arm nobody touches gets released rather than
// holding the screen capture, the audio tap and the camera indefinitely.
func TestArmCheckReleasesIdleArm(t *testing.T) {
	now := time.Date(2026, 8, 22, 14, 34, 0, 0, time.UTC)
	m := armedMod(t, now)
	m.mu.Lock()
	defer m.mu.Unlock()

	if got := m.armCheckLocked(now.Add(armIdleMax - time.Minute)); got != armHold {
		t.Fatalf("inside the window: got %v, want armHold", got)
	}
	if got := m.armCheckLocked(now.Add(armIdleMax + time.Second)); got != armRelease {
		t.Fatalf("past the deadline: got %v, want armRelease", got)
	}
}

// A recording in flight is the arm being used. Releasing under it would kill
// the capture mid-take, which is worse than the leak.
func TestArmCheckHoldsThroughRecording(t *testing.T) {
	now := time.Date(2026, 8, 22, 14, 34, 0, 0, time.UTC)
	m := armedMod(t, now)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.run = &renocore.Run{Recording: &renocore.RecordingState{StartedAt: now}}

	late := now.Add(armIdleMax + time.Hour)
	if got := m.armCheckLocked(late); got != armHold {
		t.Fatalf("mid-recording: got %v, want armHold", got)
	}
	// and the pass must have pushed the deadline out, so the arm survives
	// past the end of the take rather than expiring the instant it stops
	if !m.armDeadline.After(late) {
		t.Fatalf("deadline %v not refreshed past %v", m.armDeadline, late)
	}

	// once the take stops, the arm is idle again and expires normally
	m.run.Recording.StoppedAt = late
	if got := m.armCheckLocked(m.armDeadline.Add(time.Second)); got != armRelease {
		t.Fatalf("after the take: got %v, want armRelease", got)
	}
}

// Released by hand: the watchdog retires instead of firing a second release.
func TestArmCheckStopsWhenReleased(t *testing.T) {
	now := time.Date(2026, 8, 22, 14, 34, 0, 0, time.UTC)
	m := armedMod(t, now)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.noteArmLocked([]string{"released", "3 sources released"}); err != nil {
		t.Fatal(err)
	}
	if got := m.armCheckLocked(now.Add(armIdleMax + time.Hour)); got != armStop {
		t.Fatalf("after release: got %v, want armStop", got)
	}
}

// A failed arm is not an armed rig; nothing to watch and nothing to release.
func TestArmCheckStopsOnFailedArm(t *testing.T) {
	now := time.Date(2026, 8, 22, 14, 34, 0, 0, time.UTC)
	m := armedMod(t, now)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.noteArmLocked([]string{"error", "obs cold"}); err != nil {
		t.Fatal(err)
	}
	if got := m.armCheckLocked(now.Add(armIdleMax + time.Hour)); got != armStop {
		t.Fatalf("after a failed arm: got %v, want armStop", got)
	}
}

// Any verb counts as use: HandleCtl refreshes the deadline, so a demo that
// is being driven never expires under the presenter.
func TestHandleCtlRefreshesArmDeadline(t *testing.T) {
	now := time.Date(2026, 8, 22, 14, 34, 0, 0, time.UTC)
	m := armedMod(t, now)

	later := now.Add(20 * time.Minute)
	m.now = func() time.Time { return later }
	if _, _, err := m.HandleCtl([]string{"arm-note", "ok", "still armed"}); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if want := later.Add(armIdleMax); !m.armDeadline.Equal(want) {
		t.Fatalf("deadline = %v, want %v", m.armDeadline, want)
	}
	if !m.armWatch {
		t.Fatal("arming did not start a watchdog")
	}
}

// Nothing armed means no watchdog goroutine at all -- the idle cost of this
// whole mechanism is zero until someone arms.
func TestNoWatchdogWhenNothingArmed(t *testing.T) {
	m := New()
	m.now = time.Now
	if _, _, err := m.HandleCtl([]string{"status"}); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.armWatch {
		t.Fatal("started a watchdog with nothing armed")
	}
}
