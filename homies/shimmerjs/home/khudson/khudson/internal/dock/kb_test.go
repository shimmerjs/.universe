package dock

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/kbview"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/keydict"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

const kbFixturePath = "../keyboard/testdata/layout.json"

// kbFixtureLayout loads the aw4 layout payload fixture.
func kbFixtureLayout(t testing.TB) *oryx.Layout {
	t.Helper()
	raw, err := os.ReadFile(kbFixturePath)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	var l oryx.Layout
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatalf("fixture decode: %v", err)
	}
	return &l
}

// kbInertLoader never sees a board, a store, or the network: for models
// whose kbBoard is preloaded (or deliberately absent) and must not be
// swapped by ensureBoard.
func kbInertLoader(t testing.TB) *keyboard.Loader {
	t.Helper()
	return &keyboard.Loader{
		Poller: &usbserial.Poller{TTL: time.Hour, Read: func(context.Context) (usbserial.Identity, error) {
			return usbserial.Identity{}, usbserial.ErrNotPresent
		}},
		GenDir: t.TempDir(),
	}
}

// fixture thumb legends: the dictionary maps the left wide key (KC_DOWN)
// and right wide key (KC_UP) to arrow glyphs.
const (
	kbDownArrow = "↓"
	kbUpArrow   = "↑"
)

// kbModel is a keyboard-layout dock model preloaded with the fixture board
// and an inert loader, so the view never touches the serial, the network,
// or a real store (a fixture board must never be swapped for whatever the
// machine happens to have deployed).
// testing.TB so benchmarks share the fixture.
func kbModel(t testing.TB, w, h int) *model {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // isolate paths.Resolve state
	m := &model{
		cfg: &config.Config{
			Widgets: map[string]config.Widget{},
			Layouts: map[string]config.Layout{"keyboard": {Kind: "keyboard"}},
			Layout:  "keyboard",
		},
		layout:     "keyboard",
		width:      w,
		height:     h,
		now:        time.Now(),
		widgetData: map[string]module.Data{},
		widgetErr:  map[string]string{},
		sty:        buildStyles(day),
	}
	m.kbBoard = keyboard.FromLayout(kbFixtureLayout(t), keydict.Embedded())
	m.kbLoader = kbInertLoader(t)
	return m
}

// keyMsg builds one TypeKey bus message.
func keyMsg(kind string, row, col int, pressed bool, layer int) proto.Msg {
	return proto.Msg{Type: proto.TypeKey, Key: &proto.KeyEvent{
		Kind: kind, Row: row, Col: col, Pressed: pressed, Layer: layer,
	}}
}

// The full 196x24 Edge region renders one layer's key grid with exact line
// geometry (every line the region width) and no panic; v2 is minimal --
// legends on whitespace, no box-per-key borders.
func TestKeyboardRenderGeometry(t *testing.T) {
	m := kbModel(t, 196, 24)
	bodyH := m.height - stripH
	out := m.renderKeyboard(bodyH)
	lines := strings.Split(out, "\n")
	if len(lines) != bodyH {
		t.Fatalf("body lines = %d, want %d", len(lines), bodyH)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != m.width {
			t.Errorf("line %d width = %d, want %d", i, w, m.width)
		}
	}
	plain := ansi.Strip(out)
	// the active layer's legends must be on glass
	if !strings.Contains(plain, "Q") {
		t.Error("expected the Q key legend on the home layer")
	}
	// the minimal render draws no per-key box borders
	if strings.Contains(plain, "+--") {
		t.Error("v2 must not draw per-key box borders")
	}
}

// The whole View at 196x24 renders through the keyboard branch with the
// strip band, exact height, no panic. The body/strip boundary derives from
// stripH so a strip-geometry change cannot silently skew the split.
func TestKeyboardViewFullRegion(t *testing.T) {
	m := kbModel(t, 196, 24)
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != 24 {
		t.Fatalf("view lines = %d, want 24", len(lines))
	}
	body := 24 - stripH
	for i, l := range lines[:body] {
		if w := lipgloss.Width(l); w != 196 {
			t.Errorf("line %d width = %d, want 196", i, w)
		}
	}
	for i, l := range lines[body:] {
		if c := stripCells(l); c != 196 {
			t.Errorf("strip row %d = %d cells, want 196", i, c)
		}
	}
}

// The full render's thumb cluster mirrors the physical board: the wide piano
// key on its own key-row (tap+hold pair, so 2 lines) directly below main row
// 4 and ABOVE the 3-key arc, hugging the arc's grid-side edge -- the left
// half's piano key centered over spc/bksp (fixture home layer), the right
// half mirrored with the piano key right of Esc's column.
func TestKeyboardThumbClusterPiano(t *testing.T) {
	m := kbModel(t, 196, 24)
	bodyH := m.height - stripH
	plain := ansi.Strip(m.renderKeyboard(bodyH))
	lines := strings.Split(plain, "\n")

	lineOf := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("legend %q not rendered", sub)
		return -1
	}

	wide := lineOf(kbDownArrow) // left piano key
	arc := lineOf("spc")        // left arc first key
	if wide != arc-2 {
		t.Errorf("left piano line %d, want one key-row (2 lines) above the arc line %d", wide, arc)
	}
	rwide := lineOf(kbUpArrow) // right piano key
	rarc := lineOf("Esc")      // right arc first key
	if rwide != rarc-2 {
		t.Errorf("right piano line %d, want one key-row (2 lines) above the arc line %d", rwide, rarc)
	}
	if wide != rwide {
		t.Errorf("piano rows misaligned: left %d right %d", wide, rwide)
	}
	// the whole cluster sits below every main row (rctl is on main row 4)
	if r4 := lineOf("rctl"); r4 >= wide {
		t.Errorf("main row 4 at line %d not above the piano row at %d", r4, wide)
	}
	// grid-side alignment: the left piano key spans the arc's first two keys
	// (its centered legend lands between spc and bksp); the right piano key
	// spans the arc's last two, so its legend sits right of Esc's column
	pl, al := lines[wide], lines[arc]
	if ai, si, bi := strings.Index(pl, kbDownArrow), strings.Index(al, "spc"), strings.Index(al, "bksp"); ai < si || ai > bi {
		t.Errorf("left piano legend at %d not over spc(%d)..bksp(%d)", ai, si, bi)
	}
	if ai, ei := strings.Index(pl, kbUpArrow), strings.Index(al, "Esc"); ai <= ei {
		t.Errorf("right piano legend at %d not right of Esc(%d)", ai, ei)
	}
}

