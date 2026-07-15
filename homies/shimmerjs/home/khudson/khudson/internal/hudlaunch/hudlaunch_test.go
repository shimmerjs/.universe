package hudlaunch

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fixture: the live arrangement -- Dell primary 3840x2160 at Cocoa origin,
// Edge 2560x720 below it (Cocoa y -720).
var liveScreens = []Screen{
	{Name: "DELL U3223QE", X: 0, Y: 0, W: 3840, H: 2160},
	{Name: "XENEON EDGE", X: 342, Y: -720, W: 2560, H: 720},
}

func TestLaunchPosLiveArrangement(t *testing.T) {
	edge, ok := find(liveScreens, "XENEON EDGE")
	if !ok {
		t.Fatal("fixture missing edge")
	}
	x, y, err := LaunchPos(edge, liveScreens)
	if err != nil {
		t.Fatal(err)
	}
	if x != 342 || y != 2160 {
		t.Fatalf("got %dx%d, want 342x2160", x, y)
	}
}

func TestLaunchPosAboveRight(t *testing.T) {
	// display top-aligned to the right of the primary: Cocoa origin
	// (3840, 720), height 1440 -> glfw y = 2160 - (720 + 1440) = 0
	screens := []Screen{
		{Name: "PRIMARY", X: 0, Y: 0, W: 3840, H: 2160},
		{Name: "SIDE", X: 3840, Y: 720, W: 2560, H: 1440},
	}
	x, y, err := LaunchPos(screens[1], screens)
	if err != nil {
		t.Fatal(err)
	}
	if x != 3840 || y != 0 {
		t.Fatalf("got %dx%d, want 3840x0", x, y)
	}
}

func TestLaunchPosNoPrimary(t *testing.T) {
	screens := []Screen{{Name: "FLOATING", X: 100, Y: 100, W: 800, H: 600}}
	if _, _, err := LaunchPos(screens[0], screens); err == nil {
		t.Fatal("want error when no display sits at Cocoa origin")
	}
}

func TestParseScreens(t *testing.T) {
	payload := []byte(`[{"name":"DELL U3223QE","x":0,"y":0,"w":3840,"h":2160},` +
		`{"name":"XENEON EDGE","x":342,"y":-720,"w":2560,"h":720}]`)
	screens, err := parseScreens(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(screens, liveScreens) {
		t.Fatalf("got %+v, want %+v", screens, liveScreens)
	}
	if _, err := parseScreens([]byte("not json")); err == nil {
		t.Fatal("want error on malformed payload")
	}
}

func TestKittyArgs(t *testing.T) {
	opts := Options{
		KittyBin:    "/nix/store/x/bin/kitty",
		KittyConfig: "/cfg/hud-kitty.conf",
		KhudsonBin:  "/nix/store/y/bin/khudson",
		DockConfig:  "/cfg/edge.cue",
		Socket:      "/state/kitty-panel.sock",
	}
	got := kittyArgs(opts, 342, 2160)
	want := []string{
		"--config", "/cfg/hud-kitty.conf",
		"-o", "allow_remote_control=socket-only",
		"--listen-on", "unix:/state/kitty-panel.sock",
		"--position", "342x2160",
		"--start-as", "fullscreen",
		"--title", "khudson-hud",
		"/nix/store/y/bin/khudson", "dock", "-config", "/cfg/edge.cue",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}

	// no kitty config, no dock config: both blocks drop out
	bare := kittyArgs(Options{KittyBin: "kitty", KhudsonBin: "khudson", Socket: "/s"}, 0, 0)
	if bare[0] != "-o" {
		t.Fatalf("bare args must not start with --config: %v", bare)
	}
	if bare[len(bare)-1] != "dock" {
		t.Fatalf("bare args must end with the dock subcommand: %v", bare)
	}
}

// hudSockPath keeps the socket path under the macOS sun_path limit.
func hudSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hudlaunch")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "kitty-panel.sock")
}

// A second launcher must not steal a live HUD's kitty socket: runChild
// refuses before cmd.Start and the live listener keeps answering.
func TestRunChildRefusesLiveHudSocket(t *testing.T) {
	sock := hudSockPath(t)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	opts := Options{
		KittyBin: "/nonexistent/kitty",
		Socket:   sock,
		Poll:     time.Minute,
		Logf:     t.Logf,
	}
	if got := runChild(context.Background(), opts, 0, 0); got != childExited {
		t.Fatalf("outcome = %v, want childExited", got)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("live socket stolen: %v", err)
	}
	conn.Close()
}

