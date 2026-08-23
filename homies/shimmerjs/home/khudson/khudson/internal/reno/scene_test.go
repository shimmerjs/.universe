package reno

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func TestParseRegion(t *testing.T) {
	r, err := ParseRegion("100,50,800x600")
	if err != nil || r.X != 100 || r.Y != 50 || r.W != 800 || r.H != 600 {
		t.Fatalf("ParseRegion = %+v, %v", r, err)
	}
	for _, bad := range []string{"", "1,2,3", "1,2,0x600", "1,2,800x0", "a,b,cxd", "1,2,800x600,9"} {
		if _, err := ParseRegion(bad); err == nil {
			t.Errorf("ParseRegion(%q) must fail", bad)
		}
	}
}

func TestParseScreenSel(t *testing.T) {
	s, err := ParseScreenSel("2")
	if err != nil || s.Display != "2" || s.Region != nil {
		t.Fatalf("plain sel = %+v, %v", s, err)
	}
	s, err = ParseScreenSel("built-in:0,0,1280x720")
	if err != nil || s.Display != "built-in" || s.Region == nil || s.Region.W != 1280 {
		t.Fatalf("region sel = %+v, %v", s, err)
	}
	if _, err := ParseScreenSel(":1,2,3x4"); err == nil {
		t.Error("empty display must fail")
	}
	if _, err := ParseScreenSel("2:junk"); err == nil {
		t.Error("bad region must fail")
	}
}

func items(names ...string) []PropertyItem {
	out := make([]PropertyItem, len(names))
	for i, n := range names {
		out[i] = PropertyItem{Name: n, Value: "uid-" + n}
	}
	return out
}

func TestResolveItem(t *testing.T) {
	devs := items("MacBook Pro Microphone", "Elgato Wave:3", "Wave Link Stream")

	if it, err := ResolveItem(devs, "uid-Elgato Wave:3", "mic"); err != nil || it.Name != "Elgato Wave:3" {
		t.Errorf("by value: %+v, %v", it, err)
	}
	if it, err := ResolveItem(devs, "2", "mic"); err != nil || it.Name != "Elgato Wave:3" {
		t.Errorf("by index: %+v, %v", it, err)
	}
	if it, err := ResolveItem(devs, "macbook", "mic"); err != nil || it.Name != "MacBook Pro Microphone" {
		t.Errorf("by substring: %+v, %v", it, err)
	}
	if _, err := ResolveItem(devs, "wave", "mic"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("ambiguous must fail loudly: %v", err)
	}
	if _, err := ResolveItem(devs, "yeti", "mic"); err == nil || !strings.Contains(err.Error(), "matches nothing") {
		t.Errorf("zero match must fail loudly: %v", err)
	}
	// an exact name wins even when it substring-matches other items
	if it, err := ResolveItem(devs, "Elgato Wave:3", "mic"); err != nil || it.Value != "uid-Elgato Wave:3" {
		t.Errorf("exact name: %+v, %v", it, err)
	}
}

func sckEnv() ObsEnv {
	return ObsEnv{
		CanvasW: 1920, CanvasH: 1080,
		ScreenKind: "screen_capture", CamKind: "macos-avcapture",
		DisplayProp: "display_uuid", CamProp: "device",
		Displays: items("Built-in Retina Display", "DELL U2720Q"),
		Mics:     items("MacBook Pro Microphone"),
		Cams:     items("FaceTime HD Camera"),
		Monitors: []Monitor{
			{Index: 0, Name: "Built-in Retina Display", Width: 3024, Height: 1964},
			{Index: 1, Name: "DELL U2720Q", Width: 3840, Height: 2160},
		},
		Scenes: []string{"Scene"},
		Inputs: map[string]string{"Mic/Aux": "coreaudio_input_capture"},
	}
}

