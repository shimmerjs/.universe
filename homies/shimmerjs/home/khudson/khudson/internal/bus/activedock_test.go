package bus

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/gesture"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// startDockWithHello runs serveDock against hello and drains the greeting.
func startDockWithHello(t *testing.T, b *Bus, hello proto.Msg) (client, server net.Conn, done chan struct{}) {
	t.Helper()
	client, server = net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	done = make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		b.serveDock(server, json.NewDecoder(server), hello)
	}()
	go func() {
		dec := json.NewDecoder(client)
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				return
			}
		}
	}()
	return client, server, done
}

func (b *Bus) panelRegion() (int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.panelCols, b.panelRows
}

// The singleton slot is first-come. A second dock renders, but its hello must
// not repoint the panel region or the recognizer at its own grid -- under
// last-hello-wins a reconnecting dock stole the live surface's taps.
func TestSecondDockCannotStompSingletons(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.readGrace = 2 * time.Second

	_, first, _ := startDockWithHello(t, b, proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Protocol: proto.Version, Capabilities: testViewCaps(),
		Cols: 320, Rows: 18, PanelCols: 196, PanelRows: 24})
	if !pollUntil(2*time.Second, func() bool { c, r := b.panelRegion(); return c == 196 && r == 24 }) {
		t.Fatal("the first dock must own the panel region")
	}
	b.mu.Lock()
	active := b.activeDock == first
	b.mu.Unlock()
	if !active {
		t.Fatal("the first dock must hold the slot")
	}

	second, _, _ := startDockWithHello(t, b, proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Protocol: proto.Version, Capabilities: testViewCaps(),
		Cols: 80, Rows: 24, PanelCols: 80, PanelRows: 17})
	if !pollUntil(2*time.Second, func() bool { b.mu.Lock(); defer b.mu.Unlock(); return len(b.docks) == 2 }) {
		t.Fatal("the second dock never registered")
	}
	if c, r := b.panelRegion(); c != 196 || r != 24 {
		t.Fatalf("panel region = %dx%d, want the active dock's 196x24", c, r)
	}

	// a resize from the parked dock is refused for the same reason
	if err := json.NewEncoder(second).Encode(proto.Msg{Type: proto.TypeGrid,
		Cols: 100, Rows: 20, PanelCols: 100, PanelRows: 20}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if c, r := b.panelRegion(); c != 196 || r != 24 {
		t.Fatalf("a parked dock's resize moved the panel region to %dx%d", c, r)
	}
}

// The slot frees on the owner's disconnect, and a survivor claims it with its
// next resize -- otherwise the last dock standing could never point the
// recognizer at itself.
func TestSurvivorClaimsTheFreedSlot(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.readGrace = 2 * time.Second

	firstClient, _, firstDone := startDockWithHello(t, b, proto.Msg{Type: proto.TypeHello,
		Role: proto.RoleDock, Protocol: proto.Version, Capabilities: testViewCaps(), Cols: 320, Rows: 18, PanelCols: 196, PanelRows: 24})
	if !pollUntil(2*time.Second, func() bool { c, _ := b.panelRegion(); return c == 196 }) {
		t.Fatal("the first dock must own the panel region")
	}
	second, _, _ := startDockWithHello(t, b, proto.Msg{Type: proto.TypeHello,
		Role: proto.RoleDock, Protocol: proto.Version, Capabilities: testViewCaps(), Cols: 80, Rows: 24, PanelCols: 80, PanelRows: 17})

	firstClient.Close()
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the first dock never disconnected")
	}
	if err := json.NewEncoder(second).Encode(proto.Msg{Type: proto.TypeGrid,
		Cols: 80, Rows: 24, PanelCols: 80, PanelRows: 17}); err != nil {
		t.Fatal(err)
	}
	if !pollUntil(2*time.Second, func() bool { c, r := b.panelRegion(); return c == 80 && r == 17 }) {
		c, r := b.panelRegion()
		t.Fatalf("panel region = %dx%d, want the survivor's 80x17", c, r)
	}
}

// A gesture resolves against the ACTIVE dock's grid, so it goes to that dock
// alone; the same cell on a parked dock with another layout is a phantom tap.
func TestGesturesReachOnlyTheActiveDock(t *testing.T) {
	b, activeCh, _ := touchTestBus(t)
	parkedCh := addDecodingDock(t, b)

	b.emit([]gesture.Event{gesture.Tap{Pos: gesture.Point{Col: 7, Row: 3}}})

	g := wantGestureMsg(t, activeCh)
	if g.Kind != proto.GestureTap || g.Col != 7 || g.Row != 3 {
		t.Fatalf("active dock got %+v, want a tap at 7,3", g)
	}
	select {
	case m := <-parkedCh:
		if m.Type == proto.TypeGesture {
			t.Fatalf("a parked dock received a gesture: %+v", m.Gesture)
		}
	case <-time.After(200 * time.Millisecond):
	}
}
