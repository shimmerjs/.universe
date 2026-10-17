package bus

import (
	"net"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
)

func viewTestBus(t *testing.T) (*Bus, net.Conn, net.Conn) {
	t.Helper()
	cfg := &config.Config{Layout: "home", Layouts: map[string]config.Layout{
		"home": {Kind: "home", Tiles: []string{"cpu"}}, "keys": {Kind: "keyboard"},
	}, Widgets: map[string]config.Widget{"cpu": {ID: "cpu", Render: config.Render{Kind: "native", Module: "cpumem"}}}}
	b := &Bus{cfg: cfg, reg: NewRegistry(cfg), docks: map[net.Conn]*socketio.Writer{}, dockCaps: map[net.Conn]proto.ViewCapabilities{}}
	addFakeDock(t, b)
	addFakeDock(t, b)
	var conns []net.Conn
	for conn := range b.docks {
		conns = append(conns, conn)
	}
	a, z := conns[0], conns[1]
	for _, conn := range []net.Conn{a, z} {
		caps := proto.ViewCapabilities{Layouts: []string{"home", "keyboard"}}
		b.dockCaps[conn] = caps
		b.addViewLocked(conn, proto.Msg{Protocol: proto.Version}, caps)
	}
	return b, a, z
}

func TestViewSelectionVisibilityAndLeases(t *testing.T) {
	b, a, z := viewTestBus(t)
	lease := func(conn net.Conn) proto.Msg {
		v := b.views[conn]
		return proto.Msg{ViewID: v.id, ViewGeneration: v.generation, Cmd: "layout", Arg: "keys"}
	}
	old := lease(a)
	if err := b.setViewLayout(old); err != nil {
		t.Fatal(err)
	}
	if b.views[z].layout != "home" || b.views[a].layout != "keys" {
		t.Fatal("view navigation changed another view")
	}
	if b.viewActionCurrentLocked(old) {
		t.Fatal("old navigation lease still live")
	}
	if !b.visibleWidgetsLocked()["cpu"] {
		t.Fatal("second view lost provider demand")
	}
	b.updateView(z, proto.Msg{ViewID: b.views[z].id, Hidden: true})
	if b.visibleWidgetsLocked()["cpu"] {
		t.Fatal("hidden view kept provider demand")
	}
	if got := b.relayDemandLocked(); got != [3]bool{false, true, true} {
		t.Fatalf("relays %v", got)
	}
	current := lease(a)
	b.updateView(a, proto.Msg{ViewID: current.ViewID, Hidden: true})
	b.updateView(a, proto.Msg{ViewID: current.ViewID, Hidden: false})
	if b.viewActionCurrentLocked(current) {
		t.Fatal("hide/show revived queued action")
	}
	current = lease(a)
	if b.bindViewAction(z, &current) {
		t.Fatal("lease accepted from another connection")
	}
	b.removeDockLocked(a)
	if b.viewActionCurrentLocked(current) {
		t.Fatal("disconnected view still owns actions")
	}
}

func TestViewLeaseCannotAddressOffscreenWidget(t *testing.T) {
	b, a, _ := viewTestBus(t)
	v := b.views[a]
	m := proto.Msg{ViewID: v.id, ViewGeneration: v.generation, Widget: "cpu"}
	if !b.viewActionCurrentLocked(m) {
		t.Fatal("visible widget refused")
	}
	v.layout = "keys"
	if b.viewActionCurrentLocked(m) {
		t.Fatal("offscreen widget accepted")
	}
	m.Widget = ""
	b.invalidateViewsLocked()
	if b.viewActionCurrentLocked(m) {
		t.Fatal("replacement kept lease")
	}
}