func TestBuildScenePlanScreens(t *testing.T) {
	cfg := RecordConfig{Screens: []ScreenSel{
		{Display: "dell"},
		{Display: "1", Region: &Region{X: 0, Y: 0, W: 1512, H: 982}},
	}}
	p, err := BuildScenePlan(cfg, sckEnv())
	if err != nil {
		t.Fatal(err)
	}
	if !p.CreateScene || p.Scene != SceneName {
		t.Errorf("scene = %q create=%v", p.Scene, p.CreateScene)
	}
	if len(p.Inputs) != 2 {
		t.Fatalf("inputs = %+v", p.Inputs)
	}
	first := p.Inputs[0]
	if first.Name != "reno-screen-0" || first.Kind != "screen_capture" ||
		first.Settings["display_uuid"] != "uid-DELL U2720Q" || first.Slot != 0 || first.Slots != 2 {
		t.Errorf("first = %+v", first)
	}
	second := p.Inputs[1]
	if second.Settings["display_uuid"] != "uid-Built-in Retina Display" || second.Region == nil {
		t.Errorf("second = %+v", second)
	}
	if second.Monitor == nil || second.Monitor.Width != 3024 {
		t.Errorf("second monitor fallback = %+v", second.Monitor)
	}
}

func TestBuildScenePlanLegacyNumericDisplay(t *testing.T) {
	env := sckEnv()
	env.ScreenKind = "display_capture"
	env.DisplayProp = "display"
	env.DisplayNumeric = true
	env.Displays = []PropertyItem{{Name: "Display 1", Value: "0"}, {Name: "Display 2", Value: "1"}}
	p, err := BuildScenePlan(RecordConfig{Screens: []ScreenSel{{Display: "Display 2"}}}, env)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := p.Inputs[0].Settings["display"].(int); !ok || v != 1 {
		t.Errorf("legacy display setting = %#v", p.Inputs[0].Settings["display"])
	}
}

func TestBuildScenePlanStaleScreensPruned(t *testing.T) {
	env := sckEnv()
	env.Inputs["reno-screen-0"] = "screen_capture"
	env.Inputs["reno-screen-1"] = "screen_capture"
	p, err := BuildScenePlan(RecordConfig{Screens: []ScreenSel{{Display: "dell"}}}, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.RemoveInputs) != 1 || p.RemoveInputs[0] != "reno-screen-1" {
		t.Errorf("removes = %v", p.RemoveInputs)
	}
}

func TestBuildScenePlanAudio(t *testing.T) {
	// none mutes every foreign audio input plus a leftover reno-mic
	env := sckEnv()
	env.Inputs["reno-mic"] = "coreaudio_input_capture"
	env.Inputs["reno-screen-0"] = "screen_capture"
	p, err := BuildScenePlan(RecordConfig{Audio: AudioNone}, env)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Mutes["Mic/Aux"] || !p.Mutes["reno-mic"] {
		t.Errorf("mutes = %v", p.Mutes)
	}
	if p.Mutes["reno-screen-0"] {
		t.Error("video inputs must not be muted")
	}

	// a device selection ensures the reno mic unmuted with that device
	p, err = BuildScenePlan(RecordConfig{Audio: "macbook"}, sckEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Inputs) != 1 || p.Inputs[0].Name != MicInput ||
		p.Inputs[0].Settings["device_id"] != "uid-MacBook Pro Microphone" {
		t.Errorf("mic input = %+v", p.Inputs)
	}
	if muted, ok := p.Mutes[MicInput]; !ok || muted {
		t.Errorf("mic must be explicitly unmuted: %v", p.Mutes)
	}
	if _, err := BuildScenePlan(RecordConfig{Audio: "yeti"}, sckEnv()); err == nil {
		t.Error("unknown device must fail the plan")
	}
}

func TestBuildScenePlanWebcam(t *testing.T) {
	p, err := BuildScenePlan(RecordConfig{Webcam: "br"}, sckEnv())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Inputs) != 1 || p.Inputs[0].Name != CamInput || !p.Inputs[0].Enabled ||
		p.Inputs[0].Preset != "br" || p.Inputs[0].Settings["device"] != "uid-FaceTime HD Camera" {
		t.Errorf("cam = %+v", p.Inputs)
	}
	p, err = BuildScenePlan(RecordConfig{Webcam: "off"}, sckEnv())
	if err != nil {
		t.Fatal(err)
	}
	if p.Inputs[0].Enabled {
		t.Error("preset off keeps the item disabled")
	}
	if _, err := BuildScenePlan(RecordConfig{Webcam: "sideways"}, sckEnv()); err == nil {
		t.Error("bad preset must fail")
	}
	env := sckEnv()
	env.CamKind = ""
	if _, err := BuildScenePlan(RecordConfig{Webcam: "br"}, env); err == nil {
		t.Error("no cam kind must fail loudly")
	}
}

