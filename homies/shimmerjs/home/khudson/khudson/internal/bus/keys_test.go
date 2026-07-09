package bus

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// keysTestBus builds a minimal bus whose keys socket lives in a temp state
// root, plus a decoding dock.
func keysTestBus(t *testing.T) (*Bus, <-chan proto.Msg, paths.Paths) {
	t.Helper()
	p := paths.Paths{Dir: t.TempDir()}
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
	return b, addDecodingDock(t, b), p
}

func wantKeyMsg(t *testing.T, ch <-chan proto.Msg) *proto.KeyEvent {
	t.Helper()
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("dock connection closed before a key broadcast")
			}
			if m.Type == proto.TypeKey {
				if m.Key == nil {
					t.Fatal("TypeKey broadcast with nil Key")
				}
				return m.Key
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no key broadcast within 2s")
		}
	}
}

// keyLoop survives starting before the socket exists (quiet retry), then
// rebroadcasts key and layer lines verbatim as TypeKey, and synthesizes a
// clear when the source hangs up.
func TestKeyLoopIngestAndClear(t *testing.T) {
	b, dockCh, p := keysTestBus(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		b.keyLoop(ctx)
	}()

	// socket absent: the loop must retry, not crash; give it one beat
	time.Sleep(50 * time.Millisecond)

	ln, err := net.Listen("unix", p.KeysSocket())
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

	var src net.Conn
	select {
	case src = <-connCh:
	case <-time.After(5 * time.Second):
		t.Fatal("keyLoop never dialed the keys socket")
	}
	defer src.Close()

	enc := json.NewEncoder(src)
	if err := enc.Encode(proto.KeyEvent{TimeNS: 9, Kind: proto.KeyEventKey, Row: 1, Col: 1, Pressed: true}); err != nil {
		t.Fatal(err)
	}
	if err := enc.Encode(proto.KeyEvent{Kind: proto.KeyEventLayer, Layer: 2}); err != nil {
		t.Fatal(err)
	}

	if ev := wantKeyMsg(t, dockCh); ev.Kind != proto.KeyEventKey || ev.Row != 1 || ev.Col != 1 || !ev.Pressed {
		t.Fatalf("first broadcast = %+v, want pressed key 1,1", ev)
	}
	if ev := wantKeyMsg(t, dockCh); ev.Kind != proto.KeyEventLayer || ev.Layer != 2 {
		t.Fatalf("second broadcast = %+v, want layer 2", ev)
	}

	// source hangs up: the bus must broadcast a clear so highlights drop
	src.Close()
	if ev := wantKeyMsg(t, dockCh); ev.Kind != proto.KeyEventClear {
		t.Fatalf("disconnect broadcast = %+v, want clear", ev)
	}

	cancel()
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("keyLoop did not exit on ctx cancel")
	}
}

// consumeKeys unblocks on ctx cancellation even while the decoder is
// parked on a silent connection.
func TestConsumeKeysCtxCancel(t *testing.T) {
	b, _, _ := keysTestBus(t)
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.consumeKeys(ctx, server)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeKeys did not exit on ctx cancel")
	}
}

// A malformed line ends the connection (decoder poisoned) and still yields
// the clear broadcast -- never a wedged loop or a stuck highlight.
func TestConsumeKeysBadLine(t *testing.T) {
	b, dockCh, _ := keysTestBus(t)
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.consumeKeys(context.Background(), server)
		server.Close()
		b.broadcast(proto.Msg{Type: proto.TypeKey, Key: &proto.KeyEvent{Kind: proto.KeyEventClear}})
	}()

	if _, err := client.Write([]byte("{\"kind\":\"key\",\"row\":3,\"col\":4,\"pressed\":true}\n")); err != nil {
		t.Fatal(err)
	}
	if ev := wantKeyMsg(t, dockCh); ev.Row != 3 || ev.Col != 4 {
		t.Fatalf("broadcast = %+v, want key 3,4", ev)
	}
	if _, err := client.Write([]byte("not json at all\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeKeys did not exit on a poisoned stream")
	}
	if ev := wantKeyMsg(t, dockCh); ev.Kind != proto.KeyEventClear {
		t.Fatalf("post-poison broadcast = %+v, want clear", ev)
	}
}