// The selector strip names every layer; tapping the region cycles to the next
// layer, tapping a named button jumps to it.
func TestKeyboardLayerNavigation(t *testing.T) {
	m := kbModel(t, 196, 24)
	bodyH := m.height - stripH
	plain := ansi.Strip(m.renderKeyboard(bodyH))
	for _, name := range []string{"home", "syms", "sys"} {
		if !strings.Contains(plain, name) {
			t.Errorf("selector missing layer %q", name)
		}
	}

	// tap the body (mid-grid, away from the tab bar) cycles fwd
	if m.kbLayer != 0 {
		t.Fatalf("start layer = %d, want 0", m.kbLayer)
	}
	if !m.resolveTap(90, 10) {
		t.Fatal("body tap not consumed")
	}
	if m.kbLayer != 1 {
		t.Fatalf("after body tap layer = %d, want 1 (cycled)", m.kbLayer)
	}

	// tapping the first tab on the bar (the panel's TOP row) jumps back to
	// layer 0
	_ = m.renderKeyboard(bodyH)
	if !m.resolveTap(2, 0) {
		t.Fatal("tab tap not consumed")
	}
	if m.kbLayer != 0 {
		t.Fatalf("after tab tap layer = %d, want 0", m.kbLayer)
	}
}

// cycling wraps at the last layer back to the first.
func TestKeyboardCycleWraps(t *testing.T) {
	m := kbModel(t, 196, 24)
	n := len(m.kbBoard.Layers)
	bodyH := m.height - stripH
	for range n {
		_ = m.renderKeyboard(bodyH)
		m.resolveTap(90, 10)
	}
	if m.kbLayer != 0 {
		t.Fatalf("after %d cycles layer = %d, want 0 (wrapped)", n, m.kbLayer)
	}
}

// The fullscreen render at 196x24 is frozen against a captured fixture: the
// region-core refactor must keep renderKeyboard's output byte-identical.
// KHUDSON_KB_GOLDEN=update rewrites the fixture from the current render.
func TestKeyboardFullscreenGolden(t *testing.T) {
	m := kbModel(t, 196, 24)
	out := m.renderKeyboard(m.height - stripH)
	golden := filepath.Join("testdata", "kb_fullscreen_196x24.golden")
	if os.Getenv("KHUDSON_KB_GOLDEN") == "update" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("golden fixture: %v (KHUDSON_KB_GOLDEN=update to capture)", err)
	}
	if out != string(want) {
		t.Error("fullscreen render diverged from the captured fixture")
	}
}

// A pressed key (bus TypeKey) highlights as a reverse-video block on the
// static render and clears on release.
func TestKeyboardLiveHighlight(t *testing.T) {
	m := kbModel(t, 196, 24)
	bodyH := m.height - stripH

	// Q = left half row1 col1 = matrix (1,1) (bridge spot-checked in
	// internal/keyboard); reverse video is SGR 7
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0))
	if !m.kbBoard.Held[keyboard.SlotAt(1, 1)] {
		t.Fatal("press did not mark the slot held")
	}
	out := m.renderKeyboard(bodyH)
	if !strings.Contains(out, "\x1b[1;7m") {
		t.Error("no reverse-video run on a held render")
	}

	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, false, 0))
	if len(m.kbBoard.Held) != 0 {
		t.Fatalf("release left held = %v", m.kbBoard.Held)
	}
	if out := m.renderKeyboard(bodyH); strings.Contains(out, "\x1b[1;7m") {
		t.Error("reverse-video run survived the release")
	}

	// geometry must hold under highlight styling
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0))
	for i, l := range strings.Split(m.renderKeyboard(bodyH), "\n") {
		if w := lipgloss.Width(l); w != m.width {
			t.Errorf("held line %d width = %d, want %d", i, w, m.width)
		}
	}
}

// Unmapped matrix coordinates (KC_NO holes) and events before the board
// loads are dropped, never a panic or a phantom highlight.
func TestKeyboardLiveHighlightEdgeCases(t *testing.T) {
	m := kbModel(t, 196, 24)
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 5, 6, true, 0)) // KC_NO hole
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 99, 0, true, 0))
	if len(m.kbBoard.Held) != 0 {
		t.Fatalf("phantom highlights: %v", m.kbBoard.Held)
	}
	m.handleKeyMsg(proto.Msg{Type: proto.TypeKey}) // nil Key
	m.kbBoard = nil
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0)) // board absent
}

