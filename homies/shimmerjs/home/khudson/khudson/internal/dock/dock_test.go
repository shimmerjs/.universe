package dock

import (
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// newBusPipe is one fake bus endpoint: the returned conn is the dock side,
// got carries the first message decoded off the far end.
func newBusPipe(t *testing.T) (net.Conn, <-chan proto.Msg) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	got := make(chan proto.Msg, 1)
	go func() {
		var msg proto.Msg
		if err := json.NewDecoder(server).Decode(&msg); err == nil {
			got <- msg
		}
	}()
	return client, got
}

// attachFakeBus wires a connected fake bus onto m.
func attachFakeBus(t *testing.T, m *model) <-chan proto.Msg {
	t.Helper()
	conn, got := newBusPipe(t)
	m.bus, m.busConn = busConnected, conn
	return got
}

// wantBusMsg is the far end's next message, or a fatal timeout.
func wantBusMsg(t *testing.T, got <-chan proto.Msg) proto.Msg {
	t.Helper()
	select {
	case msg := <-got:
		return msg
	case <-time.After(time.Second):
		t.Fatal("no message reached the bus")
		return proto.Msg{}
	}
}

// A TypeReload replaces the dock's config and re-derives everything that
// hangs off it -- the composed frame drops and the next View renders the new
// strip entries and regions -- while runtime-only state (kbBoard, palette,
// caffeinate, taps) survives.
func TestReloadRerendersNewConfig(t *testing.T) {
	m := newHomeModel(320, 18)
	m.View()
	if !m.homeCache.ok {
		t.Fatal("first frame did not prime the cache")
	}
	m.caffeinate = "on"
	m.taps = 7
	m.palette = palette{"background": "#232a2e"}
	m.kbBoard = &keyboard.Board{Title: "ml", Layers: []keyboard.Layer{{Title: "base"}}}
	m.kbLoaded = true
	board := m.kbBoard

	// nil-guarded: a config-less reload changes nothing
	before := m.cfg
	m.handleBusMsg(proto.Msg{Type: proto.TypeReload})
	if m.cfg != before {
		t.Fatal("config-less reload replaced the config")
	}

	cfgB := homeTestConfig()
	cfgB.Strip = &config.Strip{Entries: []config.StripEntry{{Label: "beta", Target: "hub"}}}
	cfgB.Layouts["home"] = config.Layout{Kind: "home", Regions: []config.Region{
		{Widget: "disk", Edge: "fill"},
	}}
	m.handleBusMsg(proto.Msg{Type: proto.TypeReload, Config: cfgB})

	if m.cfg != cfgB {
		t.Fatal("reload did not install the new config")
	}
	if m.layout != "home" {
		t.Fatalf("layout = %q, want home", m.layout)
	}
	if m.homeCache.ok {
		t.Fatal("reload kept the composed frame")
	}
	content := m.View().Content
	if !strings.Contains(content, "beta") {
		t.Error("new strip entry not rendered")
	}
	if !strings.Contains(content, "disk") {
		t.Error("new region not rendered")
	}
	if strings.Contains(content, "claude") {
		t.Error("old region survived the reload")
	}

	if m.kbBoard != board {
		t.Fatal("kbBoard did not survive the reload")
	}
	if m.caffeinate != "on" {
		t.Fatalf("caffeinate = %q, want on", m.caffeinate)
	}
	if _, ok := m.palette.color("background"); !ok {
		t.Fatal("palette did not survive the reload")
	}
	if m.taps != 7 {
		t.Fatalf("taps = %d, want 7", m.taps)
	}
}

// An unknown layout from the bus (config skew) must not switch or freeze
// silently: the current layout stays and the strip surfaces the skew.
func TestUnknownLayoutIsLoud(t *testing.T) {
	m := newHomeModel(320, 18)
	m.handleBusMsg(proto.Msg{Type: proto.TypeLayout, Layout: "only-in-B"})
	if m.layout != "home" {
		t.Fatalf("layout = %q, want home (unknown layout must not switch)", m.layout)
	}
	if !strings.Contains(m.lastGst, "only-in-B") {
		t.Fatalf("lastGst = %q, want the unknown layout named", m.lastGst)
	}
	if v := m.View(); !strings.Contains(v.Content, "only-in-B") {
		t.Error("strip does not surface the unknown layout")
	}
}

