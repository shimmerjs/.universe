package dock

// The modal popover: the tap gate's three regions, the destructive-confirm
// two-tap, the long-press openers (rail tile + widget-box row), and the
// overlay-OPEN golden on the composited Canvas path.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// busSink wires a connected fake bus onto m and streams every message the
// dock writes (newBusPipe decodes only the first).
func busSink(t *testing.T, m *model) <-chan proto.Msg {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	msgs := make(chan proto.Msg, 16)
	go func() {
		dec := json.NewDecoder(server)
		for {
			var msg proto.Msg
			if err := dec.Decode(&msg); err != nil {
				close(msgs)
				return
			}
			msgs <- msg
		}
	}()
	m.bus, m.busConn = busConnected, client
	return msgs
}

// wantNoBusMsg asserts nothing reached the bus.
func wantNoBusMsg(t *testing.T, msgs <-chan proto.Msg) {
	t.Helper()
	select {
	case msg := <-msgs:
		t.Fatalf("unexpected bus message %s %v", msg.Type, msg.Argv)
	case <-time.After(50 * time.Millisecond):
	}
}

// railMenu is a quit/force-quit pair like dockmirror publishes.
func railMenu(id string) []module.Act {
	return []module.Act{
		{Label: "Quit", Argv: []string{"/inst/khudson", "ax", "quit", "--bundle", id}},
		{Label: "Force Quit", Argv: []string{"/inst/khudson", "ax", "force-quit", "--bundle", id}, Destructive: true},
	}
}

// overlayModel is a 196x24 home model whose rail carries two app tiles with
// menus; View() has run, so the base hit table is live.
func overlayModel(t *testing.T) *model {
	t.Helper()
	m := newHomeModel(196, 24)
	m.widgetData["dock-rail"] = module.Data{Title: "dock", Rows: []module.Row{
		{Kind: module.RowText, Text: "Safari", Act: []string{"open", "-a", "Safari"},
			Menu: railMenu("com.apple.Safari")},
		{Kind: module.RowText, Text: "Mail", Act: []string{"open", "-a", "Mail"},
			Menu: railMenu("com.apple.mail")},
	}}
	m.View()
	if len(m.hits) == 0 {
		t.Fatal("no base hits after View")
	}
	return m
}

// The modal tap gate's three regions: (a) an item tap fires and consumes,
// (b) an in-box non-item tap consumes and stays open, (c) an outside tap
// dismisses and consumes -- and a base hit under the outside tap (a rail
// tile) never fires through the overlay.
func TestOverlayModalTapGate(t *testing.T) {
	m := overlayModel(t)
	msgs := busSink(t, m)

	m.openOverlay("dock-rail", "safari", railMenu("com.apple.Safari"), 40, 5)
	o := m.overlay
	if o == nil {
		t.Fatal("openOverlay did not open")
	}

	// (b) box border: consumed, stays open
	if !m.resolveTap(o.anchor.x, o.anchor.y) {
		t.Fatal("in-box tap not consumed")
	}
	if m.overlay == nil {
		t.Fatal("in-box non-item tap dismissed the menu")
	}
	wantNoBusMsg(t, msgs)

	// (c) outside, on the Safari rail tile (base hit at {0,0,9,3}): the
	// overlay consumes and dismisses; the tile must not flash or fire
	if !m.resolveTap(1, 1) {
		t.Fatal("outside tap not consumed")
	}
	if m.overlay != nil {
		t.Fatal("outside tap kept the menu open")
	}
	if len(m.trayFlash) != 0 || m.flashArmed {
		t.Fatalf("base rail tile fired through the overlay: flash=%v", m.trayFlash)
	}
	wantNoBusMsg(t, msgs)
	// the same tap with the overlay closed DOES hit the tile (the gate, not
	// the geometry, was the barrier)
	if !m.resolveTap(1, 1) {
		t.Fatal("tile tap not consumed after dismiss")
	}
	if msg := wantBusMsg(t, msgs); msg.Type != proto.TypeRowAct || !slices.Equal(msg.Argv, []string{"open", "-a", "Safari"}) {
		t.Fatalf("tile act = %s %v, want the open argv", msg.Type, msg.Argv)
	}

	// (a) item tap: the non-destructive Quit fires immediately and closes
	m.openOverlay("dock-rail", "safari", railMenu("com.apple.Safari"), 40, 5)
	it := m.overlay.items[0]
	if !m.resolveTap(it.area.x+1, it.area.y+1) {
		t.Fatal("item tap not consumed")
	}
	msg := wantBusMsg(t, msgs)
	if msg.Type != proto.TypeRowAct || msg.Widget != "dock-rail" ||
		!slices.Equal(msg.Argv, []string{"/inst/khudson", "ax", "quit", "--bundle", "com.apple.Safari"}) {
		t.Fatalf("item act = %s %s %v, want the published quit argv", msg.Type, msg.Widget, msg.Argv)
	}
	if m.overlay != nil {
		t.Fatal("item fire kept the menu open")
	}
}