// A layer event follows the board's active layer; clear (source lost) drops
// every held highlight; both invalidate the memoized body when the keyboard
// layout is showing.
func TestKeyboardLiveLayerAndClear(t *testing.T) {
	m := kbModel(t, 196, 24)
	m.homeCache.ok = true
	m.handleKeyMsg(keyMsg(proto.KeyEventLayer, 0, 0, false, 2))
	if m.kbLayer != 2 {
		t.Fatalf("layer = %d, want 2", m.kbLayer)
	}
	if m.homeCache.ok {
		t.Fatal("layer change kept the stale body cache")
	}
	// out-of-range layers are ignored
	m.handleKeyMsg(keyMsg(proto.KeyEventLayer, 0, 0, false, 99))
	if m.kbLayer != 2 {
		t.Fatalf("bogus layer landed: %d", m.kbLayer)
	}

	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0))
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 7, 1, true, 0))
	m.homeCache.ok = true
	m.handleKeyMsg(proto.Msg{Type: proto.TypeKey, Key: &proto.KeyEvent{Kind: proto.KeyEventClear}})
	if len(m.kbBoard.Held) != 0 {
		t.Fatalf("clear left held = %v", m.kbBoard.Held)
	}
	if m.homeCache.ok {
		t.Fatal("clear kept the stale body cache")
	}
}

// A board never seen (cold start, nothing local) renders the calm plug-in
// hint, never crashes.
func TestKeyboardNoBoardMessage(t *testing.T) {
	m := kbModel(t, 196, 24)
	// simulate the cold-start load result
	m.kbBoard = nil
	m.kbErr = keyboard.ErrNoBoard.Error()
	bodyH := m.height - stripH
	out := m.renderKeyboard(bodyH)
	if !strings.Contains(out, "plug in the board") {
		t.Error("no-board hint missing")
	}
	lines := strings.Split(out, "\n")
	if len(lines) != bodyH {
		t.Fatalf("empty body lines = %d, want %d", len(lines), bodyH)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != m.width {
			t.Errorf("empty line %d width = %d, want %d", i, w, m.width)
		}
	}
}

// ensureBoard with no board on the bus and nothing local yields the hint:
// kbErr records keyboard.ErrNoBoard and the render shows the plug-in line,
// not an error.
func TestKeyboardEnsureBoardNoBoard(t *testing.T) {
	m := kbModel(t, 120, 20)
	m.kbBoard = nil
	m.kbErr = ""
	m.ensureBoard()
	if m.kbErr != keyboard.ErrNoBoard.Error() {
		t.Fatalf("kbErr = %q, want ErrNoBoard", m.kbErr)
	}
	if out := m.renderKeyboard(18); !strings.Contains(ansi.Strip(out), "plug in the board") {
		t.Error("no board did not render the plug-in hint")
	}
}

// A missing board must not latch a stale kbErr: a failed ensureBoard
// followed by the board appearing (plug-in with a deployed generation to
// resolve from) loads the board and clears the error, adopting without a
// dock restart.
func TestKeyboardEnsureBoardClearsErrOnLateLoad(t *testing.T) {
	m := kbModel(t, 120, 20)
	l := kbFixtureLayout(t)
	gens := t.TempDir()
	present := false
	m.kbLoader = &keyboard.Loader{
		// TTL zero: every ensureBoard re-reads the scripted serial
		Poller: &usbserial.Poller{Read: func(context.Context) (usbserial.Identity, error) {
			if !present {
				return usbserial.Identity{}, usbserial.ErrNotPresent
			}
			return usbserial.Identity{LayoutID: l.HashID, RevisionID: l.RevisionID}, nil
		}},
		GenDir: gens,
		Fetch: func(context.Context, string, string) (*oryx.Layout, error) {
			t.Error("network fetch ran with a local payload available")
			return nil, errors.New("no")
		},
	}
	m.kbBoard = nil
	m.kbErr = ""
	m.ensureBoard()
	if m.kbErr == "" {
		t.Fatal("missing board did not set kbErr")
	}
	if _, err := generations.Append(gens, generations.Record{
		FlashedAt: time.Now(), LayoutID: l.HashID, RevisionID: l.RevisionID, Layout: l,
	}); err != nil {
		t.Fatal(err)
	}
	present = true
	m.ensureBoard()
	if m.kbBoard == nil {
		t.Fatalf("late load did not adopt the board: %s", m.kbErr)
	}
	if m.kbErr != "" {
		t.Fatalf("late load left a stale kbErr = %q", m.kbErr)
	}
}

