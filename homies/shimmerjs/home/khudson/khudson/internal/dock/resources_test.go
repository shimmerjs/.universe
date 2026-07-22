package dock

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// resourcesModel is a home model whose whole interior is one resources
// chrome region.
func resourcesModel(w, h int) *model {
	m := newHomeModel(w, h)
	m.cfg.Widgets["res"] = config.Widget{ID: "res", Title: "resources", Chrome: true,
		Render: config.Render{Kind: "native", Module: "resources"}}
	l := m.cfg.Layouts["home"]
	l.Regions = []config.Region{{Widget: "res", Edge: "fill"}}
	m.cfg.Layouts["home"] = l
	return m
}

// resourcesData is the shared module contract: leading RowResource rows
// (Key carries the window hint, the raw pair rides RawX/RawY/RawHeat), one
// divider, then RowKV process rows -- the tail is bloom-only.
func resourcesData() module.Data {
	cpu := module.Resource("cpu 6h", 0.38, []float64{0.2, 0.7, 0.9}, "38%")
	cpu.RawX, cpu.RawY, cpu.RawHeat = "3.2", "/12", 0.27
	mem := module.Resource("mem 6h", 0.57, []float64{0.5, 0.6}, "20.5/36 GiB")
	mem.RawX, mem.RawY, mem.RawHeat = "20", "/36G", 0.57
	disk := module.Resource("/ 6h", 0.75, []float64{0.7, 0.75}, "3.0G/4.0G free 512M")
	disk.RawX, disk.RawY, disk.RawHeat = "512M", " free", 0.875
	disk.PctHeat = 0.875 // disk heats by free space, not used-fraction
	return module.Data{Title: "resources", Rows: []module.Row{
		cpu, mem, disk,
		{Kind: module.RowDivider},
		{Kind: module.RowKV, Key: "kernel_task", Value: "12.3%c 0.4%m"},
		{Kind: module.RowKV, Key: "windowserver", Value: "8.1%c 1.2%m"},
	}}
}

func hasBraille(s string) bool {
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28ff {
			return true
		}
	}
	return false
}

// A failed part's degrade note (the composer's dim RowText stand-in)
// renders on the card and in the bloom -- loud, never swallowed.
func TestResourcesDegradeNoteVisible(t *testing.T) {
	m := resourcesModel(80, 16)
	d := resourcesData()
	rows := []module.Row{d.Rows[0], d.Rows[1],
		{Kind: module.RowText, Text: "disk: statfs failed", Style: module.StyleDim}}
	rows = append(rows, d.Rows[3:]...)
	d.Rows = rows
	m.widgetData["res"] = d
	if out := ansi.Strip(m.renderHome(15)); !strings.Contains(out, "disk: statfs failed") {
		t.Fatal("card swallowed the degrade note")
	}
	m.openResourcesBloom(d, rect{0, 0, 40, 12})
	if m.overlay == nil {
		t.Fatal("bloom did not open")
	}
	if !strings.Contains(ansi.Strip(m.overlay.box), "disk: statfs failed") {
		t.Fatal("bloom swallowed the degrade note")
	}
}

func TestResourcesChromeRendererRegistered(t *testing.T) {
	if _, ok := chromeRenderers["resources"]; !ok {
		t.Fatal("resources not registered as a chrome renderer")
	}
}

// The vitals card: one identical row per metric -- glyph, dot-grid history,
// percent, raw pair -- and NOTHING else on glass: the divider and process
// rows stay bloom-only. Region content starts one line in (the region
// border; the home body draws no outer frame).
func TestResourcesVitalsCard(t *testing.T) {
	m := resourcesModel(80, 16)
	m.widgetData["res"] = resourcesData()
	out := m.renderHome(15)
	lines := strings.Split(out, "\n")
	if len(lines) != 15 {
		t.Fatalf("home body lines = %d, want 15", len(lines))
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("line %d width = %d, want 80", i, w)
		}
	}
	if strings.Contains(out, "no chrome renderer") {
		t.Fatal("resources fell through to the warn box")
	}

	// three card rows, one per metric (no title box: content starts at the
	// region's first line): plaintext word label + dot grid + pct + raw
	// pair; the root volume labels itself "disk", never a bare "/"
	for i, want := range []struct{ label, pct, raw string }{
		{"cpu", "38%", "3.2/12"},
		{"mem", "57%", "20/36G"},
		{"disk", "75%", "512M free"},
	} {
		plain := ansi.Strip(lines[i])
		if !strings.Contains(plain, want.label) {
			t.Errorf("row %d missing its word label %q: %q", i, want.label, plain)
		}
		if !hasBraille(plain) {
			t.Errorf("row %d has no dot-grid history: %q", i, plain)
		}
		if !strings.Contains(plain, want.pct) || !strings.Contains(plain, want.raw) {
			t.Errorf("row %d missing pct/raw: %q", i, plain)
		}
	}
	// the proc tail never reaches glass
	for _, gone := range []string{"kernel_task", "windowserver", "----"} {
		if strings.Contains(ansi.Strip(out), gone) {
			t.Errorf("%q leaked onto the card", gone)
		}
	}
	// no gauge bars anywhere (the card is grid + numbers only)
	for _, sgr := range []string{"\x1b[42m", "\x1b[100m"} {
		if strings.Contains(out, sgr) {
			t.Errorf("gauge SGR %q leaked onto the card", sgr)
		}
	}
}

