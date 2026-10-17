package bus

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
)

func testDockWriter(t *testing.T, conn net.Conn) *socketio.Writer {
	t.Helper()
	w := socketio.NewWriter(conn, 128, dockWriteGrace, nil)
	t.Cleanup(func() { w.Close(); w.Wait() })
	return w
}

func TestStalledDockDoesNotHoldBusLock(t *testing.T) {
	b, _, _ := schedTestBus(t)
	stalled, peer := net.Pipe()
	defer peer.Close()
	b.docks[stalled] = testDockWriter(t, stalled)
	healthy := addDecodingDock(t, b)
	done := make(chan struct{})
	go func() {
		b.broadcast(proto.Msg{Type: proto.TypeNotice, Error: "still responsive"})
		b.mu.Lock()
		b.mu.Unlock()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stalled peer blocked bus state access")
	}
	select {
	case m := <-healthy:
		if m.Type != proto.TypeNotice {
			t.Fatalf("got %q", m.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled peer blocked healthy peer")
	}
}

func TestSilentHandshakeExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, peer := net.Pipe()
		defer peer.Close()
		b := &Bus{}
		done := make(chan struct{})
		go func() { b.serve(context.Background(), server); close(done) }()
		synctest.Wait()
		time.Sleep(4 * time.Second)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("silent handshake retained connection")
		}
	})
}
