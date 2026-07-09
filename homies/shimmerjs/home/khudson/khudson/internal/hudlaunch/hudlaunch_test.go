package hudlaunch

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"reflect"
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

func TestFind(t *testing.T) {
	if _, ok := find(liveScreens, "NOPE"); ok {
		t.Fatal("found a display that is not there")
	}
	s, ok := find(liveScreens, "XENEON EDGE")
	if !ok || s.W != 2560 {
		t.Fatalf("find returned %+v, %v", s, ok)
	}
}
