package dock

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	reno "github.com/shimmerjs/khudson/khudson/internal/module/reno"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// renoModel is a home model whose interior is one reno presenter region.
func renoModel(w, h int) *model {
	m := newHomeModel(w, h)
	m.cfg.Widgets["reno-presenter"] = config.Widget{ID: "reno-presenter", Title: "reno", Chrome: true,
		Render: config.Render{Kind: "native", Module: "reno",
			Params: map[string]any{"view": "presenter"}}}
	l := m.cfg.Layouts["home"]
	l.Regions = []config.Region{{Widget: "reno-presenter", Edge: "fill"}}
	m.cfg.Layouts["home"] = l
	return m
}

// renoLiveData is a mid-show presenter payload in the module's row
// vocabulary (internal/module/reno view conventions).
func renoLiveData() module.Data {
	return module.Data{Title: "reno: tour", Rows: []module.Row{
		{Kind: module.RowKV, Key: reno.KeyChipStep, Value: "2/3 code", Style: module.StyleAccent,
			Menu: []module.Act{
				{Label: "1 intro", Argv: []string{"reno:goto", "intro"}, Section: "steps"},
				{Label: "2 code", Argv: []string{"reno:goto", "code"}, Section: "steps"},
				{Label: "stop demo", Argv: []string{"reno:stop"}, Destructive: true, Section: "run"},
			}},
		{Kind: module.RowKV, Key: reno.KeyChipElapsed, Value: "12:34/15:00", PctHeat: 0.83},
		{Kind: module.RowKV, Key: reno.KeyChipInStep, Value: "0:12 over", Style: module.StyleWarn, PctHeat: 1},
		{Kind: module.RowText, Key: reno.KeyNote, Text: "walk the diff bottom-up and land the punchline before switching away from the editor window."},
		{Kind: module.RowText, Key: reno.KeyNote, Text: ""},
		{Kind: module.RowText, Key: reno.KeyNote, Text: "second paragraph."},
		{Kind: module.RowKV, Key: reno.KeyNext, Value: "slides", Style: module.StyleDim},
		{Kind: module.RowText, Key: reno.KeyActNext, Text: "next", Act: []string{"reno:next"}},
		{Kind: module.RowText, Key: reno.KeyActPrev, Text: "prev", Act: []string{"reno:prev"}},
	}}
}

func TestRenoPresenterGeometryAndHits(t *testing.T) {
	m := renoModel(120, 24)
	m.widgetData["reno-presenter"] = renoLiveData()

	v := m.View()
	lines := strings.Split(v.Content, "\n")
	for i, l := range lines {
		if lipgloss.Width(l) != 120 {
			t.Fatalf("line %d width = %d, want 120", i, lipgloss.Width(l))
		}
	}
	body := ansi.Strip(v.Content)
	for _, want := range []string{"2/3 code", "12:34/15:00", "0:12 over", "next: slides", "walk the diff", "second paragraph."} {
		if !strings.Contains(body, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// notes wrap to the middle column, never overflowing into the bands
	noteW := 120 - renoPrevBandW - renoNextBandW - 2
	for l := range strings.SplitSeq(body, "\n") {
		if strings.Contains(l, "punchline") && lipgloss.Width(strings.TrimRight(l, " ")) > 120 {
			t.Errorf("note line overflows: %q", l)
		}
	}
	if len(renoWrap([]string{strings.Repeat("word ", 40)}, noteW)) < 2 {
		t.Error("long note must wrap into multiple lines")
	}

	// the fat bands send the published acts to the bus
	msgs := busSink(t, m)
	m.resolveTap(2, 10) // prev band
	m.resolveTap(119-renoNextBandW+2, 10)
	first, second := <-msgs, <-msgs
	if first.Type != proto.TypeRowAct || first.Argv[0] != "reno:prev" || first.Widget != "reno-presenter" {
		t.Fatalf("prev tap sent %+v", first)
	}
	if second.Argv[0] != "reno:next" {
		t.Fatalf("next tap sent %+v", second)
	}

	// long-press opens the module-windowed goto menu
	m.resolveLongPress(60, 12)
	if m.overlay == nil {
		t.Fatal("long-press must open the goto overlay")
	}
	found := false
	for _, it := range m.overlay.items {
		if strings.Contains(it.label, "stop demo") {
			found = true
		}
	}
	if !found {
		t.Errorf("goto overlay missing the stop item")
	}
}

func TestRenoFlatViews(t *testing.T) {
	m := renoModel(120, 24)
	// off-show payload: no act rows -> flat keyed-row rendering
	m.widgetData["reno-presenter"] = module.Data{Title: "reno", Rows: []module.Row{
		{Kind: module.RowText, Text: "no script loaded", Style: module.StyleDim},
		{Kind: module.RowText, Text: "khudson reno load <script.txtar>", Style: module.StyleDim},
	}}
	body := ansi.Strip(m.View().Content)
	if !strings.Contains(body, "no script loaded") {
		t.Errorf("flat view missing empty-state row:\n%s", body)
	}

	// recorder param routes to the flat view too
	m.cfg.Widgets["reno-presenter"] = config.Widget{ID: "reno-presenter", Title: "reno", Chrome: true,
		Render: config.Render{Kind: "native", Module: "reno",
			Params: map[string]any{"view": "recorder"}}}
	m.widgetData["reno-presenter"] = module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowKV, Key: "run", Value: "r-123"},
		{Kind: module.RowText, Key: "act:record", Text: "record", Act: []string{"reno:record"}},
	}}
	m.invalidateHome("reno-presenter")
	body = ansi.Strip(m.View().Content)
	if !strings.Contains(body, "record") || !strings.Contains(body, "r-123") {
		t.Errorf("recorder view missing rows:\n%s", body)
	}
}

// A strip entry with alt is active on both sides of its pair and its tap
// flips to the other side; plain entries keep exact-match activation.
func TestStripEntryAltFlips(t *testing.T) {
	m := stripModel()
	m.cfg.Layouts["reno-presenter"] = config.Layout{Kind: "home", Regions: []config.Region{
		{Widget: "cpumem", Edge: "fill"}}}
	m.cfg.Layouts["reno-recorder"] = config.Layout{Kind: "home", Regions: []config.Region{
		{Widget: "cpumem", Edge: "fill"}}}
	m.cfg.Strip.Entries = append(m.cfg.Strip.Entries,
		config.StripEntry{Label: "reno", Target: "reno-presenter", Alt: "reno-recorder"})

	m.layout = "reno-presenter"
	strip := stripLines(t, m)
	if !strings.Contains(ansi.Strip(strip), "reno") {
		t.Fatalf("reno tab missing from strip: %q", strip)
	}
	// find the reno tab hit and fire it: presenter -> recorder
	fireTab := func() {
		t.Helper()
		for _, h := range m.hits {
			if h.pressKey == "tab:reno" {
				h.do(h.area.x, h.area.y)
				return
			}
		}
		t.Fatal("no reno tab hit registered")
	}
	fireTab()
	if m.layout != "reno-recorder" {
		t.Fatalf("tap on active tab flipped to %q, want reno-recorder", m.layout)
	}
	m.View() // rebuild hits under the new layout
	fireTab()
	if m.layout != "reno-presenter" {
		t.Fatalf("second tap flipped to %q, want reno-presenter", m.layout)
	}

	// from an unrelated layout the tab navigates to target, not alt
	m.layout = "home"
	m.View()
	fireTab()
	if m.layout != "reno-presenter" {
		t.Fatalf("inactive tab tap landed on %q, want target", m.layout)
	}
}
