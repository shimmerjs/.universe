package reno

import (
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

func TestItemTransformSlots(t *testing.T) {
	// two slots split the canvas into columns
	tf, ok := ItemTransform(1920, 1080, 1, 2, 3840, 2160, nil)
	if !ok || tf.X != 960 || tf.BoundsW != 960 || tf.BoundsH != 1080 {
		t.Errorf("slot 1/2 = %+v ok=%v", tf, ok)
	}
	// region converts to source crops
	tf, ok = ItemTransform(1920, 1080, 0, 1, 3840, 2160, &Region{X: 100, Y: 200, W: 800, H: 600})
	if !ok || tf.CropL != 100 || tf.CropT != 200 || tf.CropR != 3840-900 || tf.CropB != 2160-800 {
		t.Errorf("crops = %+v", tf)
	}
	// zero source dims: transform stands, crop reports skipped
	tf, ok = ItemTransform(1920, 1080, 0, 1, 0, 0, &Region{X: 1, Y: 1, W: 10, H: 10})
	if ok || tf.CropL != 0 {
		t.Errorf("zero-dim crop must be skipped: %+v ok=%v", tf, ok)
	}
	// an out-of-bounds region clamps instead of going negative
	tf, ok = ItemTransform(1920, 1080, 0, 1, 1000, 1000, &Region{X: 900, Y: 900, W: 500, H: 500})
	if !ok || tf.CropR != 0 || tf.CropB != 0 || tf.CropL != 900 {
		t.Errorf("clamped crops = %+v", tf)
	}
}

func TestCamTransformPresets(t *testing.T) {
	br := CamTransform(1920, 1080, "br")
	if br.X <= 960 || br.Y <= 540 {
		t.Errorf("br must sit bottom-right: %+v", br)
	}
	tl := CamTransform(1920, 1080, "tl")
	if tl.X != 24 || tl.Y != 24 {
		t.Errorf("tl = %+v", tl)
	}
	big := CamTransform(1920, 1080, "big")
	// runtime float math, not exact constants: compare with tolerance
	if diff := big.BoundsW - 1728; diff < -0.01 || diff > 0.01 {
		t.Errorf("big bounds = %+v", big)
	}
	if diff := big.X - 96; diff < -0.01 || diff > 0.01 {
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
