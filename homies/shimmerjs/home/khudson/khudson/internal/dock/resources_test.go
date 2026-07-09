package dock

import (
	"encoding/json"
	"strings"
	"testing"

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
// (Key carries the window hint), one divider, then RowKV process rows.
func resourcesData() module.Data {
	return module.Data{Title: "resources", Rows: []module.Row{
		module.Resource("cpu 6h", 0.38, []float64{0.2, 0.7, 0.9}, "38%"),
		module.Resource("mem 6h", 0.57, []float64{0.5, 0.6}, "20.5G"),
		module.Resource("/ 6h", 0.75, []float64{0.7, 0.75}, "512G"),
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

func firstBrailleIndex(s string) int {
	for i, r := range s {
		if r >= 0x2800 && r <= 0x28ff {
			return i
		}
	}
	return -1
}

func TestResourcesChromeRendererRegistered(t *testing.T) {
	if _, ok := chromeRenderers["resources"]; !ok {
		t.Fatal("resources not registered as a chrome renderer")
	}
}

// Leading resource rows become side-by-side live cells: name, gauge, bold
// value stacked per cell, every metric's segments starting at its cell
// column. Region content starts two lines in (home border + region border).
func TestResourcesCellsSideBySide(t *testing.T) {
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

	names, values := ansi.Strip(lines[2]), ansi.Strip(lines[4])
	for _, n := range []string{"cpu", "mem", "/"} {
		if !strings.Contains(names, n) {
			t.Errorf("cell name %q missing from %q", n, names)
		}
	}
	// cells align: each value starts at its metric's cell column
	for n, v := range map[string]string{"cpu": "38%", "mem": "20.5G", "/": "512G"} {
		if strings.Index(names, n) != strings.Index(values, v) {
			t.Errorf("%s cell misaligned: name at %d, value %q at %d",
				n, strings.Index(names, n), v, strings.Index(values, v))
		}
	}
	// gauge row: ANSI-16 fill + track backgrounds; live cells carry no spark
	for _, sgr := range []string{"\x1b[42m", "\x1b[100m"} {
		if !strings.Contains(lines[3], sgr) {
			t.Errorf("gauge row missing SGR %q", sgr)
		}
	}
	if hasBraille(lines[3]) || hasBraille(lines[2]) || hasBraille(lines[4]) {
		t.Error("live cells must not carry a sparkline")
	}
	// big current value = bold
	if !strings.Contains(lines[4], "\x1b[1m") {
		t.Error("current value not bold")
	}
}

// At the real home geometry (196x24 glass, rail 20 / tray 12 / claude 8 per
// edge.cue -> a ~162x13 fill region) the three live cells must render side
// by side. The widget is declared exactly as the live config does -- kind
// "native", NO chrome flag -- so this also pins renderRegion dispatching on
// the module id alone.
func TestResourcesCellsAtLiveGeometry(t *testing.T) {
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
	m.widgetData["resources"] = resourcesData()

	out := m.renderHome(23)
	lines := strings.Split(out, "\n")
	if len(lines) != 23 {
		t.Fatalf("home body lines = %d, want 23", len(lines))
	}
	for i, ln := range lines {
		if w := lipgloss.Width(ln); w != 196 {
			t.Errorf("line %d width = %d, want 196", i, w)
		}
	}
	if strings.Contains(out, "no chrome renderer") {
		t.Fatal("resources fell through to the warn box")
	}
	cells := -1
	for i, ln := range lines {
		p := ansi.Strip(ln)
		if strings.Contains(p, "cpu") && strings.Contains(p, "mem") && strings.Contains(p, "/") {
			cells = i
			break
		}
	}
	if cells < 0 {
		t.Fatal("no line carries cpu, mem, and / side by side: cells path did not fire")
	}
}

// One full-width history sparkline per resource beneath the cells: label,
// dim window hint from the row Key, sparks column-aligned across rows.
func TestResourcesHistoryRows(t *testing.T) {
	m := resourcesModel(80, 16)
	m.widgetData["res"] = resourcesData()
	lines := strings.Split(m.renderHome(15), "\n")

	sparkAt := -1
	for i, name := range []string{"cpu", "mem", "/"} {
		plain := ansi.Strip(lines[5+i])
		if !strings.Contains(plain, name) {
			t.Errorf("history row %d missing label %q: %q", i, name, plain)
		}
		if !strings.Contains(plain, "(6h)") {
			t.Errorf("history row %d missing window hint: %q", i, plain)
		}
		at := firstBrailleIndex(plain)
		if at < 0 {
			t.Fatalf("history row %d has no sparkline: %q", i, plain)
		}
		if sparkAt < 0 {
			sparkAt = at
		} else if at != sparkAt {
			t.Errorf("history row %d spark at %d, want %d (column-aligned)", i, at, sparkAt)
		}
	}
}

// styledBGWidth counts the cells rendered under the gauge background SGRs;
// gauge content is ASCII spaces, so bytes equal cells.
func styledBGWidth(line string) int {
	n, styled := 0, false
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			j := i + 1
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j >= len(line) {
				break
			}
			seq := line[i : j+1]
			styled = seq == "\x1b[42m" || seq == "\x1b[100m"
			i = j + 1
			continue
		}
		if styled {
			n++
		}
		i++
	}
	return n
}

func countBraille(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28ff {
			n++
		}
	}
	return n
}

// The history spark dominates the live-cell gauge: the gauge is one sample
// of the series beside it, so it caps at cellGaugeCap while the history
// spark draws every emitted sample across the remaining region width.
func TestResourcesSparkDominatesGauge(t *testing.T) {
	series := make([]float64, module.MaxSeries)
	for i := range series {
		series[i] = 0.5
	}
	// 160 cols = the live fill region's content width at 196x24 with the
	// edge.cue peel (rail 20 / tray 12)
	lines := resourceClusterLines(module.Data{Rows: []module.Row{
		module.Resource("cpu 6h", 0.5, series, "50% of 12"),
	}}, 160, chromeRowStyles)
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 3 cell lines + 1 history row", len(lines))
	}
	gauge := styledBGWidth(lines[1])
	if gauge != cellGaugeCap {
		t.Errorf("cell gauge width = %d, want capped at %d", gauge, cellGaugeCap)
	}
	spark := countBraille(lines[3])
	if spark != module.MaxSeries {
		t.Errorf("history spark cells = %d, want the full emitted series %d", spark, module.MaxSeries)
	}
	if spark <= gauge*2 {
		t.Errorf("spark %d vs gauge %d: history must visually dominate", spark, gauge)
	}
}

