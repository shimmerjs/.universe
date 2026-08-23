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

// A hold on an act-only button fires the act. The press light goes up at
// contact whether or not anything can resolve the hold, so before this a
// held button lit, promised, and dropped the touch -- and the recognizer is
// silent on a menu-less held release, so nothing later corrected it.
func TestRenoPadHoldFiresTheActWhenThereIsNoMenu(t *testing.T) {
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = padRows(3)
	m.invalidateHome("reno-presenter")
	m.View()

	msgs := busSink(t, m)
	var held bool
	for _, h := range m.hits {
		if !strings.Contains(h.pressKey, ":pad:t") {
			continue
		}
		if !m.resolveLongPress(h.area.x+h.area.w/2, h.area.y+h.area.h/2) {
			t.Fatal("hold on an act-only button resolved to nothing")
		}
		got := <-msgs
		if got.Type != proto.TypeRowAct || got.Argv[0] != "act" {
			t.Fatalf("held button sent %+v", got)
		}
		if m.overlay != nil {
			t.Fatal("a menu-less button must not open a menu on hold")
		}
		held = true
		break
	}
	if !held {
		t.Fatal("no pad button found to hold")
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

// padAlarmedRows is the full recorder payload with the header carrying its
// two loud failures -- the state where the grid is at its shortest.
func padAlarmedRows() module.Data {
	d := module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowText, Text: "journal: boom", Style: module.StyleWarn},
		{Kind: module.RowText, Text: "recording FAILED: nope", Style: module.StyleWarn},
		{Kind: module.RowSpans, Key: reno.KeyHead, Spans: []module.Span{{Text: "run r-123"}}},
	}}
	for _, section := range []string{"transport", "screens", "mic", "cam", "fx"} {
		for i := range 3 {
			key := section + ":" + strconv.Itoa(i)
			r := module.Row{Kind: module.RowPad, Section: section, Key: key,
				Glyph: "*", Text: strings.ToUpper(section) + strconv.Itoa(i),
				Value: "val", Act: []string{"act", key}}
			if section == "transport" && i == 0 {
				r.MinHeight = 5
			}
			d.Rows = append(d.Rows, r)
		}
	}
	return d
}

// Every published band reaches the glass even when the header is at its
// tallest. Two alarm lines take two grid rows, and under the old even split
// that was enough to drop maxBands from five to four and discard the whole FX
// band -- silently, with nothing on the pad saying three controls had stopped
// existing. Bands are framed before any band is fattened, so the grid runs out
// of slack long before it runs out of bands.
func TestRenoRecorderPadKeepsEveryBandUnderAHeaderAlarm(t *testing.T) {
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = padAlarmedRows()
	m.invalidateHome("reno-presenter")

	body := ansi.Strip(m.View().Content)
	for _, section := range []string{"TRANSPORT", "SCREENS", "MIC", "CAM", "FX"} {
		if !strings.Contains(body, section) {
			t.Errorf("band %q dropped off the pad:\n%s", section, body)
		}
	}
	for _, key := range []string{"FX0", "FX1", "FX2"} {
		if !strings.Contains(body, key) {
			t.Errorf("button %q missing:\n%s", key, body)
		}
	}
}

// Key size comes from the glass, not from what the tiles say. Probing devices
// only adds tiles to the CHOICE bands, but a long device name used to widen
// the shared key and resize every button on the pad -- including the
// transport row, under the thumb that had just pressed one of them.
func TestRenoRecorderPadKeySizeIgnoresTileContent(t *testing.T) {
	widths := func(t *testing.T, extra ...module.Row) map[string]int {
		t.Helper()
		d := module.Data{Title: "reno rec", Rows: []module.Row{
			{Kind: module.RowSpans, Key: reno.KeyHead, Spans: []module.Span{{Text: "run r-123"}}},
			{Kind: module.RowPad, Section: "transport", Key: "t0", Text: "ARM",
				MinHeight: 5, Act: []string{"act", "t0"}},
		}}
		d.Rows = append(d.Rows, extra...)
		m := renoModel(196, 24)
		m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
		m.widgetData["reno-presenter"] = d
		m.invalidateHome("reno-presenter")
		m.View()
		got := map[string]int{}
		for _, h := range m.hits {
			if key, ok := strings.CutPrefix(h.pressKey, "reno-presenter:pad:"); ok {
				got[key] = h.area.w
			}
		}
		return got
	}

	cold := widths(t)
	probed := widths(t, module.Row{Kind: module.RowPad, Section: "screens", Key: "s0",
		Text: "display 1 3840x2160", Act: []string{"act", "s0"}})
	if cold["t0"] != probed["t0"] {
		t.Errorf("transport key was %d cells and became %d when a long label appeared in another band",
			cold["t0"], probed["t0"])
	}
	// and one key size across the pad, so a deck still reads as a grid
	if probed["s0"] != probed["t0"] {
		t.Errorf("bands sized themselves independently: transport=%d screens=%d",
			probed["t0"], probed["s0"])
	}
}