// Confirm is structural: the destructive force-quit arms on the first tap
// (no exec), re-renders a confirm target, and execs only on a second
// explicit tap on the Confirm rect; the non-destructive quit never arms.
func TestOverlayConfirmTwoTap(t *testing.T) {
	m := overlayModel(t)
	msgs := busSink(t, m)
	m.openOverlay("dock-rail", "safari", railMenu("com.apple.Safari"), 40, 5)
	fq := m.overlay.items[1]

	// first tap arms, nothing execs
	if !m.resolveTap(fq.area.x+1, fq.area.y+1) {
		t.Fatal("force-quit tap not consumed")
	}
	if m.overlay == nil {
		t.Fatal("arming dismissed the menu")
	}
	if c := m.overlay.confirm; c == nil || c.item != 1 {
		t.Fatalf("confirm = %+v, want the force-quit item armed", m.overlay.confirm)
	}
	if !strings.Contains(m.overlay.box, confirmPrefix+"Force Quit") {
		t.Fatal("armed box does not show the confirm target")
	}
	wantNoBusMsg(t, msgs)

	// a bounce tap inside the arm cooldown is consumed without firing
	if !m.resolveTap(fq.area.x+1, fq.area.y+1) {
		t.Fatal("bounce tap not consumed")
	}
	if m.overlay == nil || m.overlay.confirm == nil {
		t.Fatal("bounce tap fired or disarmed the confirm")
	}
	wantNoBusMsg(t, msgs)

	// past the cooldown, an explicit tap on the Confirm rect execs and
	// dismisses
	m.overlay.confirm.armedAt = m.overlay.confirm.armedAt.Add(-confirmArmDelay)
	if !m.resolveTap(fq.area.x+1, fq.area.y+1) {
		t.Fatal("confirm tap not consumed")
	}
	msg := wantBusMsg(t, msgs)
	if !slices.Equal(msg.Argv, []string{"/inst/khudson", "ax", "force-quit", "--bundle", "com.apple.Safari"}) {
		t.Fatalf("confirmed act argv = %v, want force-quit", msg.Argv)
	}
	if m.overlay != nil {
		t.Fatal("confirmed exec kept the menu open")
	}

	// quit stays single-tap even with a stale arm on the other item
	m.openOverlay("dock-rail", "safari", railMenu("com.apple.Safari"), 40, 5)
	fq = m.overlay.items[1]
	m.resolveTap(fq.area.x+1, fq.area.y+1) // arm force-quit
	q := m.overlay.items[0]
	if !m.resolveTap(q.area.x+1, q.area.y+1) {
		t.Fatal("quit tap not consumed")
	}
	if msg := wantBusMsg(t, msgs); !slices.Equal(msg.Argv, []string{"/inst/khudson", "ax", "quit", "--bundle", "com.apple.Safari"}) {
		t.Fatalf("quit argv = %v", msg.Argv)
	}
	if m.overlay != nil {
		t.Fatal("quit fire kept the menu open")
	}
}