// The stack names the visual items bottom-first: screens in slot order,
// the webcam over all of them, audio never.
func TestBuildScenePlanStack(t *testing.T) {
	p, err := BuildScenePlan(RecordConfig{
		Screens: []ScreenSel{{Display: "0"}, {Display: "1"}},
		Audio:   "MacBook Pro Microphone",
		Webcam:  "bl",
	}, sckEnv())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ScreenInputName(0), ScreenInputName(1), CamInput}
	if !slices.Equal(p.Stack, want) {
		t.Errorf("stack = %v, want %v", p.Stack, want)
	}
	p, err = BuildScenePlan(RecordConfig{
		Screens: []ScreenSel{{Display: "0"}},
		Webcam:  "off",
	}, sckEnv())
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(p.Stack, CamInput) {
		t.Errorf("an off webcam has nothing to stack: %v", p.Stack)
	}
}

func TestBuildScenePlanEmptyRefused(t *testing.T) {
	if _, err := BuildScenePlan(RecordConfig{}, sckEnv()); err == nil {
		t.Error("empty config must refuse to plan")
	}
}

func near(a, b float64) bool { return a-b < 0.01 && b-a < 0.01 }

func boxEq(t *testing.T, what string, got Transform, x, y, w, h float64) {
	t.Helper()
	if !near(got.X, x) || !near(got.Y, y) || !near(got.BoundsW, w) || !near(got.BoundsH, h) {
		t.Errorf("%s = (%.1f,%.1f %.0fx%.0f), want (%.1f,%.1f %.0fx%.0f)",
			what, got.X, got.Y, got.BoundsW, got.BoundsH, x, y, w, h)
	}
}

// The live pairing: a 3840x2160 dell over a 2560x720 edge on a 4K canvas.
// Height binds, so the stack is ~2520 wide with bars each side, and it
// fills the canvas vertically exactly -- rows plus the gutter between them.
func TestStackBoxesDellOverEdge(t *testing.T) {
	// the canvas StackCanvas derives for this pair, which is what the
	// executor sizes OBS to before laying the rows out
	cw, ch, ok := StackCanvas([][2]int{{3840, 2160}, {2560, 720}})
	if !ok || cw != 2560 || ch != 2200 {
		t.Fatalf("canvas = %vx%v ok=%v, want 2560x2200", cw, ch, ok)
	}
	got := StackBoxes(cw, ch, []float64{3840.0 / 2160, 2560.0 / 720})
	if len(got) != 2 {
		t.Fatalf("boxes = %+v", got)
	}
	gutter := math.Round(2560 * StackGutterFrac)
	// full width, no side bars at all, and the edge at its native height
	boxEq(t, "dell", got[0], 0, 0, 2560, 1440)
	boxEq(t, "edge", got[1], 0, 1440+gutter, 2560, 720)
	// every slot shares one width -- that is the whole point of rows
	if got[0].BoundsW != got[1].BoundsW {
		t.Errorf("widths differ: %v vs %v", got[0].BoundsW, got[1].BoundsW)
	}
	// stacking still beats the columns it replaced, which gave each 1920
	if got[0].BoundsW <= 1920 {
		t.Errorf("stack width %v must beat the old column width 1920", got[0].BoundsW)
	}
	// the narrowest source lands at 1:1 -- the whole reason the canvas is
	// derived rather than inherited
	if got[1].BoundsW != 2560 || got[1].BoundsH != 720 {
		t.Errorf("edge must render at its native 2560x720, got %+v", got[1])
	}
	// gutter, not a seam: the rows are held apart, and the gap plus the
	// rows still account for the whole canvas height
	gap := got[1].Y - (got[0].Y + got[0].BoundsH)
	if !near(gap, gutter) || gap <= 0 {
		t.Errorf("gap between rows = %v, want %v", gap, gutter)
	}
	if bottom := got[1].Y + got[1].BoundsH; !near(bottom, ch) {
		t.Errorf("stack bottom = %v, want the canvas height %v", bottom, ch)
	}
}

