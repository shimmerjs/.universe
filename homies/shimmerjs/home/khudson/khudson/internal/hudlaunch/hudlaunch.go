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
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
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
	Poll        time.Duration // display presence poll interval
	Query       QueryFunc     // nil = osascript-backed default
	Logf        func(format string, args ...any)
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

// Run blocks until ctx is done, supervising one HUD kitty at a time.
func Run(ctx context.Context, opts Options) error {
	if opts.Poll <= 0 {
		opts.Poll = 15 * time.Second
	}
	if opts.Query == nil {
		opts.Query = queryScreens
	}
	if opts.Logf == nil {
		opts.Logf = log.Printf
	}
	backoff := backoffFloor
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
		outcome := runChild(ctx, opts, x, y)
		switch {
		case ctx.Err() != nil:
			return nil
		case outcome == childDisplayLost:
			// not a crash: wait for the display, no backoff
			opts.Logf("hud-launcher: display %q disconnected; HUD torn down", opts.DisplayName)
			continue
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

type childOutcome int

const (
	childExited childOutcome = iota
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
	opts.Logf("hud-launcher: launching HUD at %dx%d", x, y)
	if err := cmd.Start(); err != nil {
		opts.Logf("hud-launcher: start kitty: %v", err)
		return childExited
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	tick := time.NewTicker(opts.Poll)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err != nil {
				opts.Logf("hud-launcher: HUD kitty: %v", err)
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

// terminate delivers SIGTERM, then SIGKILL after killGrace.
func terminate(cmd *exec.Cmd, done <-chan error, logf func(string, ...any)) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
		return
	case <-time.After(killGrace):
		logf("hud-launcher: HUD kitty ignored SIGTERM; killing")
		_ = cmd.Process.Kill()
		<-done
	}
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