// The long-press bridge: a rail-tile press opens that tile's menu anchored
// at the press; a second long-press while open closes and reopens at the
// new press, dropping any pending confirm; a press on a menu-less region
// only closes.
func TestLongPressOpensMenuAndReanchors(t *testing.T) {
	m := overlayModel(t)
	press := func(x, y int) {
		m.handleBusMsg(proto.Msg{Type: proto.TypeGesture,
			Gesture: &proto.Gesture{Kind: proto.GestureLongPress, Col: x, Row: y}})
	}

	press(4, 1) // Safari tile {0,0,9,3}
	if m.overlay == nil {
		t.Fatal("rail long-press did not open the menu")
	}
	if m.overlay.anchor.x != 4 || m.overlay.anchor.y != 1 {
		t.Fatalf("anchor = %+v, want the press cell", m.overlay.anchor)
	}
	if !slices.Equal(m.overlay.items[0].argv, []string{"/inst/khudson", "ax", "quit", "--bundle", "com.apple.Safari"}) {
		t.Fatalf("menu argv = %v, want Safari's", m.overlay.items[0].argv)
	}

	// arm the confirm, then re-press on the Mail tile: fresh menu, fresh
	// anchor, no pending confirm
	fq := m.overlay.items[1]
	m.resolveTap(fq.area.x+1, fq.area.y+1)
	if m.overlay.confirm == nil {
		t.Fatal("confirm did not arm")
	}
	press(14, 1) // Mail tile {10,0,9,3}
	if m.overlay == nil {
		t.Fatal("second long-press did not reopen")
	}
	if m.overlay.anchor.x != 14 {
		t.Fatalf("anchor.x = %d, want the new press column", m.overlay.anchor.x)
	}
	if m.overlay.confirm != nil {
		t.Fatal("re-anchor kept the pending confirm")
	}
	if !slices.Equal(m.overlay.items[0].argv, []string{"/inst/khudson", "ax", "quit", "--bundle", "com.apple.mail"}) {
		t.Fatalf("menu argv = %v, want Mail's", m.overlay.items[0].argv)
	}

	// a long-press with no menu under it (the strip) closes without reopening
	press(4, 23)
	if m.overlay != nil {
		t.Fatal("menu survived a long-press on a menu-less region")
	}
}

// The gestures-driver path delivers a touch long-press as a right click:
// it must open the same menu tier as a recognizer LongPress (left clicks
// stay taps), or the menus are unreachable while the driver owns the
// digitizer.
func TestRightClickOpensMenu(t *testing.T) {
	m := overlayModel(t)
	m.Update(tea.MouseClickMsg{X: 4, Y: 1, Button: tea.MouseRight})
	if m.overlay == nil {
		t.Fatal("right click did not open the menu")
	}
	if m.overlay.anchor.x != 4 || m.overlay.anchor.y != 1 {
		t.Fatalf("anchor = %+v, want the click cell", m.overlay.anchor)
	}
	if !slices.Equal(m.overlay.items[0].argv, []string{"/inst/khudson", "ax", "quit", "--bundle", "com.apple.Safari"}) {
		t.Fatalf("menu argv = %v, want Safari's", m.overlay.items[0].argv)
	}
	m.Update(tea.MouseClickMsg{X: 4, Y: 23, Button: tea.MouseRight})
	if m.overlay != nil {
		t.Fatal("menu survived a right click on a menu-less region")
	}
	m.Update(tea.MouseClickMsg{X: 4, Y: 1, Button: tea.MouseLeft})
	if m.overlay != nil {
		t.Fatal("left click opened a menu; it must stay a tap")
	}
}

// The widget-box row path: a long-press on a titled-region row with a Menu
// opens it (menus ride renderRows' parallel table, MinHeight-replicated);
// rows without one stay inert.
func TestRowMenuLongPressInWidgetBox(t *testing.T) {
	m := newHomeModel(196, 24)
	m.widgetData["cpumem"] = module.Data{Rows: []module.Row{
		{Kind: module.RowText, Text: "hot", Menu: []module.Act{
			{Label: "Cool", Argv: []string{"/inst/khudson", "cool"}},
		}},
		{Kind: module.RowText, Text: "plain"},
	}}
	m.View()

	// cpumem fill region is {20,10,82,12}; content starts at (21,11)
	if !m.resolveLongPress(30, 11) {
		t.Fatal("row long-press not consumed")
	}
	if m.overlay == nil {
		t.Fatal("menu row long-press did not open")
	}
	if m.overlay.widget != "cpumem" || !slices.Equal(m.overlay.items[0].argv, []string{"/inst/khudson", "cool"}) {
		t.Fatalf("overlay = %q %v, want the cpumem row menu", m.overlay.widget, m.overlay.items[0].argv)
	}

	// the menu-less row under the same region consumes but opens nothing
	if !m.resolveLongPress(30, 12) {
		t.Fatal("menu-less row long-press not consumed by its region")
	}
	if m.overlay != nil {
		t.Fatal("menu-less row opened a menu")
	}
}

