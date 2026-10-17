package socketio

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func awaitWriter(t *testing.T, w *Writer) {
	t.Helper()
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("writer did not stop")
	}
}

func TestStalledPeerTimesOut(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	w := NewWriter(conn, 2, 20*time.Millisecond, nil)
	defer w.Close()
	if err := w.Send("hello"); err != nil {
		t.Fatal(err)
	}
	awaitWriter(t, w)
	var ne net.Error
	if !errors.As(w.Err(), &ne) || !ne.Timeout() {
		t.Fatalf("got %v, want timeout", w.Err())
	}
}

func TestOverflowClosesWithoutWaitingForWriteDeadline(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	w := NewWriter(conn, 1, time.Hour, nil)
	defer w.Close()
	var err error
	for i := 0; i < 3; i++ {
		if err = w.Send(i); err != nil {
			break
		}
	}
	var bp BackpressureError
	if !errors.As(err, &bp) {
		t.Fatalf("got %v, want backpressure", err)
	}
	awaitWriter(t, w)
	if w.Send("late") == nil {
		t.Fatal("accepted message after disconnect")
	}
}

func TestQueuedMessagesAreImmutableAndOrdered(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	w := NewWriter(conn, 3, time.Second, nil)
	defer func() { w.Close(); w.Wait() }()
	value := map[string]int{"value": 1}
	if err := w.Send(value); err != nil {
		t.Fatal(err)
	}
	value["value"] = 2
	if err := w.Send(value); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(peer)
	peer.SetReadDeadline(time.Now().Add(time.Second))
	for _, want := range []int{1, 2} {
		var got map[string]int
		if err := dec.Decode(&got); err != nil {
			t.Fatal(err)
		}
		if got["value"] != want {
			t.Fatalf("got %v, want %d", got, want)
		}
	}
}

func TestCloseInterruptsWrite(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	w := NewWriter(conn, 1, time.Hour, nil)
	if err := w.Send("hello"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	awaitWriter(t, w)
	if !errors.Is(w.Err(), net.ErrClosed) {
		t.Fatalf("got %v", w.Err())
	}
}

func TestByteBudgetClosesStalledPeer(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	w := NewWriter(conn, 128, time.Hour, nil)
	defer w.Close()
	payload := strings.Repeat("x", MaxFrameBytes/2)
	var err error
	for range 4 {
		if err = w.Send(payload); err != nil {
			break
		}
	}
	var bp BackpressureError
	if !errors.As(err, &bp) {
		t.Fatalf("byte budget not enforced: %v", err)
	}
	awaitWriter(t, w)
}
func TestOversizeOutputRejected(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	w := NewWriter(conn, 128, time.Hour, nil)
	defer w.Close()
	var size FrameSizeError
	if err := w.Send(strings.Repeat("x", MaxFrameBytes)); !errors.As(err, &size) {
		t.Fatalf("oversize: %v", err)
	}
	awaitWriter(t, w)
}
