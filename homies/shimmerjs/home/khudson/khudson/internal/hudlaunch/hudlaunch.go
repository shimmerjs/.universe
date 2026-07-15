// Package hudlaunch supervises the HUD kitty. The fullscreen window is only
// launched while the target display is connected: kitty --position aimed at
// an absent display clamps a junk non-fullscreen window onto whatever display
// remains, so presence gates the launch, the child is
// torn down if the display disconnects mid-run, and exits relaunch with
// backoff once the display is back.
package hudlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/sockclaim"
)

// Options configures the launcher loop.
type Options struct {
	KittyBin    string        // kitty executable
	KittyConfig string        // --config for the HUD instance ("" = kitty defaults)
	KhudsonBin  string        // khudson executable that runs the dock
	DockConfig  string        // khudson dock -config value ("" = embedded example)
	DisplayName string        // NSScreen localizedName the HUD pins to
	Socket      string        // RC socket path, passed via --listen-on (verbatim)
	StateDir    string        // lock/pidfile/backoff state ("" = dir of Socket)
	Poll        time.Duration // presence poll while waiting for the display
	HealthyPoll time.Duration // presence poll while the HUD runs (0 = 4x Poll)
	Query       QueryFunc     // nil = osascript-backed default
	Logf        func(format string, args ...any)
}

func stateDirOf(opts Options) string {
	if opts.StateDir != "" {
		return opts.StateDir
	}
	return filepath.Dir(opts.Socket)
}

// QueryFunc returns the currently connected displays.
type QueryFunc func(ctx context.Context) ([]Screen, error)

// Screen is one connected display in Cocoa global coordinates (origin at the
// primary display's bottom-left corner, y increasing upward).
type Screen struct {
	Name string  `json:"name"`
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	W    float64 `json:"w"`
	H    float64 `json:"h"`
}

const (
	backoffFloor = time.Second
	backoffCap   = 30 * time.Second
	// a child that survives this long resets the crash backoff
	healthyRun = time.Minute
	// SIGTERM -> SIGKILL grace when tearing the child down
	killGrace = 5 * time.Second
)

