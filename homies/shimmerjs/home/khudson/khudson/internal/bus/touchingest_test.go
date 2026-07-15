package bus

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// touchTestBus builds a minimal bus with a recognizer grid and a decoding
// dock. The state root is a bare MkdirTemp (the hudSockPath idiom), not
// t.TempDir(): the test-name-derived dir pushes touch.sock past the macOS
// sun_path limit.
func touchTestBus(t *testing.T) (*Bus, <-chan proto.Msg, paths.Paths) {
	t.Helper()
	dir, err := os.MkdirTemp("", "touchbus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := paths.Paths{Dir: dir}
	cfg := &config.Config{
		Widgets: map[string]config.Widget{},
		Layouts: map[string]config.Layout{"main": {Kind: "home"}},
		Layout:  "main",
	}
	b := &Bus{
		opts:  Options{Paths: p},
		cfg:   cfg,
		reg:   NewRegistry(cfg),
		docks: make(map[net.Conn]*json.Encoder),
	}
	b.setGrid(320, 18)
	return b, addDecodingDock(t, b), p
}

func wantGestureMsg(t *testing.T, ch <-chan proto.Msg) *proto.Gesture {
	t.Helper()
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("dock connection closed before a gesture broadcast")
			}
			if m.Type == proto.TypeGesture {
				if m.Gesture == nil {
					t.Fatal("TypeGesture broadcast with nil Gesture")
				}
				return m.Gesture
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no gesture broadcast within 2s")
		}
	}
}

// TestTouchIngestEndToEnd: the bus half of the touch path -- touchLoop dials
// touch.sock, consumeFrames decodes TouchFrame ndjson into the recognizer,
// and the completed gesture reaches docks as TypeGesture in the cell the
// grid resolves the pixel to. touchd's half is covered by touchd's own
// replay test.
func TestTouchIngestEndToEnd(t *testing.T) {
	b, dockCh, p := touchTestBus(t)

	ln, err := net.Listen("unix", p.TouchSocket())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	connCh := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		connCh <- c
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		b.touchLoop(ctx)
	}()

	var src net.Conn
	select {
	case src = <-connCh:
	case <-time.After(5 * time.Second):
		t.Fatal("touchLoop never dialed the touch socket")
	}
	defer src.Close()

	// 320x18 cells over the default 2560x720 calibration: 8x40 px cells.
	// Digitizer (5147, 5073) scales to panel px (804, 380) = cell (100, 9).
	// Contact down then up 50ms later, still, under the long-press hold: a
	// tap at the down point. Real wall-clock TimeNS keeps the recognizer's
	// long-press deadline in the future while the frames are in flight.
	now := time.Now()
	enc := json.NewEncoder(src)
	if err := enc.Encode(proto.TouchFrame{TimeNS: now.UnixNano(), Count: 1,
		Contacts: []proto.TouchContact{{ID: 3, Tip: true, X: 5147, Y: 5073}}}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(proto.TouchFrame{TimeNS: now.Add(50 * time.Millisecond).UnixNano()}); err != nil {
		t.Fatal(err)
	}

	g := wantGestureMsg(t, dockCh)
	if g.Kind != proto.GestureTap || g.Col != 100 || g.Row != 9 {
		t.Fatalf("broadcast = %+v, want tap at cell 100,9", g)
	}

	cancel()
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("touchLoop did not exit on ctx cancel")
	}
}
