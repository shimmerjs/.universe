package dock

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

func homeTestConfig() *config.Config {
	return &config.Config{
		Widgets: map[string]config.Widget{
			"dock-rail": {ID: "dock-rail", Title: "dock", Chrome: true,
				Render: config.Render{Kind: "native", Module: "dock-mirror"}},
			"nav-tray": {ID: "nav-tray", Title: "nav", Chrome: true,
				Render: config.Render{Kind: "native", Module: "nav-tray", Params: map[string]any{
					"entries": []any{
						map[string]any{"label": "home", "target": "home"},
						map[string]any{"label": "hub", "target": "hub"},
						map[string]any{"label": "keyboard", "target": "keyboard"},
					},
					"toggles": []any{
						map[string]any{"kind": "caffeinate"},
					},
				}}},
			"claude-hud": {ID: "claude-hud", Title: "claude",
				Render: config.Render{Kind: "native", Module: "claude-sessions"}},
			"cpumem": {ID: "cpumem", Title: "cpu / mem",
				Render: config.Render{Kind: "native", Module: "cpumem"}},
			"disk": {ID: "disk", Title: "disk",
				Render: config.Render{Kind: "native", Module: "disk"}},
		},
		Layouts: map[string]config.Layout{
			"home": {Kind: "home", Regions: []config.Region{
				{Widget: "dock-rail", Edge: "left", Size: 20},
				{Widget: "nav-tray", Edge: "right", Size: 12},
				{Widget: "claude-hud", Edge: "top", Size: 10},
				{Widget: "cpumem", Edge: "fill"},
				{Widget: "disk", Edge: "fill"},
			}},
			// a second home-kind layout: the nav target that proves the layout
			// switch, hit-table rebuild, and home-strip return
			"hub": {Kind: "home", Regions: []config.Region{
				{Widget: "cpumem", Edge: "fill"},
			}},
		},
		Layout: "home",
	}
}

func newHomeModel(w, h int) *model {
	return &model{
		cfg:        homeTestConfig(),
		layout:     "home",
		width:      w,
		height:     h,
		now:        time.Now(),
		widgetData: map[string]module.Data{},
		widgetErr:  map[string]string{},
		sty:        buildStyles(day),
	}
}

func railData() module.Data {
	return module.Data{Title: "dock", Rows: []module.Row{
		{Kind: module.RowText, Text: "Safari", Act: []string{"open", "-a", "Safari"}},
		{Kind: module.RowText, Text: "Mail", Act: []string{"open", "-a", "Mail"}},
	}}
}