// The hint renders ONLY when the module put one in the Key.
func TestResourcesHistoryHintOnlyWhenProvided(t *testing.T) {
	with := historyLine(module.Resource("cpu 6h", 0.4, []float64{0.5}, "40%"), 40, chromeRowStyles)
	without := historyLine(module.Resource("cpu", 0.4, []float64{0.5}, "40%"), 40, chromeRowStyles)
	if !strings.Contains(ansi.Strip(with), "(6h)") {
		t.Error("provided hint not rendered")
	}
	if strings.Contains(ansi.Strip(without), "(") {
		t.Error("hint invented for a bare key")
	}
	for _, l := range []string{with, without} {
		if w := lipgloss.Width(l); w != 40 {
			t.Errorf("history line width = %d, want 40", w)
		}
	}
}

// Divider then process rows as a column-aligned table: names left-aligned
// and padded, cpu and mem fields right-aligned in per-column cells so the
// %c and %m columns line up vertically.
func TestResourcesProcessTableAligned(t *testing.T) {
	m := resourcesModel(80, 16)
	m.widgetData["res"] = resourcesData()
	lines := strings.Split(m.renderHome(15), "\n")

	if !strings.Contains(ansi.Strip(lines[8]), "----") {
		t.Errorf("divider row missing: %q", ansi.Strip(lines[8]))
	}
	p1, p2 := ansi.Strip(lines[9]), ansi.Strip(lines[10])
	if !strings.Contains(p1, "kernel_task") || !strings.Contains(p2, "windowserver") {
		t.Fatalf("process rows missing: %q / %q", p1, p2)
	}
	if strings.Index(p1, "%c") != strings.Index(p2, "%c") {
		t.Errorf("%%c column not aligned: %q / %q", p1, p2)
	}
	if strings.Index(p1, "%m") != strings.Index(p2, "%m") {
		t.Errorf("%%m column not aligned: %q / %q", p1, p2)
	}
}

