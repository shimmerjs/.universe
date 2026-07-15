package dock

// The strip-hosted nav band at the Edge's 196x24. Icons are cell-drawn
// 2-row block art -- bubbletea's compositor forwards only SGR and OSC 8,
// so anything fancier never reaches kitty; TestStripSurvivesCompositor
// pins that whole class of failure by round-tripping the view through the
// real cell compositor.

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// stripModel is a 196x24 home model with a strip block: one stub tab, one
// real home-kind target, one deliberate stub, the flip chevron pair (home
// expanded, home-no-kb collapsed), and the caffeinate cup.
func stripModel() *model {
	m := newHomeModel(196, 24)
	m.cfg.Layouts["home-no-kb"] = config.Layout{Kind: "home", Regions: []config.Region{
		{Widget: "cpumem", Edge: "fill"},
	}}
	m.cfg.Strip = &config.Strip{
		Entries: []config.StripEntry{
			{Label: "kb", Target: "keyboard"}, // no keyboard layout in the fixture: stub
			{Label: "clod", Target: "hub"},    // real home-kind target
			{Label: "sys", Target: "sys"},     // stub
		},
		Toggles: []config.StripToggle{{Kind: "caffeinate"}},
		Flip:    &config.StripFlip{Expanded: "home", Collapsed: "home-no-kb"},
	}
	return m
}

// stripCells measures one strip row: drawn-cell icons measure like any
// text, so this is plain width.
func stripCells(line string) int {
	return lipgloss.Width(line)
}

// stripLines renders the full view and returns the two strip rows.
func stripLines(t *testing.T, m *model) (top, bot string) {
	t.Helper()
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != m.height {
		t.Fatalf("view lines = %d, want %d", len(lines), m.height)
	}
	return lines[m.height-2], lines[m.height-1]
}

