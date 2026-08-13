package dock

import (
	"strconv"
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

	// the recorder param routes to the control pad: readout rows land in
	// the header, pad rows become buttons
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowKV, Key: "run", Value: "r-123"},
		{Kind: module.RowPad, Key: reno.KeyActRecord, Text: "REC", Value: "ready",
			Act: []string{"reno:record"}},
	}}
	m.invalidateHome("reno-presenter")
	body = ansi.Strip(m.View().Content)
	if !strings.Contains(body, "REC") || !strings.Contains(body, "r-123") {
		t.Errorf("recorder view missing rows:\n%s", body)
	}
}

func recorderWidget(id string) config.Widget {
	return config.Widget{ID: id, Title: "reno rec", Chrome: true,
		Render: config.Render{Kind: "native", Module: "reno",
			Params: map[string]any{"view": "recorder"}}}
}

// padRows is a recorder payload: a readout header plus n buttons split
// across two labeled sections.
func padRows(n int) module.Data {
	d := module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowSpans, Key: reno.KeyHead, Spans: []module.Span{{Text: "run r-123"}}},
	}}
	for i := range n {
		section := "transport"
		if i >= 2 {
			section = "mic"
		}
		d.Rows = append(d.Rows, module.Row{Kind: module.RowPad, Section: section,
			Key:  "t" + strconv.Itoa(i),
			Text: "T" + strconv.Itoa(i), Act: []string{"act", strconv.Itoa(i)}})
	}
	return d
}

// The pad fills its region exactly, groups buttons under their section
// labels, and every button is a live tap target resolved by geometry --
// the recorder is a surface you operate with a thumb, so a button that
// paints but does not press (the bug the first flat cut shipped) is the
// failure that matters.
func TestRenoRecorderPadGridAndHits(t *testing.T) {
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = padRows(7)
	m.invalidateHome("reno-presenter")

	v := m.View()
	for i, l := range strings.Split(v.Content, "\n") {
		if lipgloss.Width(l) != 196 {
			t.Fatalf("line %d width = %d, want 196", i, lipgloss.Width(l))
		}
	}
	body := ansi.Strip(v.Content)
	if !strings.Contains(body, "run r-123") {
		t.Error("readout header missing from the pad")
	}
	for _, want := range []string{"TRANSPORT", "MIC"} {
		if !strings.Contains(body, want) {
			t.Errorf("section label %q missing:\n%s", want, body)
		}
	}
	for i := range 7 {
		if !strings.Contains(body, "T"+strconv.Itoa(i)) {
			t.Errorf("button T%d missing:\n%s", i, body)
		}
	}

	// every button answers a plain tap, resolved by its own rect
	msgs := busSink(t, m)
	seen := map[string]bool{}
	for _, h := range m.hits {
		if !strings.Contains(h.pressKey, ":pad:t") {
			continue
		}
		m.resolveTap(h.area.x+h.area.w/2, h.area.y+h.area.h/2)
		got := <-msgs
		if got.Type != proto.TypeRowAct || got.Argv[0] != "act" {
			t.Fatalf("button tap sent %+v", got)
		}
		seen[got.Argv[1]] = true
	}
	if len(seen) != 7 {
		t.Errorf("pressed %d distinct buttons, want 7", len(seen))
	}
}

// padBandRows is a recorder payload spanning every band the real pad
// publishes -- the drift this guards against grows one row per band, so a
// two-section payload cannot see it.
func padBandRows() module.Data {
	d := module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowSpans, Key: reno.KeyHead, Spans: []module.Span{{Text: "run r-123"}}},
	}}
	for _, section := range []string{"transport", "screens", "mic", "cam"} {
		for i := range 3 {
			key := section + ":" + strconv.Itoa(i)
			d.Rows = append(d.Rows, module.Row{Kind: module.RowPad, Section: section,
				Key: key, Text: strings.ToUpper(section) + strconv.Itoa(i),
				Act: []string{"act", key}})
		}
	}
	return d
}

// Every button's tap rect IS the button it paints, and the tap lands on
// that button's act. The rect used to start on the section label row and
// slide one further per band, so the last band's buttons drew where
// nothing was listening and took their taps from the band above -- a pad
// whose bottom rows quietly stopped being buttons.
func TestRenoRecorderPadHitsFrameTheirButtons(t *testing.T) {
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = padBandRows()
	m.invalidateHome("reno-presenter")

	grid := strings.Split(ansi.Strip(m.View().Content), "\n")
	msgs := busSink(t, m)
	pads := 0
	for _, h := range m.hits {
		key, ok := strings.CutPrefix(h.pressKey, "reno-presenter:pad:")
		if !ok {
			continue
		}
		pads++
		for _, corner := range []struct {
			y    int
			want string
		}{{h.area.y, "┌"}, {h.area.y + h.area.h - 1, "└"}} {
			if corner.y >= len(grid) {
				t.Fatalf("%s: rect row %d past the glass", key, corner.y)
			}
			if got := ansi.Cut(grid[corner.y], h.area.x, h.area.x+1); got != corner.want {
				t.Errorf("%s: rect corner at (%d,%d) = %q, want %q -- the rect is not on the button:\n%s",
					key, h.area.x, corner.y, got, corner.want, strings.Join(grid, "\n"))
			}
		}
		m.resolveTap(h.area.x+h.area.w/2, h.area.y+h.area.h/2)
		if got := <-msgs; got.Type != proto.TypeRowAct || got.Argv[1] != key {
			t.Errorf("tap on %s sent %+v", key, got)
		}
	}
	if pads != 12 {
		t.Errorf("registered %d button rects, want 12", pads)
	}
}

// A tile whose only control is a menu opens it on a PLAIN tap: a pad has
// no discoverable long-press, so a menu-only button that ignores taps is
// a dead button.
func TestRenoRecorderPadMenuTileOpensOnTap(t *testing.T) {
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowPad, Key: reno.KeyCfgAudio, Text: "MIC", Value: "obs as-is",
			Menu: []module.Act{{Label: "none (mute all)", Argv: []string{"reno:cfg-audio", "none"}}}},
	}}
	m.invalidateHome("reno-presenter")
	m.View()

	var tile rect
	for _, h := range m.hits {
		if strings.HasSuffix(h.pressKey, ":pad:"+reno.KeyCfgAudio) {
			tile = h.area
		}
	}
	if tile.w == 0 {
		t.Fatal("no hit registered for the mic tile")
	}
	m.resolveTap(tile.x+tile.w/2, tile.y+tile.h/2)
	if m.overlay == nil {
		t.Fatal("a menu-only tile must open its menu on a tap")
	}
	if len(m.overlay.items) != 1 || !strings.Contains(m.overlay.items[0].label, "mute all") {
		t.Errorf("overlay items = %+v", m.overlay.items)
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