// Non-home layouts render full-width: the strip's persistent home icon is
// the only return affordance, so no per-view column reserves body cells.
// The hit table is locked in full -- it is how touch works.
func TestNonHomeLayoutFullWidth(t *testing.T) {
	m := newHomeModel(120, 20)
	m.cfg.Layouts["grid"] = config.Layout{Kind: "dock-grid"}
	m.layout = "grid"
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != 20 {
		t.Fatalf("view lines = %d, want 20", len(lines))
	}
	for i, l := range lines[:18] {
		if w := lipgloss.Width(l); w != 120 {
			t.Errorf("line %d width = %d, want 120 (full width, no affordance column)", i, w)
		}
	}
	for i, l := range lines[18:] {
		if c := stripCells(l); c != 120 {
			t.Errorf("strip row %d = %d cells, want 120", i, c)
		}
	}

	// full hit-table lock: only the status strip's chrome (home icon +
	// whole-strip consume; no strip config here)
	want := []rect{
		{0, 18, 3, 2},   // strip: home icon glyph
		{0, 18, 120, 2}, // strip: whole-strip consume rect
	}
	if len(m.hits) != len(want) {
		t.Fatalf("hits = %d, want %d", len(m.hits), len(want))
	}
	for i, w := range want {
		if m.hits[i].area != w {
			t.Errorf("hit %d area = %+v, want %+v", i, m.hits[i].area, w)
		}
	}

	// tapping the strip's home icon goes home (bus absent: local switch)
	if !m.resolveTap(1, 19) {
		t.Fatal("tap on the strip home icon not consumed")
	}
	if m.layout != "home" {
		t.Fatalf("layout = %q, want home", m.layout)
	}
}

// The keyboard view renders full-width too; the strip's home icon is its
// return path.
func TestKeyboardViewFullWidth(t *testing.T) {
	m := kbModel(t, 196, 24)
	m.cfg.Layouts["main"] = config.Layout{Kind: "home"}
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != 24 {
		t.Fatalf("view lines = %d, want 24", len(lines))
	}
	for i, l := range lines[:22] {
		if w := lipgloss.Width(l); w != 196 {
			t.Errorf("line %d width = %d, want 196 (full width)", i, w)
		}
	}
	for i, l := range lines[22:] {
		if c := stripCells(l); c != 196 {
			t.Errorf("strip row %d = %d cells, want 196", i, c)
		}
	}
	for _, h := range m.hits {
		if h.area.y < 22 && h.area.x+h.area.w > 193 && h.area.w == 3 {
			t.Fatalf("keyboard view still carries an affordance column at %+v", h.area)
		}
	}
	if !m.resolveTap(1, 23) {
		t.Fatal("tap on the strip home icon not consumed")
	}
	if m.layout != "main" {
		t.Fatalf("layout = %q, want main (home by kind)", m.layout)
	}
}

// The status strip can never wrap the frame: overlong layout names and
// gesture labels truncate to the dock width. The two strip rows carry the
// scaled home-icon run, so they measure by the hand budget, not lipgloss.
func TestStripNeverExceedsWidth(t *testing.T) {
	m := newHomeModel(60, 10)
	m.lastGst = strings.Repeat("swipe-", 20)
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != 10 {
		t.Fatalf("view lines = %d, want 10 (strip wrapped?)", len(lines))
	}
	for i, l := range lines[:8] {
		if w := lipgloss.Width(l); w != 60 {
			t.Errorf("line %d width = %d, want 60", i, w)
		}
	}
	for i, l := range lines[8:] {
		if c := stripCells(l); c != 60 {
			t.Errorf("strip row %d = %d cells, want 60", i, c)
		}
	}
}