// The strip is exactly 2 rows of full-width real cells: drawn icon art on
// both rows, 1x text on the bottom row alone, no escapes beyond SGR, the
// clock flush right.
func TestStripGeometry(t *testing.T) {
	m := stripModel()
	v := m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != 24 {
		t.Fatalf("view lines = %d, want 24", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 196 {
			t.Errorf("line %d width = %d, want 196", i, w)
		}
	}
	top, bot := lines[22], lines[23]
	for _, esc := range []string{"\x1b]66;", "\x1b[2C", "\x1b]8;"} {
		if strings.Contains(top, esc) || strings.Contains(bot, esc) {
			t.Errorf("strip carries a non-SGR escape %q the compositor would eat", esc)
		}
	}
	if s := strings.TrimSpace(ansi.Strip(top)); s != "" {
		t.Errorf("top row carries text %q, want blank (icons sit on the baseline row)", s)
	}
	if s := ansi.Strip(bot); !strings.HasPrefix(s, " "+homeGlyph+" ") {
		t.Errorf("bottom row does not open with the home glyph: %q", s[:12])
	}
	if !strings.Contains(ansi.Strip(bot), cupOffGlyph) {
		t.Error("bottom row missing the cup glyph")
	}
	if !strings.Contains(ansi.Strip(bot), batUnknownGlyph) {
		t.Error("bottom row missing the battery placeholder glyph (no logi frame yet)")
	}
	if !strings.Contains(ansi.Strip(bot), stripCollapseGlyph) {
		t.Error("bottom row missing the flip chevron (home is the expanded layout)")
	}
	plain := ansi.Strip(bot)
	for _, sub := range []string{"kb", "clod", "sys", "home", "bus absent"} {
		if !strings.Contains(plain, sub) {
			t.Errorf("bottom row missing %q", sub)
		}
	}
	if clock := strings.ToLower(m.now.Weekday().String()[:3]) + m.now.Format(" 15:04"); !strings.HasSuffix(plain, clock) {
		t.Errorf("bottom row %q does not end with the clock %q", plain, clock)
	}
}

// Strip hit zones ride the body table's tail: icon, the three tabs spanning
// both rows, the flip chevron, the cup, the always-present battery readout,
// then the whole-strip consume rect last -- no overlap among the specific
// rects. (No kitty_mod cell here: stripModel leaves Strip.KittyMod empty.)
func TestStripHitTable(t *testing.T) {
	m := stripModel()
	m.View()
	want := []rect{
		{0, 22, 3, 2},   // home icon glyph
		{3, 22, 4, 2},   // tab: kb
		{7, 22, 6, 2},   // tab: clod
		{13, 22, 5, 2},  // tab: sys
		{18, 22, 3, 2},  // flip chevron glyph
		{22, 22, 3, 2},  // caffeinate cup glyph (after the 1-col gap)
		{25, 22, 10, 2}, // battery readout cell (always-present chrome)
		{0, 22, 196, 2}, // whole-strip consume rect, last
	}
	if len(m.hits) < len(want) {
		t.Fatalf("hits = %d, want at least %d", len(m.hits), len(want))
	}
	got := m.hits[len(m.hits)-len(want):]
	for i, w := range want {
		if got[i].area != w {
			t.Errorf("strip hit %d area = %+v, want %+v", i, got[i].area, w)
		}
	}
	for i, h := range m.hits[:len(m.hits)-len(want)] {
		if h.area.y+h.area.h > 22 {
			t.Errorf("body hit %d (%+v) reaches into the strip", i, h.area)
		}
	}
	specific := len(want) - 1 // every rect but the whole-strip consume tail
	for i := range specific {
		for j := i + 1; j < specific; j++ {
			a, b := got[i].area, got[j].area
			if a.x < b.x+b.w && b.x < a.x+a.w {
				t.Errorf("strip hits %d and %d overlap: %+v %+v", i, j, a, b)
			}
		}
	}
}

// The rendered strip must SURVIVE the real render pipeline: bubbletea v2
// hands View().Content to ultraviolet's cell compositor, which forwards
// only SGR and OSC 8 -- this round-trip is what the terminal actually
// receives. The OSC 66 strip shipped green against View() strings and was
// invisible on glass; never again.
func TestStripSurvivesCompositor(t *testing.T) {
	m := stripModel()
	v := m.View()
	buf := uv.NewScreenBuffer(196, 24)
	uv.NewStyledString(v.Content).Draw(buf, buf.Bounds())

	cellRow := func(y, x0, x1 int) string {
		var b strings.Builder
		for x := x0; x < x1; x++ {
			if c := buf.CellAt(x, y); c != nil {
				b.WriteString(c.Content)
			}
		}
		return b.String()
	}
	if got := strings.TrimSpace(cellRow(22, 0, 24)); got != "" {
		t.Errorf("icon-band top row cells = %q, want blank", got)
	}
	if got := cellRow(23, 0, 3); got != " "+homeGlyph+" " {
		t.Errorf("home glyph cells = %q, want %q", got, " "+homeGlyph+" ")
	}
	// the kb tab's first letter must land INSIDE its hit rect {3,22,4,2}:
	// a dropped skip/escape would shift the row left and misroute taps
	if got := cellRow(23, 3, 7); !strings.Contains(got, "kb") {
		t.Errorf("kb tab cells = %q, want the label inside its hit rect", got)
	}
	if got := cellRow(23, 18, 21); !strings.Contains(got, stripCollapseGlyph) {
		t.Errorf("chevron cells = %q, want the collapse chevron inside its hit rect", got)
	}
	if got := cellRow(23, 22, 25); !strings.Contains(got, cupOffGlyph) {
		t.Errorf("cup cells = %q, want the cup glyph inside its hit rect", got)
	}
	// clock flush right on the bottom row
	clock := strings.ToLower(m.now.Weekday().String()[:3]) + m.now.Format(" 15:04")
	if got := cellRow(23, 196-len(clock), 196); got != clock {
		t.Errorf("clock cells = %q, want %q", got, clock)
	}
}

// An unknown toggle kind renders DEAD -- dim placeholder glyph, tap
// consumed with no effect -- so a config ahead of the binary stays
// visible, never a healthy-looking silent no-op.
func TestStripUnknownToggleRendersDead(t *testing.T) {
	m := stripModel()
	m.cfg.Strip.Toggles = []config.StripToggle{{Kind: "mystery"}}
	_, bot := stripLines(t, m)
	if !strings.Contains(ansi.Strip(bot), "?") {
		t.Error("unknown toggle kind did not render the dead placeholder")
	}
	if strings.Contains(ansi.Strip(bot), cupOffGlyph) {
		t.Error("unknown toggle kind rendered the cup")
	}
	before := m.layout
	if !m.resolveTap(23, 22) {
		t.Fatal("dead toggle tap not consumed")
	}
	if m.layout != before || m.lastGst == "caffeinate: bus absent" {
		t.Error("dead toggle tap had an effect")
	}
}

// Tab accent follows m.layout == target.
func TestStripTabAccent(t *testing.T) {
	m := stripModel()
	_, bot := stripLines(t, m)
	if strings.Contains(bot, "\x1b[32mclod") {
		t.Error("inactive tab accent-toned")
	}
	m.layout = "hub"
	m.resetLayout()
	_, bot = stripLines(t, m)
	if !strings.Contains(bot, "\x1b[32mclod") {
		t.Error("active-target tab not accent-toned")
	}
}

// Strip taps ride the tray verbs: the cup routes a caffeinate toggle (loud
// when the bus is absent), a stub target flashes "soon" in the warn tone, a
// real target switches layout locally while the bus is absent, the home icon
// leads back, and a bare-strip tap is consumed, never leaking into the body.
func TestStripTaps(t *testing.T) {
	m := stripModel()
	m.View()

	if !m.resolveTap(23, 22) {
		t.Fatal("tap on the cup not consumed")
	}
	if m.lastGst != "caffeinate: bus absent" {
		t.Fatalf("lastGst = %q, want the bus-absent notice", m.lastGst)
	}

	if !m.resolveTap(14, 23) {
		t.Fatal("tap on the sys tab not consumed")
	}
	if m.layout != "home" {
		t.Fatalf("stub tab switched layout to %q", m.layout)
	}
	if _, ok := m.trayFlash["sys"]; !ok {
		t.Fatal("stub tap did not arm the flash")
	}
	m.now = time.Now()
	_, bot := stripLines(t, m)
	if !strings.Contains(bot, "\x1b[33msoon") {
		t.Error("flashing tab does not render soon in the warn tone")
	}
	// the status tally still names the stub ("nav: sys (soon)"), so probe
	// for the warn-toned tab label specifically
	m.now = time.Now().Add(trayFlashFor + time.Second)
	_, bot = stripLines(t, m)
	if strings.Contains(bot, "\x1b[33msoon") {
		t.Error("flash did not clear after the window")
	}

	if !m.resolveTap(8, 22) {
		t.Fatal("tap on the clod tab not consumed")
	}
	if m.layout != "hub" {
		t.Fatalf("layout = %q, want hub", m.layout)
	}

	m.View()
	if !m.resolveTap(0, 23) {
		t.Fatal("tap on the home icon not consumed")
	}
	if m.layout != "home" {
		t.Fatalf("layout = %q, want home", m.layout)
	}

	m.View()
	if !m.resolveTap(100, 22) {
		t.Fatal("bare strip tap not consumed by the whole-strip rect")
	}
	if m.layout != "home" {
		t.Fatalf("bare strip tap changed layout to %q", m.layout)
	}
}

// The flip chevron renders on both sides of the pair with the right glyph,
// and its tap flips to the other side (bus absent: dock-local switch, the
// TestStripTaps precedent): collapse on the expanded layout hides the kb
// column, expand on the collapsed layout restores it.
func TestStripFlipChevron(t *testing.T) {
	m := stripModel()
	_, bot := stripLines(t, m)
	plain := ansi.Strip(bot)
	if !strings.Contains(plain, stripCollapseGlyph) {
		t.Error("expanded layout missing the collapse chevron")
	}
	if strings.Contains(plain, stripExpandGlyph) {
		t.Error("expanded layout renders the expand chevron")
	}

	// chevron slot: home icon (3) + kb (4) + clod (6) + sys (5) -> x 18
	if !m.resolveTap(19, 22) {
		t.Fatal("collapse tap not consumed")
	}
	if m.layout != "home-no-kb" {
		t.Fatalf("collapse tap landed on %q, want home-no-kb", m.layout)
	}

	_, bot = stripLines(t, m)
	plain = ansi.Strip(bot)
	if !strings.Contains(plain, stripExpandGlyph) {
		t.Error("collapsed layout missing the expand chevron")
	}
	if strings.Contains(plain, stripCollapseGlyph) {
		t.Error("collapsed layout renders the collapse chevron")
	}
	if !m.resolveTap(19, 23) {
		t.Fatal("expand tap not consumed")
	}
	if m.layout != "home" {
		t.Fatalf("expand tap landed on %q, want home", m.layout)
	}
}

// No chevron outside the pair: another layout renders neither glyph and the
// chevron slot falls to the toggles; a flip-less config never renders one.
func TestStripFlipAbsent(t *testing.T) {
	m := stripModel()
	m.layout = "hub"
	m.resetLayout()
	_, bot := stripLines(t, m)
	plain := ansi.Strip(bot)
	if strings.Contains(plain, stripCollapseGlyph) || strings.Contains(plain, stripExpandGlyph) {
		t.Error("chevron rendered on a layout outside the flip pair")
	}
	for _, h := range m.hits {
		if h.area == (rect{18, 22, 3, 2}) {
			t.Error("chevron hit rect registered outside the flip pair")
		}
	}

	m = stripModel()
	m.cfg.Strip.Flip = nil
	_, bot = stripLines(t, m)
	plain = ansi.Strip(bot)
	if strings.Contains(plain, stripCollapseGlyph) || strings.Contains(plain, stripExpandGlyph) {
		t.Error("chevron rendered without a flip block")
	}
}

// A cache-hit frame aliases homeCache's hit slice: appending the strip hits
// must reallocate instead of growing into the cached backing array.
func TestStripHitsLeaveCacheBackingAlone(t *testing.T) {
	m := stripModel()
	m.View() // prime the cache
	m.View() // cache hit: m.hits starts as homeCache.hits
	c := m.homeCache.hits
	for i, h := range c[len(c):cap(c)] {
		if h.do != nil || h.area != (rect{}) {
			t.Fatalf("strip hit grew into the cached backing array at %d: %+v", len(c)+i, h.area)
		}
	}
	for i, h := range c {
		if h.area.y >= 22 {
			t.Errorf("cached hit %d is a strip rect: %+v", i, h.area)
		}
	}
}

// A tapped strip control flashes for tapFlashFor then reverts. No palette
// = the indexed bright-fg fallback; the hit table stays identical across
// flash and no-flash frames (the flash is bg/fg only).
func TestStripTapFlashIndexedFallback(t *testing.T) {
	m := stripModel()
	m.View()
	areas := func() []rect {
		out := make([]rect, len(m.hits))
		for i, h := range m.hits {
			out[i] = h.area
		}
		return out
	}
	if !m.resolveTap(0, 23) { // home icon (layout is home: nav no-ops)
		t.Fatal("tap on the home icon not consumed")
	}
	flashed := m.tapStyle(m.sty.brand).Render(homeGlyph)
	base := m.sty.brand.Render(homeGlyph)

	m.now = time.Now()
	if v := m.View(); !strings.Contains(v.Content, flashed) {
		t.Error("tapped home icon not flash-styled")
	}
	during := areas()

	m.now = time.Now().Add(tapFlashFor + time.Second)
	v := m.View()
	if strings.Contains(v.Content, flashed) {
		t.Error("home icon flash did not revert after expiry")
	}
	if !strings.Contains(v.Content, base) {
		t.Error("home icon lost its brand tone after the flash")
	}
	after := areas()
	if !slices.Equal(during, after) {
		t.Fatalf("hit table changed across flash/no-flash:\nduring %v\nafter  %v", during, after)
	}
}

// A tap-dispatched Update arms the one-shot expiry redraw: a tap that
// flashes returns a non-nil Cmd, the expiry msg advances the clock past
// the flash and returns nil (no re-arm), and a tap that flashes nothing
// returns nil.
func TestTapFlashArmsExpiryTick(t *testing.T) {
	m := stripModel()
	m.View()
	_, cmd := m.Update(tea.MouseClickMsg{X: 0, Y: 23}) // home icon: tap flash
	if cmd == nil {
		t.Fatal("tap-dispatched Update armed no expiry redraw")
	}
	if !m.flashLive("icon:home") {
		t.Fatal("tap did not arm the icon flash")
	}
	_, cmd = m.Update(flashTickMsg(m.now.Add(tapFlashFor + time.Millisecond)))
	if cmd != nil {
		t.Fatal("flash expiry msg re-armed a Cmd")
	}
	if m.flashLive("icon:home") {
		t.Fatal("flash survived its expiry tick")
	}
	m.View()
	_, cmd = m.Update(tea.MouseClickMsg{X: 100, Y: 10}) // body: no flash
	if cmd != nil {
		t.Fatal("flash-less tap armed an expiry redraw")
	}
}

// With a palette broadcast the tap flash lifts the control's background
// (lipgloss.Lighten of the theme background), not the indexed fallback.
func TestStripTapFlashPaletteLift(t *testing.T) {
	m := stripModel()
	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day", Palette: busPalette()})
	m.View()
	if !m.resolveTap(0, 23) {
		t.Fatal("tap on the home icon not consumed")
	}
	flashed := m.tapStyle(m.sty.brand).Render(homeGlyph)
	if !strings.Contains(flashed, "48;2;") {
		t.Fatalf("palette tap style %q carries no background lift", flashed)
	}
	m.now = time.Now()
	if v := m.View(); !strings.Contains(v.Content, flashed) {
		t.Error("tapped home icon not bg-lifted under a palette")
	}
	m.now = time.Now().Add(tapFlashFor + time.Second)
	if v := m.View(); strings.Contains(v.Content, flashed) {
		t.Error("bg-lift flash did not revert after expiry")
	}
}

// The battery glyph tracks the state-of-charge bucket, and charging outranks
// the bucket at every level.
func TestStripBatteryGlyphBuckets(t *testing.T) {
	cases := []struct {
		soc  int
		want string
	}{
		{0, batEmptyGlyph},
		{25, batQuarterGlyph},
		{50, batHalfGlyph},
		{75, batThreeQuarterGlyph},
		{100, batFullGlyph},
	}
	for _, c := range cases {
		if g := batteryGlyph(c.soc, false); g != c.want {
			t.Errorf("batteryGlyph(%d, false) = %q, want %q", c.soc, g, c.want)
		}
		if g := batteryGlyph(c.soc, true); g != batChargingGlyph {
			t.Errorf("batteryGlyph(%d, true) = %q, want the charging glyph", c.soc, g)
		}
	}
}

// A fresh battery frame renders its bucket glyph + pct in the live tone; a
// stale frame keeps the last-known SoC but dims it (bold-vs-faint liveness);
// no-data renders the neutral placeholder with no pct -- never blank, and the
// cell width never shifts across these states.
func TestStripBatteryReadout(t *testing.T) {
	m := stripModel()
	m.logi = &proto.LogiState{TimeNS: m.now.UnixNano(), Kind: "mx", SoC: 50, Charging: false}
	_, bot := stripLines(t, m)
	plain := ansi.Strip(bot)
	if !strings.Contains(plain, mouseGlyph+" "+batHalfGlyph) {
		t.Error("fresh battery frame missing the mouse marker + half glyph")
	}
	if !strings.Contains(plain, "50%") {
		t.Error("fresh battery frame missing the pct")
	}

	m.logi = &proto.LogiState{TimeNS: m.now.Add(-2 * logiStale).UnixNano(), Kind: "mx", SoC: 50}
	_, bot = stripLines(t, m)
	if !strings.Contains(ansi.Strip(bot), "50%") {
		t.Error("stale battery frame dropped the last-known pct")
	}
	if !strings.Contains(bot, chromeDim.Render(mouseGlyph+" "+batHalfGlyph+" 50%")) {
		t.Error("stale battery frame not rendered dim")
	}

	m.logi = nil
	_, bot = stripLines(t, m)
	plain = ansi.Strip(bot)
	if !strings.Contains(plain, mouseGlyph+" "+batUnknownGlyph) {
		t.Error("no-data battery cell missing the mouse marker + placeholder glyph")
	}
	if strings.Contains(plain, "%") {
		t.Error("no-data battery cell rendered a pct")
	}
}

// The act-fail warn cell renders the bus's latest failure in the warn tone
// while fresh, and is gone entirely -- no cell, no text -- once it decays
// past actFailFor; the decay is a clock compare against m.now, so the test
// walks the clock instead of sleeping.
func TestStripActFailWarnCell(t *testing.T) {
	m := stripModel()
	_, bot := stripLines(t, m)
	if strings.Contains(ansi.Strip(bot), "exit status 1") {
		t.Fatal("act-fail text rendered before any failure broadcast")
	}

	m.handleBusMsg(proto.Msg{Type: proto.TypeActFail,
		ActFail: &proto.ActFail{TimeNS: m.now.UnixNano(), Msg: "open: exit status 1"}})
	_, bot = stripLines(t, m)
	if !strings.Contains(bot, chromeWarn.Render("! open: exit status 1")) {
		t.Error("fresh act failure not rendered in the warn tone")
	}

	m.now = m.now.Add(actFailFor + time.Second)
	_, bot = stripLines(t, m)
	if strings.Contains(ansi.Strip(bot), "exit status 1") {
		t.Error("act-fail cell survived past the decay window")
	}
}

// The kitty_mod cell renders a "kitty_mod" text label ahead of the chord's
// modifier glyphs (bare glyphs read as floating keys), and renders nothing
// at all when the chord is empty.
func TestStripKittyModNote(t *testing.T) {
	want := "kitty_mod " + kittyModLabel("ctrl+opt+shift")
	if want == "kitty_mod " {
		t.Fatal("kittyModLabel produced no glyphs for a known chord")
	}

	m := stripModel()
	m.cfg.Strip.KittyMod = "ctrl+opt+shift"
	_, bot := stripLines(t, m)
	if !strings.Contains(ansi.Strip(bot), want) {
		t.Errorf("kitty_mod note %q not rendered in %q", want, ansi.Strip(bot))
	}

	m2 := stripModel()
	m2.cfg.Strip.KittyMod = ""
	_, bot2 := stripLines(t, m2)
	if strings.Contains(ansi.Strip(bot2), want) {
		t.Error("empty kitty_mod still rendered the chord glyphs")
	}
}
