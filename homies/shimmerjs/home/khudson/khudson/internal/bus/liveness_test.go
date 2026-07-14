package bus

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// startLivenessDock runs serveDock on the bus end of a pipe and drains
// everything the bus writes (greeting included), so no write ever blocks.
// The bus end closes when serveDock returns, mirroring serve()'s deferred
// conn.Close, so a post-reap client write fails instead of wedging.
func startLivenessDock(t *testing.T, b *Bus) (client, server net.Conn, done chan struct{}) {
	t.Helper()
	client, server = net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	done = make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
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

// A connected-but-silent dock is reaped at the read grace and its fan-out
// slot dropped: the write-side grace never fires for a peer that sends
// nothing, so before the read deadline a mute dock lingered forever.
func TestServeDockReapsSilentDock(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.readGrace = 50 * time.Millisecond
	_, server, done := startLivenessDock(t, b)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("silent dock was not reaped at the read grace")
	}
	b.mu.Lock()
	_, still := b.docks[server]
	n := len(b.docks)
	b.mu.Unlock()
	if still || n != 0 {
		t.Fatalf("reaped dock left fan-out state (%d docks)", n)
	}
}

// Heartbeat frames re-arm the read deadline: a dock pinging inside the
// grace stays registered well past it, and reaps once the pings stop.
func TestServeDockHeartbeatKeepsDockAlive(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.readGrace = 250 * time.Millisecond
	client, server, done := startLivenessDock(t, b)

	enc := json.NewEncoder(client)
	until := time.Now().Add(4 * b.readGrace)
	for time.Now().Before(until) {
		if err := enc.Encode(proto.Msg{Type: proto.TypePing}); err != nil {
			t.Fatalf("ping write failed: %v", err)
		}
		select {
		case <-done:
			t.Fatal("pinging dock was reaped inside the grace")
		case <-time.After(50 * time.Millisecond):
		}
	}
	b.mu.Lock()
	_, live := b.docks[server]
	b.mu.Unlock()
	if !live {
		t.Fatal("pinging dock lost its fan-out slot")
	}

	// silence past the grace: now the reap must land and clean up
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dock was not reaped after the pings stopped")
	}
	b.mu.Lock()
	n := len(b.docks)
	b.mu.Unlock()
	if n != 0 {
		t.Fatalf("reaped dock left fan-out state (%d docks)", n)
	}
}