// A serial that CHANGES after a successful load is re-resolved (a firmware
// flash re-enumerating the board with a new revision): the board on glass
// follows the deployment without a restart, keyed on the serial.
func TestKeyboardEnsureBoardReloadsOnNewRevision(t *testing.T) {
	m := kbModel(t, 196, 24)
	l := kbFixtureLayout(t)
	l2 := kbFixtureLayout(t)
	l2.Title = "aw5"
	l2.RevisionID = "aw5aw5"
	gens := t.TempDir()
	for _, p := range []*oryx.Layout{l, l2} {
		if _, err := generations.Append(gens, generations.Record{
			FlashedAt: time.Now(), LayoutID: p.HashID, RevisionID: p.RevisionID, Layout: p,
		}); err != nil {
			t.Fatal(err)
		}
	}
	serialRev := l.RevisionID
	m.kbLoader = &keyboard.Loader{
		// TTL zero: every ensureBoard re-reads the scripted serial
		Poller: &usbserial.Poller{Read: func(context.Context) (usbserial.Identity, error) {
			return usbserial.Identity{LayoutID: l.HashID, RevisionID: serialRev}, nil
		}},
		GenDir: gens,
	}
	m.kbBoard = nil
	m.ensureBoard()
	if m.kbBoard == nil || m.kbBoard.Title != "aw4" {
		t.Fatalf("initial load: board %+v, err %q", m.kbBoard, m.kbErr)
	}

	// a flash lands: the board re-enumerates with the new revision
	serialRev = l2.RevisionID
	m.homeCache.ok = true
	m.ensureBoard()
	if m.kbBoard == nil || m.kbBoard.Title != "aw5" {
		t.Fatalf("new revision not adopted: board title %q, err %q", m.kbBoard.Title, m.kbErr)
	}
	if m.homeCache.ok {
		t.Error("reload kept the stale home cache")
	}

	// unchanged revision: no re-parse
	before := m.kbBoard
	m.ensureBoard()
	if m.kbBoard != before {
		t.Error("unchanged revision re-parsed the board")
	}
}

// The kb-live region widget at the home layout's 75-col rect: exact line
// geometry, board content on glass, tab hits offset into the region, and
// a layer-cycle hit covering ONLY the bottom tab-bar row -- a board-area tap
// is consumed without cycling.
func TestKBLiveRegionRender(t *testing.T) {
	m := kbModel(t, 196, 24)
	w := config.Widget{ID: "kb-live", Title: "keyboard", Chrome: true,
		Render: config.Render{Kind: "chrome", Module: "kb-live"}}
	rr := rect{120, 1, 75, 21} // right peel off the 194x21 home interior
	interior := rect{rr.x + 1, rr.y + 1, rr.w - 2, rr.h - 2}

	m.resetHits()
	out := m.renderKBLive(w, rr)
	lines := strings.Split(out, "\n")
	if len(lines) != rr.h {
		t.Fatalf("region lines = %d, want %d", len(lines), rr.h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != rr.w {
			t.Errorf("line %d width = %d, want %d", i, lw, rr.w)
		}
	}
	if !strings.Contains(ansi.Strip(out), "Q") {
		t.Error("expected the Q key legend in the region render")
	}

	// a board-area tap is consumed by the region without cycling the layer
	if m.kbLayer != 0 {
		t.Fatalf("start layer = %d, want 0", m.kbLayer)
	}
	if !m.resolveTap(interior.x+10, interior.y+8) {
		t.Fatal("board tap not consumed")
	}
	if m.kbLayer != 0 {
		t.Fatalf("board tap cycled the layer to %d", m.kbLayer)
	}

	// a tab-bar tap on the band (right of the tabs, off the oryx button)
	// cycles to the next layer; the bar is the region's TOP row
	if !m.resolveTap(rr.x+40, rr.y) {
		t.Fatal("tab-bar tap not consumed")
	}
	if m.kbLayer != 1 {
		t.Fatalf("tab-bar tap layer = %d, want 1 (cycled)", m.kbLayer)
	}

	// tab hits are offset into the region: the first tab on the bar jumps
	// back to layer 0
	m.resetHits()
	_ = m.renderKBLive(w, rr)
	if !m.resolveTap(rr.x+1, rr.y) {
		t.Fatal("tab tap not consumed")
	}
	if m.kbLayer != 0 {
		t.Fatalf("tab tap layer = %d, want 0", m.kbLayer)
	}
}

// kbCompact reports whether out is the compact render: main rows fold to
// single tap lines, so the Q row (main row 1) and Z row (main row 3) sit
// 2 lines apart instead of the full render's 4 (tap+hold pairs). The thumb
// cluster shape rides the mode -- compact folds the wide key onto the arc
// line, full seats it one key-row (2 lines) above -- and is asserted per
// mode alongside the pitch.
func kbCompact(t *testing.T, out string) bool {
	t.Helper()
	lines := strings.Split(ansi.Strip(out), "\n")
	lineOf := func(sub string) int {
		for i, l := range lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		t.Fatalf("legend %q not rendered", sub)
		return -1
	}
	wide, arc := lineOf(kbDownArrow), lineOf("spc")
	switch pitch := lineOf("Z") - lineOf("Q"); pitch {
	case 2:
		if wide != arc {
			t.Fatalf("compact wide key line %d off the cluster line %d", wide, arc)
		}
		return true
	case 4:
		if wide != arc-2 {
			t.Fatalf("full piano line %d, want 2 lines above the arc line %d", wide, arc)
		}
		return false
	default:
		t.Fatalf("main row pitch %d, want 2 (compact) or 4 (full)", pitch)
		return false
	}
}

// The kb-live region as a short full-width strip (194x9, a size-9 bottom
// peel): compact auto-engages -- exact line geometry, held-key reverse video
// preserved, the thumb cluster folded to one line, and every hit (selector
// buttons included) inside the box. No layout ships this shape since the
// home-kb-strip variant retired, but compact mode is still config-reachable
// (params.mode) and short regions must keep degrading exactly like this.
func TestKBLiveCompactStripRender(t *testing.T) {
	m := kbModel(t, 196, 24)
	w := config.Widget{ID: "kb-live", Title: "keyboard", Chrome: true,
		Render: config.Render{Kind: "chrome", Module: "kb-live"}}
	rr := rect{1, 13, 194, 9}

	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0)) // hold Q
	m.resetHits()
	out := m.renderKBLive(w, rr)
	lines := strings.Split(out, "\n")
	if len(lines) != rr.h {
		t.Fatalf("region lines = %d, want %d", len(lines), rr.h)
	}
	for i, l := range lines {
		if lw := lipgloss.Width(l); lw != rr.w {
			t.Errorf("line %d width = %d, want %d", i, lw, rr.w)
		}
	}
	if !strings.Contains(ansi.Strip(out), "Q") {
		t.Error("expected the Q key legend in the compact render")
	}
	if !kbCompact(t, out) {
		t.Error("short region did not engage the compact render")
	}
	if !strings.Contains(out, "\x1b[1;7m") {
		t.Error("no reverse-video run on a held compact render")
	}
	for i, h := range m.hits {
		if h.area.x < rr.x || h.area.x+h.area.w > rr.x+rr.w ||
			h.area.y < rr.y || h.area.y+h.area.h > rr.y+rr.h {
			t.Errorf("hit %d (%+v) outside the region %+v", i, h.area, rr)
		}
	}
}

