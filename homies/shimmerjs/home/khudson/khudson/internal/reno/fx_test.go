package reno

import (
	"strings"
	"testing"
	"time"
)

func fxTestEnv() FXEnv {
	return FXEnv{
		CanvasW: 1920, CanvasH: 1080, FPS: 60,
		Scene: SceneName, Source: CamInput, ItemID: 7,
		From: Transform{X: 24, Y: 24, BoundsW: 384, BoundsH: 216},
	}
}

func TestParseCamFXRoundTrip(t *testing.T) {
	for _, args := range [][]string{
		{"move", "big", "600ms", "bounce"},
		{"jump", "br"},
		{"show"},
		{"hide"},
		{"pixelate", "on", "64"},
		{"pixelate", "off", "48"},
	} {
		fx, err := ParseCamFX(args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		again, err := ParseCamFX(fx.Args())
		if err != nil {
			t.Fatalf("%v round trip: %v", args, err)
		}
		if again != fx {
			t.Errorf("%v round trip: %+v != %+v", args, again, fx)
		}
	}
}

func TestParseCamFXDefaultsAndErrors(t *testing.T) {
	fx, err := ParseCamFX([]string{"move", "tl"})
	if err != nil {
		t.Fatal(err)
	}
	if fx.Dur != FXDefaultDur || fx.Ease != FXDefaultEase {
		t.Errorf("defaults = %+v", fx)
	}
	// the two optional words are order-free: a duration parses as one,
	// anything else is the ease
	fx, err = ParseCamFX([]string{"move", "tl", "linear", "2s"})
	if err != nil {
		t.Fatal(err)
	}
	if fx.Dur != 2*time.Second || fx.Ease != "linear" {
		t.Errorf("swapped args = %+v", fx)
	}

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{}, "wants one of"},
		{[]string{"wiggle"}, "wants one of"},
		{[]string{"move"}, "wants a placement preset"},
		{[]string{"move", "off"}, "wants one of"},
		{[]string{"move", "big", "sproing"}, "ease"},
		{[]string{"move", "big", "30s"}, "duration"},
		{[]string{"jump", "nowhere"}, "cam target"},
		{[]string{"hide", "now"}, "takes no arguments"},
		{[]string{"pixelate", "maybe"}, "on or off"},
		{[]string{"pixelate", "on", "2"}, "block width"},
		{[]string{"pixelate", "on", "lots"}, "not a number"},
	} {
		_, err := ParseCamFX(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q", tc.args, err, tc.want)
		}
	}
}

// A move is one enable plus alternating transform/sleep samples, ending
// exactly on the target placement -- an easing that stops short would
// leave the camera a few pixels off its preset forever.
func TestBuildMovePlan(t *testing.T) {
	fx, err := ParseCamFX([]string{"move", "big", "500ms", "linear"})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildCamFXPlan(fx, fxTestEnv())
	if err != nil {
		t.Fatal(err)
	}
	// 500ms at 60fps = 30 frames, under the sample cap: one sample per
	// frame with a single-frame sleep between
	const samples = 30
	if got := len(plan.Requests); got != 1+samples+(samples-1) {
		t.Fatalf("%d requests for %d samples", got, samples)
	}
	if plan.Requests[0].Type != "SetSceneItemEnabled" || plan.Requests[0].Data["sceneItemEnabled"] != true {
		t.Errorf("move must lead with enable: %+v", plan.Requests[0])
	}
	var sleeps, xforms int
	for _, r := range plan.Requests[1:] {
		switch r.Type {
		case "Sleep":
			sleeps++
			if r.Data["sleepFrames"] != 1 {
				t.Errorf("sleep = %v", r.Data["sleepFrames"])
			}
		case "SetSceneItemTransform":
			xforms++
			if r.Data["sceneName"] != SceneName || r.Data["sceneItemId"] != 7 {
				t.Errorf("transform target = %+v", r.Data)
			}
		default:
			t.Errorf("unexpected request %q", r.Type)
		}
	}
	if sleeps != samples-1 || xforms != samples {
		t.Errorf("%d sleeps, %d transforms", sleeps, xforms)
	}

	want := CamTransform(1920, 1080, "big")
	last := plan.Requests[len(plan.Requests)-1].Data["sceneItemTransform"].(map[string]any)
	if last["positionX"] != want.X || last["boundsWidth"] != want.BoundsW {
		t.Errorf("last sample = %v, want %+v", last, want)
	}
	first := plan.Requests[1].Data["sceneItemTransform"].(map[string]any)
	if first["positionX"] == want.X {
		t.Errorf("first sample already at the target: %v", first)
	}
}

// A long move strides instead of multiplying requests: the sample cap
// bounds one batch, and the sleeps stretch to keep the wall time.
func TestMoveStridesPastTheSampleCap(t *testing.T) {
	fx, _ := ParseCamFX([]string{"move", "tl", "5s", "smooth"})
	plan, err := BuildCamFXPlan(fx, fxTestEnv())
	if err != nil {
		t.Fatal(err)
	}
	var sleepFrames, samples int
	for _, r := range plan.Requests {
		switch r.Type {
		case "Sleep":
			sleepFrames += r.Data["sleepFrames"].(int)
		case "SetSceneItemTransform":
			samples++
		}
	}
	if samples != FXMaxSamples {
		t.Fatalf("samples = %d, want the cap %d", samples, FXMaxSamples)
	}
	// 300 frames over 90 samples: a 3-frame stride, so the path still
	// spans roughly the asked-for 5s
	if sleepFrames < 240 || sleepFrames > 300 {
		t.Errorf("total sleep = %d frames, want ~300", sleepFrames)
	}
}