func TestStackBoxesSingleSlot(t *testing.T) {
	// one row has nothing to be held apart from, so it takes no gutter and
	// a screen matching the canvas fills it exactly, no bars anywhere
	boxEq(t, "dell alone", StackBoxes(3840, 2160, []float64{3840.0 / 2160})[0], 0, 0, 3840, 2160)
	// a lone wide strip spans the width and centers vertically
	boxEq(t, "edge alone", StackBoxes(3840, 2160, []float64{2560.0 / 720})[0], 0, 540, 3840, 1080)
}

func TestStackBoxesWidthBound(t *testing.T) {
	// two tall slots: width binds instead of height, so the gutter costs
	// nothing here and the block centers vertically rather than overflowing
	got := StackBoxes(1000, 4000, []float64{1, 1})
	gutter := math.Round(1000 * StackGutterFrac)
	boxEq(t, "top", got[0], 0, (4000-2000-gutter)/2, 1000, 1000)
	boxEq(t, "bottom", got[1], 0, (4000-2000-gutter)/2+1000+gutter, 1000, 1000)
	if got[0].BoundsW != 1000 {
		t.Errorf("width-bound stack must still use the full width: %+v", got[0])
	}
}

func TestStackBoxesUnknownAspect(t *testing.T) {
	// a source not reporting a size falls back to the canvas aspect rather
	// than dividing by zero or collapsing the row
	got := StackBoxes(1920, 1080, []float64{0, 0})
	if len(got) != 2 || got[0].BoundsH <= 0 || got[0].BoundsH != got[1].BoundsH {
		t.Fatalf("unknown aspects = %+v", got)
	}
	if got[0].BoundsW/got[0].BoundsH-1920.0/1080 > 0.01 {
		t.Errorf("fallback row must be canvas-shaped: %+v", got[0])
	}
	if StackBoxes(1920, 1080, nil) != nil {
		t.Error("no slots plans no boxes")
	}
}

func TestEffectiveSize(t *testing.T) {
	if w, h := EffectiveSize(3840, 2160, nil); w != 3840 || h != 2160 {
		t.Errorf("uncropped = %dx%d", w, h)
	}
	// a crop is what the canvas sees, so it is what the aspect and the
	// scale are both denominated in
	if w, h := EffectiveSize(3840, 2160, &Region{X: 0, Y: 0, W: 1280, H: 720}); w != 1280 || h != 720 {
		t.Errorf("cropped = %dx%d", w, h)
	}
	// out of bounds clamps rather than reporting more than exists
	if w, h := EffectiveSize(1000, 1000, &Region{X: 900, Y: 900, W: 500, H: 500}); w != 100 || h != 100 {
		t.Errorf("clamped = %dx%d", w, h)
	}
	// a source with no frames yet has no effective size to report
	if w, h := EffectiveSize(0, 0, &Region{X: 1, Y: 1, W: 10, H: 10}); w != 0 || h != 0 {
		t.Errorf("dead source = %dx%d", w, h)
	}
}

func TestItemTransformCrops(t *testing.T) {
	box := Transform{X: 640, Y: 1440, BoundsW: 2560, BoundsH: 720}
	// no region: the box passes through untouched
	tf, ok := ItemTransform(box, 3840, 2160, nil)
	if !ok || tf != box {
		t.Errorf("uncropped = %+v ok=%v", tf, ok)
	}
	// region converts to source crops, box preserved
	tf, ok = ItemTransform(box, 3840, 2160, &Region{X: 100, Y: 200, W: 800, H: 600})
	if !ok || tf.CropL != 100 || tf.CropT != 200 || tf.CropR != 3840-900 || tf.CropB != 2160-800 {
		t.Errorf("crops = %+v", tf)
	}
	if tf.X != box.X || tf.BoundsW != box.BoundsW {
		t.Errorf("crop must not move the box: %+v", tf)
	}
	// zero source dims: box stands, crop reports skipped
	tf, ok = ItemTransform(box, 0, 0, &Region{X: 1, Y: 1, W: 10, H: 10})
	if ok || tf.CropL != 0 {
		t.Errorf("zero-dim crop must be skipped: %+v ok=%v", tf, ok)
	}
	// an out-of-bounds region clamps instead of going negative
	tf, ok = ItemTransform(box, 1000, 1000, &Region{X: 900, Y: 900, W: 500, H: 500})
	if !ok || tf.CropR != 0 || tf.CropB != 0 || tf.CropL != 900 {
		t.Errorf("clamped crops = %+v", tf)
	}
}