func TestHomeRegionGeometry(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["dock-rail"] = railData()
	m.widgetData["cpumem"] = module.Data{Rows: []module.Row{
		module.Resource("cpu", 0.38, []float64{0.1, 0.7, 0.9}, "38% of 12"),
		module.Resource("mem", 0.57, []float64{0.5}, "20.5/36 GiB"),
	}}

	out := m.renderHome(17)
	lines := strings.Split(out, "\n")
	if len(lines) != 17 {
		t.Fatalf("home body lines = %d, want 17", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 320 {
			t.Errorf("line %d width = %d, want 320", i, w)
		}
	}

	// hit table order: regions in peel order, chrome tiles ahead of their
	// region's consume-all area. Rail tiles are a two-column bordered grid:
	// width (20-1)/2 = 9, one gap column between, railTileH rows tall. The
	// caffeinate cup toggle pins to the tray bottom (17-row tray: y 14).
	want := []rect{
		{0, 0, 9, 3},      // rail button: Safari (col 0)
		{10, 0, 9, 3},     // rail button: Mail (col 1)
		{0, 0, 20, 17},    // rail area
		{308, 0, 12, 3},   // tray: home
		{308, 3, 12, 3},   // tray: hub
		{308, 6, 12, 3},   // tray: keyboard
		{308, 14, 12, 3},  // tray toggle: caffeinate cup
		{308, 0, 12, 17},  // tray area
		{20, 0, 288, 10},  // claude-hud
		{20, 10, 144, 7},  // cpumem
		{164, 10, 144, 7}, // disk
	}
	if len(m.hits) != len(want) {
		t.Fatalf("hits = %d, want %d", len(m.hits), len(want))
	}
	for i, w := range want {
		if m.hits[i].area != w {
			t.Errorf("hit %d area = %+v, want %+v", i, m.hits[i].area, w)
		}
	}
}

// The home-no-kb variant (the strip flip's collapsed layout, region math
// mirroring edge.cue at the Edge's 196x24): no kb-live region anywhere,
// claude-panel takes the freed width (148 = home's 73 + kb's 75). Both
// layouts run at the full 196-col body -- no outer frame, no per-view
// return column.
func TestHomeNoKBClaudeColumnWidens(t *testing.T) {
	m := &model{
		cfg: &config.Config{
			Widgets: map[string]config.Widget{
				"kb-live": {ID: "kb-live", Title: "keyboard", Chrome: true,
					Render: config.Render{Kind: "chrome", Module: "kb-live"}},
				"claude-panel": {ID: "claude-panel", Title: "claude",
					Render: config.Render{Kind: "native", Module: "claude-panel"}},
				"dock-rail": {ID: "dock-rail", Title: "dock", Chrome: true,
					Render: config.Render{Kind: "native", Module: "dock-mirror"}},
				"resources": {ID: "resources", Title: "resources",
					Render: config.Render{Kind: "native", Module: "resources"}},
			},
			Layouts: map[string]config.Layout{
				"home": {Kind: "home", Regions: []config.Region{
					{Widget: "kb-live", Edge: "right", Size: 75},
					{Widget: "claude-panel", Edge: "right", Size: 73},
					{Widget: "dock-rail", Edge: "top", Size: 8},
					{Widget: "resources", Edge: "fill"},
				}},
				"home-no-kb": {Kind: "home", Regions: []config.Region{
					{Widget: "claude-panel", Edge: "right", Size: 148},
					{Widget: "dock-rail", Edge: "top", Size: 8},
					{Widget: "resources", Edge: "fill"},
				}},
			},
			Layout: "home",
		},
		layout:     "home",
		width:      196,
		height:     24,
		now:        time.Now(),
		widgetData: map[string]module.Data{},
		widgetErr:  map[string]string{},
		sty:        buildStyles(day),
		kbLoaded:   true, // board deliberately absent: kb-live renders the sync hint
	}
	hasHit := func(r rect) bool {
		for _, h := range m.hits {
			if h.area == r {
				return true
			}
		}
		return false
	}

	// expanded home (the config default): 196x22 body, kb column 75 cols,
	// claude 73
	v := m.View()
	if lines := strings.Split(v.Content, "\n"); len(lines) != 24 {
		t.Fatalf("home view lines = %d, want 24", len(lines))
	}
	kbRegion := rect{121, 0, 75, 22}
	claudeExpanded := rect{48, 0, 73, 22}
	if !hasHit(kbRegion) {
		t.Fatalf("expanded home missing the kb region rect %+v", kbRegion)
	}
	if !hasHit(claudeExpanded) {
		t.Fatalf("expanded home missing the claude rect %+v", claudeExpanded)
	}

	// collapsed variant: claude at 148 cols, no kb region
	m.layout = "home-no-kb"
	m.resetLayout()
	v = m.View()
	lines := strings.Split(v.Content, "\n")
	if len(lines) != 24 {
		t.Fatalf("home-no-kb view lines = %d, want 24", len(lines))
	}
	for i, l := range lines[:22] {
		if w := lipgloss.Width(l); w != 196 {
			t.Errorf("line %d width = %d, want 196", i, w)
		}
	}
	claudeCollapsed := rect{48, 0, 148, 22}
	if !hasHit(claudeCollapsed) {
		t.Fatalf("home-no-kb missing the widened claude rect %+v", claudeCollapsed)
	}
	if hasHit(kbRegion) {
		t.Fatal("home-no-kb still places the kb region")
	}
	if claudeCollapsed.w <= claudeExpanded.w {
		t.Fatal("claude column did not widen against expanded home")
	}
}

func TestHomeRegionClampSurvivesOversizedPeel(t *testing.T) {
	m := newHomeModel(40, 10)
	// rail size 20 + tray 12 leave 6 cols for claude/fills on a 38-wide
	// interior; nothing may panic and every line stays exact
	out := m.renderHome(9)
	lines := strings.Split(out, "\n")
	if len(lines) != 9 {
		t.Fatalf("home body lines = %d, want 9", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 40 {
			t.Errorf("line %d width = %d, want 40", i, w)
		}
	}
}

func TestHomeTapRailTileSendsRowAct(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["dock-rail"] = railData()
	_ = m.renderHome(17)
	got := attachFakeBus(t, m)

	// second tile (Mail): right column of band 0, x 11..19, y 1..2
	if !m.resolveTap(12, 2) {
		t.Fatal("tap inside the rail not consumed")
	}
	msg := wantBusMsg(t, got)
	if msg.Type != proto.TypeRowAct || msg.Widget != "dock-rail" {
		t.Fatalf("got %s/%s, want row-act/dock-rail", msg.Type, msg.Widget)
	}
	if len(msg.Argv) != 3 || msg.Argv[2] != "Mail" {
		t.Fatalf("argv = %v, want open -a Mail", msg.Argv)
	}
}

// widenRail grows the fixture rail region so full names fit a tile
// (width (size-1)/2).
func widenRail(m *model, size int) {
	l := m.cfg.Layouts["home"]
	l.Regions[0].Size = size
	m.cfg.Layouts["home"] = l
}

// Rail buttons render side by side: both band-0 names share the button's
// middle row, lowercased, framed by border rows above and below.
func TestRailTwoColumnBands(t *testing.T) {
	m := newHomeModel(320, 18)
	widenRail(m, 22)
	m.widgetData["dock-rail"] = railData()
	lines := strings.Split(m.renderHome(17), "\n")
	// rail band 0 occupies body rows 0..2; the name row is the middle
	mid := ansi.Strip(lines[1])
	if !strings.Contains(mid, "safari") || !strings.Contains(mid, "mail") {
		t.Fatalf("band 0 name row = %q, want safari and mail side by side", mid)
	}
	if strings.Contains(ansi.Strip(lines[0]), "safari") {
		t.Error("name leaked onto the button's border row")
	}
	if !strings.Contains(lines[0], lipgloss.RoundedBorder().TopLeft) {
		t.Error("band 0 border row missing the button frame")
	}
}

// Button text is the app name through params.nicknames, then lowercased; no
// monogram shorthand anywhere.
func TestRailNicknameAndLowercase(t *testing.T) {
	m := newHomeModel(320, 18)
	widenRail(m, 22)
	w := m.cfg.Widgets["dock-rail"]
	w.Render.Params = map[string]any{"nicknames": map[string]any{"Google Chrome": "chrome"}}
	m.cfg.Widgets["dock-rail"] = w
	m.widgetData["dock-rail"] = module.Data{Rows: []module.Row{
		{Kind: module.RowText, Text: "Google Chrome", Act: []string{"open", "-a", "Google Chrome"}},
		{Kind: module.RowText, Text: "Safari", Act: []string{"open", "-a", "Safari"}},
	}}
	plain := ansi.Strip(m.renderHome(17))
	if !strings.Contains(plain, "chrome") {
		t.Error("nicknamed app not rendered as chrome")
	}
	if strings.Contains(plain, "google") || strings.Contains(plain, "Google") {
		t.Error("nickname not applied before display")
	}
	if !strings.Contains(plain, "safari") || strings.Contains(plain, "Safari") {
		t.Error("un-nicknamed app not lowercased")
	}
	if strings.Contains(plain, "GC") || strings.Contains(plain, "SA") {
		t.Error("monogram shorthand rendered")
	}
}

// A rail button is railTileH lines of exactly w cells: rounded border rows
// top and bottom, the name centered on the middle row -- the frame is the
// button read, with no padding beyond it.
func TestRailTilePlacement(t *testing.T) {
	lines := railTile("safari", 8, chromeFG)
	if len(lines) != railTileH {
		t.Fatalf("lines = %d, want %d", len(lines), railTileH)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 8 {
			t.Errorf("line %d width = %d, want 8", i, w)
		}
	}
	if !strings.Contains(ansi.Strip(lines[1]), "safari") {
		t.Error("name not on the button's middle row")
	}
	if !strings.Contains(lines[0], lipgloss.RoundedBorder().TopLeft) ||
		!strings.Contains(lines[2], lipgloss.RoundedBorder().BottomLeft) {
		t.Error("button frame missing")
	}
	// long names truncate inside the button, never widen it
	long := railTile("verylongname", 8, chromeFG)
	for i, l := range long {
		if w := lipgloss.Width(l); w != 8 {
			t.Errorf("long-name line %d width = %d, want 8", i, w)
		}
	}
}

// A 15-row rail holds 5 bands x 2 = 10 bordered buttons: 16 apps truncate
// to 9 buttons plus a dim "+7" cell, never silently.
func TestRailOverflowCell(t *testing.T) {
	m := newHomeModel(320, 18)
	widenRail(m, 22)
	rows := make([]module.Row, 0, 16)
	for i := range 16 {
		name := fmt.Sprintf("app%02d", i)
		rows = append(rows, module.Row{Kind: module.RowText, Text: name, Act: []string{"open", "-a", name}})
	}
	m.widgetData["dock-rail"] = module.Data{Rows: rows}
	plain := ansi.Strip(m.renderHome(17))
	if !strings.Contains(plain, "app08") {
		t.Error("last shown app missing")
	}
	if strings.Contains(plain, "app09") {
		t.Error("truncated app still rendered")
	}
	if !strings.Contains(plain, "+7") {
		t.Error("overflow cell missing")
	}
	buttons := 0
	for _, h := range m.hits {
		if h.area.w == 10 && h.area.h == railTileH {
			buttons++
		}
	}
	if buttons != 9 {
		t.Errorf("tile hits = %d, want 9 (overflow cell is not a tile)", buttons)
	}
}

// Two columns must fit 16 running apps in 24 rows -- the 1-column rail
// height-truncated the running tail and kitty went silently missing.
func TestRail24RowsFitsSixteenApps(t *testing.T) {
	m := newHomeModel(40, 30)
	rows := make([]module.Row, 0, 16)
	for i := range 15 {
		name := fmt.Sprintf("app%02d", i)
		rows = append(rows, module.Row{Kind: module.RowText, Text: name, Act: []string{"open", "-a", name}})
	}
	rows = append(rows, module.Row{Kind: module.RowText, Text: "kitty", Act: []string{"open", "-a", "kitty"}})
	m.widgetData["dock-rail"] = module.Data{Rows: rows}
	m.resetHits()
	out := m.renderRail(m.cfg.Widgets["dock-rail"], rect{0, 0, 15, 24})
	plain := ansi.Strip(out)
	for i := range 15 {
		if !strings.Contains(plain, fmt.Sprintf("app%02d", i)) {
			t.Errorf("app%02d missing from a 24-row rail", i)
		}
	}
	if !strings.Contains(plain, "kitty") {
		t.Error("kitty missing from a 24-row rail")
	}
	if strings.Contains(plain, "+") {
		t.Error("16 apps in 24 rows must not overflow")
	}
	buttons := 0
	for _, h := range m.hits {
		if h.area.w == 7 && h.area.h == railTileH {
			buttons++
		}
	}
	if buttons != 16 {
		t.Errorf("tile hits = %d, want 16", buttons)
	}
}

// A 46x8 rail scales columns to the width instead of stretching two tiles:
// 4 columns of 10-wide bordered buttons in 2 bands (capacity 8), and "+N"
// appears only past a genuine overflow.
func TestRail46x8WidthScaledColumns(t *testing.T) {
	m := newHomeModel(320, 18)
	rows := make([]module.Row, 0, 9)
	for i := range 8 {
		name := fmt.Sprintf("app%02d", i)
		rows = append(rows, module.Row{Kind: module.RowText, Text: name, Act: []string{"open", "-a", name}})
	}
	m.widgetData["dock-rail"] = module.Data{Rows: rows}
	m.resetHits()
	out := m.renderRail(m.cfg.Widgets["dock-rail"], rect{0, 0, 46, 8})
	plain := ansi.Strip(out)
	for i := range 8 {
		if !strings.Contains(plain, fmt.Sprintf("app%02d", i)) {
			t.Errorf("app%02d missing from a 46x8 rail", i)
		}
	}
	if strings.Contains(plain, "+") {
		t.Error("8 apps at exact capacity must not overflow")
	}
	var buttons []rect
	for _, h := range m.hits {
		if h.area.w == 10 && h.area.h == railTileH {
			buttons = append(buttons, h.area)
		}
	}
	if len(buttons) != 8 {
		t.Fatalf("tile hits = %d, want 8", len(buttons))
	}
	// 4 columns: lpad (46-43)/2 = 1, columns at x 1, 12, 23, 34; 3-row bands
	want := []rect{
		{1, 0, 10, 3}, {12, 0, 10, 3}, {23, 0, 10, 3}, {34, 0, 10, 3},
		{1, 3, 10, 3}, {12, 3, 10, 3}, {23, 3, 10, 3}, {34, 3, 10, 3},
	}
	for i, w := range want {
		if buttons[i] != w {
			t.Errorf("tile %d rect = %+v, want %+v", i, buttons[i], w)
		}
	}

	// one past capacity: 7 buttons plus a "+2" cell, never silently
	rows = append(rows, module.Row{Kind: module.RowText, Text: "app08", Act: []string{"open", "-a", "app08"}})
	m.widgetData["dock-rail"] = module.Data{Rows: rows}
	m.resetHits()
	out = m.renderRail(m.cfg.Widgets["dock-rail"], rect{0, 0, 46, 8})
	plain = ansi.Strip(out)
	if !strings.Contains(plain, "+2") {
		t.Error("overflow cell missing")
	}
	if strings.Contains(plain, "app07") {
		t.Error("truncated app still rendered")
	}
}

// Minimized-window rows (dim + act) are their own tier after the running
// buttons -- same grid, dim label -- and an act-less dim note row lists
// under the grid instead of becoming a button.
func TestRailMinimizedSectionAndNote(t *testing.T) {
	m := newHomeModel(320, 18)
	widenRail(m, 22)
	m.widgetData["dock-rail"] = module.Data{Rows: []module.Row{
		{Kind: module.RowText, Text: "Safari", Act: []string{"open", "-a", "Safari"}},
		{Kind: module.RowText, Text: "Mail", Act: []string{"open", "-a", "Mail"}},
		{Kind: module.RowText, Text: "Scratch", Key: "kitty", Style: module.StyleDim,
			Act: []string{"open", "-b", "net.kovidgoyal.kitty"}},
		{Kind: module.RowText, Text: "minimized: grant accessibility", Style: module.StyleDim},
	}}
	lines := strings.Split(m.renderHome(17), "\n")

	// band 0 = running names on its middle row; band 1's middle row carries
	// the minimized title, dim-labeled (running labels carry identity hue)
	if mid := ansi.Strip(lines[1]); !strings.Contains(mid, "safari") || !strings.Contains(mid, "mail") {
		t.Fatalf("band 0 = %q, want the running tiles first", mid)
	}
	if !strings.Contains(ansi.Strip(lines[4]), "scratch") {
		t.Fatalf("band 1 = %q, want the minimized title", ansi.Strip(lines[4]))
	}
	if !strings.Contains(lines[4], "\x1b[90mscratch") {
		t.Error("minimized label not dim")
	}
	if strings.Contains(lines[1], "\x1b[90msafari") {
		t.Error("running label dimmed")
	}
	// the note fitCells to the rail width, so match its surviving prefix
	note := false
	for _, l := range lines[6:] {
		if strings.Contains(ansi.Strip(l), "minimized: grant") {
			note = true
		}
	}
	if !note {
		t.Error("degrade note not listed under the grid")
	}

	// hit rects: exactly three tiles, the minimized one at band 1 col 0
	var buttons []rect
	for _, h := range m.hits {
		if h.area.w == 10 && h.area.h == railTileH {
			buttons = append(buttons, h.area)
		}
	}
	if len(buttons) != 3 {
		t.Fatalf("tile hits = %d, want 3 (note row is not a tile)", len(buttons))
	}
	if want := (rect{0, 3, 10, 3}); buttons[2] != want {
		t.Fatalf("minimized tile rect = %+v, want %+v", buttons[2], want)
	}

	// tap on the minimized tile activates the owning app
	got := attachFakeBus(t, m)
	if !m.resolveTap(1, 4) {
		t.Fatal("tap on the minimized tile not consumed")
	}
	msg := wantBusMsg(t, got)
	if msg.Type != proto.TypeRowAct || len(msg.Argv) != 3 ||
		msg.Argv[1] != "-b" || msg.Argv[2] != "net.kovidgoyal.kitty" {
		t.Fatalf("argv = %v, want open -b net.kovidgoyal.kitty", msg.Argv)
	}
}

func TestHomeTapTraySwitchesLayout(t *testing.T) {
	m := newHomeModel(320, 18)
	_ = m.renderHome(17)

	// tray entries stack from y=1 in 3-row buttons: home, hub, keyboard
	if !m.resolveTap(310, 4) {
		t.Fatal("tap on the hub entry not consumed")
	}
	if m.layout != "hub" {
		t.Fatalf("layout = %q, want hub", m.layout)
	}
}

// TestHomeTapTraySwitchesLayout (above) is the bus-absent fallback: nav
// stays dock-local. With the bus connected, nav must route through it as a
// ctl layout instead -- the local switch waits for the TypeLayout broadcast.
func TestHomeTapTraySendsCtlWhenBusConnected(t *testing.T) {
	m := newHomeModel(320, 18)
	_ = m.renderHome(17)
	got := attachFakeBus(t, m)

	if !m.resolveTap(310, 4) {
		t.Fatal("tap on the hub entry not consumed")
	}
	if m.layout != "home" {
		t.Fatalf("bus-connected nav switched locally to %q", m.layout)
	}
	msg := wantBusMsg(t, got)
	if msg.Type != proto.TypeCtl || msg.Cmd != "layout" || msg.Arg != "hub" {
		t.Fatalf("got %s/%s/%s, want ctl/layout/hub", msg.Type, msg.Cmd, msg.Arg)
	}
}

func TestHomeTapTrayStubFlashesSoon(t *testing.T) {
	m := newHomeModel(320, 18)
	_ = m.renderHome(17)

	if !m.resolveTap(310, 8) {
		t.Fatal("tap on the keyboard entry not consumed")
	}
	if m.layout != "home" {
		t.Fatalf("stub target switched layout to %q", m.layout)
	}
	if _, ok := m.trayFlash["keyboard"]; !ok {
		t.Fatal("stub tap did not arm the flash")
	}
	m.now = time.Now()
	out := m.renderHome(17)
	if !strings.Contains(out, "soon") {
		t.Error("flashing entry does not render soon")
	}
	m.now = time.Now().Add(trayFlashFor + time.Second)
	out = m.renderHome(17)
	if strings.Contains(out, "soon") {
		t.Error("flash did not clear after the window")
	}
}

// The cup renders the outline glyph until the bus says on (unknown == off
// shape); on flips to the filled glyph in the accent tone.
func TestTrayCupRendersBothStates(t *testing.T) {
	m := newHomeModel(320, 18)
	out := m.renderHome(17)
	if !strings.Contains(out, cupOffGlyph) {
		t.Error("pre-broadcast cup not the outline glyph")
	}
	if strings.Contains(out, cupOnGlyph) {
		t.Error("pre-broadcast cup rendered filled")
	}

	m.caffeinate = "on"
	out = m.renderHome(17)
	if !strings.Contains(out, cupOnGlyph) {
		t.Error("on-state cup not the filled glyph")
	}
	if strings.Contains(out, cupOffGlyph) {
		t.Error("on-state cup still outline")
	}
	if !strings.Contains(out, "\x1b[32m"+cupOnGlyph) {
		t.Error("on-state cup not accent-toned")
	}

	m.caffeinate = "off"
	out = m.renderHome(17)
	if !strings.Contains(out, cupOffGlyph) || strings.Contains(out, cupOnGlyph) {
		t.Error("off-state cup not the outline glyph")
	}
}

// Tapping the cup routes a caffeinate toggle through the bus (the broadcast
// is the ack); the fixture cup sits at the tray bottom, y 13..15.
func TestTrayCupTapSendsToggle(t *testing.T) {
	m := newHomeModel(320, 18)
	_ = m.renderHome(17)
	got := attachFakeBus(t, m)

	if !m.resolveTap(310, 14) {
		t.Fatal("tap on the cup not consumed")
	}
	msg := wantBusMsg(t, got)
	if msg.Type != proto.TypeCtl || msg.Cmd != "caffeinate" || msg.Arg != "toggle" {
		t.Fatalf("got %s/%s/%s, want ctl/caffeinate/toggle", msg.Type, msg.Cmd, msg.Arg)
	}
	if m.layout != "home" {
		t.Fatalf("cup tap switched layout to %q", m.layout)
	}
}

// Bus absent: the cup tap is consumed and loud, never a silent no-op.
func TestTrayCupTapBusAbsentIsLoud(t *testing.T) {
	m := newHomeModel(320, 18)
	_ = m.renderHome(17)

	if !m.resolveTap(310, 14) {
		t.Fatal("tap on the cup not consumed")
	}
	if m.lastGst != "caffeinate: bus absent" {
		t.Fatalf("lastGst = %q, want the bus-absent notice", m.lastGst)
	}
}

// An unknown toggle kind LOOKS dead -- dim glyph -- and its tap is loud,
// never a healthy-looking silent no-op (a config ahead of the binary).
func TestTrayUnknownToggleDimAndLoud(t *testing.T) {
	m := newHomeModel(320, 18)
	m.cfg.Widgets["nav-tray"].Render.Params["toggles"] = []any{
		map[string]any{"kind": "mystery"},
	}
	m.resetHits()
	rr := rect{100, 2, 12, 6}
	out := m.renderTray(m.cfg.Widgets["nav-tray"], rr)
	if !strings.Contains(out, "\x1b[90m"+cupOffGlyph) {
		t.Error("unknown-kind toggle glyph not dim-rendered")
	}
	if !m.resolveTap(101, 6) {
		t.Fatal("unknown toggle tap not consumed")
	}
	if m.lastGst != "toggle mystery: unknown kind" {
		t.Fatalf("lastGst = %q, want the unknown-kind notice", m.lastGst)
	}
}

// A TypeCaffeinate broadcast drops the composed home frame so the cup
// re-renders immediately; a same-state broadcast keeps the cache.
func TestCaffeinateBroadcastRerendersCup(t *testing.T) {
	m := newHomeModel(320, 18)
	if v := m.View(); !strings.Contains(v.Content, cupOffGlyph) {
		t.Fatal("first frame missing the outline cup")
	}
	if !m.homeCache.ok {
		t.Fatal("first frame did not prime the cache")
	}

	m.handleBusMsg(proto.Msg{Type: proto.TypeCaffeinate, Caffeinate: "on"})
	if m.homeCache.ok {
		t.Fatal("state flip kept the cached frame")
	}
	if v := m.View(); !strings.Contains(v.Content, cupOnGlyph) {
		t.Fatal("broadcast did not re-render the filled cup")
	}

	m.handleBusMsg(proto.Msg{Type: proto.TypeCaffeinate, Caffeinate: "on"})
	if !m.homeCache.ok {
		t.Fatal("same-state broadcast dropped the cache")
	}
}

// A short tray keeps the cup: toggles claim bands from the bottom, entries
// truncate, geometry stays exact.
func TestTrayShortRegionKeepsCup(t *testing.T) {
	m := newHomeModel(320, 18)
	m.resetHits()
	rr := rect{100, 2, 12, 6} // 2 bands: 1 entry + the cup
	out := m.renderTray(m.cfg.Widgets["nav-tray"], rr)

	lines := strings.Split(out, "\n")
	if len(lines) != 6 {
		t.Fatalf("tray lines = %d, want 6", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 12 {
			t.Errorf("line %d width = %d, want 12", i, w)
		}
	}
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "home") {
		t.Error("first entry missing")
	}
	if strings.Contains(plain, "hub") || strings.Contains(plain, "keyboard") {
		t.Error("truncated entries still rendered")
	}
	if !strings.Contains(out, cupOffGlyph) {
		t.Error("cup missing from a short tray")
	}

	// hits: entry band, cup band, consume area
	want := []rect{
		{100, 2, 12, 3}, // entry: home
		{100, 5, 12, 3}, // cup, flush with the region bottom
		{100, 2, 12, 6}, // tray area
	}
	if len(m.hits) != len(want) {
		t.Fatalf("hits = %d, want %d", len(m.hits), len(want))
	}
	for i, w := range want {
		if m.hits[i].area != w {
			t.Errorf("hit %d area = %+v, want %+v", i, m.hits[i].area, w)
		}
	}
}

func TestHomeTapContentRowAct(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["claude-hud"] = module.Data{Rows: []module.Row{
		{Kind: module.RowKV, Key: "sess", Value: "live", Act: []string{"open", "-b", "net.kovidgoyal.kitty"}},
	}}
	_ = m.renderHome(17)
	got := attachFakeBus(t, m)

	// claude region content starts at (21,1); row 0 carries the act
	if !m.resolveTap(25, 1) {
		t.Fatal("tap inside the claude region not consumed")
	}
	msg := wantBusMsg(t, got)
	if msg.Type != proto.TypeRowAct || msg.Widget != "claude-hud" {
		t.Fatalf("got %s/%s, want row-act/claude-hud", msg.Type, msg.Widget)
	}
}

// The composed home body is cached between frames: a backdoor data mutation
// must not show (proof the cache served), while the real TypeWidgetData path
// invalidates and re-renders. Off-screen widgets keep the cache warm.
func TestHomeBodyCachedUntilInvalidated(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["cpumem"] = module.Data{Rows: []module.Row{module.KV("cpu", "1%")}}
	if v := m.View(); !strings.Contains(v.Content, "1%") {
		t.Fatal("first frame missing widget data")
	}
	m.widgetData["cpumem"] = module.Data{Rows: []module.Row{module.KV("cpu", "99%")}}
	if v := m.View(); strings.Contains(v.Content, "99%") {
		t.Fatal("home body re-rendered without an invalidation")
	}
	raw, err := json.Marshal(module.Data{Rows: []module.Row{module.KV("cpu", "99%")}})
	if err != nil {
		t.Fatal(err)
	}
	m.handleBusMsg(proto.Msg{Type: proto.TypeWidgetData, Widget: "cpumem", Data: raw})
	if v := m.View(); !strings.Contains(v.Content, "99%") {
		t.Fatal("visible widget update did not invalidate the cache")
	}
	m.handleBusMsg(proto.Msg{Type: proto.TypeWidgetData, Widget: "not-placed", Data: raw})
	if !m.homeCache.ok {
		t.Fatal("off-screen widget update dropped the cache")
	}
}

// While a "soon" flash is live the cache is bypassed so the label can
// expire; afterwards frames cache again.
func TestHomeFlashBypassesCache(t *testing.T) {
	m := newHomeModel(320, 18)
	m.View()
	if !m.homeCache.ok {
		t.Fatal("first home frame did not prime the cache")
	}
	m.resolveTap(310, 8) // keyboard stub arms the flash
	if m.homeCache.ok {
		t.Fatal("flash write kept the cache")
	}
	m.now = time.Now()
	m.View()
	if m.homeCache.ok {
		t.Fatal("flashing frame was cached")
	}
	m.now = time.Now().Add(trayFlashFor + time.Second)
	m.View()
	if !m.homeCache.ok {
		t.Fatal("post-flash frame did not cache")
	}
	if strings.Contains(m.homeCache.body, "soon") {
		t.Fatal("flash did not clear from the body")
	}
}

// The hit table belongs to whichever layout renderer ran last: after a
// switch away from home, the home targets are gone and taps on their old
// cells fire nothing.
func TestHitTableRebuiltPerLayout(t *testing.T) {
	m := newHomeModel(320, 18)
	_ = m.renderHome(17)
	m.layout = "hub"
	m.View()

	gstBefore := m.lastGst
	m.resolveTap(310, 8) // where the home tray's keyboard stub was
	if m.layout != "hub" {
		t.Fatalf("stale home hit fired: layout = %q", m.layout)
	}
	if len(m.trayFlash) != 0 {
		t.Fatal("stale tray hit armed a flash")
	}
	if m.lastGst != gstBefore {
		t.Fatalf("stale tray hit changed lastGst: %q -> %q", gstBefore, m.lastGst)
	}
}

// A chrome widget whose module has no registered dock-side renderer must be
// loud, never a silent titled box.
func TestChromeWithoutRendererIsLoud(t *testing.T) {
	m := newHomeModel(80, 12)
	m.cfg.Widgets["mystery"] = config.Widget{ID: "mystery", Title: "mystery", Chrome: true,
		Render: config.Render{Kind: "native", Module: "unregistered"}}
	l := m.cfg.Layouts["home"]
	l.Regions = []config.Region{{Widget: "mystery", Edge: "fill"}}
	m.cfg.Layouts["home"] = l

	out := m.renderHome(11)
	if !strings.Contains(out, "no chrome renderer") {
		t.Error("unregistered chrome module rendered silently")
	}
	if !strings.Contains(out, "\x1b[33m") {
		t.Error("warn box not warn-styled")
	}
}

// Kind drives the engine: a home-KIND layout whose NAME is not the home
// layout renders full-width (no per-view affordance); the strip's
// persistent home icon is the way back (bus absent: local switch).
func TestHomeKindNonHomeNameReturnsViaStripIcon(t *testing.T) {
	m := newHomeModel(320, 18)
	m.layout = "hub"
	m.View()

	for _, h := range m.hits {
		if h.area.y < 16 && h.area.w == 3 && h.area.x == 317 {
			t.Fatalf("hub still carries an affordance column at %+v", h.area)
		}
	}
	if !m.resolveTap(1, 17) {
		t.Fatal("tap on the strip home icon not consumed")
	}
	if m.layout != "home" {
		t.Fatalf("layout = %q, want home", m.layout)
	}
}

func TestHomeTapSendsCtlWhenBusConnected(t *testing.T) {
	m := newHomeModel(320, 18)
	m.layout = "hub"
	m.View()
	got := attachFakeBus(t, m)

	// through the real gesture dispatch: the strip icon rides the hit table
	m.handleBusMsg(proto.Msg{Type: proto.TypeGesture,
		Gesture: &proto.Gesture{Kind: proto.GestureTap, Col: 1, Row: 17}})
	if m.layout != "hub" {
		t.Fatalf("bus-connected home tap switched locally to %q", m.layout)
	}
	msg := wantBusMsg(t, got)
	if msg.Type != proto.TypeCtl || msg.Cmd != "layout" || msg.Arg != "home" {
		t.Fatalf("got %s/%s/%s, want ctl/layout/home", msg.Type, msg.Cmd, msg.Arg)
	}
}

// homeTap resolves its target by layout KIND, not the literal name "home".
// A default that is not home-kind (config skew) sends homeLayout to the
// sorted-name scan for the first home-kind layout.
func TestHomeTapResolvesHomeByKind(t *testing.T) {
	m := newHomeModel(320, 18)
	delete(m.cfg.Layouts, "home")
	m.cfg.Layouts["skew"] = config.Layout{Kind: "unknown"}
	m.cfg.Layout = "skew" // default is not home-kind: sorted-name fallback
	m.layout = "skew"
	m.View()

	if !m.resolveTap(1, 17) {
		t.Fatal("tap on the strip home icon not consumed")
	}
	if m.layout != "hub" {
		t.Fatalf("layout = %q, want hub (home by kind)", m.layout)
	}
}

// The layout NAMED home outranks the config default: layout.state persists
// the runtime selection into cfg.Layout, so parking on another home-KIND
// layout (the fullscreen clod panel) across a bus restart made the config
// default THE clod layout -- a kind-of-the-default pick then resolved home
// to clod itself and the strip home icon no-opped (glass-reported).
func TestHomeTapEscapesPersistedHomeKindDefault(t *testing.T) {
	m := newHomeModel(320, 18)
	// "claude": home-kind, single fill, mirroring edge.cue; persisted as
	// the config default and currently active
	m.cfg.Layouts["claude"] = config.Layout{Kind: "home", Regions: []config.Region{
		{Widget: "cpumem", Edge: "fill"},
	}}
	m.cfg.Layout = "claude"
	m.layout = "claude"
	m.View()

	if !m.resolveTap(1, 17) {
		t.Fatal("tap on the strip home icon not consumed")
	}
	if m.layout != "home" {
		t.Fatalf("layout = %q, want home (the NAMED home layout)", m.layout)
	}

	// and the alphabetical trap: "claude" sorts before "home", so the
	// sorted-name fallback must not win while a named home exists
	m.cfg.Layout = "skew" // not a layout: kind lookup misses
	m.layout = "claude"
	m.resetLayout()
	m.View()
	if !m.resolveTap(1, 17) {
		t.Fatal("tap on the strip home icon not consumed (skew default)")
	}
	if m.layout != "home" {
		t.Fatalf("layout = %q, want home over the alphabetically-first claude", m.layout)
	}
}

func TestBrailleRuneLevels(t *testing.T) {
	cases := []struct {
		v    float64
		want rune
	}{
		{0, rune(0x28c0)},   // floor dots only
		{0.3, rune(0x28e4)}, // + level 2
		{0.6, rune(0x28f6)}, // + level 3
		{1.0, rune(0x28ff)}, // all dots
		{-1, rune(0x28c0)},  // clamps low
		{2.5, rune(0x28ff)}, // clamps high
	}
	for _, c := range cases {
		if got := brailleRune(c.v); got != c.want {
			t.Errorf("brailleRune(%v) = %U, want %U", c.v, got, c.want)
		}
	}
}

func TestSparkHeatBucketsAndWidth(t *testing.T) {
	s := spark([]float64{0.1, 0.7, 0.95}, 10, heatStyles)
	if w := lipgloss.Width(s); w != 10 {
		t.Errorf("spark width = %d, want 10 (padded)", w)
	}
	for _, sgr := range []string{"\x1b[32m", "\x1b[33m", "\x1b[31m"} {
		if !strings.Contains(s, sgr) {
			t.Errorf("spark missing heat SGR %q", sgr)
		}
	}
	// newest samples win when history exceeds the width
	s = spark([]float64{0.1, 0.1, 0.95}, 1, heatStyles)
	if strings.Contains(s, "\x1b[32m") {
		t.Error("cropped spark kept the oldest sample")
	}
	if !strings.Contains(s, "\x1b[31m") {
		t.Error("cropped spark lost the newest sample")
	}
}

func TestChromeRowsMinHeightAndActs(t *testing.T) {
	act := []string{"do", "it"}
	lines, acts := renderChromeRows(module.Data{Rows: []module.Row{
		{Kind: module.RowText, Text: "tall", MinHeight: 3, Act: act},
		{Kind: module.RowText, Text: "after"},
	}}, 40, 10, chromeRowStyles)
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (3 tall + 1 after)", len(lines))
	}
	for i := range 3 {
		if len(acts[i]) != 2 {
			t.Errorf("line %d lost the row act", i)
		}
	}
	if acts[3] != nil {
		t.Error("act bled past the MinHeight row")
	}
}

// One spans row is one line of independently styled runs; the title span
// tracks the row's base tone (bold), so live and stale lines stylize their
// name without extra vocabulary.
func TestChromeRowsSpansLine(t *testing.T) {
	r := module.SpansRow(
		module.Span{Text: "name", Style: module.StyleTitle},
		module.Span{Text: " 12:34", Style: module.StyleAccent},
		module.Span{Text: " \uf013 2", Style: module.StyleHighlight},
		module.Span{Text: " > fix", Style: module.StyleDim},
	)
	r.Style = module.StyleAccent
	r.Act = []string{"open"}
	lines, acts := renderChromeRows(module.Data{Rows: []module.Row{r}}, 60, 5, chromeRowStyles)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	for _, want := range []string{
		chromeAccent.Bold(true).Render("name"),
		chromeAccent.Render(" 12:34"),
		chromeHighlight.Render(" \uf013 2"),
		chromeDim.Render(" > fix"),
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("spans line %q missing styled run %q", lines[0], want)
		}
	}
	if len(acts[0]) != 1 {
		t.Error("spans row lost its act")
	}

	stale := module.SpansRow(module.Span{Text: "name", Style: module.StyleTitle})
	stale.Style = module.StyleDim
	lines, _ = renderChromeRows(module.Data{Rows: []module.Row{stale}}, 60, 5, chromeRowStyles)
	if !strings.Contains(lines[0], chromeDim.Bold(true).Render("name")) {
		t.Errorf("stale spans line %q, want dim-bold title span", lines[0])
	}
}

