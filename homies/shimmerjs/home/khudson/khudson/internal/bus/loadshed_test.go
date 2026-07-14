package bus

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// TestShouldShed pins the shed decision to the NumCPU*shedLoadFactor line;
// the threshold itself is exclusive (at-threshold keeps polling).
func TestShouldShed(t *testing.T) {
	tests := []struct {
		name  string
		load1 float64
		ncpu  int
		want  bool
	}{
		{"idle", 0.2, 8, false},
		{"busy but clearing", 7.9, 8, false},
		{"just under threshold", 11.99, 8, false},
		{"at threshold", 12.0, 8, false},
		{"just over threshold", 12.01, 8, true},
		{"saturated", 40, 8, true},
		{"single core under", 1.4, 1, false},
		{"single core over", 1.6, 1, true},
		{"zero load", 0, 8, false},
	}
	for _, tt := range tests {
		if got := shouldShed(tt.load1, tt.ncpu); got != tt.want {
			t.Errorf("%s: shouldShed(%v, %d) = %v, want %v", tt.name, tt.load1, tt.ncpu, got, tt.want)
		}
	}
}

// TestLoadShedder: reads are bounded to one per shedCheckEvery, state is
// cached between reads, and an unreadable loadavg fails open.
func TestLoadShedder(t *testing.T) {
	reads := 0
	load := 0.0
	var fail error
	s := loadShedder{read: func() (float64, error) { reads++; return load, fail }}
	over := float64(runtime.NumCPU())*shedLoadFactor + 1

	now := time.Unix(1000, 0)
	if s.active(now) {
		t.Fatal("shedding while idle")
	}
	s.active(now.Add(time.Second))
	if reads != 1 {
		t.Fatalf("reads = %d, want 1 (cached inside shedCheckEvery)", reads)
	}

	load = over
	now = now.Add(shedCheckEvery)
	if !s.active(now) {
		t.Fatal("not shedding above threshold")
	}
	if !s.active(now.Add(time.Second)) || reads != 2 {
		t.Fatalf("shed state not cached between reads (reads = %d)", reads)
	}

	load = 0
	now = now.Add(shedCheckEvery)
	if s.active(now) {
		t.Fatal("still shedding after load recovered")
	}

	load, fail = over, nil
	now = now.Add(shedCheckEvery)
	if !s.active(now) {
		t.Fatal("not shedding above threshold after recovery")
	}
	fail = errors.New("boom")
	now = now.Add(shedCheckEvery)
	if s.active(now) {
		t.Fatal("shedding with unreadable loadavg; must fail open")
	}
}

// TestShedSkipsScrape: a shed tick fires no exec scrape; the poll stays
// due and fires on the first non-shed tick.
func TestShedSkipsScrape(t *testing.T) {
	b, _, scr := schedTestBus(t)
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	b.schedulerPass(ctx, now, entries, busyCh, false, false)
	drainBusy(t, entries, busyCh)

	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false, true)
	if scr.count() != 0 {
		t.Fatalf("shed tick fired %d scrapes, want 0", scr.count())
	}

	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false, false)
	if scr.count() != 1 {
		t.Fatalf("post-shed tick fired %d scrapes, want 1 (poll stayed due)", scr.count())
	}
}

// plainMod is a minimal native module; essentialMod additionally opts out
// of load shedding via the essential marker.
type plainMod struct{ name string }

func (m *plainMod) Name() string { return m.name }
func (*plainMod) Poll(context.Context, map[string]any) (module.Data, error) {
	return module.Data{}, nil
}

type essentialMod struct{ plainMod }

func (*essentialMod) Essential() {}

// TestShedNativeEssentialOptOut: on a shed tick only the essential module
// polls; the sheddable one stays due and fires once shedding stops.
func TestShedNativeEssentialOptOut(t *testing.T) {
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"s": {ID: "s", Render: config.Render{Kind: "native", Module: "plain", Poll: "1s"}},
			"v": {ID: "v", Render: config.Render{Kind: "native", Module: "vital", Poll: "1s"}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"s", "v"}}},
		Layout:  "main",
	}
	b := &Bus{
		cfg:   cfg,
		reg:   NewRegistry(cfg),
		docks: make(map[net.Conn]*json.Encoder),
		sup:   &fakeSup{},
		mods: map[string]module.Module{
			"plain": &plainMod{name: "plain"},
			"vital": &essentialMod{plainMod{name: "vital"}},
		},
		snapshots: make(chan snapshotResult, 16),
		natives:   make(chan nativeResult, 16),
	}
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	b.schedulerPass(ctx, now, entries, busyCh, false, true)
	drainBusy(t, entries, busyCh)
	r := <-b.natives
	if r.id != "v" || r.err != nil {
		t.Fatalf("shed tick polled %q (err %v), want the essential widget v", r.id, r.err)
	}
	if entries["s"].busy {
		t.Fatal("sheddable module polled on a shed tick")
	}

	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false, false)
	drainBusy(t, entries, busyCh)
	r = <-b.natives
	if r.id != "s" || r.err != nil {
		t.Fatalf("post-shed tick polled %q (err %v), want the shed-deferred widget s", r.id, r.err)
	}
}