// An anchor near the frame edge clamps the box fully on glass.
func TestOverlayClampsIntoFrame(t *testing.T) {
	m := overlayModel(t)
	m.openOverlay("dock-rail", "safari", railMenu("com.apple.Safari"), 195, 23)
	o := m.overlay
	if o == nil {
		t.Fatal("openOverlay did not open")
	}
	if o.anchor.x+o.anchor.w > m.width || o.anchor.y+o.anchor.h > m.height {
		t.Fatalf("box %+v overflows the %dx%d frame", o.anchor, m.width, m.height)
	}
}

// The overlay-OPEN golden: the full 196x24 frame with a rail menu open,
// composited through the real Canvas path (View), frozen against a captured
// fixture at CELL granularity -- Canvas.Render re-serializes SGR per cell
// run, so byte equality false-fails; the GraphemeWidth reference buffer is
// the mode-2027 glass measure. KHUDSON_OVERLAY_GOLDEN=update recaptures.
func TestOverlayOpenGolden(t *testing.T) {
	m := stripModel()
	m.now = time.Date(2026, 1, 5, 12, 34, 0, 0, time.UTC) // frozen strip clock
	m.widgetData["dock-rail"] = module.Data{Title: "dock", Rows: []module.Row{
		{Kind: module.RowText, Text: "Safari", Act: []string{"open", "-a", "Safari"},
			Menu: railMenu("com.apple.Safari")},
	}}
	m.View()
	m.handleBusMsg(proto.Msg{Type: proto.TypeGesture,
		Gesture: &proto.Gesture{Kind: proto.GestureLongPress, Col: 4, Row: 1}})
	if m.overlay == nil {
		t.Fatal("long-press did not open the menu")
	}
	out := m.View().Content
	if lines := strings.Count(out, "\n") + 1; lines != 24 {
		t.Fatalf("composited frame lines = %d, want 24", lines)
	}

	golden := filepath.Join("testdata", "overlay_open_196x24.golden")
	if os.Getenv("KHUDSON_OVERLAY_GOLDEN") == "update" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("golden fixture: %v (KHUDSON_OVERLAY_GOLDEN=update to capture)", err)
	}

	wantBuf := canvasRefBuffer(string(want), ansi.GraphemeWidth)
	gotBuf := canvasRefBuffer(out, ansi.GraphemeWidth)
	if diffs := canvasCellDiff(wantBuf, gotBuf, nil, t.Errorf); len(diffs) > 0 {
		t.Errorf("%d cells diverge from the overlay-open golden", len(diffs))
	}
}

// Lifecycle: layout switches, config reloads (both via resetLayout), and
// resizes dismiss an open menu -- an armed confirm must never float over a
// layout or geometry it was not anchored in.
func TestOverlayClearedOnLayoutAndResize(t *testing.T) {
	m := overlayModel(t)
	m.openOverlay("dock-rail", "safari", railMenu("com.apple.Safari"), 40, 5)
	fq := m.overlay.items[1]
	m.resolveTap(fq.area.x+1, fq.area.y+1) // arm the destructive confirm
	if m.overlay == nil || m.overlay.confirm == nil {
		t.Fatal("confirm did not arm")
	}
	m.resetLayout()
	if m.overlay != nil {
		t.Fatal("resetLayout kept the armed overlay")
	}

	m.openOverlay("dock-rail", "safari", railMenu("com.apple.Safari"), 40, 5)
	if m.overlay == nil {
		t.Fatal("reopen failed")
	}
	m.Update(tea.WindowSizeMsg{Width: 196, Height: 24})
	if m.overlay != nil {
		t.Fatal("resize kept the overlay")
	}
}