// normalize fills the Options defaults. The healthy-tick cadence defaults
// to 4x the waiting poll: every tick execs osascript (the JXA screens
// query), and while the HUD is healthy that exec is the loop's only cost --
// the same cadence-cache seam as the modules, paid for with
// disconnect-teardown latency up to the healthy cadence.
func normalize(opts Options) Options {
	if opts.Poll <= 0 {
		opts.Poll = 15 * time.Second
	}
	if opts.HealthyPoll <= 0 {
		opts.HealthyPoll = 4 * opts.Poll
	}
	if opts.Query == nil {
		opts.Query = queryScreens
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	return opts
}

// Run blocks until ctx is done, supervising one HUD kitty at a time.
func Run(ctx context.Context, opts Options) error {
	opts = normalize(opts)
	sd := stateDirOf(opts)
	// Singleton: a second launcher (a dev run racing the agent, or overlap
	// around a KeepAlive respawn) exits loudly instead of stacking HUDs. This
	// is also the flock'd sidecar sockclaim's probe-then-claim caveat names.
	unlock, err := acquireLock(filepath.Join(sd, "hud-launcher.lock"))
	if err != nil {
		opts.Logf("hud-launcher: %v", err)
		return err
	}
	defer unlock()
	// A predecessor that died dirty (SIGKILLed under memory pressure) left
	// its kitty+dock tree running unsupervised; reap it before launching or
	// HUD instances stack -- the 2026-07-14 cascade.
	sweepOrphan(opts, filepath.Join(sd, "hud-kitty.pid"))
	// Resume the persisted relaunch schedule: backoff used to live only in
	// process memory, so every launchd respawn relaunched at full speed.
	backoffPath := filepath.Join(sd, "hud-backoff.state")
	st := loadBackoff(backoffPath)
	backoff := clampBackoff(st.Backoff)
	if wait := resumeWait(st, time.Now()); wait > 0 {
		opts.Logf("hud-launcher: resuming persisted backoff: %s", wait.Round(time.Second))
		if !sleepCtx(ctx, wait) {
			return nil
		}
	}
	waitingLogged := false
	for {
		if ctx.Err() != nil {
			return nil
		}
		screens, err := opts.Query(ctx)
		if err != nil {
			opts.Logf("hud-launcher: display query: %v", err)
			if !sleepCtx(ctx, opts.Poll) {
				return nil
			}
			continue
		}
		target, ok := find(screens, opts.DisplayName)
		if !ok {
			if !waitingLogged {
				opts.Logf("hud-launcher: display %q not connected; waiting", opts.DisplayName)
				waitingLogged = true
			}
			if !sleepCtx(ctx, opts.Poll) {
				return nil
			}
			continue
		}
		waitingLogged = false
		x, y, err := LaunchPos(target, screens)
		if err != nil {
			opts.Logf("hud-launcher: %v", err)
			if !sleepCtx(ctx, opts.Poll) {
				return nil
			}
			continue
		}

		start := time.Now()
		saveBackoff(backoffPath, backoffState{LastLaunch: start, Backoff: backoff})
		// dev override read fresh per launch: clearing the marker and killing
		// the HUD kitty is the whole restore path
		launch := opts
		if dev := devOverride(sd, opts.Logf); dev != "" {
			launch.KhudsonBin = dev
		}
		outcome := runChild(ctx, launch, x, y)
		switch {
		case ctx.Err() != nil:
			return nil
		case outcome == childDisplayLost:
			// not a crash: wait for the display, no backoff
			opts.Logf("hud-launcher: display %q disconnected; HUD torn down", opts.DisplayName)
			continue
		case outcome == childSignaled:
			// a signaled child (jetsam, OOM killer, manual kill) is system
			// pressure, not health: escalate even after a long run. The
			// 2026-07-14 incident's 40m29s run resetting backoff to 1s while
			// the machine drowned is the hole this closes.
			backoff = min(backoff*2, backoffCap)
		case time.Since(start) >= healthyRun:
			backoff = backoffFloor
		default:
			backoff = min(backoff*2, backoffCap)
		}
		opts.Logf("hud-launcher: HUD exited after %s; relaunch in %s", time.Since(start).Round(time.Second), backoff)
		if !sleepCtx(ctx, backoff) {
			return nil
		}
	}
}

// The dev-override marker (fast UX iteration, tier 3): a state-dir file
// naming a working-tree khudson binary the launcher runs the dock from
// instead of the deployed install. Read fresh per launch and loudly
// logged; ignored (loudly) once stale so a forgotten override cannot
// outlive the iteration session. The dock holds no TCC grants, so the
// swap never touches the signed installs.
const (
	devOverrideFile   = "hud-dev-binary"
	devOverrideMaxAge = 6 * time.Hour
)

// devOverride returns the dev binary to launch instead of the deployed
// khudson, or "" for none.
func devOverride(sd string, logf func(string, ...any)) string {
	p := filepath.Join(sd, devOverrideFile)
	fi, err := os.Stat(p)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	bin := strings.TrimSpace(string(b))
	if bin == "" {
		return ""
	}
	if age := time.Since(fi.ModTime()); age > devOverrideMaxAge {
		logf("hud-launcher: dev override %s is %s old; IGNORED (rm or re-touch %s)", bin, age.Round(time.Minute), p)
		return ""
	}
	if _, err := os.Stat(bin); err != nil {
		logf("hud-launcher: dev override binary missing (%v); using the deployed install", err)
		return ""
	}
	logf("hud-launcher: DEV OVERRIDE active: dock runs %s (marker expires %s after its mtime)", bin, devOverrideMaxAge)
	return bin
}

type childOutcome int

const (
	childExited   childOutcome = iota
	childSignaled              // died by signal: treat as pressure, never resets backoff
	childDisplayLost
)

// runChild launches one HUD kitty and blocks until it exits, the display
// disappears (child torn down), or ctx is done (child torn down).
func runChild(ctx context.Context, opts Options, x, y int) childOutcome {
	// a stale socket file from a previous instance would shadow the new
	// bind, but a LIVE one belongs to a running HUD: refuse to touch it and
	// let Run's crash backoff pace the retry. No bind here -- kitty owns the
	// socket via --listen-on.
	if err := sockclaim.Free(opts.Socket); err != nil {
		opts.Logf("hud-launcher: %v", err)
		return childExited
	}
	args := kittyArgs(opts, x, y)
	cmd := exec.Command(opts.KittyBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Own process group: teardown signals the whole kitty+dock+module tree,
	// and a launcher that dies uncleanly leaves a group the next instance's
	// sweepOrphan can kill by pgid instead of orphaning it.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	opts.Logf("hud-launcher: launching HUD at %dx%d", x, y)
	if err := cmd.Start(); err != nil {
		opts.Logf("hud-launcher: start kitty: %v", err)
		return childExited
	}
	pidPath := filepath.Join(stateDirOf(opts), "hud-kitty.pid")
	writePidfile(pidPath, cmd.Process.Pid)
	defer os.Remove(pidPath)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	tick := time.NewTicker(opts.HealthyPoll)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				opts.Logf("hud-launcher: HUD kitty: %v", err)
			}
			if exitedBySignal(err) {
				return childSignaled
			}
			return childExited
		case <-ctx.Done():
			terminate(cmd, done, opts.Logf)
			return childExited
		case <-tick.C:
			screens, err := opts.Query(ctx)
			if err != nil {
				// a transient query failure must not kill a healthy HUD
				opts.Logf("hud-launcher: display query: %v", err)
				continue
			}
			if _, ok := find(screens, opts.DisplayName); !ok {
				terminate(cmd, done, opts.Logf)
				return childDisplayLost
			}
		}
	}
}

