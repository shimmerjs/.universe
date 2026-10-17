package surface

import (
	"net"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
)

type fakeClient struct {
	writer *socketio.Writer
	Conn   net.Conn
}

func (c *fakeClient) Send(m proto.Msg) error { return c.writer.Send(m) }
func (c *fakeClient) Close() error           { c.writer.Close(); return nil }
func testBusClient(t *testing.T, conn net.Conn) *fakeClient {
	c := &fakeClient{writer: socketio.NewWriter(conn, 128, 2*time.Second, nil), Conn: conn}
	t.Cleanup(func() { c.Close(); c.writer.Wait() })
	return c
}
func TestUIUpdateDoesNotWaitForBusRead(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	m := newHomeModel(320, 18)
	m.bus, m.busConn = busConnected, testBusClient(t, conn)
	done := make(chan struct{})
	go func() {
		m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
		m.sendHeartbeat()
		m.navigateTo("home")
		m.sendCaffeinateToggle()
		m.sendRowAct("test", []string{"test"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("UI waited on an unreading bus")
	}
}

type fakeConnector struct{}

func (fakeConnector) Connect(int, string, proto.Msg) tea.Cmd { return func() tea.Msg { return nil } }
func (fakeConnector) Send(m proto.Msg, c Connection) error   { return c.Send(m) }
