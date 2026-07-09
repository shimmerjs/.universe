package dock

import (
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
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/keymappdb"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

const kbFixtureDB = "../keyboard/keymappdb/testdata/fixture.sqlite3"
const kbEmptyDB = "../keyboard/keymappdb/testdata/empty.sqlite3"

// fixture thumb legends: the dictionary maps the left wide key (KC_DOWN)
// and right wide key (KC_UP) to arrow glyphs.
const (
	kbDownArrow = "↓"
	kbUpArrow   = "↑"
)

// kbModel is a keyboard-layout dock model preloaded with the fixture board so
// the view never touches the real Keymapp store. Skips when the exec'd
// keymappdb reader's sqlite3 CLI is missing (skip-on-missing: the nix
// checkPhase has no host binaries).
// testing.TB so benchmarks share the fixture.
func kbModel(t testing.TB, w, h int) *model {
	t.Helper()
	if _, err := keymappdb.Sqlite3Bin(); err != nil {
		t.Skipf("sqlite3: %v", err)
	}
	rev, err := keymappdb.Active(kbFixtureDB)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
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
	m.kbBoard = keyboard.FromRevision(rev)
	m.kbLoaded = true
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
// 2-row strip, exact height, no panic. The strip rows carry scaled runs, so
// they measure by the hand budget, not lipgloss.
func TestKeyboardViewFullRegion(t *testing.T) {
	m := kbModel(t, 196, 24)
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != 24 {
		t.Fatalf("view lines = %d, want 24", len(lines))
	}
	for i, l := range lines[:22] {
		if w := lipgloss.Width(l); w != 196 {
			t.Errorf("line %d width = %d, want 196", i, w)
		}
	}
	for i, l := range lines[22:] {
		if c := stripCells(l); c != 196 {
			t.Errorf("strip row %d = %d cells by hand budget, want 196", i, c)
		}
	}
}

// The physical thumb cluster: the wide piano key's legend renders on a line
// ABOVE the 3-key arc's line, for both halves (fixture home layer: left
// wide=down-arrow over spc/bksp/tab, right wide=up-arrow over esc/../enter).
func TestKeyboardThumbClusterRaised(t *testing.T) {
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

	wide := lineOf(kbDownArrow) // left wide key
	arc := lineOf("spc")        // left arc first key
	if wide >= arc {
		t.Errorf("wide key line %d not raised above arc line %d", wide, arc)
	}
	rwide := lineOf(kbUpArrow) // right wide key
	rarc := lineOf("Esc")      // right arc first key
	if rwide >= rarc {
		t.Errorf("right wide key line %d not raised above arc line %d", rwide, rarc)
	}
	if wide != rwide || arc != rarc {
		t.Errorf("thumb rows misaligned: left %d/%d right %d/%d", wide, arc, rwide, rarc)
	}
	// the thumb cluster sits below every main row (Q is on main row 1)
	if q := lineOf("Q"); q >= wide {
		t.Errorf("main row Q at line %d not above thumb cluster at %d", q, wide)
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

	// tap the body (bottom of the region, away from the selector) cycles fwd
	if m.kbLayer != 0 {
		t.Fatalf("start layer = %d, want 0", m.kbLayer)
	}
	if !m.resolveTap(90, 20) {
		t.Fatal("body tap not consumed")
	}
	if m.kbLayer != 1 {
		t.Fatalf("after body tap layer = %d, want 1 (cycled)", m.kbLayer)
	}

	// tapping the first selector button jumps back to layer 0
	_ = m.renderKeyboard(bodyH)
	if !m.resolveTap(2, 1) {
		t.Fatal("selector tap not consumed")
	}
	if m.kbLayer != 0 {
		t.Fatalf("after selector tap layer = %d, want 0", m.kbLayer)
	}
}

// cycling wraps at the last layer back to the first.
func TestKeyboardCycleWraps(t *testing.T) {
	m := kbModel(t, 196, 24)
	n := len(m.kbBoard.Layers)
	bodyH := m.height - stripH
	for range n {
		_ = m.renderKeyboard(bodyH)
		m.resolveTap(90, 20)
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

// An empty / never-synced store renders the calm sync hint, never crashes.
func TestKeyboardEmptyDBMessage(t *testing.T) {
	m := kbModel(t, 196, 24)
	// simulate the empty-store load result
	m.kbBoard = nil
	m.kbErr = keymappdb.ErrNoRevision.Error()
	bodyH := m.height - stripH
	out := m.renderKeyboard(bodyH)
	if !strings.Contains(out, "open Keymapp") {
		t.Error("empty-store hint missing")
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

// A genuinely empty DB file loaded through ensureBoard yields the sync
// hint: kbErr records keymappdb.ErrNoRevision and the render shows the
// "open Keymapp" line, not an error.
func TestKeyboardEnsureBoardEmptyDB(t *testing.T) {
	if _, err := keymappdb.Sqlite3Bin(); err != nil {
		t.Skipf("sqlite3: %v", err)
	}
	db, err := os.ReadFile(kbEmptyDB)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", ".keymapp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keymapp.sqlite3"), db, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	m := kbModel(t, 120, 20)
	m.kbBoard = nil
	m.kbLoaded = false
	m.kbErr = ""
	m.ensureBoard()
	if m.kbErr != keymappdb.ErrNoRevision.Error() {
		t.Fatalf("kbErr = %q, want ErrNoRevision", m.kbErr)
	}
	if out := m.renderKeyboard(18); !strings.Contains(ansi.Strip(out), "open Keymapp") {
		t.Error("empty DB did not render the sync hint")
	}
}

// A missing store must not latch a stale kbErr: a failed ensureBoard followed
// by a Keymapp sync (the store appearing) loads the board and clears the
// error, adopting without a dock restart.
func TestKeyboardEnsureBoardClearsErrOnLateLoad(t *testing.T) {
	if _, err := keymappdb.Sqlite3Bin(); err != nil {
		t.Skipf("sqlite3: %v", err)
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	m := kbModel(t, 120, 20)
	m.kbBoard = nil
	m.kbLoaded = false
	m.kbErr = ""
	m.ensureBoard()
	if m.kbErr == "" {
		t.Fatal("missing store did not set kbErr")
	}
	if m.kbLoaded {
		t.Fatal("missing store latched kbLoaded")
	}
	db, err := os.ReadFile(kbFixtureDB)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	dir := filepath.Join(home, "Library", "Application Support", ".keymapp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keymapp.sqlite3"), db, 0o644); err != nil {
		t.Fatal(err)
	}
	m.ensureBoard()
	if m.kbBoard == nil {
		t.Fatalf("late load did not adopt the board: %s", m.kbErr)
	}
	if m.kbErr != "" {
		t.Fatalf("late load left a stale kbErr = %q", m.kbErr)
	}
}

// The kb-live region widget at the home layout's 75-col rect: exact line
// geometry, board content on glass, selector hits offset into the region, and
// a layer-cycle hit covering ONLY the selector row -- a board-area tap is
// consumed without cycling.
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

	// a selector-row tap right of the buttons cycles to the next layer
	if !m.resolveTap(interior.x+interior.w-2, interior.y) {
		t.Fatal("selector-row tap not consumed")
	}
	if m.kbLayer != 1 {
		t.Fatalf("selector-row tap layer = %d, want 1 (cycled)", m.kbLayer)
	}

	// selector button hits are offset into the region: the first button
	// jumps back to layer 0
	m.resetHits()
	_ = m.renderKBLive(w, rr)
	if !m.resolveTap(interior.x+1, interior.y) {
		t.Fatal("selector button tap not consumed")
	}
	if m.kbLayer != 0 {
		t.Fatalf("selector button tap layer = %d, want 0", m.kbLayer)
	}
}

// kbThumbFolded reports whether out renders the left wide key and the left
// arc on one line (the compact fold) or on separate lines (the full render's
// raised wide key). Fails the test when either legend is missing entirely.
func kbThumbFolded(t *testing.T, out string) bool {
	t.Helper()
	sameLine, wideSeen, arcSeen := false, false, false
	for _, l := range strings.Split(ansi.Strip(out), "\n") {
		hasWide := strings.Contains(l, kbDownArrow)
		hasArc := strings.Contains(l, "spc")
		if hasWide && hasArc {
			sameLine = true
		}
		wideSeen = wideSeen || hasWide
		arcSeen = arcSeen || hasArc
	}
	if !wideSeen || !arcSeen {
		t.Fatalf("thumb legends missing (wide %v, arc %v)", wideSeen, arcSeen)
	}
	return sameLine
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
	if !kbThumbFolded(t, out) {
		t.Error("compact render did not fold the thumb cluster to one line")
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

// Auto mode flips on the region interior: 17 rows still holds the full
// render, one row less engages compact; an explicit params mode overrides
// auto in BOTH directions.
func TestKBLiveModeSelection(t *testing.T) {
	m := kbModel(t, 196, 24)
	render := func(params map[string]any, h int) string {
		m.resetHits()
		w := config.Widget{ID: "kb-live", Title: "keyboard", Chrome: true,
			Render: config.Render{Kind: "chrome", Module: "kb-live", Params: params}}
		return m.renderKBLive(w, rect{120, 1, 75, h})
	}

	if kbThumbFolded(t, render(nil, 19)) { // interior 17 = full height
		t.Error("interior 17 rendered compact, want full")
	}
	if !kbThumbFolded(t, render(nil, 18)) { // interior 16 < 17
		t.Error("interior 16 rendered full, want compact")
	}
	if !kbThumbFolded(t, render(map[string]any{"mode": "compact"}, 19)) {
		t.Error("mode=compact override ignored on a tall region")
	}
	if kbThumbFolded(t, render(map[string]any{"mode": "full"}, 18)) {
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

// A TypeKey event arriving before the board has loaded triggers the lazy load
// and is folded in, not dropped. The loader resolves the store through $HOME
// (keymappdb.DefaultPath), so the fixture is staged under a temp home.
func TestKeyboardFirstEventLoadsBoard(t *testing.T) {
	if _, err := keymappdb.Sqlite3Bin(); err != nil {
		t.Skipf("sqlite3: %v", err)
	}
	db, err := os.ReadFile(kbFixtureDB)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, "Library", "Application Support", ".keymapp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keymapp.sqlite3"), db, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	m := kbModel(t, 196, 24)
	m.kbBoard = nil
	m.kbLoaded = false
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
// and log every layer's grid. Set KHUDSON_KEYMAPP_DB=1 to run; the logged output
// is the real on-glass render.
func TestKeyboardRenderRealDB(t *testing.T) {
	if os.Getenv("KHUDSON_KEYMAPP_DB") == "" {
		t.Skip("set KHUDSON_KEYMAPP_DB=1 to render the real Keymapp layout")
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
	m.ensureBoard()
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

// The layer fill is an OFF-BASE indicator: the base layer renders with
// no fill even under a palette; a non-base layer floods every INTERIOR
// cell -- the frame rows stay unfilled (NO OVERFLOWS ON FILLS; the
// compositor test pins the perimeter) -- and held keys stay plain
// reverse-video so presses pop. Palette-less docks render exactly as
// before (the golden pins that).
func TestKeyboardLayerChipTint(t *testing.T) {
	m := kbModel(t, 196, 24)
	bodyH := m.height - stripH

	// no palette broadcast: no background runs at all
	if strings.Contains(m.renderKeyboard(bodyH), "48;2;") {
		t.Fatal("palette-less render carries a background fill")
	}

	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day", Palette: busPalette()})
	if m.kbLayerChip(0) != nil {
		t.Fatal("base layer must carry no fill (off-base indicator)")
	}
	if strings.Contains(m.renderKeyboard(bodyH), "48;2;") {
		t.Fatal("base layer render carries a background fill")
	}
	if len(m.kbBoard.Layers) < 3 {
		t.Fatalf("fixture board has %d layers, need 3+", len(m.kbBoard.Layers))
	}
	chip1 := m.kbLayerChip(1)
	if chip1 == nil {
		t.Fatal("no chip color for a non-base layer with a palette present")
	}
	if chip2 := m.kbLayerChip(2); chip2 == chip1 {
		t.Error("two layers share one chip color")
	}
	m.kbLayer = 1
	m.homeCache.ok = false
	out1 := m.renderKeyboard(bodyH)
	// interior flood, clean frame: every interior line carries the fill,
	// the border rows carry none
	lines1 := strings.Split(out1, "\n")
	if strings.Contains(lines1[0], "48;2;") || strings.Contains(lines1[len(lines1)-1], "48;2;") {
		t.Error("frame row carries the fill")
	}
	for i, l := range lines1[1 : len(lines1)-1] {
		if !strings.Contains(l, "48;2;") {
			t.Errorf("interior line %d carries no fill", i+1)
		}
	}

	// held key: plain reverse-video, no chip on its tap cell
	m.handleKeyMsg(keyMsg(proto.KeyEventKey, 1, 1, true, 0))
	if !strings.Contains(m.renderKeyboard(bodyH), "\x1b[1;7m") {
		t.Error("held key lost its plain reverse-video block under the tint")
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
		if cell, _ := kbTexCellFn(recipe); cell == nil {
			t.Errorf("%s: vocabulary recipe has no kbTexCellFn cell", recipe)
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

// NO OVERFLOWS ON FILLS: through the real cell compositor, no frame
// cell -- row 0, row h-1, col 0, col w-1 -- of a filled render carries
// ANY background (a chip bg on a border cell shows outside the mid-cell
// stroke), while every interior cell carries the fill, textures included.
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
				frame := y == 0 || y == bodyH-1 || x == 0 || x == m.width-1
				if frame && c.Style.Bg != nil {
					t.Fatalf("%s: frame cell (%d,%d) carries a background %v", tex, x, y, c.Style.Bg)
				}
				if !frame && c.Style.Bg == nil {
					t.Fatalf("%s: interior cell (%d,%d) carries no fill", tex, x, y)
				}
			}
		}
	}
}

// The base, tagless frame is byte-identical to renderTitledBox: only the
// border COLOR signals the layer, never the glyphs.
func TestKBTitledBoxBaseMatchesChrome(t *testing.T) {
	body := []string{"one", "two", "three"}
	if got, want := kbTitledBox("t", body, 24, 6, nil), renderTitledBox("t", body, 24, 6); got != want {
		t.Fatalf("base kbTitledBox diverged from renderTitledBox:\ngot  %q\nwant %q", got, want)
	}
	// off-base: same geometry, hue-colored frame
	hued := kbTitledBox("t", body, 24, 6, lipgloss.Color("#d699b6"))
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
	if got, want := m.oryxURL(), "https://configure.zsa.io/moonlander/layouts/AbC12/rEv34/2"; got != want {
		t.Errorf("oryxURL = %q, want %q", got, want)
	}
	m.kbBoard.RevisionID = ""
	if got, want := m.oryxURL(), "https://configure.zsa.io/moonlander/layouts/AbC12/latest/2"; got != want {
		t.Errorf("revisionless oryxURL = %q, want %q", got, want)
	}
	m.kbBoard.LayoutID = ""
	if got := m.oryxURL(); got != "" {
		t.Errorf("slugless oryxURL = %q, want empty", got)
	}
	m.kbBoard = nil
	if got := m.oryxURL(); got != "" {
		t.Errorf("boardless oryxURL = %q, want empty", got)
	}
}

// The oryx link renders INSIDE the widget on the last interior row,
// right-aligned -- off the selector row (it must not read as a layer
// button) and off the border (a tag there breaks the frame line) -- and
// its hit hands the configurator URL to the opener seam. A nil opener
// (every bare test model) must never panic.
func TestKBOryxInteriorLink(t *testing.T) {
	m := kbModel(t, 196, 24)
	var opened string
	m.openURL = func(u string) { opened = u }
	bodyH := m.height - stripH
	out := m.renderKeyboard(bodyH)
	lines := strings.Split(out, "\n")
	if strings.Contains(ansi.Strip(lines[1]), " oryx ") {
		t.Fatal("oryx still renders on the selector row")
	}
	for _, y := range []int{0, len(lines) - 1} {
		if strings.Contains(ansi.Strip(lines[y]), "oryx") {
			t.Fatalf("oryx on the border row %d; the frame line must stay unbroken", y)
		}
	}
	last := ansi.Strip(lines[len(lines)-2]) // last interior row
	bi := strings.Index(last, " oryx ")
	if bi < 0 {
		t.Fatal("oryx link not on the last interior row")
	}
	i := lipgloss.Width(last[:bi]) // cell column, not byte offset
	// interior spans cols 1..w-2; the link sits kbOryxPad cells off its
	// right edge
	if got, want := i, 1+(m.width-2)-kbOryxPad-len(" oryx "); got != want {
		t.Errorf("oryx link starts at col %d, want %d (right-aligned)", got, want)
	}
	if !m.resolveTap(i+1, bodyH-2) {
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