// A dead socket file from a previous instance is removed so kitty's
// --listen-on bind succeeds (cmd.Start then fails fast on the fake binary).
func TestRunChildRemovesStaleHudSocket(t *testing.T) {
	sock := hudSockPath(t)
	addr, err := net.ResolveUnixAddr("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	corpse, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	corpse.SetUnlinkOnClose(false)
	corpse.Close()

	opts := Options{
		KittyBin: "/nonexistent/kitty",
		Socket:   sock,
		Poll:     time.Minute,
		Logf:     t.Logf,
	}
	if got := runChild(context.Background(), opts, 0, 0); got != childExited {
		t.Fatalf("outcome = %v, want childExited", got)
	}
	if _, err := os.Stat(sock); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stale socket not removed: stat = %v", err)
	}
}

func TestNormalizeDefaults(t *testing.T) {
	opts := normalize(Options{})
	if opts.Poll != 15*time.Second || opts.HealthyPoll != time.Minute {
		t.Fatalf("defaults = %s/%s, want 15s/1m", opts.Poll, opts.HealthyPoll)
	}
	if opts.Query == nil || opts.Logf == nil {
		t.Fatal("query/logf defaults missing")
	}
	if opts = normalize(Options{Poll: 5 * time.Second}); opts.HealthyPoll != 20*time.Second {
		t.Fatalf("HealthyPoll = %s, want 4x Poll", opts.HealthyPoll)
	}
	if opts = normalize(Options{Poll: time.Second, HealthyPoll: time.Hour}); opts.HealthyPoll != time.Hour {
		t.Fatal("explicit HealthyPoll overridden")
	}
}

// fakeChild writes an executable script the launcher spawns as its "kitty"
// (kittyArgs appends the real argv; the script ignores it).
func fakeChild(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-kitty")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// The healthy tick rides HealthyPoll, never the waiting Poll: with a hot
// waiting poll and a cold healthy poll the display query must not run while
// the child lives.
func TestRunChildHealthyTickCadence(t *testing.T) {
	sock := hudSockPath(t)
	queries := 0
	opts := Options{
		KittyBin:    fakeChild(t, "sleep 0.2"),
		Socket:      sock,
		DisplayName: "XENEON EDGE",
		Poll:        5 * time.Millisecond,
		HealthyPoll: time.Hour,
		Query: func(context.Context) ([]Screen, error) {
			queries++
			return liveScreens, nil
		},
		Logf: t.Logf,
	}
	if got := runChild(context.Background(), opts, 0, 0); got != childExited {
		t.Fatalf("outcome = %v, want childExited", got)
	}
	if queries != 0 {
		t.Fatalf("display query ran %d times on the waiting cadence; healthy ticks must ride HealthyPoll", queries)
	}
}

// startRun drives Run in a goroutine; the returned stop cancels it and
// waits for the loop to return (Logf must not fire after the test ends).
func startRun(t *testing.T, opts Options) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opts) }()
	return func() error {
		cancel()
		return <-done
	}
}

// TestRunDisplayGatedLaunch pins the Run loop's orchestration: no child
// launches while the display is absent, the child launches once the display
// appears, and cancellation returns cleanly.
func TestRunDisplayGatedLaunch(t *testing.T) {
	sock := hudSockPath(t)
	mark := filepath.Join(filepath.Dir(sock), "mark")
	t.Setenv("HUDLAUNCH_TEST_MARK", mark)
	var present atomic.Bool
	var queried atomic.Int32
	opts := Options{
		KittyBin:    fakeChild(t, `echo x >> "$HUDLAUNCH_TEST_MARK"; sleep 0.1`),
		Socket:      sock,
		DisplayName: "XENEON EDGE",
		Poll:        10 * time.Millisecond,
		HealthyPoll: time.Hour,
		Query: func(context.Context) ([]Screen, error) {
			queried.Add(1)
			if present.Load() {
				return liveScreens, nil
			}
			return nil, nil
		},
		Logf: t.Logf,
	}
	stop := startRun(t, opts)
	deadline := time.Now().Add(2 * time.Second)
	for queried.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := os.Stat(mark); !errors.Is(err, fs.ErrNotExist) {
		stop()
		t.Fatal("child launched while the display was absent")
	}
	present.Store(true)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(mark); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if _, err := os.Stat(mark); err != nil {
		t.Fatal("child never launched after the display appeared")
	}
}