// Auto mode flips on the grid area (box interior; the tab bar rides the
// box's bottom row outside it): 14 rows -- the full grid -- still holds the
// full render, one row less engages compact; an explicit params mode
// overrides auto in BOTH directions.
func TestKBLiveModeSelection(t *testing.T) {
	m := kbModel(t, 196, 24)
	render := func(params map[string]any, h int) string {
		m.resetHits()
		w := config.Widget{ID: "kb-live", Title: "keyboard", Chrome: true,
			Render: config.Render{Kind: "chrome", Module: "kb-live", Params: params}}
		return m.renderKBLive(w, rect{120, 1, 75, h})
	}

	if kbCompact(t, render(nil, 15)) { // grid area 14 = full height
		t.Error("grid area 14 rendered compact, want full")
	}
	if !kbCompact(t, render(nil, 14)) { // grid area 13 < 14
		t.Error("grid area 13 rendered full, want compact")
	}
	if !kbCompact(t, render(map[string]any{"mode": "compact"}, 17)) {
		t.Error("mode=compact override ignored on a tall region")
	}
	if kbCompact(t, render(map[string]any{"mode": "full"}, 14)) {
		t.Error("mode=full override ignored on a short region")
	}
}

// A TypeKey press invalidates the memoized home frame when the active layout
// places a kb-live region; a layout with neither the keyboard kind nor a
// kb-live region keeps the cache.
func TestKBLiveTypeKeyInvalidation(t *testing.T) {
	m := kbModel(t, 196, 24)
	m.cfg = &config.Config{
		Widgets: map[string]config.Widget{
			"kb-live": {ID: "kb-live", Chrome: true,
				Render: config.Render{Kind: "chrome", Module: "kb-live"}},
			"resources": {ID: "resources",
				Render: config.Render{Kind: "native", Module: "resources"}},
		},
		Layouts: map[string]config.Layout{
			"home": {Kind: "home", Regions: []config.Region{
				{Widget: "kb-live", Edge: "right", Size: 75},
				{Widget: "resources", Edge: "fill"},
			}},
			"plain": {Kind: "home", Regions: []config.Region{
				{Widget: "resources", Edge: "fill"},
			}},
		},
		Layout: "home",
	}

	m.layout = "home"
	m.homeCache.ok = true
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0))
	if m.homeCache.ok {
		t.Fatal("press with kb-live visible kept the stale home cache")
	}
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, false, 0))

	m.layout = "plain"
	m.homeCache.ok = true
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0))
	if !m.homeCache.ok {
		t.Fatal("press with no keyboard surface dropped the home cache")
	}
}

// The kb-live hot path: with a kb-live region on the active home layout,
// EVERY TypeKey press/release drops the memoized home frame and the next
// View recomposes it whole (kb.go handleKeyMsg). No CI threshold -- the
// number is one `go test -bench BenchmarkKBLiveRecompose` away when the
// glass feels sluggish; per-region caching is the escape hatch if it ever
// measures too hot (kb.go, the homeCache invalidation comment).
func BenchmarkKBLiveRecompose(b *testing.B) {
	m := kbModel(b, 196, 24)
	m.cfg = &config.Config{
		Widgets: map[string]config.Widget{
			"kb-live": {ID: "kb-live", Chrome: true,
				Render: config.Render{Kind: "chrome", Module: "kb-live"}},
			"resources": {ID: "resources",
				Render: config.Render{Kind: "native", Module: "resources"}},
		},
		Layouts: map[string]config.Layout{
			"home": {Kind: "home", Regions: []config.Region{
				{Widget: "kb-live", Edge: "right", Size: 75},
				{Widget: "resources", Edge: "fill"},
			}},
		},
		Layout: "home",
	}
	m.layout = "home"
	press := keyMsg(proto.KeyEventKey, 1, 1, true, 0)
	release := keyMsg(proto.KeyEventKey, 1, 1, false, 0)
	for b.Loop() {
		// 100 TypeKey events, a View after each: every one invalidates and
		// recomposes the whole home frame
		for range 50 {
			m.handleKeyMsg(press)
			m.View()
			m.handleKeyMsg(release)
			m.View()
		}
	}
}

