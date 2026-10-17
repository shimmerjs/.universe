package reno

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// An unused armed rig retains screen, audio, and camera capture resources.
// Commands refresh this deadline; an active recording prevents idle release.
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
	if m.closed || m.armWatch || !m.armed() {
		return
	}
	m.armWatch = true
	if m.stop == nil {
		m.stop = make(chan struct{})
	}
	m.workers.Go(m.watchArm)
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
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
		}
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
			releaseIdleArm(m.stop, exe)
			return
		}
	}
}

// releaseIdleArm runs the verb the END RUN pad runs. Going through the CLI
// rather than dialling obs-websocket from here keeps one release path: the
// verb notes its outcome back to this module, so the tiles still agree with
// the hardware afterwards.
func releaseIdleArm(stop <-chan struct{}, exe string) {
	if exe == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	cmd := exec.CommandContext(ctx, exe, "reno", "obs", "release")
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "reno: releasing an arm idle for %s failed: %v: %s\n", armIdleMax, err, out)
		return
	}
	fmt.Printf("reno: released an arm idle for %s\n", armIdleMax)
}