// TestRunRelaunchesAfterExit pins the crash-relaunch leg: a child that
// exits immediately relaunches after the backoff floor.
func TestRunRelaunchesAfterExit(t *testing.T) {
	sock := hudSockPath(t)
	mark := filepath.Join(filepath.Dir(sock), "mark")
	t.Setenv("HUDLAUNCH_TEST_MARK", mark)
	opts := Options{
		KittyBin:    fakeChild(t, `echo x >> "$HUDLAUNCH_TEST_MARK"`),
		Socket:      sock,
		DisplayName: "XENEON EDGE",
		Poll:        10 * time.Millisecond,
		HealthyPoll: time.Hour,
		Query:       func(context.Context) ([]Screen, error) { return liveScreens, nil },
		Logf:        t.Logf,
	}
	stop := startRun(t, opts)
	launches := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(mark); err == nil {
			if launches = strings.Count(string(b), "x"); launches >= 2 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := stop(); err != nil {
		t.Fatalf("Run returned %v", err)
	}
	if launches < 2 {
		t.Fatalf("launches = %d, want >= 2 (relaunch after exit)", launches)
	}
}

// TestRunHonorsPersistedBackoff pins the resume leg: a fresh launcher under
// a live persisted schedule waits it out instead of relaunching at full
// speed (the launchd-respawn hole the persistence closed).
func TestRunHonorsPersistedBackoff(t *testing.T) {
	sock := hudSockPath(t)
	sd := filepath.Dir(sock)
	mark := filepath.Join(sd, "mark")
	t.Setenv("HUDLAUNCH_TEST_MARK", mark)
	saveBackoff(filepath.Join(sd, "hud-backoff.state"),
		backoffState{LastLaunch: time.Now(), Backoff: backoffCap})
	opts := Options{
		KittyBin:    fakeChild(t, `echo x >> "$HUDLAUNCH_TEST_MARK"`),
		Socket:      sock,
		DisplayName: "XENEON EDGE",
		Poll:        10 * time.Millisecond,
		HealthyPoll: time.Hour,
		Query:       func(context.Context) ([]Screen, error) { return liveScreens, nil },
		Logf:        t.Logf,
	}
	stop := startRun(t, opts)
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(mark); err == nil {
		stop()
		t.Fatal("launched inside the persisted backoff window")
	}
	if err := stop(); err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

// Display loss on a healthy tick tears the child down and classifies as
// childDisplayLost.
func TestRunChildDisplayLost(t *testing.T) {
	sock := hudSockPath(t)
	opts := Options{
		KittyBin:    fakeChild(t, "sleep 30"),
		Socket:      sock,
		DisplayName: "XENEON EDGE",
		Poll:        time.Hour,
		HealthyPoll: 20 * time.Millisecond,
		Query: func(context.Context) ([]Screen, error) {
			return nil, nil // no screens: the display is gone
		},
		Logf: t.Logf,
	}
	if got := runChild(context.Background(), opts, 0, 0); got != childDisplayLost {
		t.Fatalf("outcome = %v, want childDisplayLost", got)
	}
}

func TestFind(t *testing.T) {
	if _, ok := find(liveScreens, "NOPE"); ok {
		t.Fatal("found a display that is not there")
	}
	s, ok := find(liveScreens, "XENEON EDGE")
	if !ok || s.W != 2560 {
		t.Fatalf("find returned %+v, %v", s, ok)
	}
}

func TestClampBackoff(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		0:                backoffFloor,
		-time.Second:     backoffFloor,
		backoffFloor:     backoffFloor,
		10 * time.Second: 10 * time.Second,
		backoffCap:       backoffCap,
		time.Hour:        backoffFloor, // corrupt persisted value
	}
	for in, want := range cases {
		if got := clampBackoff(in); got != want {
			t.Errorf("clampBackoff(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestResumeWait(t *testing.T) {
	now := time.Unix(1000, 0)
	if w := resumeWait(backoffState{}, now); w != 0 {
		t.Fatalf("zero state must not wait, got %s", w)
	}
	mid := backoffState{LastLaunch: now.Add(-2 * time.Second), Backoff: 10 * time.Second}
	if w := resumeWait(mid, now); w != 8*time.Second {
		t.Fatalf("mid-window resume = %s, want 8s", w)
	}
	past := backoffState{LastLaunch: now.Add(-time.Minute), Backoff: 10 * time.Second}
	if w := resumeWait(past, now); w != 0 {
		t.Fatalf("elapsed window must not wait, got %s", w)
	}
}

func TestBackoffStateRoundtrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hud-backoff.state")
	if got := loadBackoff(p); !got.LastLaunch.IsZero() {
		t.Fatal("missing file must load as zero state")
	}
	st := backoffState{LastLaunch: time.Unix(1234, 0).UTC(), Backoff: 8 * time.Second}
	saveBackoff(p, st)
	got := loadBackoff(p)
	if !got.LastLaunch.Equal(st.LastLaunch) || got.Backoff != st.Backoff {
		t.Fatalf("roundtrip: got %+v, want %+v", got, st)
	}
	if err := os.WriteFile(p, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadBackoff(p); !got.LastLaunch.IsZero() {
		t.Fatal("corrupt file must load as zero state")
	}
}

func TestOrphanMatch(t *testing.T) {
	kitty, sock := "/nix/store/x/.kitty-wrapped", "/state/kitty-panel.sock"
	argv := kitty + " -o allow_remote_control=socket-only --listen-on unix:" + sock
	if !orphanMatch(argv, kitty, sock) {
		t.Fatal("live matching argv must be a kill")
	}
	if orphanMatch("", kitty, sock) {
		t.Fatal("dead pid (empty argv) must never be a kill")
	}
	if orphanMatch("/usr/bin/vim notes.txt", kitty, sock) {
		t.Fatal("reused pid running something else must never be a kill")
	}
	if orphanMatch(argv, kitty, "/other/socket") {
		t.Fatal("a kitty on a different socket is not ours")
	}
	if orphanMatch(argv, "", "") {
		t.Fatal("empty binary/socket must never match")
	}
}

func TestExitedBySignal(t *testing.T) {
	if exitedBySignal(nil) {
		t.Fatal("nil error is not a signal death")
	}
	if exitedBySignal(exec.Command("/bin/sh", "-c", "exit 3").Run()) {
		t.Fatal("a plain non-zero exit is not a signal death")
	}
	if !exitedBySignal(exec.Command("/bin/sh", "-c", "kill -9 $$").Run()) {
		t.Fatal("SIGKILL death must classify as signaled")
	}
}

func TestAcquireLockSingleton(t *testing.T) {
	p := filepath.Join(t.TempDir(), "hud-launcher.lock")
	unlock, err := acquireLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(p); err == nil {
		t.Fatal("second acquire must fail while the lock is held")
	}
	unlock()
	unlock2, err := acquireLock(p)
	if err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}
	unlock2()
}

// terminate must kill the whole process group, not just the leader --
// surviving grandchildren were the 2026-07-14 stack.
func TestTerminateKillsGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	terminate(cmd, done, t.Logf)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(-pid, 0) != nil {
			return // whole group gone
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("process group still alive after terminate")
}

// sweepOrphan kills a live argv-matched group and removes the pidfile; a
// stale pidfile is cleaned without killing anything. Skips where ps cannot
// read process argv (the nix builder) -- house skip-on-missing convention;
// the kill decision itself is covered hermetically by TestOrphanMatch.
func TestSweepOrphan(t *testing.T) {
	if psCommand(os.Getpid()) == "" {
		t.Skip("ps -o command= unusable in this environment")
	}
	sock := hudSockPath(t)
	// the loop keeps sh from exec-optimizing itself away (a bare `sleep 30`
	// would replace sh and drop the -c argv the sweep matches against)
	cmd := exec.Command("/bin/sh", "-c", "while :; do sleep 30; done # --listen-on unix:"+sock)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }() // reap so the killed pid does not linger as a zombie
	pidPath := filepath.Join(filepath.Dir(sock), "hud-kitty.pid")
	writePidfile(pidPath, cmd.Process.Pid)

	opts := Options{KittyBin: "/bin/sh", Socket: sock, Logf: t.Logf}
	sweepOrphan(opts, pidPath)
	if _, err := os.Stat(pidPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("pidfile not removed after sweep")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && syscall.Kill(cmd.Process.Pid, 0) == nil {
		time.Sleep(50 * time.Millisecond)
	}
	if syscall.Kill(cmd.Process.Pid, 0) == nil {
		t.Fatal("orphan survived the sweep")
	}

	// stale pidfile: a pid that cannot exist is cleaned, nothing killed
	writePidfile(pidPath, 99999999)
	sweepOrphan(opts, pidPath)
	if _, err := os.Stat(pidPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("stale pidfile not removed")
	}
}
