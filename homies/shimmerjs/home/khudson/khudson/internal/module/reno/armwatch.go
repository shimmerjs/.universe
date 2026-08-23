package reno

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// armIdleMax bounds how long OBS stays armed with nothing using it.
//
// Armed is three live captures -- a ScreenCaptureKit screen source, a Core
// Audio process tap, and the camera -- and ArmRig deliberately stops after
// bringing them up, so until now the only thing that ever put them down was
// someone tapping END RUN. On 2026-08-22 an arm at 14:34 was still holding
// all three at the 21:07 hard reset: a webcam light, a replayd capture
// session and a 60Hz encode outliving the session that wanted them by hours.
//
// Every reno verb refreshes the deadline and a running recording holds it
// open, so this fires only on an arm nothing has touched since -- a presenter
// mid-demo is tapping steps, not idling.
const armIdleMax = 30 * time.Minute

// armWatchTick paces the watchdog: one wakeup a minute while something is
// armed, and no goroutine at all while nothing is.
const armWatchTick = time.Minute

// armAction is what one watchdog pass decides.
type armAction int

const (
	armHold    armAction = iota // in use, or still inside the window
	armStop                     // nothing armed: the watchdog retires
	armRelease                  // idle past the deadline: take the hardware back
)

// touchArmLocked pushes the idle deadline out, and starts a watchdog if the
// rig is armed and none is running. Caller holds mu.
func (m *Mod) touchArmLocked() {
	m.armDeadline = m.now().Add(armIdleMax)
	if m.armWatch || !m.armed() {
		return
	}
	m.armWatch = true
	go m.watchArm()
}

// armCheckLocked is one watchdog pass, split from the goroutine so the policy
// is testable without a clock or a subprocess. Caller holds mu.
func (m *Mod) armCheckLocked(now time.Time) armAction {
	if !m.armed() {
		return armStop
	}
	// a running recording IS the arm being used, whatever the deadline says
	if r := m.recording(); r != nil && !r.StartedAt.IsZero() && r.StoppedAt.IsZero() {
		m.armDeadline = now.Add(armIdleMax)
		return armHold
	}
	if now.Before(m.armDeadline) {
		return armHold
	}
	return armRelease
}

// watchArm runs until the rig is released, by someone else or by it.
func (m *Mod) watchArm() {
	t := time.NewTicker(armWatchTick)
	defer t.Stop()
	for range t.C {
		m.mu.Lock()
		action := m.armCheckLocked(m.now())
		if action != armHold {
			m.armWatch = false
		}
		exe := m.exe
		m.mu.Unlock()

		switch action {
		case armStop:
			return
		case armRelease:
			releaseIdleArm(exe)
			return
		}
	}
}

// releaseIdleArm runs the verb the END RUN pad runs. Going through the CLI
// rather than dialling obs-websocket from here keeps one release path: the
// verb notes its outcome back to this module, so the tiles still agree with
// the hardware afterwards.
func releaseIdleArm(exe string) {
	if exe == "" {
		return
	}
	out, err := exec.Command(exe, "reno", "obs", "release").CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "reno: releasing an arm idle for %s failed: %v: %s\n", armIdleMax, err, out)
		return
	}
	fmt.Printf("reno: released an arm idle for %s\n", armIdleMax)
}