// A TypeKey event arriving before the board has loaded triggers the lazy
// load and is folded in, not dropped: the loader resolves the scripted
// serial against a staged generation.
func TestKeyboardFirstEventLoadsBoard(t *testing.T) {
	m := kbModel(t, 196, 24)
	l := kbFixtureLayout(t)
	gens := t.TempDir()
	if _, err := generations.Append(gens, generations.Record{
		FlashedAt: time.Now(), LayoutID: l.HashID, RevisionID: l.RevisionID, Layout: l,
	}); err != nil {
		t.Fatal(err)
	}
	m.kbLoader = &keyboard.Loader{
		Poller: &usbserial.Poller{TTL: time.Hour, Read: func(context.Context) (usbserial.Identity, error) {
			return usbserial.Identity{LayoutID: l.HashID, RevisionID: l.RevisionID}, nil
		}},
		GenDir: gens,
	}
	m.kbBoard = nil
	m.kbErr = ""

	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0))
	if m.kbBoard == nil {
		t.Fatalf("first event did not load the board: %s", m.kbErr)
	}
	if !m.kbBoard.Held[keyboard.SlotAt(1, 1)] {
		t.Fatal("pre-render key event dropped: slot not held")
	}
}

// Live-gated: render the user's REAL layout through the actual keyboard view
// and log every layer's grid. Set KHUDSON_KB_REAL=1 to run; the logged
// output is the real on-glass render.
func TestKeyboardRenderRealDB(t *testing.T) {
	if os.Getenv("KHUDSON_KB_REAL") == "" {
		t.Skip("set KHUDSON_KB_REAL=1 to render the real deployed layout")
	}
	m := &model{
		cfg: &config.Config{
			Layouts: map[string]config.Layout{"keyboard": {Kind: "keyboard"}},
			Layout:  "keyboard",
		},
		layout: "keyboard", width: 196, height: 24, now: time.Now(),
		widgetData: map[string]module.Data{}, widgetErr: map[string]string{},
		sty: buildStyles(day),
	}
	// default loader: real serial, real caches; the payload may arrive on
	// the async fetch, so give it a few ticks
	for i := 0; i < 100 && m.kbBoard == nil; i++ {
		m.ensureBoard()
		if m.kbBoard == nil {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if m.kbBoard == nil {
		t.Fatalf("no board loaded: %s", m.kbErr)
	}
	bodyH := m.height - stripH
	for i := range m.kbBoard.Layers {
		m.kbLayer = i
		out := ansi.Strip(m.renderKeyboard(bodyH))
		t.Logf("\n--- layer %d: %s ---\n%s", i, m.kbBoard.Layers[i].Title, out)
	}
}

// The layer fill is an OFF-BASE indicator: the base layer renders with no
// fill even under a palette -- only the tab-bar band row carries a
// background -- and a non-base layer floods every INTERIOR cell (the top
// border row stays unfilled; NO OVERFLOWS ON FILLS, the compositor test
// pins the perimeter) while held keys stay plain reverse-video so presses
// pop.
func TestKeyboardLayerChipTint(t *testing.T) {
	m := kbModel(t, 196, 24)
	bodyH := m.height - stripH

	// no palette broadcast: no truecolor background runs at all
	if strings.Contains(m.renderKeyboard(bodyH), "48;2;") {
		t.Fatal("palette-less render carries a background fill")
	}

	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day", Palette: busPalette()})
	if kbview.LayerChip(m.kbBoard, 0, m.kbTheme()) != nil {
		t.Fatal("base layer must carry no chip (off-base indicator)")
	}
	lines0 := strings.Split(m.renderKeyboard(bodyH), "\n")
	if !strings.Contains(lines0[0], "48;2;") {
		t.Error("tab-bar band (the panel header) carries no background")
	}
	for i, l := range lines0[1:] {
		if strings.Contains(l, "48;2;") {
			t.Errorf("base layer line %d carries a background fill", i+1)
		}
	}
	if len(m.kbBoard.Layers) < 3 {
		t.Fatalf("fixture board has %d layers, need 3+", len(m.kbBoard.Layers))
	}
	chip1 := kbview.LayerChip(m.kbBoard, 1, m.kbTheme())
	if chip1 == nil {
		t.Fatal("no chip color for a non-base layer with a palette present")
	}
	if chip2 := kbview.LayerChip(m.kbBoard, 2, m.kbTheme()); chip2 == chip1 {
		t.Error("two layers share one chip color")
	}
	m.kbLayer = 1
	m.homeCache.ok = false
	out1 := m.renderKeyboard(bodyH)
	// interior flood: every line carries a background (header band, chip
	// flood, bar band)
	lines1 := strings.Split(out1, "\n")
	for i, l := range lines1 {
		if !strings.Contains(l, "48;2;") {
			t.Errorf("line %d carries no fill", i)
		}
	}

	// held key: plain reverse-video on the tap cell, and ONLY there -- the
	// hold half keeps the chip (glass-reported: it dropped to the bare
	// background, a hole under every held key on a filled layer)
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0))
	held := m.renderKeyboard(bodyH)
	if !strings.Contains(held, "\x1b[1;7m") {
		t.Error("held key lost its plain reverse-video block under the tint")
	}
	buf := uv.NewScreenBuffer(m.width, bodyH)
	uv.NewStyledString(held).Draw(buf, buf.Bounds())
	bare := 0
	for y := 1; y < bodyH-1; y++ {
		for x := range m.width {
			if c := buf.CellAt(x, y); c != nil && c.Style.Bg == nil {
				bare++
			}
		}
	}
	// exactly the held key's reverse-video tap cell (kw=11 on this glass)
	// escapes the interior flood
	if bare != 11 {
		t.Errorf("%d bare interior cells under a held key, want 11 (the tap cell alone)", bare)
	}
}