func TestCamTransformPresets(t *testing.T) {
	br := CamTransform(1920, 1080, "br", 1920, 1080)
	if br.X <= 960 || br.Y <= 540 {
		t.Errorf("br must sit bottom-right: %+v", br)
	}
	tl := CamTransform(1920, 1080, "tl", 1920, 1080)
	if tl.X != 24 || tl.Y != 24 {
		t.Errorf("tl = %+v", tl)
	}
	big := CamTransform(1920, 1080, "big", 1920, 1080)
	// runtime float math, not exact constants: compare with tolerance
	if diff := big.BoundsW - 1728; diff < -0.01 || diff > 0.01 {
		t.Errorf("big bounds = %+v", big)
	}
	if diff := big.X - 96; diff < -0.01 || diff > 0.01 {
		t.Errorf("big must center: %+v", big)
	}
}

// A "+" corner is a QUARTER of the canvas area -- half of each side -- and
// it stays pinned to its corner. It is the size between a PiP and big,
// without big's move to the middle of the content.
func TestCamTransformQuarterCorners(t *testing.T) {
	pip, quarter := CamTransform(1920, 1080, "tl", 1920, 1080), CamTransform(1920, 1080, "tl+", 1920, 1080)
	boxEq(t, "tl+", quarter, 24, 24, 960, 540)
	if area := quarter.BoundsW * quarter.BoundsH; !near(area, 1920*1080/4) {
		t.Errorf("a + corner must cover a quarter of the canvas, got %v", area)
	}
	// same size between the PiP and big, which is why it exists
	big := CamTransform(1920, 1080, "big", 1920, 1080)
	if !(pip.BoundsW < quarter.BoundsW && quarter.BoundsW < big.BoundsW) {
		t.Errorf("quarter %v must fall between pip %v and big %v", quarter.BoundsW, pip.BoundsW, big.BoundsW)
	}
	// each + variant pins to ITS corner with the same pad as the small one
	boxEq(t, "br+", CamTransform(1920, 1080, "br+", 1920, 1080), 1920-960-24, 1080-540-24, 960, 540)
	boxEq(t, "tr+", CamTransform(1920, 1080, "tr+", 1920, 1080), 1920-960-24, 24, 960, 540)
	boxEq(t, "bl+", CamTransform(1920, 1080, "bl+", 1920, 1080), 24, 1080-540-24, 960, 540)
	// a + corner keeps the plain one's ANCHORED edges -- the two boxes are
	// different sizes, so it is the pinned edge that has to match, not the
	// origin
	for _, c := range []string{"tl", "tr", "bl", "br"} {
		small, grown := CamTransform(1920, 1080, c, 1920, 1080), CamTransform(1920, 1080, c+"+", 1920, 1080)
		hSmall, hGrown := small.X, grown.X
		if strings.HasSuffix(c, "r") {
			hSmall, hGrown = 1920-(small.X+small.BoundsW), 1920-(grown.X+grown.BoundsW)
		}
		vSmall, vGrown := small.Y, grown.Y
		if strings.HasPrefix(c, "b") {
			vSmall, vGrown = 1080-(small.Y+small.BoundsH), 1080-(grown.Y+grown.BoundsH)
		}
		if !near(hSmall, hGrown) || !near(vSmall, vGrown) {
			t.Errorf("%s+ left its corner: small %+v, grown %+v", c, small, grown)
		}
		if !near(hGrown, 24) || !near(vGrown, 24) {
			t.Errorf("%s+ must keep the corner pad: inset %v,%v", c, hGrown, vGrown)
		}
		if err := CheckWebcam(c + "+"); err != nil {
			t.Errorf("%s+ must be a legal preset: %v", c, err)
		}
	}
	// big is still the only preset that centers
	if !near(big.X, (1920-big.BoundsW)/2) {
		t.Errorf("big must center: %+v", big)
	}
}

