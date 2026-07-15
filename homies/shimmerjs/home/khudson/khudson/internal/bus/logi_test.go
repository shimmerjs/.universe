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

// logiTestBus builds a minimal bus whose logi socket lives in a temp state
// root, plus a decoding dock.
func logiTestBus(t *testing.T) (*Bus, <-chan proto.Msg, paths.Paths) {
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

func wantLogiMsg(t *testing.T, ch <-chan proto.Msg) *proto.LogiState {
	t.Helper()
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				t.Fatal("dock connection closed before a logi broadcast")
			}
			if m.Type == proto.TypeLogiState {
				if m.Logi == nil {
					t.Fatal("TypeLogiState broadcast with nil Logi")
				}
				return m.Logi
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no logi broadcast within 2s")
		}
	}
}

// LogiState survives a JSON round-trip on the pinned wire shape.
func TestLogiStateRoundTrip(t *testing.T) {
	in := proto.LogiState{TimeNS: 12345, Kind: "mx-master-4", SoC: 73, Charging: true, State: 2}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out proto.LogiState
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round-trip = %+v, want %+v", out, in)
	}
	// the pinned field names must land on the wire, not the Go names
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"t", "kind", "soc", "charging", "state"} {
		if _, ok := m[k]; !ok {
			t.Errorf("wire is missing key %q: %s", k, raw)
		}
	}
}

// logiLoop survives starting before the socket exists (quiet retry), then
// forwards the latest LogiState to a dock verbatim, and broadcasts NO
// synthetic clear when the source hangs up (battery just goes stale).
func TestLogiLoopIngestNoClearOnLoss(t *testing.T) {
	b, dockCh, p := logiTestBus(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		b.logiLoop(ctx)
	}()

	// socket absent: the loop must retry, not crash; give it one beat
	time.Sleep(50 * time.Millisecond)

	ln, err := net.Listen("unix", p.LogiSocket())
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
		t.Fatal("logiLoop never dialed the logi socket")
	}
	defer src.Close()

	enc := json.NewEncoder(src)
	if err := enc.Encode(proto.LogiState{TimeNS: 9, Kind: "mx", SoC: 55, Charging: false, State: 1}); err != nil {
		t.Fatal(err)
	}
	if st := wantLogiMsg(t, dockCh); st.SoC != 55 || st.Kind != "mx" || st.Charging {
		t.Fatalf("broadcast = %+v, want soc 55 kind mx not charging", st)
	}

	// the cached frame is replayed to a fresh dock (greeting replay path)
	b.mu.Lock()
	cached := b.lastLogi
	b.mu.Unlock()
	if cached == nil || cached.SoC != 55 {
		t.Fatalf("lastLogi = %+v, want the cached soc-55 frame", cached)
	}

	// source hangs up: NO clear -- draining briefly must surface nothing
	src.Close()
	select {
	case m, ok := <-dockCh:
		if ok && m.Type == proto.TypeLogiState {
			t.Fatalf("disconnect broadcast a logi frame %+v, want silence (no synthetic clear)", m.Logi)
		}
	case <-time.After(300 * time.Millisecond):
		// silence is correct
	}

	cancel()
	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("logiLoop did not exit on ctx cancel")
	}
}

// consumeLogi unblocks on ctx cancellation even while the decoder is parked
// on a silent connection.
func TestConsumeLogiCtxCancel(t *testing.T) {
	b, _, _ := logiTestBus(t)
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.consumeLogi(ctx, server)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeLogi did not exit on ctx cancel")
	}
}

// A malformed line ends the connection (decoder poisoned) so logiLoop can
// redial -- never a wedged loop or a panic -- and still no synthetic clear.
func TestConsumeLogiBadLine(t *testing.T) {
	b, dockCh, _ := logiTestBus(t)
	client, server := net.Pipe()
	defer client.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		b.consumeLogi(context.Background(), server)
		server.Close()
	}()

	if _, err := client.Write([]byte("{\"t\":1,\"kind\":\"mx\",\"soc\":40,\"charging\":false,\"state\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if st := wantLogiMsg(t, dockCh); st.SoC != 40 {
		t.Fatalf("broadcast = %+v, want soc 40", st)
	}
	if _, err := client.Write([]byte("not json at all\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("consumeLogi did not exit on a poisoned stream")
	}
	// no clear is broadcast on the poisoned stream: the channel stays quiet
	select {
	case m, ok := <-dockCh:
		if ok && m.Type == proto.TypeLogiState {
			t.Fatalf("post-poison broadcast a logi frame %+v, want silence", m.Logi)
		}
	case <-time.After(200 * time.Millisecond):
	}
}