// Column alignment holds across short and long names and wide and narrow
// values; names cap at kvNameCap and truncate via fitCell, never pushing
// their row's value columns out of line.
func TestResourcesProcessColumnsShortAndLongNames(t *testing.T) {
	long := "averylongprocessnamewellpastthecap"
	lines := resourceClusterLines(module.Data{Rows: []module.Row{
		{Kind: module.RowDivider},
		{Kind: module.RowKV, Key: "sh", Value: "289.7%c 13.0%m"},
		{Kind: module.RowKV, Key: long, Value: "8.1%c 0.4%m"},
		{Kind: module.RowKV, Key: "kernel_task", Value: "28.5%c 0.1%m"},
	}}, 78, chromeRowStyles)
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (divider + 3 rows)", len(lines))
	}
	rows := make([]string, 3)
	for i := range rows {
		rows[i] = ansi.Strip(lines[1+i])
	}
	cAt, mAt := strings.Index(rows[0], "%c"), strings.Index(rows[0], "%m")
	for i, r := range rows[1:] {
		if at := strings.Index(r, "%c"); at != cAt {
			t.Errorf("row %d %%c at %d, want %d: %q", i+1, at, cAt, r)
		}
		if at := strings.Index(r, "%m"); at != mAt {
			t.Errorf("row %d %%m at %d, want %d: %q", i+1, at, mAt, r)
		}
	}
	if strings.Contains(rows[1], long) {
		t.Errorf("long name not capped: %q", rows[1])
	}
	if !strings.Contains(rows[1], long[:kvNameCap]) {
		t.Errorf("capped name missing its %d-col prefix: %q", kvNameCap, rows[1])
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

// liveResourcesJSON is an exact TypeWidgetData payload for the resources
// widget captured from a live bus: old-sampler junk kv rows included. The
// replay below must cell the leading resource rows regardless.
const liveResourcesJSON = `{"title":"resources","rows":[{"kind":"resource","key":"cpu 6h","value":"14% of 12","frac":0.139892578125,"series":[0.234619140625,0.27587890625,0.2604573567708333,0.2529296875,0.2993977864583333,0.2722574869791667,0.3171793619791667,0.311767578125,0.29345703125,0.2899576822916667,0.2667236328125,0.2520345052083333,0.24515787760416666,0.232177734375,0.22025553385416666,0.19970703125,0.197021484375,0.194580078125,0.19234212239583334,0.18359375,0.18888346354166666,0.180419921875,0.172607421875,0.15877278645833334,0.1527099609375,0.14713541666666666,0.16202799479166666,0.16239420572916666,0.16939290364583334,0.16280110677083334,0.15641276041666666,0.15055338541666666,0.15848795572916666,0.1524658203125,0.14689127604166666,0.13509114583333334,0.124267578125,0.1209716796875,0.137939453125,0.14689127604166666,0.14176432291666666,0.14375813802083334,0.14558919270833334,0.135986328125,0.14119466145833334,0.1298828125,0.13948567708333334,0.13496907552083334,0.1441650390625,0.1392822265625,0.134765625,0.13728841145833334,0.1329345703125,0.12894694010416666,0.12528483072916666,0.14192708333333334,0.13720703125,0.1395263671875,0.14168294270833334,0.139892578125]},{"kind":"resource","key":"mem 6h","value":"18.4/36 GiB","frac":0.5107447306315104,"series":[0.5635422600640191,0.5649672614203559,0.5657365587022569,0.5611983405219184,0.5633714463975694,0.5639533996582031,0.5276370578342013,0.5250087314181857,0.5254033406575521,0.5274912516276041,0.5262400309244791,0.5263642205132378,0.5247535705566406,0.5236083136664497,0.5256678263346354,0.5300627814398872,0.5263184441460503,0.52430174085829,0.5239134894476997,0.5240838792588975,0.5310609605577257,0.5251803927951388,0.5230962965223525,0.5225062900119357,0.5223553975423177,0.5208702087402344,0.5314153035481771,0.5308409796820747,0.5270491706000434,0.5318904452853732,0.526808844672309,0.5266876220703125,0.5328025817871094,0.5251837836371528,0.5227843390570747,0.53205320570204,0.526717291937934,0.5259518093532987,0.5246768527560763,0.5210982428656684,0.5035663180881076,0.5025575425889757,0.5078587002224393,0.5130568610297309,0.5166753133138021,0.5094087388780382,0.5088691711425781,0.5099275377061632,0.5235438876681857,0.5129890441894531,0.510650634765625,0.5105226304796007,0.5107205708821615,0.5141766866048177,0.5140410529242622,0.5127283732096354,0.5106993781195747,0.5104336208767362,0.5111024644639757,0.5141525268554688]},{"kind":"resource","key":"/ 6h","value":"406G/460G free 54G","frac":0.8816888280972833,"series":[0.8815273940064646,0.8815186118593634,0.8819221349485767,0.8815296972488176,0.8815376674615643,0.8819006932535787,0.8815353310790335,0.8815330029815472,0.8815330526918138,0.8815400701244503,0.8815464744637987,0.8815536410272351,0.8815536907375018,0.8815537818729906,0.8815689352192624,0.8815763669041208,0.8815804928562494,0.8815836163180015,0.881583417476935,0.8815835334675571,0.8815970298049419,0.8816115286327034,0.8816115534878368,0.8815950082540998,0.881595049679322,0.8816498718183493,0.8816082560401516,0.8816154308886324,0.8816153480381881,0.8816154391736768,0.8816219097933807,0.8816290597867282,0.8816383058963179,0.8816383390364956,0.8816384964523398,0.8816308162161485,0.8816370134293859,0.8816174938646968,0.8816178086963853,0.8816126471137022,0.8816042626487338,0.8816037572610232,0.881610053894794,0.881617328163808,0.8816212635599148,0.8816215452514256,0.8816215038262034,0.8816298965762162,0.8816451576280657,0.8816471046135079,0.8816471046135079,0.8816542214666777,0.8816542546068554,0.8816608080770036,0.8816678006545069,0.8816679000750401,0.88166797464044,0.881675132918832,0.8816751080636988,0.8816888280972833]},{"kind":"divider"},{"kind":"kv","key":"<defunct>","value":"0.0%c 0.0%m"},{"kind":"kv","key":"ps","value":"0.0%c 0.0%m"},{"kind":"kv","key":"caffeinate","value":"0.0%c 0.0%m"},{"kind":"kv","key":"mdworker_shared","value":"0.0%c 0.0%m"},{"kind":"kv","key":"com.apple.safariplatformsupport.helper","value":"0.0%c 0.0%m"},{"kind":"kv","key":"kitten","value":"0.0%c 0.0%m"},{"kind":"kv","key":"khudson","value":"0.0%c 0.0%m"},{"kind":"kv","key":"kitty","value":"0.0%c 0.0%m"},{"kind":"kv","key":"zsh","value":"0.0%c 0.0%m"},{"kind":"kv","key":"(taskgated)","value":"0.0%c 0.0%m"}]}`

// TestResourcesReplayLiveWidgetData replays the captured live payload
// through the real decode + render path at the live glass geometry.
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
	cells := -1
	for i, ln := range lines {
		p := ansi.Strip(ln)
		if strings.Contains(p, "cpu") && strings.Contains(p, "mem") && strings.Contains(p, "/") {
			cells = i
			break
		}
	}
	if cells < 0 {
		t.Fatal("live payload did not render side-by-side cells")
	}
	// the three live values share the cells' value line
	v := ansi.Strip(lines[cells+2])
	for _, want := range []string{"14% of 12", "18.4/36 GiB", "406G/460G free 54G"} {
		if !strings.Contains(v, want) {
			t.Errorf("cell value %q missing from %q", want, v)
		}
	}
}