// terminate delivers SIGTERM to the child's process group, then SIGKILL after
// killGrace. Group delivery is the point: kitty alone dying leaves the dock
// and module subprocesses running. Falls back to the single process if the
// group signal fails (pgid already gone).
func terminate(cmd *exec.Cmd, done <-chan error, logf func(string, ...any)) {
	if cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if syscall.Kill(-pid, syscall.SIGTERM) != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	select {
	case <-done:
		return
	case <-time.After(killGrace):
		logf("hud-launcher: HUD kitty ignored SIGTERM; killing")
		if syscall.Kill(-pid, syscall.SIGKILL) != nil {
			_ = cmd.Process.Kill()
		}
		<-done
	}
}

// exitedBySignal reports whether a cmd.Wait error means the child died by
// signal (jetsam/OOM/manual kill) rather than exiting on its own.
func exitedBySignal(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	return ok && ws.Signaled()
}

// acquireLock takes the launcher singleton flock; the returned func releases
// it. A held lock means another launcher is alive: fail loudly, never race.
func acquireLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("launcher lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another hud-launcher holds %s", path)
	}
	return func() { f.Close() }, nil
}

// sweepOrphan reaps the previous launcher's child if it is still running: the
// pidfile names the group leader, and the kill decision requires a live argv
// matching OUR kitty binary and OUR socket -- never kill by name or title.
func sweepOrphan(opts Options, pidPath string) {
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		os.Remove(pidPath)
		return
	}
	argv := psCommand(pid)
	if !orphanMatch(argv, opts.KittyBin, opts.Socket) {
		// dead, pid reused by something not ours, or ps unusable -- refuse to
		// kill what we cannot identify, but say so when the pid is live.
		if argv == "" && processAlive(pid) {
			opts.Logf("hud-launcher: pidfile pid %d alive but unverifiable (ps returned nothing); not killing", pid)
		}
		os.Remove(pidPath)
		return
	}
	opts.Logf("hud-launcher: sweeping orphaned HUD group (pid %d)", pid)
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	deadline := time.Now().Add(killGrace)
	for time.Now().Before(deadline) && processAlive(pid) {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(pid) {
		opts.Logf("hud-launcher: orphan ignored SIGTERM; killing group %d", pid)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	os.Remove(pidPath)
}

// orphanMatch is the pure kill decision behind sweepOrphan.
func orphanMatch(argv, kittyBin, socket string) bool {
	return argv != "" && kittyBin != "" && socket != "" &&
		strings.Contains(argv, kittyBin) && strings.Contains(argv, socket)
}

func psCommand(pid int) string {
	out, err := exec.Command("/bin/ps", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func writePidfile(path string, pid int) {
	tmp := path + ".tmp"
	if os.WriteFile(tmp, []byte(strconv.Itoa(pid)+"\n"), 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// backoffState is the relaunch schedule persisted across launcher deaths.
type backoffState struct {
	LastLaunch time.Time     `json:"last_launch"`
	Backoff    time.Duration `json:"backoff_ns"`
}

// resumeWait: how long a fresh launcher must still wait to honor the
// persisted schedule; zero when there is no prior launch or the window passed.
func resumeWait(st backoffState, now time.Time) time.Duration {
	if st.LastLaunch.IsZero() || st.Backoff <= 0 {
		return 0
	}
	if w := st.LastLaunch.Add(st.Backoff).Sub(now); w > 0 {
		return w
	}
	return 0
}

// clampBackoff maps a missing/corrupt persisted value to the floor.
func clampBackoff(d time.Duration) time.Duration {
	if d < backoffFloor || d > backoffCap {
		return backoffFloor
	}
	return d
}

func loadBackoff(path string) backoffState {
	b, err := os.ReadFile(path)
	if err != nil {
		return backoffState{}
	}
	var st backoffState
	if json.Unmarshal(b, &st) != nil {
		return backoffState{}
	}
	return st
}

func saveBackoff(path string, st backoffState) {
	b, err := json.Marshal(st)
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// kittyArgs builds the kitty argv after the binary name. --listen-on goes on
// the CLI, never in the config: the config form appends -PID to the path and
// the bus needs the verbatim socket. allow_remote_control rides as an
// override so the launcher works against bare kitty defaults too.
func kittyArgs(opts Options, x, y int) []string {
	var args []string
	if opts.KittyConfig != "" {
		args = append(args, "--config", opts.KittyConfig)
	}
	args = append(args,
		"-o", "allow_remote_control=socket-only",
		"--listen-on", "unix:"+opts.Socket,
		"--position", fmt.Sprintf("%dx%d", x, y),
		"--start-as", "fullscreen",
		"--title", "khudson-hud",
		opts.KhudsonBin, "dock",
	)
	if opts.DockConfig != "" {
		args = append(args, "-config", opts.DockConfig)
	}
	return args
}

// LaunchPos converts a Cocoa frame to kitty --position coordinates: global
// top-left origin with y increasing downward, flipped around the primary
// display's height. The primary display is the one at Cocoa origin (0,0).
func LaunchPos(target Screen, all []Screen) (x, y int, err error) {
	var primary *Screen
	for i := range all {
		if all[i].X == 0 && all[i].Y == 0 {
			primary = &all[i]
			break
		}
	}
	if primary == nil {
		return 0, 0, fmt.Errorf("launch position: no primary display (Cocoa origin 0,0) among %d screens", len(all))
	}
	x = int(math.Round(target.X))
	y = int(math.Round(primary.H - (target.Y + target.H)))
	return x, y, nil
}

func find(screens []Screen, name string) (Screen, bool) {
	for _, s := range screens {
		if s.Name == name {
			return s, true
		}
	}
	return Screen{}, false
}

// screensJXA emits every NSScreen as JSON in Cocoa global coordinates.
// osascript is the only dependency: NSScreen needs an Aqua session, which a
// LaunchAgent has, and shelling out keeps AppKit out of the binary (the
// only in-process framework link is internal/ax's ApplicationServices).
const screensJXA = `ObjC.import("AppKit");
function run() {
  var scr = $.NSScreen.screens, out = [];
  for (var i = 0; i < scr.count; i++) {
    var s = scr.objectAtIndex(i), f = s.frame;
    out.push({name: s.localizedName.js, x: f.origin.x, y: f.origin.y, w: f.size.width, h: f.size.height});
  }
  return JSON.stringify(out);
}`

func queryScreens(ctx context.Context) ([]Screen, error) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(qctx, "/usr/bin/osascript", "-l", "JavaScript", "-e", screensJXA).Output()
	if err != nil {
		return nil, fmt.Errorf("osascript screens: %w", err)
	}
	return parseScreens(out)
}

func parseScreens(out []byte) ([]Screen, error) {
	var screens []Screen
	if err := json.Unmarshal(out, &screens); err != nil {
		return nil, fmt.Errorf("parse screens: %w", err)
	}
	return screens, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
