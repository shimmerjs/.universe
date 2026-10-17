package bus

import (
	"net"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

func TestLegacyAndTerminalViewsCannotOwnPhysicalInput(t *testing.T) {
	for _, hello := range []proto.Msg{
		{Role: proto.RoleDock},
		{Role: proto.RoleDock, Protocol: proto.Version, Capabilities: &proto.ViewCapabilities{Layouts: []string{"home", "keyboard"}}},
	} {
		caps, err := negotiateView(hello)
		if err != nil {
			t.Fatal(err)
		}
		b, _, _ := schedTestBus(t)
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		b.dockCaps = map[net.Conn]proto.ViewCapabilities{server: caps}
		if b.claimActive(server) {
			t.Fatal("terminal or legacy view claimed Edge geometry")
		}
		if b.edgeAvailableLocked() {
			t.Fatal("terminal view enabled sleep assertions")
		}
	}
}

func TestUnsupportedRenderingDoesNotCreateDemand(t *testing.T) {
	b, _, _ := schedTestBus(t)
	addFakeDock(t, b)
	b.dockCaps = map[net.Conn]proto.ViewCapabilities{}
	for conn := range b.docks {
		b.dockCaps[conn] = proto.ViewCapabilities{Layouts: []string{"home", "keyboard"}, Edge: true}
	}
	if b.rendererWanted() {
		t.Fatal("shipped native view requested hidden renderer")
	}
	if err := b.setLayout(b.cfg.Layout); err == nil {
		t.Fatal("unsupported layout accepted")
	}
	for conn := range b.docks {
		b.dockCaps[conn] = proto.ViewCapabilities{Layouts: []string{b.cfg.Layouts[b.cfg.Layout].Kind}, Scraped: true}
	}
	if !b.rendererWanted() {
		t.Fatal("explicit scraped capability did not create demand")
	}
}

func TestUnsupportedProtocolRefused(t *testing.T) {
	for _, hello := range []proto.Msg{{Protocol: proto.Version + 1}, {Protocol: proto.Version}, {Protocol: proto.Version, Capabilities: &proto.ViewCapabilities{}}} {
		if _, err := negotiateView(hello); err == nil {
			t.Fatalf("accepted invalid hello: %+v", hello)
		}
	}
}

// Tests exercising the scraper use a hypothetical client which renders those
// frames. The shipping dock advertises only home and keyboard, without Scraped.
func testViewCaps() *proto.ViewCapabilities {
	return &proto.ViewCapabilities{Layouts: []string{"home", "keyboard", "dock-grid", "full-panel", "tray"}, Scraped: true, Edge: true}
}