// A spans line wider than the region truncates inside the renderer; the
// panel geometry never stretches.
func TestChromeRowsSpansTruncated(t *testing.T) {
	r := module.SpansRow(
		module.Span{Text: strings.Repeat("a", 30), Style: module.StyleTitle},
		module.Span{Text: strings.Repeat("b", 30), Style: module.StyleDim},
	)
	lines, _ := renderChromeRows(module.Data{Rows: []module.Row{r}}, 20, 5, chromeRowStyles)
	if w := lipgloss.Width(lines[0]); w > 20 {
		t.Errorf("spans line width = %d, want <= 20", w)
	}
	if !strings.Contains(lines[0], "a") || strings.Contains(lines[0], "b") {
		t.Errorf("spans line %q, want the leading run kept and the tail cut", lines[0])
	}
}

func TestChromeRowsSeriesLine(t *testing.T) {
	lines, _ := renderChromeRows(module.Data{Rows: []module.Row{
		module.Series("cpu", []float64{0.2, 0.9}, "38% of 12"),
	}}, 60, 5, chromeRowStyles)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "38% of 12") {
		t.Error("series line lost its value")
	}
	if !strings.ContainsRune(lines[0], brailleRune(0.9)) {
		t.Error("series line lost its sparkline")
	}
}

func TestChromeRowsResourceLine(t *testing.T) {
	lines, _ := renderChromeRows(module.Data{Rows: []module.Row{
		module.Resource("cpu", 0.38, []float64{0.2, 0.9}, "38% of 12"),
	}}, 60, 5, chromeRowStyles)
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	if !strings.Contains(lines[0], "38% of 12") {
		t.Error("resource line lost its value")
	}
	if !strings.ContainsRune(lines[0], brailleRune(0.9)) {
		t.Error("resource line lost its sparkline")
	}
	// gauge fill + track are ANSI-16 backgrounds (style deferral)
	for _, sgr := range []string{"\x1b[42m", "\x1b[100m"} {
		if !strings.Contains(lines[0], sgr) {
			t.Errorf("resource line missing gauge SGR %q", sgr)
		}
	}
}

