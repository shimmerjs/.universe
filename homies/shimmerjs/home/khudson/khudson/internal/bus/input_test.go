package bus

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

type fakeInj struct {
	mu   sync.Mutex
	sgr  []string
	keys []string
}

func (f *fakeInj) SendSGR(match string, button, x, y int, release bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sgr = append(f.sgr, fmt.Sprintf("%s b%d @%d,%d r=%v", match, button, x, y, release))
	return nil
}

func (f *fakeInj) SendKey(match string, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, fmt.Sprintf("%s %v", match, keys))
	return nil
}

func inputTestBus() (*Bus, *fakeInj) {
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"w": {ID: "w",
				Render: config.Render{Kind: "exec", Argv: []string{"true"}},
				Gestures: map[string]config.Action{
					"swipe-left": {Verb: "send-key", Keys: "right", Target: "hud-window:w"},
					"swipe-up":   {Verb: "send-key", Keys: "x", Target: "main-kitty:oops"},
					"long-press": {Verb: "run", Argv: []string{"echo", "vetted"}},
				},
			},
		},
		Layouts: map[string]config.Layout{"main": {Tiles: []string{"w"}}},
		Layout:  "main",
	}
	inj := &fakeInj{}
	b := &Bus{cfg: cfg, reg: NewRegistry(cfg), inj: inj}
	st, _ := b.reg.Get("w")
	st.setWindowID(7)
	st.setSize(100, 30)
	return b, inj
}

func TestHandleForward(t *testing.T) {
	b, inj := inputTestBus()

	// tap: press+release, 0-based widget coords -> 1-based SGR
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureTap, Col: 4, Row: 2}})
	want := []string{"id:7 b0 @5,3 r=false", "id:7 b0 @5,3 r=true"}
	if len(inj.sgr) != 2 || inj.sgr[0] != want[0] || inj.sgr[1] != want[1] {
		t.Fatalf("tap injected %v, want %v", inj.sgr, want)
	}

	// out-of-range coords clamp to the widget grid
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureTap, Col: 500, Row: -3}})
	if inj.sgr[0] != "id:7 b0 @100,1 r=false" {
		t.Fatalf("clamp failed: %v", inj.sgr[0])
	}

	// wheel: burst-capped press-only reports, button by direction
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureWheel, Col: 0, Row: 0, DY: -9}})
	if len(inj.sgr) != wheelBurstCap {
		t.Fatalf("wheel burst = %d, want %d", len(inj.sgr), wheelBurstCap)
	}
	if inj.sgr[0] != "id:7 b64 @1,1 r=false" {
		t.Fatalf("wheel up wrong: %v", inj.sgr[0])
	}

	// horizontal wheel: DX sign picks the button (>0 right = 67, <0 left = 66)
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureWheel, Col: 0, Row: 0, DX: 3}})
	if len(inj.sgr) != 3 || inj.sgr[0] != "id:7 b67 @1,1 r=false" {
		t.Fatalf("wheel right wrong: %v", inj.sgr)
	}
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureWheel, Col: 0, Row: 0, DX: -2}})
	if len(inj.sgr) != 2 || inj.sgr[0] != "id:7 b66 @1,1 r=false" {
		t.Fatalf("wheel left wrong: %v", inj.sgr)
	}

	// mixed report: vertical loop first, then horizontal
	inj.sgr = nil
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureWheel, Col: 0, Row: 0, DX: 1, DY: -1}})
	want = []string{"id:7 b64 @1,1 r=false", "id:7 b67 @1,1 r=false"}
	if len(inj.sgr) != 2 || inj.sgr[0] != want[0] || inj.sgr[1] != want[1] {
		t.Fatalf("mixed wheel = %v, want %v", inj.sgr, want)
	}

	// unmaterialized widget: nothing injected
	inj.sgr = nil
	st, _ := b.reg.Get("w")
	st.setWindowID(0)
	b.handleForward(proto.Msg{Type: proto.TypeForward, Widget: "w",
		Gesture: &proto.Gesture{Kind: proto.GestureTap}})
	if len(inj.sgr) != 0 {
		t.Fatalf("injected into missing window: %v", inj.sgr)
	}
}

func TestHandleAction(t *testing.T) {
	b, inj := inputTestBus()

	// send-key resolves the hud-window target to its kitty window
	b.handleAction(proto.Msg{Type: proto.TypeAction, Widget: "w", Arg: "swipe-left"})
	if len(inj.keys) != 1 || inj.keys[0] != "id:7 [right]" {
		t.Fatalf("send-key injected %v", inj.keys)
	}

	// non-hud target is refused, not misrouted
	inj.keys = nil
	b.handleAction(proto.Msg{Type: proto.TypeAction, Widget: "w", Arg: "swipe-up"})
	if len(inj.keys) != 0 {
		t.Fatalf("main-kitty target leaked to hud injector: %v", inj.keys)
	}

	// unbound gesture is a no-op
	b.handleAction(proto.Msg{Type: proto.TypeAction, Widget: "w", Arg: "tap"})
	if len(inj.keys) != 0 {
		t.Fatalf("unbound gesture injected %v", inj.keys)
	}
}

// A row act only execs argv the bus itself published: the widget's last
// successful poll's acts or a config "run" gesture; wire-crafted argv is
// refused with no process start.
func TestHandleRowActRejectsForeignArgv(t *testing.T) {
	b, _ := inputTestBus()
	var starts [][]string
	b.execStart = func(argv []string) (func() error, error) {
		starts = append(starts, argv)
		return func() error { return nil }, nil
	}
	published := []string{"khudson", "claude", "focus", "abc"}
	st, _ := b.reg.Get("w")
	st.setActs([][]string{published})

	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "w",
		Argv: []string{"rm", "-rf", "/"}})
	if len(starts) != 0 {
		t.Fatalf("foreign argv exec'd: %v", starts)
	}

	// a published row act executes
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "w", Argv: published})
	if len(starts) != 1 || !slices.Equal(starts[0], published) {
		t.Fatalf("published act starts = %v, want [%v]", starts, published)
	}

	// the vetted-config leg: a run gesture's argv executes too
	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "w",
		Argv: []string{"echo", "vetted"}})
	if len(starts) != 2 {
		t.Fatalf("config run argv starts = %v", starts)
	}
}

// TestHelperProcess is the re-exec target for TestRowActSurfacesExit: the
// child exits 1 so the row-act waiter has a failure to surface.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	os.Exit(1)
}

// A row act that exits nonzero is surfaced: the waiter logs it and
// broadcasts a TypeNotice carrying the exit error.
func TestRowActSurfacesExit(t *testing.T) {
	b, _ := inputTestBus()
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	argv := []string{os.Args[0], "-test.run=TestHelperProcess"}
	st, _ := b.reg.Get("w")
	st.setActs([][]string{argv})

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	b.docks = map[net.Conn]*json.Encoder{server: json.NewEncoder(server)}
	notices := make(chan proto.Msg, 1)
	go func() {
		dec := json.NewDecoder(client)
		for {
			var m proto.Msg
			if dec.Decode(&m) != nil {
				return
			}
			if m.Type == proto.TypeNotice {
				notices <- m
				return
			}
		}
	}()

	b.handleRowAct(proto.Msg{Type: proto.TypeRowAct, Widget: "w", Argv: argv})

	select {
	case n := <-notices:
		if !strings.Contains(n.Error, "exit status 1") {
			t.Fatalf("notice %q does not carry the exit status", n.Error)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no notice for the failed row act")
	}
}