// kbTexGlyphs maps each v2 recipe to the glyphs its render may carry. The
// nerd-font entries are width-gated at the painter (kbSafeGlyph): when a
// measurer disagrees on 1 cell they fall back to a plain space, so glyph
// presence asserts only when kbGlyphSafe passes.
var kbTexGlyphs = map[string][]string{
	"dots":         {"\u00b7"},
	"oct-dot":      {"\uf444"},
	"circle-small": {"\U000F09DE"},
	"dots-column":  {"\U000F01D9"},
	"grabber":      {"\uf45a"},
	"dots-grid":    {"\U000F15FC"},
	"crosshair":    {"\ue621"},
	"dot-grid":     {"\u00b7"},
	"line-grid":    {"-", "|"},
}

// kbGlyphSafe mirrors the painter's width gate: 1 cell under BOTH
// measurers.
func kbGlyphSafe(gs []string) bool {
	for _, g := range gs {
		if ansi.StringWidth(g) != 1 || lipgloss.Width(g) != 1 {
			return false
		}
	}
	return true
}

// kbTexModel is kbModel with a palette broadcast, a non-base layer
// active, and the kb-live widget carrying the texture param (the
// fullscreen kind resolves it module-keyed off the config).
func kbTexModel(t testing.TB, texture string) *model {
	m := kbModel(t, 196, 24)
	m.cfg.Widgets["kb-live"] = config.Widget{ID: "kb-live", Chrome: true,
		Render: config.Render{Kind: "chrome", Module: "kb-live",
			Params: map[string]any{"texture": texture}}}
	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day", Palette: busPalette()})
	m.kbLayer = 1
	return m
}

// Every v2 texture (densities included) renders deterministically -- two
// renders byte-identical, exact line geometry (cells are per absolute
// coordinate, no randomness) -- and puts its glyph on glass whenever the
// glyph passes the width gate.
func TestKeyboardTextureDeterministic(t *testing.T) {
	var textures []string
	for _, recipe := range config.KBTextureRecipes {
		if cell, _ := kbview.TexCellFn(recipe); cell == nil {
			t.Errorf("%s: vocabulary recipe has no TexCellFn cell", recipe)
		}
		for _, density := range []string{"", ":sparse", ":dense"} {
			textures = append(textures, recipe+density)
		}
	}
	for _, tex := range textures {
		m := kbTexModel(t, tex)
		bodyH := m.height - stripH
		a := m.renderKeyboard(bodyH)
		if b := m.renderKeyboard(bodyH); a != b {
			t.Errorf("%s: render diverged across two renders", tex)
		}
		recipe, _, _ := strings.Cut(tex, ":")
		found := false
		for _, g := range kbTexGlyphs[recipe] {
			if strings.Contains(a, g) {
				found = true
			}
		}
		if kbGlyphSafe(kbTexGlyphs[recipe]) && !found {
			t.Errorf("%s: no texture glyph on glass", tex)
		}
		for i, l := range strings.Split(a, "\n") {
			if w := lipgloss.Width(l); w != m.width {
				t.Errorf("%s: line %d width = %d, want %d", tex, i, w, m.width)
			}
		}
	}
}

// A palette-less dock renders plain spaces whatever the texture param:
// byte-identical to the param-less render (the fullscreen golden pins
// that shape; the texture is part of the layer fill, absent when the
// fill is).
func TestKeyboardTextureChipNilPlain(t *testing.T) {
	m := kbModel(t, 196, 24)
	m.cfg.Widgets["kb-live"] = config.Widget{ID: "kb-live", Chrome: true,
		Render: config.Render{Kind: "chrome", Module: "kb-live",
			Params: map[string]any{"texture": "dots-grid"}}}
	bodyH := m.height - stripH
	out := m.renderKeyboard(bodyH)
	if strings.Contains(out, "\U000F15FC") {
		t.Fatal("palette-less render drew texture glyphs")
	}
	plain := kbModel(t, 196, 24)
	if out != plain.renderKeyboard(bodyH) {
		t.Fatal("texture param changed the palette-less render")
	}
}

// Textured renders survive the real cell compositor
// (TestStripSurvivesCompositor precedent): every body row measures
// exactly the glass width under BOTH measurers, and the glyphs land as
// real cells. oct-dot exercises the PUA width gate -- when the gate
// trips, the fail-safe contract holds instead (no glyph on glass,
// geometry intact); line-grid pins the ASCII lattice.
func TestKeyboardTextureSurvivesCompositor(t *testing.T) {
	for _, tex := range []string{"oct-dot", "line-grid"} {
		glyphs := kbTexGlyphs[tex]
		m := kbTexModel(t, tex)
		v := m.View()
		lines := strings.Split(v.Content, "\n")
		for i, l := range lines[:22] {
			if aw, lw := ansi.StringWidth(l), lipgloss.Width(l); aw != 196 || lw != 196 {
				t.Errorf("%s: line %d measures %d/%d cells (ansi/lipgloss), want 196", tex, i, aw, lw)
			}
		}
		buf := uv.NewScreenBuffer(196, 24)
		uv.NewStyledString(v.Content).Draw(buf, buf.Bounds())
		found := false
		for y := 0; y < 22 && !found; y++ {
			for x := range 196 {
				if c := buf.CellAt(x, y); c != nil && slices.Contains(glyphs, c.Content) {
					found = true
					break
				}
			}
		}
		if safe := kbGlyphSafe(glyphs); safe && !found {
			t.Errorf("%s: no texture glyph landed as a real cell after the compositor round-trip", tex)
		} else if !safe && found {
			t.Errorf("%s: width-gated glyph leaked onto glass", tex)
		}
	}
}