// The raw pair aligns as sub-columns across rows: numerators right-aligned
// into one column, the "/" separators stacked (disk's suffix pair leaves a
// space in the slot), the bounds/suffixes starting together.
func TestResourcesRawColumnAligned(t *testing.T) {
	lines := vitalsLines(resourcesData(), 60, chromeRowStyles)
	if len(lines) != 3 {
		t.Fatalf("card lines = %d, want 3", len(lines))
	}
	w0 := lipgloss.Width(lines[0])
	for i, l := range lines {
		if lipgloss.Width(l) != w0 {
			t.Errorf("row %d width %d, want %d", i, lipgloss.Width(l), w0)
		}
	}
	// measure sub-columns relative to the percent sign: the glyph prefixes
	// have differing UTF-8 lengths, so absolute byte indexes lie about
	// cells; the "%"-to-raw segment is pure ASCII.
	cpu, mem, disk := ansi.Strip(lines[0]), ansi.Strip(lines[1]), ansi.Strip(lines[2])
	rel := func(s, needle string) int {
		at, pct := strings.Index(s, needle), strings.Index(s, "%")
		if at < 0 || pct < 0 {
			t.Fatalf("row missing %q or %%: %q", needle, s)
		}
		return at - pct
	}
	sepRel := rel(cpu, "/12")
	if at := rel(mem, "/36G"); at != sepRel {
		t.Errorf("mem separator at %%+%d, want %%+%d: %q", at, sepRel, mem)
	}
	if at := rel(disk, "free"); at != sepRel+1 {
		t.Errorf("disk bound at %%+%d, want %%+%d (sep slot + 1): %q", at, sepRel+1, disk)
	}
	if c := disk[strings.Index(disk, "%")+sepRel]; c != ' ' {
		t.Errorf("disk separator slot = %q, want space: %q", c, disk)
	}
}

// The card caps its own width: growing the region past the spark cap must
// not widen the rows (the freed right side stays blank).
func TestResourcesWidthCapped(t *testing.T) {
	at46 := vitalsLines(resourcesData(), 46, chromeRowStyles)
	at60 := vitalsLines(resourcesData(), 60, chromeRowStyles)
	for i := range at46 {
		w46, w60 := lipgloss.Width(at46[i]), lipgloss.Width(at60[i])
		if w46 != w60 {
			t.Errorf("row %d grew with the region: %d vs %d", i, w46, w60)
		}
	}
}

// PctHeat overrides the percent's ramp: a mostly-full disk with a quiet
// PctHeat renders its percent neutral, and a hot PctHeat renders a low
// percent loud.
func TestPctHeatOverridesFrac(t *testing.T) {
	ss := chromeRowStyles
	quiet := module.Resource("/ 6h", 0.9, []float64{0.9}, "")
	quiet.PctHeat = 0.2
	hot := module.Resource("/ 6h", 0.2, []float64{0.2}, "")
	hot.PctHeat = 0.95
	if got := textHeat(pctHeat(quiet), ss); got.GetForeground() != ss.fg.GetForeground() {
		t.Error("quiet PctHeat did not neutralize a high used-fraction")
	}
	if got := textHeat(pctHeat(hot), ss); got.GetForeground() != ss.heat[2].GetForeground() {
		t.Error("hot PctHeat did not heat a low used-fraction")
	}
}

// textHeat: neutral fg at rest, the spark ramp's hotter buckets above --
// digits and dots heat on the same thresholds.
func TestTextHeatBuckets(t *testing.T) {
	ss := chromeRowStyles
	if got := textHeat(0.2, ss); got.GetForeground() != ss.fg.GetForeground() {
		t.Error("low fraction not neutral fg")
	}
	if got := textHeat(0.7, ss); got.GetForeground() != ss.heat[1].GetForeground() {
		t.Error("mid fraction not heat[1]")
	}
	if got := textHeat(0.9, ss); got.GetForeground() != ss.heat[2].GetForeground() {
		t.Error("high fraction not heat[2]")
	}
}