// A cam with no bounds box has no pose to interpolate out of; the plan
// says what to do about it instead of animating from the origin.
func TestMoveWithoutAPoseRefuses(t *testing.T) {
	env := fxTestEnv()
	env.From = Transform{}
	fx, _ := ParseCamFX([]string{"move", "big"})
	_, err := BuildCamFXPlan(fx, env)
	if err == nil || !strings.Contains(err.Error(), "webcam preset") {
		t.Fatalf("err = %v", err)
	}
}

func TestJumpAndVisibilityPlans(t *testing.T) {
	env := fxTestEnv()
	fx, _ := ParseCamFX([]string{"jump", "tr"})
	plan, err := BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 2 || plan.Requests[1].Type != "SetSceneItemTransform" {
		t.Fatalf("jump plan = %+v", plan.Requests)
	}
	want := CamTransform(1920, 1080, "tr")
	got := plan.Requests[1].Data["sceneItemTransform"].(map[string]any)
	if got["positionX"] != want.X || got["positionY"] != want.Y {
		t.Errorf("jump landed at %v, want %+v", got, want)
	}

	for kind, wantEnabled := range map[string]bool{FXShow: true, FXHide: false} {
		fx, _ := ParseCamFX([]string{kind})
		plan, err := BuildCamFXPlan(fx, env)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Requests) != 1 || plan.Requests[0].Data["sceneItemEnabled"] != wantEnabled {
			t.Errorf("%s plan = %+v", kind, plan.Requests)
		}
	}
}

// Pixelate rides the stock scaling filter, created once and updated
// after: a second CreateSourceFilter would fail the batch on the
// duplicate.
func TestPixelatePlan(t *testing.T) {
	env := fxTestEnv()
	env.FilterKinds = []string{"color_filter", FXFilterKind}
	fx, _ := ParseCamFX([]string{"pixelate", "on", "64"})

	plan, err := BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 2 || plan.Requests[0].Type != "CreateSourceFilter" {
		t.Fatalf("first pixelate = %+v", plan.Requests)
	}
	settings := plan.Requests[0].Data["filterSettings"].(map[string]any)
	// 64 wide against the 384x216 pip box: 36 tall, point-sampled
	if settings["resolution"] != "64x36" || settings["sampling"] != "point" {
		t.Errorf("settings = %v", settings)
	}

	env.Filters = []string{FXFilterName}
	plan, err = BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Requests[0].Type != "SetSourceFilterSettings" {
		t.Errorf("second pixelate = %+v", plan.Requests[0])
	}

	off, _ := ParseCamFX([]string{"pixelate", "off"})
	plan, err = BuildCamFXPlan(off, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 1 || plan.Requests[0].Data["filterEnabled"] != false {
		t.Errorf("pixelate off = %+v", plan.Requests)
	}
	// nothing attached: nothing to send, and the summary says why
	env.Filters = nil
	plan, err = BuildCamFXPlan(off, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 0 || !strings.Contains(plan.Summary, "no filter") {
		t.Errorf("pixelate off with no filter = %+v %q", plan.Requests, plan.Summary)
	}
}

// The plugin-free subset is the only one that ships: a pixelate against
// an OBS without the filter kind refuses by name rather than erroring
// mid-batch on an unknown request.
func TestPixelateDegradesLoudly(t *testing.T) {
	env := fxTestEnv()
	fx, _ := ParseCamFX([]string{"pixelate", "on"})
	if _, err := BuildCamFXPlan(fx, env); err == nil || !strings.Contains(err.Error(), "no filter kinds") {
		t.Errorf("unprobed err = %v", err)
	}
	env.FilterKinds = []string{"color_filter"}
	_, err := BuildCamFXPlan(fx, env)
	if err == nil || !strings.Contains(err.Error(), FXFilterKind) {
		t.Errorf("missing kind err = %v", err)
	}
}

func TestEaseCurves(t *testing.T) {
	for _, name := range FXEases {
		if got := Ease(name, 0); got != 0 {
			t.Errorf("%s(0) = %v", name, got)
		}
		if got := Ease(name, 1); got != 1 {
			t.Errorf("%s(1) = %v", name, got)
		}
		// no curve may leave the unit interval: a sample outside it would
		// throw the cam off the canvas mid-path
		for i := 0; i <= 100; i++ {
			if v := Ease(name, float64(i)/100); v < 0 || v > 1 {
				t.Fatalf("%s(%v) = %v", name, float64(i)/100, v)
			}
		}
	}
	if mid := Ease("smooth", 0.5); mid != 0.5 {
		t.Errorf("smooth(0.5) = %v", mid)
	}
	if Ease("bounce", 0.5) <= Ease("linear", 0.5) {
		t.Error("bounce must run ahead of linear at the midpoint")
	}
}

// A canvas with no reported frame rate still animates: sleeps are
// denominated in frames, so the fallback only mis-paces.
func TestMoveWithoutFPS(t *testing.T) {
	env := fxTestEnv()
	env.FPS = 0
	fx, _ := ParseCamFX([]string{"move", "bl", "1s", "linear"})
	plan, err := BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	var samples int
	for _, r := range plan.Requests {
		if r.Type == "SetSceneItemTransform" {
			samples++
		}
	}
	if samples != int(FXFallbackFPS) {
		t.Errorf("samples = %d for 1s at the %v fallback", samples, FXFallbackFPS)
	}
}