// The pad's loudest alarm has its longest value, and a readout that does
// not survive the tile is the pad lying by omission. It is the one state
// worth checking whole: everything else here is a device name or an age.
func TestRenoRecorderPadRendersTheLongestAlarmValue(t *testing.T) {
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	d := padAlarmedRows()
	d.Rows = append(d.Rows, module.Row{Kind: module.RowPad, Section: "transport",
		Key: "probe", Glyph: "*", Text: "RE-PROBE", Value: "displays moved",
		Style: module.StyleWarn, Act: []string{"act", "probe"}})
	m.widgetData["reno-presenter"] = d
	m.invalidateHome("reno-presenter")

	body := ansi.Strip(m.View().Content)
	if !strings.Contains(body, "displays moved") {
		t.Errorf("the alarm's value did not reach the glass:\n%s", body)
	}
}

// A long label crops; it never costs the tile its icon. The band reads as a
// strip of icons, and one tile losing its own because its text ran long
// reads as that tile being a different kind of thing.
func TestRenoRecorderPadKeepsTheGlyphOnALongLabel(t *testing.T) {
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	d := padAlarmedRows()
	d.Rows = append(d.Rows, module.Row{Kind: module.RowPad, Section: "screens",
		Key: "wide", Glyph: "@", Text: "display 1 3840x2160",
		Act: []string{"act", "wide"}})
	m.widgetData["reno-presenter"] = d
	m.invalidateHome("reno-presenter")

	body := ansi.Strip(m.View().Content)
	if !strings.Contains(body, "@") {
		t.Errorf("a long label dropped the tile's glyph:\n%s", body)
	}
}