// A too-narrow region degrades to name + percent, never panics.
func TestResourcesNarrowFallback(t *testing.T) {
	lines := vitalsLines(resourcesData(), 12, chromeRowStyles)
	for i, l := range lines {
		plain := ansi.Strip(l)
		if hasBraille(plain) {
			t.Errorf("narrow row %d kept its grid: %q", i, plain)
		}
	}
	_ = lines
}

// Tapping the card arms the debounced bloom; once the delay lapses it
// opens the info popover: full-width dot grids, the process table, the
// disk free reading -- and no fireable items.
func TestResourcesBloomOpens(t *testing.T) {
	m := resourcesModel(80, 16)
	m.widgetData["res"] = resourcesData()
	m.View() // build hits
	if m.overlay != nil {
		t.Fatal("overlay open before any tap")
	}
	if !m.resolveTap(10, 5) {
		t.Fatal("card tap not consumed")
	}
	if m.overlay != nil {
		t.Fatal("bloom opened inside the debounce (double-tap flicker)")
	}
	if m.resPending == nil {
		t.Fatal("tap did not arm the pending bloom")
	}
	// the debounce lapses: the tick opens it
	m.resPending.at = time.Now().Add(-bloomDelay)
	m.openPendingBloom()
	o := m.overlay
	if o == nil || !o.info {
		t.Fatalf("lapsed debounce did not open the info bloom: %+v", o)
	}
	if m.resPending != nil {
		t.Fatal("open left the pending mark set")
	}
	if len(o.items) != 0 {
		t.Fatal("info bloom carries fireable items")
	}
	plain := ansi.Strip(o.box)
	for _, want := range []string{"cpu", "kernel_task", "windowserver", "512M free"} {
		if !strings.Contains(plain, want) {
			t.Errorf("bloom missing %q", want)
		}
	}
	if !hasBraille(plain) {
		t.Error("bloom has no dot-grid histories")
	}

	// outside tap dismisses (the box is clamped inside the frame; 79,15 is
	// the far corner outside the anchor)
	if !m.resolveTap(o.anchor.x+o.anchor.w, min(o.anchor.y+o.anchor.h, 15)) {
		t.Fatal("outside tap not consumed")
	}
	if m.overlay != nil {
		t.Fatal("outside tap kept the bloom open")
	}
}

// A fast second tap converts to the monitor layout BEFORE the bloom ever
// renders (the debounce eats the flicker); a mid-speed second tap converts
// through the open bloom; a slow one holds the bloom open.
func TestResourcesBloomDoubleTapConverts(t *testing.T) {
	m := resourcesModel(80, 16)
	m.widgetData["res"] = resourcesData()
	m.cfg.Layouts[monitorLayout] = config.Layout{Kind: "home",
		Regions: []config.Region{{Widget: "res", Edge: "fill"}}}
	m.View()

	// fast double tap: converts pre-bloom, no overlay ever shows
	if !m.resolveTap(10, 5) || !m.resolveTap(10, 5) {
		t.Fatal("card taps not consumed")
	}
	if m.overlay != nil {
		t.Fatal("fast double tap flashed the bloom")
	}
	if m.resPending != nil {
		t.Fatal("conversion left the pending bloom armed")
	}
	if m.layout != monitorLayout {
		t.Fatalf("layout = %q, want %q", m.layout, monitorLayout)
	}

	// back home; a single tap whose debounce lapsed opens the bloom
	m.layout = "home"
	m.resetLayout()
	m.View()
	if !m.resolveTap(10, 5) {
		t.Fatal("card tap not consumed")
	}
	m.resPending.at = time.Now().Add(-bloomDelay)
	m.openPendingBloom()
	o := m.overlay
	if o == nil {
		t.Fatal("bloom did not open")
	}

	// slow tap: past the window, stays open
	o.openedWall = time.Now().Add(-time.Second)
	if !m.resolveTap(o.anchor.x+1, o.anchor.y+1) {
		t.Fatal("in-box tap not consumed")
	}
	if m.overlay == nil {
		t.Fatal("slow in-box tap dismissed the bloom")
	}
	if m.layout == monitorLayout {
		t.Fatal("slow tap converted")
	}

	// mid-speed tap: bloom open, still inside the window, converts
	o.openedWall = time.Now()
	if !m.resolveTap(o.anchor.x+1, o.anchor.y+1) {
		t.Fatal("in-box tap not consumed")
	}
	if m.overlay != nil {
		t.Fatal("conversion left the bloom open")
	}
	if m.layout != monitorLayout {
		t.Fatalf("layout = %q, want %q", m.layout, monitorLayout)
	}
}

