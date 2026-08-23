package reno

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// A jitter runs for as long as its level is up. One batch only covers
// FXJitterChunk, because OBS runs one frame-locked batch at a time, so a
// sustained shake is that batch fired again and again -- this is what fires
// it. Level 0 is not an effect to send, it is the driver stopping: every
// chunk ends on the pose it started from, so a shake that simply stops
// re-firing leaves the camera exactly where it was placed.
//
// Same shape as the arm watchdog next door: one goroutine while it is
// needed and none when it is not, the policy split from the clock and the
// subprocess so it tests without either.
//
// The re-fire is deliberately sequential -- the next chunk goes out only
// once the last one has returned. A chunk reads the cam's CURRENT pose to
// shake around, so overlapping two of them would have the second read a
// pose mid-swing and shake around that instead, walking the camera off its
// placement. The cost is a still moment between chunks while the next one
// dials.

// jitterAction is what one driver pass decides.
type jitterAction int

const (
	jitterFire jitterAction = iota // level is up: send another chunk
	jitterStop                     // level is 0 or the cam is gone: retire
)

// touchJitterLocked starts the driver if a jitter is up and none is
// running. Called wherever the jitter level changes. Caller holds mu.
func (m *Mod) touchJitterLocked() {
	if m.jitterWatch || m.fxLevels[renocore.FXJitter] <= 0 {
		return
	}
	m.jitterWatch = true
	go m.watchJitter()
}

// jitterCheckLocked is one driver pass: the level to shake at, and whether
// to keep going. Caller holds mu.
func (m *Mod) jitterCheckLocked() (int, jitterAction) {
	level := m.fxLevels[renocore.FXJitter]
	if level <= 0 || level > renocore.FXMaxLevel {
		return 0, jitterStop
	}
	// a shake needs something to shake: no webcam configured, or one turned
	// off, and the chunk would fail against a missing item every 3 seconds
	if m.desired.Webcam == "" || m.desired.Webcam == "off" {
		return 0, jitterStop
	}
	return level, jitterFire
}

// watchJitter re-fires chunks until the level drops or the cam goes away.
func (m *Mod) watchJitter() {
	for {
		m.mu.Lock()
		level, action := m.jitterCheckLocked()
		if action == jitterStop {
			m.jitterWatch = false
		}
		exe := m.exe
		m.mu.Unlock()

		if action == jitterStop {
			return
		}
		if !fireJitterChunk(exe, level) {
			// a failing chunk would otherwise retry every 3 seconds forever;
			// the level stays where it is so the tile still reads true, and a
			// later tap starts a fresh driver
			m.mu.Lock()
			m.jitterWatch = false
			m.mu.Unlock()
			return
		}
	}
}

// fireJitterChunk runs one chunk and reports whether to keep going. Going
// through the CLI rather than dialling obs-websocket from here keeps one FX
// path: the verb notes its outcome back to this module, so the tiles still
// agree with the camera afterwards.
func fireJitterChunk(exe string, level int) bool {
	if exe == "" {
		return false
	}
	out, err := exec.Command(exe, "reno", "obs", "cam", renocore.FXJitter, strconv.Itoa(level)).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "reno: jitter chunk at level %d failed, stopping the shake: %v: %s\n", level, err, out)
		return false
	}
	return true
}