// The transport carries weight: it asks for height with MinHeight and gets it
// before the choice bands divide the remainder, which is what buys its glyph a
// line of its own -- a record circle you hit without aiming. A choice tile
// keeps the glyph joined to its label.
func TestRenoRecorderPadTransportOutweighsTheChoiceBands(t *testing.T) {
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = padAlarmedRows()
	m.invalidateHome("reno-presenter")
	m.View()

	h := map[string]int{}
	for _, hit := range m.hits {
		if key, ok := strings.CutPrefix(hit.pressKey, "reno-presenter:pad:"); ok {
			h[key] = hit.area.h
		}
	}
	if h["transport:0"] < h["cam:0"]+2 {
		t.Errorf("transport tile h=%d against a choice tile h=%d -- the weighted band did not win its rows",
			h["transport:0"], h["cam:0"])
	}
	grid := strings.Split(ansi.Strip(m.View().Content), "\n")
	glyphOnly := false
	for _, line := range grid {
		if strings.Contains(line, "*") && !strings.Contains(line, "TRANSPORT0") {
			glyphOnly = true
			break
		}
	}
	if !glyphOnly {
		t.Errorf("the transport glyph never got a line of its own:\n%s", strings.Join(grid, "\n"))
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

// A five-band recorder pad leaves each tile three rows -- one interior
// line -- and every state this surface reports lives in Value. Before the
// fold, Value simply did not draw at that height: standby, the device
// list's age and a clip's name were all invisible on the real glass while
// every unit test that built its own two-band payload passed.
func TestPadTileFoldsValueWhenShort(t *testing.T) {
	plain := lipgloss.NewStyle()
	short := ansi.Strip(strings.Join(
		padTile(module.Row{Text: "STANDBY", Value: "ready"}, 22, 3, plain, plain), "\n"))
	if !strings.Contains(short, "STANDBY") || !strings.Contains(short, "ready") {
		t.Errorf("a three-row tile dropped its value:\n%s", short)
	}
	// folded onto ONE interior line, not spilled into the border
	if n := strings.Count(short, "ready"); n != 1 {
		t.Errorf("value drawn %d times:\n%s", n, short)
	}

	// with room, the two-line form is unchanged
	tall := ansi.Strip(strings.Join(
		padTile(module.Row{Text: "STANDBY", Value: "ready"}, 22, 4, plain, plain), "\n"))
	lines := strings.Split(strings.TrimRight(tall, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("tall tile = %d lines:\n%s", len(lines), tall)
	}
	if !strings.Contains(lines[1], "STANDBY") || !strings.Contains(lines[2], "ready") {
		t.Errorf("tall tile lost its two-line layout:\n%s", tall)
	}

	// a value that cannot fit beside its label is dropped rather than
	// crushing the label out of the tile
	tight := ansi.Strip(strings.Join(
		padTile(module.Row{Text: "STANDBY", Value: "an entire sentence of detail"}, 14, 3, plain, plain), "\n"))
	if !strings.Contains(tight, "STANDBY") {
		t.Errorf("label lost to an oversized value:\n%s", tight)
	}
}

// A stepper is ONE tile that answers TWO presses. Its ends have to land on
// separate hit regions inside the same frame, or the element is a single
// button that does something different depending where you hit it -- which
// is what it is, and exactly what must not be a surprise.
func TestRenoPadStepperEndsArePressedIndependently(t *testing.T) {
	d := module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowStepper, Section: "fx", Key: "fx:purple", MinHeight: 4,
			Text: "purple", Value: "12/20",
			Ends: []module.End{
				{Label: "-", Key: "fx:purple:down", Argv: []string{"act", "purple", "down"}},
				{Label: "+", Key: "fx:purple:up", Argv: []string{"act", "purple", "up"}},
			}},
	}}
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = d
	m.invalidateHome("reno-presenter")
	body := ansi.Strip(m.View().Content)

	spans := map[string]rect{}
	for _, h := range m.hits {
		if key, ok := strings.CutPrefix(h.pressKey, "reno-presenter:pad:"); ok {
			spans[key] = h.area
		}
	}
	down, okD := spans["fx:purple:down"]
	up, okU := spans["fx:purple:up"]
	if !okD || !okU {
		t.Fatalf("both ends must be pressable, got %v", spans)
	}
	// two targets, not one: they cannot overlap, and the minus is left of
	// the plus so the element reads the way every remote does
	if down.x+down.w > up.x {
		t.Errorf("ends overlap: minus %+v plus %+v", down, up)
	}
	if down.w <= 0 || up.w <= 0 {
		t.Errorf("an end with no width is not a button: %+v %+v", down, up)
	}
	// one name and ONE reading for the pair, which is the whole reason the
	// element exists -- two plain buttons each had to repeat both
	if n := strings.Count(body, "12/20"); n != 1 {
		t.Errorf("reading appears %d times, want once:\n%s", n, body)
	}
	if n := strings.Count(body, "purple"); n != 1 {
		t.Errorf("name appears %d times, want once:\n%s", n, body)
	}
}

// An end at the end of its range stays visible and stops firing. Dropping
// it would slide the other end sideways under a thumb already coming down.
func TestRenoPadStepperDimEndDoesNotFire(t *testing.T) {
	d := module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowStepper, Section: "fx", Key: "fx:jitter", MinHeight: 4,
			Text: "jitter", Value: "off",
			Ends: []module.End{
				{Label: "-", Key: "fx:jitter:down", Dim: true,
					Argv: []string{"act", "jitter", "down"}},
				{Label: "+", Key: "fx:jitter:up", Argv: []string{"act", "jitter", "up"}},
			}},
	}}
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = d
	m.invalidateHome("reno-presenter")
	body := ansi.Strip(m.View().Content)

	for _, h := range m.hits {
		if h.pressKey == "reno-presenter:pad:fx:jitter:down" {
			t.Error("a dim end must not register a press")
		}
	}
	var sawUp bool
	for _, h := range m.hits {
		if h.pressKey == "reno-presenter:pad:fx:jitter:up" {
			sawUp = true
		}
	}
	if !sawUp {
		t.Error("the live end must still fire")
	}
	// dim, not gone
	if !strings.Contains(body, "-") {
		t.Errorf("the dim end vanished:\n%s", body)
	}
}

