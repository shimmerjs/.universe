package viewtransport

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/surface"
)

func testBusClient(t *testing.T, conn net.Conn) *busClient {
	t.Helper()
	c := newBusClient(conn)
	t.Cleanup(func() { c.Close(); c.wait() })
	return c
}

func TestCloseReleasesAbandonedReader(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	c := testBusClient(t, conn)
	// Fill the reader's delivery buffer and leave it trying to deliver one
	// more frame. Closing only the socket would not release that send.
	for i := 0; i <= cap(c.ch); i++ {
		peer.SetWriteDeadline(time.Now().Add(time.Second))
		if err := json.NewEncoder(peer).Encode(proto.Msg{Type: proto.TypePing}); err != nil {
			t.Fatal(err)
		}
	}
	c.Close()
	select {
	case <-c.done:
	case <-time.After(time.Second):
		t.Fatal("reader remained blocked delivering to an abandoned UI")
	}
}

func TestSessionClosesUnadoptedConnectionAndRejectsLateDial(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	s := newBusSession()
	defer s.close()
	s.dial = func(context.Context, string, string) (net.Conn, error) { return conn, nil }
	msg := s.connect(1, "fake", proto.Msg{Type: proto.TypeHello})()
	c, ok := msg.(surface.Connected)
	if !ok {
		t.Fatalf("connect = %#v", msg)
	}
	// Never pass c to Update: the program can quit between dial and delivery.
	s.close()
	select {
	case <-c.Conn.(*busClient).done:
	default:
		t.Fatal("session returned before joining unadopted client")
	}
	if _, ok := s.connect(2, "fake", proto.Msg{})().(surface.Disconnected); !ok {
		t.Fatal("closed session dialled again")
	}
}

func TestSessionExitCancelsInFlightDial(t *testing.T) {
	s := newBusSession()
	started := make(chan struct{})
	s.dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- s.connect(1, "fake", proto.Msg{})() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("dial did not start")
	}
	s.close()
	select {
	case msg := <-result:
		if gone, ok := msg.(surface.Disconnected); !ok || gone.Err != context.Canceled {
			t.Fatalf("dial result = %#v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("exit did not release dial command")
	}
}

func TestDeliveryCommitsBeforeWireAndRetainsOnDisconnect(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client, server := net.Pipe()
	c := newBusClient(client)
	defer func() { c.Close(); server.Close(); c.wait() }()
	d := newDeliveryWorker(store)
	defer d.close()
	m := proto.Msg{Type: proto.TypeRowAct, OperationID: "durable-action", Widget: "cpu", Argv: []string{"open", "x"}, ViewID: "view", ViewGeneration: 1}
	if err := d.submit(m, c); err != nil {
		t.Fatal(err)
	}
	_ = server.SetReadDeadline(time.Now().Add(time.Second))
	var got proto.Msg
	if err := json.NewDecoder(server).Decode(&got); err != nil {
		t.Fatal(err)
	}
	records, err := store.List(context.Background(), actionjournal.Pending)
	if err != nil || len(records) != 1 || records[0].ID != got.OperationID {
		t.Fatalf("wire preceded persistence: %v %v", records, err)
	}
	server.Close()
	// Reopening the database emulates client loss. Pending transient work must
	// remain recorded and be discarded by the next exclusive bus owner.
	if err := store.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, err = store.List(context.Background(), actionjournal.Discarded)
	if err != nil || len(records) != 1 {
		t.Fatalf("recovery: %v %v", records, err)
	}
}