func TestRecordConfigSummary(t *testing.T) {
	if !(RecordConfig{}).Empty() || (RecordConfig{Audio: AudioNone}).Empty() {
		t.Error("Empty is wrong")
	}
	cfg := RecordConfig{
		Screens: []ScreenSel{{Display: "dell", Region: &Region{X: 0, Y: 0, W: 10, H: 10}}},
		Audio:   AudioNone, Webcam: "br",
	}
	got := cfg.Summary()
	for _, want := range []string{"screen dell:0,0,10x10", "audio none", "webcam br"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
}

// A corner preset hugs its corner on ANY canvas. The box is shaped to the
// CAMERA, not the frame: once the canvas started following the screens it
// stopped being 16:9, and a canvas-proportional box left a 16:9 camera
// floating hundreds of pixels clear of the edge it is named after.
func TestCamTransformHugsTheCornerOnATallCanvas(t *testing.T) {
	const cw, ch = 2560.0, 2200.0 // the derived dell+edge canvas
	got := CamTransform(cw, ch, "br+", 1920, 1080)
	// the box IS the picture: 16:9, so no dead band inside it to float in
	if ar := got.BoundsW / got.BoundsH; !near(ar, 1920.0/1080) {
		t.Errorf("box aspect = %v, want the camera's 16:9: %+v", ar, got)
	}
	if right, bottom := cw-(got.X+got.BoundsW), ch-(got.Y+got.BoundsH); !near(right, 24) || !near(bottom, 24) {
		t.Errorf("br+ floats: %.1f from the right, %.1f from the bottom", right, bottom)
	}
	// and it still fits inside the requested fraction of the canvas
	if got.BoundsW > cw*0.5+0.01 || got.BoundsH > ch*0.5+0.01 {
		t.Errorf("box overran its share of the canvas: %+v", got)
	}
	// unknown source size falls back to canvas proportions rather than
	// dividing by zero
	fb := CamTransform(cw, ch, "br+", 0, 0)
	if fb.BoundsW != cw*0.5 || fb.BoundsH != ch*0.5 {
		t.Errorf("fallback box = %+v", fb)
	}
}

// Two selectors can name ONE display: a config written half from the CLI
// (`--screen XENEON`, a name substring) and half from the pad (a UUID) held
// the Edge twice. The plan built two full-size captures of it -- a doubled
// canvas, two ScreenCaptureKit sessions on one display, and an arm that
// refused because one of them never produced a frame.
func TestPlanDedupesOneDisplaySelectedTwice(t *testing.T) {
	env := ObsEnv{
		CanvasW: 2560, CanvasH: 720,
		ScreenKind: "screen_capture", DisplayProp: "display_uuid",
		Scenes: []string{SceneName}, Inputs: map[string]string{},
		Displays: []PropertyItem{
			{Name: "DELL U3223QE(0)", Value: "F8AF13FA"},
			{Name: "XENEON EDGE(1)", Value: "79CF2857"},
		},
	}
	p, err := BuildScenePlan(RecordConfig{Screens: []ScreenSel{
		{Display: "XENEON"},   // name substring, the CLI form
		{Display: "79CF2857"}, // the same display's uuid, the pad form
	}}, env)
	if err != nil {
		t.Fatal(err)
	}
	var screens []PlanInput
	for _, in := range p.Inputs {
		if IsScreenInput(in.Name) {
			screens = append(screens, in)
		}
	}
	if len(screens) != 1 {
		t.Fatalf("one display selected twice built %d captures: %+v", len(screens), screens)
	}
	if screens[0].Slots != 1 {
		t.Errorf("Slots = %d, want 1 -- the canvas is derived from this", screens[0].Slots)
	}
	if screens[0].Name != ScreenInputName(0) {
		t.Errorf("slot numbering left a hole: %s", screens[0].Name)
	}

	// but the same display CROPPED two ways is a real composition
	r1, _ := ParseRegion("0,0,1280x720")
	r2, _ := ParseRegion("1280,0,1280x720")
	p, err = BuildScenePlan(RecordConfig{Screens: []ScreenSel{
		{Display: "XENEON", Region: r1},
		{Display: "79CF2857", Region: r2},
	}}, env)
	if err != nil {
		t.Fatal(err)
	}
	screens = nil
	for _, in := range p.Inputs {
		if IsScreenInput(in.Name) {
			screens = append(screens, in)
		}
	}
	if len(screens) != 2 {
		t.Errorf("two crops of one display built %d captures, want 2", len(screens))
	}
}