// A d-pad's presses are where they point, and that is the whole reason it
// beats four buttons in a row: the top-left target has to be in the top
// left. Four regions inside one frame, none overlapping, split at the
// tile's waist.
func TestRenoPadDpadCornersLandWhereTheyPoint(t *testing.T) {
	d := module.Data{Title: "reno rec", Rows: []module.Row{
		{Kind: module.RowDpad, Section: "fx", Key: "fx:move", MinHeight: 4,
			Text: "slide", Value: "br",
			Ends: []module.End{
				{Label: "tl", Key: "fx:move:tl", Argv: []string{"act", "move", "tl"}},
				{Label: "tr", Key: "fx:move:tr", Argv: []string{"act", "move", "tr"}},
				{Label: "bl", Key: "fx:move:bl", Argv: []string{"act", "move", "bl"}},
				{Label: "br", Key: "fx:move:br", Dim: true, Argv: []string{"act", "move", "br"}},
			}},
	}}
	m := renoModel(196, 24)
	m.cfg.Widgets["reno-presenter"] = recorderWidget("reno-presenter")
	m.widgetData["reno-presenter"] = d
	m.invalidateHome("reno-presenter")
	body := ansi.Strip(m.View().Content)

	spans := map[string]rect{}
	for _, h := range m.hits {
		if key, ok := strings.CutPrefix(h.pressKey, "reno-presenter:pad:"); ok {
			spans[key] = h.area
		}
	}
	tl, okTL := spans["fx:move:tl"]
	tr, okTR := spans["fx:move:tr"]
	bl, okBL := spans["fx:move:bl"]
	if !okTL || !okTR || !okBL {
		t.Fatalf("every live corner must be pressable, got %v", spans)
	}
	// the corner the camera is already in fires nothing, and must not have
	// handed its region to a neighbour
	if _, ok := spans["fx:move:br"]; ok {
		t.Error("a dim corner must not register a press")
	}
	if tl.x >= tr.x {
		t.Errorf("top-left is not left of top-right: %+v %+v", tl, tr)
	}
	if tl.y+tl.h > bl.y {
		t.Errorf("top-left overlaps bottom-left: %+v %+v", tl, bl)
	}
	if tl.x != bl.x || tl.w != bl.w {
		t.Errorf("the left corners are not in one column: %+v %+v", tl, bl)
	}
	for _, s := range []rect{tl, tr, bl} {
		if s.w <= 0 || s.h <= 0 {
			t.Errorf("a corner with no area is not a button: %+v", s)
		}
	}
	// one name and ONE reading for all four, which is what four separate
	// buttons could never do
	if n := strings.Count(body, "slide"); n != 1 {
		t.Errorf("name appears %d times, want once:\n%s", n, body)
	}
	// dim, not gone: a corner that vanished would leave a hole where a
	// corner ought to be
	if !strings.Contains(body, "br") {
		t.Errorf("the dim corner vanished:\n%s", body)
	}
}

// A d-pad needs two interior lines to BE a d-pad. Given a tile that cannot
// hold them it falls back to a plain button rather than drawing corners
// stacked on one line, which would be four targets in the same place.
func TestRenoPadDpadTooShortFallsBackToAButton(t *testing.T) {
	ends := []module.End{
		{Label: "tl", Key: "fx:move:tl", Argv: []string{"act", "move", "tl"}},
		{Label: "tr", Key: "fx:move:tr", Argv: []string{"act", "move", "tr"}},
		{Label: "bl", Key: "fx:move:bl", Argv: []string{"act", "move", "bl"}},
		{Label: "br", Key: "fx:move:br", Argv: []string{"act", "move", "br"}},
	}
	row := module.Row{Kind: module.RowDpad, Section: "fx", Key: "fx:move",
		Text: "slide", Value: "br", Ends: ends}
	m := renoModel(196, 24)
	plain := lipgloss.NewStyle()
	// padTileFloorH is one interior line: not enough for corners
	lines := m.endsTile("w", row, padTileMaxW, padTileFloorH, plain, plain)
	body := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(body, "slide") {
		t.Errorf("the fallback lost the label:\n%s", body)
	}
	if strings.Contains(body, "tl") {
		t.Errorf("a one-line d-pad drew corners it has no room for:\n%s", body)
	}
	// and the hits fall back with it: one region for the whole tile, never
	// four stacked on the same cells
	before := len(m.hits)
	m.registerEndsHits("w", row, rect{0, 0, padTileMaxW, padTileFloorH})
	for _, h := range m.hits[before:] {
		if strings.HasSuffix(h.pressKey, ":tl") || strings.HasSuffix(h.pressKey, ":br") {
			t.Errorf("a corner registered on a tile too short to place it: %+v", h.area)
		}
	}
}
