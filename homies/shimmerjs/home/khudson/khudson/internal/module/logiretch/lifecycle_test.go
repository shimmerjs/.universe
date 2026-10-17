package logiretch

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestDisconnectedFeedBacksOffAndCancellationStopsRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := testMod()
		m.sock = "fake"
		var dials atomic.Int32
		m.dial = func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			conn, peer := net.Pipe()
			peer.Close()
			return conn, nil
		}
		m.Poll(context.Background(), nil)
		synctest.Wait()
		time.Sleep(redial - time.Nanosecond)
		if dials.Load() != 1 {
			t.Fatal("EOF feed retried before backoff")
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if dials.Load() != 2 {
			t.Fatal("feed did not retry after backoff")
		}
		m.Close()
		time.Sleep(10 * redial)
		if dials.Load() != 2 {
			t.Fatal("closed feed retried")
		}
	})
}

func TestDemandOwnsReaderAndCloseIsTerminal(t *testing.T) {
	m := testMod()
	m.sock = "fake"
	peers := make(chan net.Conn, 3)
	var dials atomic.Int32
	m.dial = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		conn, peer := net.Pipe()
		peers <- peer
		return conn, nil
	}
	t.Cleanup(func() { m.Close() })
	pollCtx, cancelPoll := context.WithCancel(context.Background())
	m.SetDemand(true)
	m.Poll(pollCtx, nil)
	var peer net.Conn
	select {
	case peer = <-peers:
	case <-time.After(time.Second):
		t.Fatal("reader did not start")
	}
	defer peer.Close()
	cancelPoll()
	peer.SetWriteDeadline(time.Now().Add(time.Second))
	if err := json.NewEncoder(peer).Encode(wireLine{Kind: "input", Pressed: []string{"left"}}); err != nil {
		t.Fatalf("poll cancellation stopped shared reader: %v", err)
	}
	m.SetDemand(false)
	m.mu.Lock()
	up, held := m.sockUp, len(m.held)
	m.mu.Unlock()
	if up || held != 0 {
		t.Fatal("retired feed retained live state")
	}
	peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Fatal("demand loss left socket open")
	}
	m.Poll(context.Background(), nil)
	if dials.Load() != 1 {
		t.Fatal("undemanded Poll opened a feed")
	}
	m.SetDemand(true)
	m.Poll(context.Background(), nil)
	select {
	case peer2 := <-peers:
		defer peer2.Close()
	case <-time.After(time.Second):
		t.Fatal("renewed demand did not reconnect")
	}
	m.Close()
	m.SetDemand(true)
	m.Poll(context.Background(), nil)
	if dials.Load() != 2 {
		t.Fatal("terminal Close allowed restart")
	}
}

func TestCloseCancelsDial(t *testing.T) {
	m := testMod()
	m.sock = "fake"
	started := make(chan struct{})
	m.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	m.Poll(context.Background(), nil)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel and join dial")
	}
}