// Bus transitions and a config-skew broadcast are chrome state (the strip
// cup renders them): each drops the composed home frame exactly once;
// retry refires and repeat skews keep it.
func TestBusAndSkewInvalidateHomeCache(t *testing.T) {
	m := newHomeModel(320, 18)
	m.View()
	if !m.homeCache.ok {
		t.Fatal("first frame did not prime the cache")
	}
	m.Update(busGoneMsg{}) // already absent: no transition
	if !m.homeCache.ok {
		t.Fatal("no-transition busGone dropped the cache")
	}
	conn, _ := newBusPipe(t)
	m.Update(busConnectedMsg{conn: conn, ch: make(chan proto.Msg)})
	if m.homeCache.ok {
		t.Fatal("bus connect kept the cache")
	}
	m.View()
	m.Update(busGoneMsg{})
	if m.homeCache.ok {
		t.Fatal("bus loss kept the cache")
	}
	m.View()
	m.Update(busGoneMsg{}) // the ~2s retry refire while absent
	if !m.homeCache.ok {
		t.Fatal("busGone retry dropped the cache")
	}

	m.handleBusMsg(proto.Msg{Type: proto.TypeLayout, Layout: "only-in-B"})
	if m.homeCache.ok {
		t.Fatal("config skew kept the cache")
	}
	if !m.skew {
		t.Fatal("skew flag not set")
	}
	m.View()
	m.handleBusMsg(proto.Msg{Type: proto.TypeLayout, Layout: "only-in-B"})
	if !m.homeCache.ok {
		t.Fatal("repeat skew dropped the cache")
	}
	m.handleBusMsg(proto.Msg{Type: proto.TypeLayout, Layout: "hub"})
	if m.skew {
		t.Fatal("successful switch did not clear the skew flag")
	}
}

// A resize that lands while the dial Cmd is in flight must reach the bus:
// connect re-asserts the CURRENT grid, not the dims captured at dial time.
func TestBusConnectedReassertsCurrentGrid(t *testing.T) {
	m := newHomeModel(0, 0)
	if _, cmd := m.Update(tea.WindowSizeMsg{Width: 100, Height: 20}); cmd == nil {
		t.Fatal("first resize did not arm the bus dial")
	}
	// bus not connected yet: this resize has nowhere to go
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 24})

	conn, got := newBusPipe(t)
	m.Update(busConnectedMsg{gen: m.busGen, conn: conn, ch: make(chan proto.Msg)})
	msg := wantBusMsg(t, got)
	if msg.Type != proto.TypeGrid || msg.Cols != 200 || msg.Rows != 24 {
		t.Fatalf("got %s %dx%d, want grid 200x24", msg.Type, msg.Cols, msg.Rows)
	}
	// the panel region is the whole body (no outer frame): width x
	// height-stripH; the bus sizes scraped windows and the recognizer to it
	if msg.PanelCols != 200 || msg.PanelRows != 24-stripH {
		t.Fatalf("panel region %dx%d, want %dx%d (full body)",
			msg.PanelCols, msg.PanelRows, 200, 24-stripH)
	}
}

// A replacement bus connection closes the conn it displaces (EOF on its
// peer), and a stale generation's busGoneMsg -- the displaced reader dying
// -- must not tear down the healthy replacement.
func TestBusDialGenerations(t *testing.T) {
	m := newHomeModel(320, 18)
	conn1, peer1 := net.Pipe()
	conn2, peer2 := net.Pipe()
	t.Cleanup(func() { conn1.Close(); peer1.Close(); conn2.Close(); peer2.Close() })
	// drain the connect handlers' grid re-asserts so the pipes never block
	go func() { _, _ = io.Copy(io.Discard, peer1) }()
	go func() { _, _ = io.Copy(io.Discard, peer2) }()

	m.busGen = 1
	m.Update(busConnectedMsg{gen: 1, conn: conn1, ch: make(chan proto.Msg)})
	if m.bus != busConnected || m.busConn != conn1 {
		t.Fatal("gen-1 connect did not install conn1")
	}

	m.busGen = 2
	m.Update(busConnectedMsg{gen: 2, conn: conn2, ch: make(chan proto.Msg)})
	if m.busConn != conn2 {
		t.Fatal("gen-2 connect did not install conn2")
	}
	peer1.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer1.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("displaced conn read = %v, want EOF", err)
	}

	// the displaced reader's gone message carries the old gen: ignored
	m.Update(busGoneMsg{gen: 1, err: io.EOF})
	if m.bus != busConnected || m.busConn != conn2 {
		t.Fatalf("stale busGone tore down the live conn (bus=%v)", m.bus)
	}
	// conn2 stays open: its peer times out instead of seeing EOF
	peer2.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := peer2.Read(make([]byte, 1)); err == io.EOF {
		t.Fatal("stale busGone closed the live conn")
	}

	// a stale dial winning late is closed, never adopted
	conn3, peer3 := net.Pipe()
	t.Cleanup(func() { conn3.Close(); peer3.Close() })
	m.Update(busConnectedMsg{gen: 1, conn: conn3, ch: make(chan proto.Msg)})
	if m.busConn != conn2 {
		t.Fatal("stale connect displaced the live conn")
	}
	peer3.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer3.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("stale dial's conn read = %v, want EOF (closed)", err)
	}
}
