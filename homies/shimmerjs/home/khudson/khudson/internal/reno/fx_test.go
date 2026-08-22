package reno

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

func fxTestEnv() FXEnv {
	return FXEnv{
		CanvasW: 1920, CanvasH: 1080, FPS: 60,
		Scene: SceneName, Source: CamInput, ItemID: 7,
		From: Transform{X: 24, Y: 24, BoundsW: 384, BoundsH: 216},
		// deliberately none of the canvas, the bounds, or a multiple of
		// either: pixelate must scale back to the SOURCE
		SourceW: 1280, SourceH: 720,
	}
}

func TestParseCamFXRoundTrip(t *testing.T) {
	for _, args := range [][]string{
		{"move", "big", "600ms", "bounce"},
		{"jump", "br"},
		{"show"},
		{"hide"},
		{"pixelate", "on"},
		{"pixelate", "0"},
		{"pixelate", "5"},
		{"purple", "off"},
		{"purple", "2"},
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
		{[]string{"pixelate", "maybe"}, "not a number"},
		{[]string{"pixelate", "on", "2"}, "wants a level"},
		{[]string{"pixelate", "9"}, "wants 0..5"},
		{[]string{"pixelate", "-1"}, "wants 0..5"},
		{[]string{"purple"}, "wants a level"},
		{[]string{"purple", "lots"}, "not a number"},
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

// Pixelate is a TWO-stage chain on the stock scaling filter: down to the
// level's grid, then point-sampled back up. The upscale is what makes it
// visible at all -- a lone downscale renders sharp, because OBS allows the
// scale filter direct rendering straight into the destination rect.
// Created once and updated after: a second CreateSourceFilter would fail
// the batch on the duplicate.
func TestPixelatePlan(t *testing.T) {
	env := fxTestEnv()
	env.FilterKinds = []string{"color_filter", FXFilterKind}
	fx, _ := ParseCamFX([]string{"pixelate", "3"})

	plan, err := BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	// two creates, two enables
	if len(plan.Requests) != 4 {
		t.Fatalf("first pixelate = %+v", plan.Requests)
	}
	for i, want := range []string{FXPixelateDownName, FXPixelateUpName} {
		if plan.Requests[i].Type != "CreateSourceFilter" ||
			plan.Requests[i].Data["filterName"] != want {
			t.Fatalf("stage %d = %+v", i, plan.Requests[i])
		}
	}
	down := plan.Requests[0].Data["filterSettings"].(map[string]any)
	// level 3 is 64 wide; against the 1280x720 source that is 36 tall
	if down["resolution"] != "64x36" || down["sampling"] != "point" {
		t.Errorf("down = %v", down)
	}
	// and back up to the SOURCE's own size. Any other size and the item is
	// left holding a scale derived for a source it no longer has -- which
	// bites hardest when the level drops to 0 and the filters switch off.
	up := plan.Requests[1].Data["filterSettings"].(map[string]any)
	if up["resolution"] != "1280x720" || up["sampling"] != "point" {
		t.Errorf("up = %v", up)
	}

	env.Filters = []string{FXPixelateDownName, FXPixelateUpName}
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
	if len(plan.Requests) != 2 || plan.Requests[0].Data["filterEnabled"] != false {
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

// A chain whose stages sit in the wrong order pins itself back. Upstream
// of the downscale the up stage does nothing, and nothing about the frame
// says which way round they are.
func TestPixelateRepairsStageOrder(t *testing.T) {
	env := fxTestEnv()
	env.FilterKinds = []string{FXFilterKind}
	env.Filters = []string{FXPurpleFilterName, FXPixelateUpName, FXPixelateDownName}
	fx, _ := ParseCamFX([]string{"pixelate", "2"})

	plan, err := BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	var idx []any
	for _, r := range plan.Requests {
		if r.Type == "SetSourceFilterIndex" {
			idx = append(idx, r.Data["filterName"], r.Data["filterIndex"])
		}
	}
	// the pair sorts to the chain's tail, down before up, purple untouched
	want := []any{FXPixelateDownName, 1, FXPixelateUpName, 2}
	if !slices.Equal(idx, want) {
		t.Errorf("order repair = %v, want %v", idx, want)
	}

	// already in order: no index writes at all
	env.Filters = []string{FXPixelateDownName, FXPixelateUpName}
	plan, err = BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range plan.Requests {
		if r.Type == "SetSourceFilterIndex" {
			t.Errorf("an ordered chain was reordered: %+v", r)
		}
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
	// and a cam with no source size refuses too: there is no size to scale
	// back up to, and inventing one leaves the item's scale wrong
	env.FilterKinds = []string{FXFilterKind}
	env.SourceW, env.SourceH = 0, 0
	if _, err := BuildCamFXPlan(fx, env); err == nil || !strings.Contains(err.Error(), "no source size") {
		t.Errorf("sizeless cam err = %v", err)
	}
	// turning it OFF still works: nothing needs a size to be disabled
	off, _ := ParseCamFX([]string{"pixelate", "0"})
	env.Filters = []string{FXPixelateDownName, FXPixelateUpName}
	if _, err := BuildCamFXPlan(off, env); err != nil {
		t.Errorf("pixelate off on a sizeless cam: %v", err)
	}
}

// Purple drank is one colour multiply on the same create-once-update-after
// path as pixelate, so the two level effects cannot drift apart.
func TestPurplePlan(t *testing.T) {
	env := fxTestEnv()
	env.FilterKinds = []string{FXFilterKind, FXPurpleFilterKind}
	fx, _ := ParseCamFX([]string{"purple", "5"})

	plan, err := BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 2 || plan.Requests[0].Type != "CreateSourceFilter" {
		t.Fatalf("first purple = %+v", plan.Requests)
	}
	if got := plan.Requests[0].Data["filterName"]; got != FXPurpleFilterName {
		t.Errorf("purple must own its own filter, got %v", got)
	}
	settings := plan.Requests[0].Data["filterSettings"].(map[string]any)
	// level 5 is the full violet: R=170 G=0 B=255 packed 0xAABBGGRR
	if settings["color_multiply"] != 0xFF00AA {
		t.Errorf("full drank = %#v", settings)
	}

	env.Filters = []string{FXPurpleFilterName}
	plan, err = BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Requests[0].Type != "SetSourceFilterSettings" {
		t.Errorf("second purple = %+v", plan.Requests[0])
	}

	off, _ := ParseCamFX([]string{"purple", "0"})
	plan, err = BuildCamFXPlan(off, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 1 || plan.Requests[0].Data["filterEnabled"] != false ||
		plan.Requests[0].Data["filterName"] != FXPurpleFilterName {
		t.Errorf("purple off = %+v", plan.Requests)
	}
	// and a purple off leaves the PIXELATE filter alone: two effects, two
	// filters, and neither disables the other's
	env.Filters = []string{FXPixelateDownName, FXPixelateUpName}
	plan, err = BuildCamFXPlan(off, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 0 {
		t.Errorf("purple off touched the pixelate filter: %+v", plan.Requests)
	}
}

// A jitter shakes around the pose it starts in and LANDS on it. Anywhere
// else and repeated taps would walk the camera off its placement.
func TestJitterPlan(t *testing.T) {
	env := fxTestEnv()
	fx, err := ParseCamFX([]string{"jitter", "4"})
	if err != nil {
		t.Fatal(err)
	}
	if fx.Dur != FXJitterDur {
		t.Errorf("jitter dur = %s", fx.Dur)
	}
	plan, err := BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Requests[0].Type != "SetSceneItemEnabled" {
		t.Fatalf("a shake implies visible: %+v", plan.Requests[0])
	}
	var poses []map[string]any
	for _, r := range plan.Requests {
		if r.Type == "SetSceneItemTransform" {
			poses = append(poses, r.Data["sceneItemTransform"].(map[string]any))
		}
	}
	if len(poses) < 8 {
		t.Fatalf("samples = %d", len(poses))
	}
	last := poses[len(poses)-1]
	if last["positionX"] != env.From.X || last["positionY"] != env.From.Y {
		t.Errorf("landed at %v,%v, started at %v,%v",
			last["positionX"], last["positionY"], env.From.X, env.From.Y)
	}
	// it actually moves, and never off the canvas
	var moved bool
	for _, pose := range poses {
		x, y := pose["positionX"].(float64), pose["positionY"].(float64)
		if x != env.From.X || y != env.From.Y {
			moved = true
		}
		if x < 0 || y < 0 || x+env.From.BoundsW > env.CanvasW || y+env.From.BoundsH > env.CanvasH {
			t.Fatalf("shook off the canvas: %v,%v", x, y)
		}
	}
	if !moved {
		t.Error("a jitter that never moves is not a jitter")
	}

	// a bigger level throws further
	small, _ := ParseCamFX([]string{"jitter", "1"})
	if peak(t, small, env) >= peak(t, fx, env) {
		t.Error("level 1 must throw less than level 4")
	}

	// level 0 has nothing to shake, and says so
	off, _ := ParseCamFX([]string{"jitter", "0"})
	plan, err = BuildCamFXPlan(off, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 0 || !strings.Contains(plan.Summary, "nothing to shake") {
		t.Errorf("jitter off = %+v %q", plan.Requests, plan.Summary)
	}

	// and an unplaced cam refuses rather than shaking a box it cannot see
	env.From = Transform{}
	if _, err := BuildCamFXPlan(fx, env); err == nil || !strings.Contains(err.Error(), "bounds box") {
		t.Errorf("unplaced cam err = %v", err)
	}
}

// peak is the furthest a plan's samples get from the starting pose.
func peak(t *testing.T, fx CamFX, env FXEnv) float64 {
	t.Helper()
	plan, err := BuildCamFXPlan(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	var out float64
	for _, r := range plan.Requests {
		if r.Type != "SetSceneItemTransform" {
			continue
		}
		pose := r.Data["sceneItemTransform"].(map[string]any)
		out = max(out, math.Abs(pose["positionX"].(float64)-env.From.X))
	}
	return out
}

// Both level scales run monotonically from level 1 to the top: a step that
// went the wrong way, or nowhere, is a remote button that does nothing.
func TestLevelScalesAreMonotonic(t *testing.T) {
	for level := 2; level <= FXMaxLevel; level++ {
		if FXPixelateBlocks(level) >= FXPixelateBlocks(level-1) {
			t.Errorf("pixelate level %d: %d blocks is not coarser than %d",
				level, FXPixelateBlocks(level), FXPixelateBlocks(level-1))
		}
		// each step drops more green: the tint walks white -> violet
		if green(FXPurpleTint(level)) >= green(FXPurpleTint(level-1)) {
			t.Errorf("purple level %d: tint %#x is not deeper than %#x",
				level, FXPurpleTint(level), FXPurpleTint(level-1))
		}
	}
	// the pixelate grid stays a usable image size at both ends
	if FXPixelateBlocks(1) != 256 || FXPixelateBlocks(FXMaxLevel) != 16 {
		t.Errorf("pixelate range = %d..%d", FXPixelateBlocks(1), FXPixelateBlocks(FXMaxLevel))
	}
	// the LOW byte is red (live OBS 32.2), so a violet keeps red above green
	top := FXPurpleTint(FXMaxLevel)
	if top&0xFF != 170 || green(top) != 0 || top>>16&0xFF != 255 {
		t.Errorf("violet packed wrong: %#x", top)
	}
}

func green(tint int) int { return tint >> 8 & 0xFF }

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
