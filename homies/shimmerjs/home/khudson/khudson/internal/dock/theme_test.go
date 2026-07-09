package dock

import (
	"maps"
	"strings"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// busPalette is a full get-colors-shaped broadcast fixture (everforest
// values, as the live HUD kitty reports them).
func busPalette() map[string]string {
	return map[string]string{
		"foreground": "#d3c6aa", "background": "#232a2e",
		"color1": "#e67e80", "color2": "#a7c080",
		"color3": "#dbbc7f", "color6": "#83c092",
	}
}

// TestThemeBroadcastStoresPalette: a TypeTheme broadcast stores the theme
// name and the bus-fetched palette; a palette-less rebroadcast (bus hasn't
// fetched yet) keeps the last known palette instead of clearing it.
func TestThemeBroadcastStoresPalette(t *testing.T) {
	m := newHomeModel(320, 18)
	pal := map[string]string{"background": "#000000", "foreground": "#e6dcbe"}

	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "night", Palette: pal})
	if m.theme != "night" {
		t.Fatalf("theme = %q, want night", m.theme)
	}
	if !maps.Equal(m.palette, pal) {
		t.Fatalf("palette = %v, want %v", m.palette, pal)
	}

	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day"})
	if m.theme != "day" {
		t.Fatalf("theme = %q, want day", m.theme)
	}
	if !maps.Equal(m.palette, palette(pal)) {
		t.Fatalf("palette-less broadcast cleared the stored palette: %v", m.palette)
	}
}

// TestThemeBroadcastDerivesAccents: after a palette broadcast the heat ramp
// is the palette's own truecolor tones, the gauge fill/track and the
// accent/highlight emphasis tones are palette-derived, and the base text
// chrome stays indexed-ANSI (the kitty theme still owns it).
func TestThemeBroadcastDerivesAccents(t *testing.T) {
	m := newHomeModel(320, 18)
	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day", Palette: busPalette()})
	ss := m.rowStyles()

	heatWant := []string{"38;2;167;192;128", "38;2;219;188;127", "38;2;230;126;128"}
	for i, want := range heatWant {
		if got := ss.heat[i].Render("x"); !strings.Contains(got, want) {
			t.Errorf("heat[%d] = %q, want palette truecolor %q", i, got, want)
		}
	}

	// half-full gauge: gradient fill cells + derived track, indexed SGRs gone
	gauge := gaugeBar(0.5, 8, ss)
	if n := strings.Count(gauge, "48;2;"); n < 3 {
		t.Errorf("gauge = %q, want per-cell gradient + derived track (>= 3 truecolor bgs), got %d", gauge, n)
	}
	for _, sgr := range []string{"\x1b[42m", "\x1b[100m"} {
		if strings.Contains(gauge, sgr) {
			t.Errorf("gauge kept indexed SGR %q with a palette present", sgr)
		}
	}

	// emphasis tones are blends, not raw indexed hues
	if got := ss.accent.Render("x"); !strings.Contains(got, "38;2;") {
		t.Errorf("accent = %q, want a palette-derived truecolor blend", got)
	}
	if got := ss.highlight.Render("x"); !strings.Contains(got, "38;2;") {
		t.Errorf("highlight = %q, want a palette-derived truecolor blend", got)
	}
	if got := m.sty.brand.Render("k"); !strings.Contains(got, "38;2;") {
		t.Errorf("brand = %q, want a palette-derived truecolor blend", got)
	}

	// base text chrome stays indexed
	if got := ss.dim.Render("x"); !strings.Contains(got, "\x1b[90m") {
		t.Errorf("dim = %q, want indexed bright-black", got)
	}
	if got := ss.warn.Render("x"); !strings.Contains(got, "\x1b[33m") {
		t.Errorf("warn = %q, want indexed yellow", got)
	}
}

// Before any broadcast every style is indexed-ANSI -- the first frame must
// render correctly under the kitty theme, never hardcoded truecolor.
func TestPreBroadcastStylesAreIndexed(t *testing.T) {
	m := newHomeModel(320, 18)
	ss := m.rowStyles()
	gauge := gaugeBar(0.5, 8, ss)
	for _, sgr := range []string{"\x1b[42m", "\x1b[100m"} {
		if !strings.Contains(gauge, sgr) {
			t.Errorf("pre-broadcast gauge = %q, missing indexed SGR %q", gauge, sgr)
		}
	}
	if strings.Contains(gauge, "48;2;") {
		t.Errorf("pre-broadcast gauge = %q carries truecolor", gauge)
	}
	if got := m.sty.brand.Render("k"); strings.Contains(got, "38;2;") {
		t.Errorf("pre-broadcast brand = %q carries truecolor", got)
	}
	s := spark([]float64{0.1, 0.7, 0.95}, 3, ss.heat)
	for _, sgr := range []string{"\x1b[32m", "\x1b[33m", "\x1b[31m"} {
		if !strings.Contains(s, sgr) {
			t.Errorf("pre-broadcast spark = %q missing indexed heat %q", s, sgr)
		}
	}
}

// A partial palette falls back per key: derived tones only where the inputs
// exist, indexed defaults everywhere else, and no gradient without the whole
// cool -> hot ramp.
func TestPartialPaletteFallsBackPerKey(t *testing.T) {
	m := newHomeModel(320, 18)
	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day",
		Palette: map[string]string{"color2": "#a7c080"}})
	ss := m.rowStyles()

	if got := ss.heat[0].Render("x"); !strings.Contains(got, "38;2;167;192;128") {
		t.Errorf("heat[0] = %q, want derived color2", got)
	}
	if got := ss.heat[1].Render("x"); !strings.Contains(got, "\x1b[33m") {
		t.Errorf("heat[1] = %q, want indexed yellow fallback", got)
	}
	if got := ss.heat[2].Render("x"); !strings.Contains(got, "\x1b[31m") {
		t.Errorf("heat[2] = %q, want indexed red fallback", got)
	}
	// flat derived fill (one truecolor run), indexed track fallback
	gauge := gaugeBar(1, 4, ss)
	if n := strings.Count(gauge, "48;2;"); n != 1 {
		t.Errorf("gauge = %q, want one flat derived fill run, got %d", gauge, n)
	}
	if got := gaugeBar(0, 4, ss); !strings.Contains(got, "\x1b[100m") {
		t.Errorf("track = %q, want indexed fallback (fg/bg missing)", got)
	}
	// emphasis blends need the foreground: indexed fallbacks here
	if got := ss.highlight.Render("x"); strings.Contains(got, "38;2;") {
		t.Errorf("highlight = %q, want indexed fallback without a foreground", got)
	}
}

// A theme broadcast invalidates the composed home frame so the derived
// styles land immediately, and the re-render carries them.
func TestThemeBroadcastInvalidatesHomeCache(t *testing.T) {
	m := newHomeModel(320, 18)
	m.widgetData["cpumem"] = module.Data{Rows: []module.Row{
		module.Resource("cpu", 0.38, []float64{0.1, 0.7}, "38% of 12"),
	}}
	if v := m.View(); !strings.Contains(v.Content, "\x1b[42m") {
		t.Fatal("pre-broadcast frame missing the indexed gauge")
	}
	if !m.homeCache.ok {
		t.Fatal("first frame did not prime the cache")
	}
	m.handleBusMsg(proto.Msg{Type: proto.TypeTheme, Theme: "day", Palette: busPalette()})
	if m.homeCache.ok {
		t.Fatal("theme broadcast kept the stale frame")
	}
	if v := m.View(); !strings.Contains(v.Content, "48;2;") {
		t.Fatal("post-broadcast frame missing the derived gauge")
	}
}
