package bus

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
	"github.com/shimmerjs/khudson/khudson/provider"
)

type demandMod struct {
	plainMod
	wanted bool
}

func (m *demandMod) SetDemand(wanted bool) { m.wanted = wanted }

func TestProviderDemandFollowsAllWidgetsAndLastClient(t *testing.T) {
	mod := &demandMod{}
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"a": {ID: "a", Render: config.Render{Kind: "native", Module: "shared"}},
			"b": {ID: "b", Render: config.Render{Kind: "native", Module: "shared"}},
		},
		Layouts: map[string]config.Layout{
			"a":     {Kind: "dock-grid", Tiles: []string{"a"}},
			"b":     {Kind: "dock-grid", Tiles: []string{"b"}},
			"empty": {Kind: "keyboard"},
		}, Layout: "a",
	}
	b := &Bus{cfg: cfg, reg: NewRegistry(cfg), mods: map[string]module.Module{"shared": mod}, docks: make(map[net.Conn]*socketio.Writer)}
	// Busy entries isolate demand reconciliation from polling; demand loss
	// must still reach a provider while its last Poll is in flight.
	entries := map[string]*schedEntry{"a": {busy: true}, "b": {busy: true}}
	pass := func(want bool) {
		t.Helper()
		b.schedulerPass(context.Background(), time.Now(), entries, make(chan busyDone, 2), false, false)
		if mod.wanted != want {
			t.Fatalf("demand = %v, want %v", mod.wanted, want)
		}
	}
	pass(false)
	addFakeDock(t, b)
	addFakeDock(t, b)
	pass(true)
	b.cfg.Layout = "b"
	pass(true)
	b.cfg.Layout = "empty"
	pass(false)
	b.cfg.Layout = "a"
	pass(true)
	b.pending = cfg
	pass(false)
	b.pending = nil
	for conn := range b.docks {
		delete(b.docks, conn)
		break
	}
	pass(true)
	clear(b.docks)
	pass(false)
}