// A 2-col titled region cannot hold a border pair plus content: it must
// degrade to a blank block of the exact requested geometry, never panic.
func TestRenderTitledBoxWidth2Degrades(t *testing.T) {
	out := renderTitledBox("t", nil, 2, 5)
	lines := strings.Split(out, "\n")
	if len(lines) != 5 {
		t.Fatalf("lines = %d, want 5", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 2 {
			t.Errorf("line %d width = %d, want 2", i, w)
		}
	}
}

// A data-carrying widget squeezed into a 1-row region (a big top peel on a
// short dock) must degrade with exact geometry, never a negative row-budget
// slice.
func TestHomeOneRowRegionWithDataDegrades(t *testing.T) {
	m := newHomeModel(80, 14)
	m.widgetData["cpumem"] = module.Data{Rows: []module.Row{
		module.Resource("cpu", 0.38, []float64{0.1, 0.7, 0.9}, "38% of 12"),
	}}
	// interior is 11 rows; claude's 10-row peel leaves the fills 1 row tall
	out := m.renderHome(13)
	lines := strings.Split(out, "\n")
	if len(lines) != 13 {
		t.Fatalf("home body lines = %d, want 13", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("line %d width = %d, want 80", i, w)
		}
	}
}

// size: 2 passes config vetting (size >= 1), so the whole home render must
// survive it with exact line geometry.
func TestHomeSize2RegionDegrades(t *testing.T) {
	m := newHomeModel(60, 12)
	l := m.cfg.Layouts["home"]
	l.Regions = []config.Region{
		{Widget: "claude-hud", Edge: "left", Size: 2},
		{Widget: "cpumem", Edge: "fill"},
	}
	m.cfg.Layouts["home"] = l
	out := m.renderHome(11)
	lines := strings.Split(out, "\n")
	if len(lines) != 11 {
		t.Fatalf("home body lines = %d, want 11", len(lines))
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != 60 {
			t.Errorf("line %d width = %d, want 60", i, w)
		}
	}
}

func TestFitCellNeverExceedsWidth(t *testing.T) {
	samples := []string{
		"plain",
		"",
		"21" + string(rune(0x00b0)) + "C",
		string(rune(0x2190)) + " back",
		string(rune(0x65e5)) + string(rune(0x672c)) + string(rune(0x8a9e)),
		chromeAccent.Render("styled " + string(rune(0x25cf))),
	}
	for _, s := range samples {
		for w := 0; w <= 8; w++ {
			if got := fitCell(s, w); lipgloss.Width(got) > w {
				t.Errorf("fitCell(%q, %d) width = %d", s, w, lipgloss.Width(got))
			}
		}
	}
}

// Ambiguous-width (degree, arrow) and wide (CJK) glyphs in config strings
// must never break chrome geometry: no panic, every line exactly w cells.
func TestChromeLinesSurviveWideGlyphs(t *testing.T) {
	labels := []string{
		"21" + string(rune(0x00b0)) + "C",                                  // degree
		string(rune(0x2190)) + " back",                                     // leftwards arrow
		string(rune(0x65e5)) + string(rune(0x672c)) + string(rune(0x8a9e)), // CJK
		"caf" + string(rune(0x00e9)) + " au lait",
	}
	for _, s := range labels {
		for w := 3; w <= 8; w++ {
			check := func(kind string, lines []string) {
				t.Helper()
				for i, ln := range lines {
					if lw := lipgloss.Width(ln); lw != w {
						t.Errorf("%s(%q, w=%d) line %d width = %d", kind, s, w, i, lw)
					}
				}
			}
			check("railTile", railTile(s, w, chromeFG))
			check("trayButton", trayButton(s, false, false, w))
			check("renderTitledBox", strings.Split(renderTitledBox(s, nil, w, 4), "\n"))
		}
	}
}

// resourceSegs locates the sparkline and value columns in a rendered
// resource line; the gauge extent is implied (label+gauge end where the
// braille starts).
func resourceSegs(t *testing.T, line, value string) (sparkAt, valueAt int) {
	t.Helper()
	plain := ansi.Strip(line)
	sparkAt = -1
	for i, r := range []rune(plain) {
		if r >= 0x2800 && r <= 0x28ff {
			sparkAt = i
			break
		}
	}
	if sparkAt < 0 {
		t.Fatalf("no sparkline in %q", plain)
	}
	b := strings.Index(plain, value)
	if b < 0 {
		t.Fatalf("no value %q in %q", value, plain)
	}
	return sparkAt, utf8.RuneCountInString(plain[:b])
}

// cpu and a disk volume are one family of resource clusters: at the same
// region width every segment (label+gauge, spark, value) starts at the same
// column regardless of which resource it is.
func TestResourceRowsStructurallyIdentical(t *testing.T) {
	const cols = 80
	cpuVal, volVal := "38% of 12", "3.0G/4.0G free 512M"
	cpu := resourceLine(module.Resource("cpu", 0.38, []float64{0.2, 0.7, 0.95}, cpuVal), cols, chromeRowStyles)
	vol := resourceLine(module.Resource("/", 0.75, []float64{0.5, 0.75, 0.9}, volVal), cols, chromeRowStyles)

	cpuSpark, cpuValue := resourceSegs(t, cpu, cpuVal)
	volSpark, volValue := resourceSegs(t, vol, volVal)
	if cpuSpark != volSpark {
		t.Errorf("spark column: cpu %d, disk %d -- label+gauge widths diverge", cpuSpark, volSpark)
	}
	if cpuValue != volValue {
		t.Errorf("value column: cpu %d, disk %d -- spark widths diverge", cpuValue, volValue)
	}
	// segment order: label+gauge, then spark, then value
	if cpuSpark <= 8 || cpuValue <= cpuSpark {
		t.Errorf("segment order broken: spark %d value %d", cpuSpark, cpuValue)
	}
}

// attentionData is a claude-hud view model carrying the attention bit.
func attentionData() module.Data {
	return module.Data{Title: "claude 1/1", Attention: true,
		Rows: []module.Row{{Kind: module.RowText, Text: "needs input"}}}
}

// The attention border marches: one tick apart the border cells carry
// shifted styles (the palette ramp path), plain content and geometry
// identical.
func TestAttentionBorderShiftsBetweenTicks(t *testing.T) {
	m := newHomeModel(320, 18)
	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day", Palette: busPalette()})
	m.widgetData["claude-hud"] = attentionData()
	a := m.renderHome(17)
	m.now = m.now.Add(time.Second)
	b := m.renderHome(17)
	if a == b {
		t.Fatal("attention border did not shift between two ticks")
	}
	if ansi.Strip(a) != ansi.Strip(b) {
		t.Fatal("tick shifted plain content, not just border styles")
	}
	for i, l := range strings.Split(b, "\n") {
		if w := lipgloss.Width(l); w != 320 {
			t.Errorf("line %d width = %d, want 320", i, w)
		}
	}
}

// A palette-less dock alternates the indexed warn/dim tones by
// (perimeterPos + tick) % 2: the box corner flips tone each tick.
func TestAttentionBorderIndexedAlternation(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["claude-hud"] = attentionData()
	warnCorner := chromeWarn.Render(lipgloss.NormalBorder().TopLeft)
	dimCorner := chromeDim.Render(lipgloss.NormalBorder().TopLeft)

	m.now = time.Unix(1000, 0) // even phase: perimeter position 0 is warn
	a := m.renderHome(17)
	if !strings.Contains(a, warnCorner) {
		t.Fatal("even phase: attention corner not warn-toned")
	}
	m.now = time.Unix(1001, 0)
	b := m.renderHome(17)
	if !strings.Contains(b, dimCorner) {
		t.Fatal("odd phase: attention corner not dim-toned")
	}
	if strings.Contains(b, warnCorner) {
		t.Fatal("odd phase: warn corner did not flip")
	}
}

// Attention=false renders byte-identical to renderTitledBox's output --
// the animated variant engages only on the attention bit.
func TestAttentionOffByteIdenticalToTitledBox(t *testing.T) {
	m := newHomeModel(320, 18)
	d := module.Data{Title: "claude 0/1", Rows: []module.Row{{Kind: module.RowText, Text: "calm"}}}
	m.widgetData["claude-hud"] = d
	rr := rect{21, 1, 286, 10}
	m.resetHits()
	got := m.renderHomeWidget(m.cfg.Widgets["claude-hud"], rr)
	lines, _ := renderChromeRows(d, rr.w-2, rr.h-2, m.rowStyles())
	if got != renderTitledBox("claude 0/1", lines, rr.w, rr.h) {
		t.Fatal("attention-less region diverged from renderTitledBox")
	}
}

// The border ramp leads with a pure-warn plateau: a solid head on the
// crawl, then the fade to dim.
func TestAttentionRampPlateau(t *testing.T) {
	m := newHomeModel(320, 18)
	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day", Palette: busPalette()})
	ramp := m.attentionRamp()
	if len(ramp) != attentionRampLen {
		t.Fatalf("ramp len = %d, want %d", len(ramp), attentionRampLen)
	}
	for i := 1; i < attentionPlateau; i++ {
		if ramp[i] != ramp[0] {
			t.Errorf("ramp[%d] = %v, want the plateau tone %v", i, ramp[i], ramp[0])
		}
	}
	// the plateau is EXACTLY attentionPlateau cells: Blend1D re-emits the
	// warn endpoint, which must not stretch it
	if ramp[attentionPlateau] == ramp[0] {
		t.Error("ramp did not fade immediately after the plateau")
	}
	if ramp[attentionRampLen-1] == ramp[0] {
		t.Error("ramp tail did not fade off the plateau")
	}
}

// An attention row renders over a STEADY mid-blend background wash:
// truecolor bg SGRs across the FULL region width, plain text and act
// intact, frames identical between renders (no animation -- text must stay
// readable). Without a palette the row renders plain (the border still
// calls out the widget).
func TestAttentionRowWash(t *testing.T) {
	r := module.SpansRow(
		module.Span{Text: "name", Style: module.StyleTitle},
		module.Span{Text: " needs input", Style: module.StyleWarn},
	)
	r.Attention = true
	r.Act = []string{"open"}
	ss := newRowStyles(busPalette())
	if ss.attnBG == nil {
		t.Fatal("attnBG not derived from a full palette")
	}
	lines, acts := renderChromeRows(module.Data{Rows: []module.Row{r}}, 60, 5, ss)
	if len(lines) != 1 || len(acts[0]) != 1 {
		t.Fatalf("attention row lines/acts = %d/%v", len(lines), acts)
	}
	if !strings.Contains(lines[0], "48;2;") {
		t.Error("attention row carries no truecolor background")
	}
	if w := lipgloss.Width(lines[0]); w != 60 {
		t.Errorf("attention row width = %d, want the full 60 (the whole row washes)", w)
	}
	if plain := ansi.Strip(lines[0]); !strings.Contains(plain, "name") || !strings.Contains(plain, "needs input") {
		t.Errorf("attention row plain text = %q", plain)
	}

	// steady, not animated: two renders are byte-identical
	lines2, _ := renderChromeRows(module.Data{Rows: []module.Row{r}}, 60, 5, ss)
	if lines2[0] != lines[0] {
		t.Error("attention wash changed between renders; it must be steady")
	}

	// no palette: plain render, byte-identical to the attention-less row
	calm := r
	calm.Attention = false
	withAttn, _ := renderChromeRows(module.Data{Rows: []module.Row{r}}, 60, 5, chromeRowStyles)
	without, _ := renderChromeRows(module.Data{Rows: []module.Row{calm}}, 60, 5, chromeRowStyles)
	if withAttn[0] != without[0] {
		t.Error("palette-less attention row diverged from the plain render")
	}

	// wide runes straddling the right edge still yield an exact-width row
	// (CJK prompts ride session lines)
	wide := module.SpansRow(module.Span{Text: strings.Repeat("\u4f1a", 8), Style: module.StyleDim})
	wide.Attention = true
	for _, cols := range []int{10, 11, 12} {
		lines, _ := renderChromeRows(module.Data{Rows: []module.Row{wide}}, cols, 5, ss)
		if w := lipgloss.Width(lines[0]); w != cols {
			t.Errorf("wide-rune attention row width = %d, want %d", w, cols)
		}
	}

	// combining marks survive (the wash path shares the plain path's
	// fitCell semantics)
	marked := module.SpansRow(module.Span{Text: "cafe\u0301", Style: module.StyleDim})
	marked.Attention = true
	lines, _ = renderChromeRows(module.Data{Rows: []module.Row{marked}}, 20, 5, ss)
	if plain := ansi.Strip(lines[0]); !strings.Contains(plain, "cafe\u0301") {
		t.Errorf("attention row dropped the combining mark: %q", plain)
	}
	if w := lipgloss.Width(lines[0]); w != 20 {
		t.Errorf("marked attention row width = %d, want 20", w)
	}
}

// The home cache is bypassed only while a widget PLACED BY THE ACTIVE
// LAYOUT carries the attention bit (the trayFlash precedent): live
// attention keeps the frame clock-driven, clearing it caches again, and
// off-layout attention never bypasses.
func TestAttentionBypassesHomeCacheOnlyWhileLive(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["claude-hud"] = attentionData()
	m.View()
	if m.homeCache.ok {
		t.Fatal("attention frame was cached")
	}
	d := m.widgetData["claude-hud"]
	d.Attention = false
	m.widgetData["claude-hud"] = d
	m.View()
	if !m.homeCache.ok {
		t.Fatal("attention-less frame did not cache")
	}
	m.widgetData["off-screen"] = module.Data{Attention: true}
	m.View()
	if !m.homeCache.ok {
		t.Fatal("off-layout attention bypassed the cache")
	}
}

// A tapped rail tile flashes for tapFlashFor (indexed bright fallback
// without a palette) and reverts to its identity hue on expiry; the tap
// still fires the row act path.
func TestRailTapFlashAndRevert(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["dock-rail"] = railData()
	m.View()
	if !m.resolveTap(2, 2) {
		t.Fatal("tap on the Safari tile not consumed")
	}
	if m.homeCache.ok {
		t.Fatal("tap flash kept the composed frame")
	}
	flashed := m.tapStyle(lipgloss.NewStyle().Foreground(identityHue("safari"))).Render("safari")
	base := lipgloss.NewStyle().Foreground(identityHue("safari")).Render("safari")

	m.now = time.Now()
	if v := m.View(); !strings.Contains(v.Content, flashed) {
		t.Error("tapped rail tile not flash-styled")
	}
	m.now = time.Now().Add(tapFlashFor + time.Second)
	v := m.View()
	if strings.Contains(v.Content, flashed) {
		t.Error("rail flash did not revert after expiry")
	}
	if !strings.Contains(v.Content, base) {
		t.Error("rail tile lost its identity hue after the flash")
	}
	if !m.homeCache.ok {
		t.Fatal("post-flash frame did not cache")
	}
}

// Rail flash keys are the tile's rail index, not the display name: two
// tiles sharing a name must not flash together.
func TestRailFlashKeysOnRowIdentity(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["dock-rail"] = module.Data{Title: "dock", Rows: []module.Row{
		{Kind: module.RowText, Text: "Safari", Act: []string{"open", "-a", "Safari"}},
		{Kind: module.RowText, Text: "Safari", Act: []string{"open", "-b", "com.apple.Safari"}},
	}}
	m.View()
	if !m.resolveTap(2, 2) {
		t.Fatal("tap on the first Safari tile not consumed")
	}
	if _, ok := m.trayFlash["rail:0"]; !ok {
		t.Fatalf("flash keys = %v, want rail:0", m.trayFlash)
	}
	if _, ok := m.trayFlash["rail:1"]; ok {
		t.Fatal("second same-named tile flashed too")
	}
}