// Column alignment holds across short and long names and wide and narrow
// values; names cap at kvNameCap and truncate via fitCell, never pushing
// their row's value columns out of line.
func TestResourcesProcessColumnsShortAndLongNames(t *testing.T) {
	long := "averylongprocessnamewellpastthecap"
	rows := []module.Row{
		{Kind: module.RowKV, Key: "sh", Value: "289.7%c 13.0%m"},
		{Kind: module.RowKV, Key: long, Value: "8.1%c 0.4%m"},
		{Kind: module.RowKV, Key: "kernel_task", Value: "28.5%c 0.1%m"},
	}
	keyW, valW := kvColumns(rows)
	if keyW != kvNameCap {
		t.Errorf("keyW = %d, want the cap %d", keyW, kvNameCap)
	}
	vals := make([]string, len(rows))
	for i, r := range rows {
		vals[i] = alignFields(r.Value, valW)
	}
	cAt, mAt := strings.Index(vals[0], "%c"), strings.Index(vals[0], "%m")
	for i, v := range vals[1:] {
		if at := strings.Index(v, "%c"); at != cAt {
			t.Errorf("row %d %%c at %d, want %d: %q", i+1, at, cAt, v)
		}
		if at := strings.Index(v, "%m"); at != mAt {
			t.Errorf("row %d %%m at %d, want %d: %q", i+1, at, mAt, v)
		}
	}
}

// A resources poll error is loud inside the region frame.
func TestResourcesErrorIsLoud(t *testing.T) {
	m := resourcesModel(80, 16)
	m.widgetErr["res"] = "cpumem: boom"
	out := m.renderHome(15)
	if !strings.Contains(out, "boom") {
		t.Error("poll error not surfaced")
	}
	if !strings.Contains(out, "\x1b[33m") {
		t.Error("poll error not warn-styled")
	}
}

// liveResourcesJSON is an exact TypeWidgetData payload captured from a live
// bus BEFORE the raw-pair fields existed: the card must render its three
// rows regardless (empty raw column, never a crash).
const liveResourcesJSON = `{"title":"resources","rows":[{"kind":"resource","key":"cpu 6h","value":"14% of 12","frac":0.139892578125,"series":[0.23,0.27,0.26]},{"kind":"resource","key":"mem 6h","value":"18.4/36 GiB","frac":0.51,"series":[0.56,0.56]},{"kind":"resource","key":"/ 6h","value":"406G/460G free 54G","frac":0.88,"series":[0.88,0.88]},{"kind":"divider"},{"kind":"kv","key":"kitty","value":"0.0%c 0.0%m"},{"kind":"kv","key":"zsh","value":"0.0%c 0.0%m"}]}`

// TestResourcesReplayLiveWidgetData replays a captured (pre-raw-pair)
// payload through the real decode + render path at the live glass geometry.
func TestResourcesReplayLiveWidgetData(t *testing.T) {
	var d module.Data
	if err := json.Unmarshal([]byte(liveResourcesJSON), &d); err != nil {
		t.Fatalf("unmarshal live payload: %v", err)
	}
	m := newHomeModel(196, 24)
	m.cfg.Widgets["resources"] = config.Widget{ID: "resources", Title: "resources",
		Render: config.Render{Kind: "native", Module: "resources"}}
	l := m.cfg.Layouts["home"]
	l.Regions = []config.Region{
		{Widget: "dock-rail", Edge: "left", Size: 20},
		{Widget: "nav-tray", Edge: "right", Size: 12},
		{Widget: "claude-hud", Edge: "top", Size: 8},
		{Widget: "resources", Edge: "fill"},
	}
	m.cfg.Layouts["home"] = l
	m.widgetData["resources"] = d

	lines := strings.Split(m.renderHome(23), "\n")
	rowsSeen := 0
	for _, ln := range lines {
		p := ansi.Strip(ln)
		if hasBraille(p) && (strings.Contains(p, "%")) {
			rowsSeen++
		}
	}
	if rowsSeen < 3 {
		t.Fatalf("card rows with grid+pct = %d, want 3 (pre-raw payload must still render)", rowsSeen)
	}
	if strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "kitty ") {
		t.Error("proc tail leaked onto the card from the live payload")
	}
}