// FILLS ARE FULL-BLEED: through the real cell compositor, the borderless
// fullscreen panel floods every cell to the absolute edges (columns 0 and
// w-1 included -- there is no frame to protect): header band on the title
// row, chip flood on the grid, band fill on the bar row.
func TestKeyboardFilledFrameNoOverflow(t *testing.T) {
	for _, tex := range []string{"none", "dots"} {
		m := kbTexModel(t, tex)
		bodyH := m.height - stripH
		out := m.renderKeyboard(bodyH)
		buf := uv.NewScreenBuffer(m.width, bodyH)
		uv.NewStyledString(out).Draw(buf, buf.Bounds())
		for y := range bodyH {
			for x := range m.width {
				c := buf.CellAt(x, y)
				if c == nil {
					continue
				}
				if c.Style.Bg == nil {
					t.Fatalf("%s: cell (%d,%d) carries no fill", tex, x, y)
				}
			}
		}
	}
}

// The base, tagless frame is byte-identical to renderTitledBox: only the
// border COLOR signals the layer, never the glyphs.
func TestKBTitledBoxBaseMatchesChrome(t *testing.T) {
	body := []string{"one", "two", "three"}
	bare := kbview.Theme{FG: chromeFG, Dim: chromeDim}
	if got, want := kbview.TitledBox("t", body, 24, 6, nil, bare), renderTitledBox("t", body, 24, 6); got != want {
		t.Fatalf("base TitledBox diverged from renderTitledBox:\ngot  %q\nwant %q", got, want)
	}
	// off-base: same geometry, hue-colored frame
	hued := kbview.TitledBox("t", body, 24, 6, lipgloss.Color("#d699b6"), bare)
	if ansi.Strip(hued) != ansi.Strip(renderTitledBox("t", body, 24, 6)) {
		t.Fatal("hued frame changed glyphs; color is the only allowed signal")
	}
}

// oryxURL addresses the synced revision at the active layer in the web
// configurator; a board without its Oryx identity hides the button.
func TestOryxURL(t *testing.T) {
	m := kbModel(t, 196, 24)
	m.kbBoard.LayoutID = "AbC12"
	m.kbBoard.Geometry = "moonlander"
	m.kbBoard.RevisionID = "rEv34"
	m.kbLayer = 2
	if got, want := kbview.OryxURL(m.kbBoard, m.kbLayer), "https://configure.zsa.io/moonlander/layouts/AbC12/rEv34/2"; got != want {
		t.Errorf("oryxURL = %q, want %q", got, want)
	}
	m.kbBoard.RevisionID = ""
	if got, want := kbview.OryxURL(m.kbBoard, m.kbLayer), "https://configure.zsa.io/moonlander/layouts/AbC12/latest/2"; got != want {
		t.Errorf("revisionless oryxURL = %q, want %q", got, want)
	}
	m.kbBoard.LayoutID = ""
	if got := kbview.OryxURL(m.kbBoard, m.kbLayer); got != "" {
		t.Errorf("slugless oryxURL = %q, want empty", got)
	}
	m.kbBoard = nil
	if got := kbview.OryxURL(m.kbBoard, m.kbLayer); got != "" {
		t.Errorf("boardless oryxURL = %q, want empty", got)
	}
}

// The oryx button renders on the bar row capping the panel's TOP, flush
// against the view's right edge opposite the layer tabs (the band runs to
// the absolute edges) -- and its hit hands the configurator URL to the
// opener seam. A nil opener (every bare test model) must never panic.
func TestKBOryxInteriorLink(t *testing.T) {
	m := kbModel(t, 196, 24)
	var opened string
	m.openURL = func(u string) { opened = u }
	bodyH := m.height - stripH
	out := m.renderKeyboard(bodyH)
	lines := strings.Split(out, "\n")
	if strings.Contains(ansi.Strip(lines[len(lines)-1]), "oryx") {
		t.Fatal("oryx on the panel's bottom row; the bar moved to the top")
	}
	bar := ansi.Strip(lines[0]) // the bar row caps the panel's top
	bi := strings.Index(bar, " oryx ")
	if bi < 0 {
		t.Fatal("oryx link not on the bar row")
	}
	i := lipgloss.Width(bar[:bi]) // cell column, not byte offset
	if got, want := i, m.width-len(" oryx "); got != want {
		t.Errorf("oryx link starts at col %d, want %d (flush right)", got, want)
	}
	if !m.resolveTap(i+1, 0) {
		t.Fatal("tap on the link not consumed")
	}
	if !strings.HasPrefix(opened, "https://configure.zsa.io/moonlander/layouts/") {
		t.Errorf("opened %q, want a configure.zsa.io layout URL", opened)
	}

	// nil opener: the tap is a safe no-op
	m2 := kbModel(t, 196, 24)
	m2.renderKeyboard(m2.height - stripH)
	for _, h := range m2.hits {
		h.do(h.area.x, h.area.y)
	}
}
