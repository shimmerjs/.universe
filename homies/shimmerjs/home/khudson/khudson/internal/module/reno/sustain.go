package reno

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// A sustained effect -- the shake, the lamp -- runs for as long as its
// level is up, which makes it the one kind of FX that is a PROCESS rather
// than a request. This is the supervisor: one driver per effect while its
// level is above zero, none when it is not, and the level itself still
// lives in fxLevels like every other level FX.
//
// The driver is a `khudson reno obs cam <kind> <level> --sustain` child
// holding one socket, and it is stopped by CLOSING ITS STDIN. That is the
// whole reason the pipe exists: a stop that needs no signal to be delivered
// and no supervisor to still be alive to deliver it. A bus killed outright
// leaves the pipe closed anyway, so an orphan driver puts the camera back
// and exits rather than shaking it at an empty room.
//
// Going through the CLI rather than dialling obs-websocket from here keeps
// one FX path: the driver notes its outcome back to this module, so the
// tiles still agree with the camera afterwards.

// sustainStopGrace is how long a driver gets to notice the closed pipe,
// restore the state it found, and exit. Past it the child is killed and the
// effect is left wherever its last chunk put it -- worse than a clean exit,
// which is why the grace is generous next to a quarter-second chunk.
const sustainStopGrace = 5 * time.Second

// sustainDriver is one running effect: the child, the pipe that stops it,
// and the level it was started at (a level change is a restart, because the
// magnitude is baked into the chunks it is already generating).
//
// done is closed once the child has been reaped. Exactly one goroutine ever
// calls Wait -- the reaper -- and everyone else waits on this, because two
// Waits on one process is a race over who gets the status.
type sustainDriver struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	level int
	done  chan struct{}
}

// touchSustainLocked reconciles the drivers with the levels: start one for
// an effect that is up and unattended, stop one whose effect went to zero,
// restart one whose level moved under it. Called wherever a level changes.
// Caller holds mu.
func (m *Mod) touchSustainLocked(kind string) {
	if !renocore.IsSustainFX(kind) {
		return
	}
	want := m.fxLevels[kind]
	// a shake needs something to shake, and a lamp something to tint: with
	// no webcam configured, or one turned off, the driver would fail against
	// a missing item the moment it dialled. Turning the camera off is
	// therefore also how you stop every effect on it.
	if m.desired.Webcam == "" || m.desired.Webcam == "off" {
		want = 0
	}
	if d := m.sustain[kind]; d != nil {
		if d.level == want {
			return
		}
		delete(m.sustain, kind)
		// off the module goroutine: stopping waits out a grace, and the ctl
		// path that got here must not block on a subprocess
		go stopSustainDriver(kind, d)
	}
	if want <= 0 {
		return
	}
	d, err := startSustainDriver(m.exe, kind, want)
	if err != nil {
		// the level stays where it is so the tile still reads true, and a
		// later tap starts a fresh driver
		fmt.Fprintf(os.Stderr, "reno: %s driver at level %d: %v\n", kind, want, err)
		return
	}
	if m.sustain == nil {
		m.sustain = map[string]*sustainDriver{}
	}
	m.sustain[kind] = d
	go m.reapSustainDriver(kind, d)
}

// touchEverySustainLocked reconciles every sustained effect at once, for
// the changes that are not a level: the camera going away, or coming back.
// Caller holds mu.
func (m *Mod) touchEverySustainLocked() {
	for _, kind := range renocore.SustainFXKinds {
		m.touchSustainLocked(kind)
	}
}

// Close is module.Closer: the bus calls it at clean shutdown. A driver is
// a child this module started OUTSIDE the effect executor -- it has to be,
// since a process that runs for minutes would block every effect queued
// behind it -- so nothing else in the bus knows to reap one. The drivers go
// down in parallel because each waits out its own grace putting the camera
// back, and shutdown should not pay for those one after another.
func (m *Mod) Close() error {
	m.mu.Lock()
	running := m.sustain
	m.sustain = nil
	m.mu.Unlock()

	var wg sync.WaitGroup
	for kind, d := range running {
		wg.Go(func() { stopSustainDriver(kind, d) })
	}
	wg.Wait()
	return nil
}

// sustainArgv is the driver's argv tail, a seam so tests can substitute a
// child that reads stdin without needing OBS behind it.
var sustainArgv = func(kind string, level int) []string {
	return []string{"reno", "obs", "cam", kind, strconv.Itoa(level), "--sustain"}
}

// startSustainDriver spawns one driver. Stdout and stderr go to the bus's
// own, so a driver that failed says so where every other verb does.
func startSustainDriver(exe, kind string, level int) (*sustainDriver, error) {
	if exe == "" {
		return nil, fmt.Errorf("no khudson binary to run")
	}
	cmd := exec.Command(exe, sustainArgv(kind, level)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}
	return &sustainDriver{cmd: cmd, stdin: stdin, level: level, done: make(chan struct{})}, nil
}

// stopSustainDriver closes the pipe and waits out the grace, killing only
// a driver that would not go. The kill is the bad path: a killed driver
// never restored the pose or switched the filter off.
func stopSustainDriver(kind string, d *sustainDriver) {
	d.stdin.Close()
	select {
	case <-d.done:
	case <-time.After(sustainStopGrace):
		_ = d.cmd.Process.Kill()
		<-d.done
		fmt.Fprintf(os.Stderr, "reno: %s driver would not stop in %s; killed with the effect still applied\n",
			kind, sustainStopGrace)
	}
}

// reapSustainDriver is the one goroutine that waits on a driver. It clears
// the slot when the child stopped on its own -- its lifetime cap, a lost
// socket, an OBS that quit -- so the next level change starts a fresh one.
// It deliberately does NOT restart: whatever killed one driver would kill
// its replacement, and a respawn loop against a dead OBS is a worse failure
// than an effect that stopped.
func (m *Mod) reapSustainDriver(kind string, d *sustainDriver) {
	err := d.cmd.Wait()
	close(d.done)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sustain[kind] != d {
		return // already replaced or stopped deliberately; the stopper owns it
	}
	delete(m.sustain, kind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reno: %s driver exited: %v\n", kind, err)
	}
}
