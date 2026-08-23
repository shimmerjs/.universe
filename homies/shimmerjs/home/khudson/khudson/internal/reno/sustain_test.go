package reno

import (
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// poseXs pulls the x of every transform a plan writes.
func poseXs(t *testing.T, p *FXPlan) []float64 {
	t.Helper()
	var out []float64
	for _, r := range p.Requests {
		if r.Type != "SetSceneItemTransform" {
			continue
		}
		out = append(out, r.Data["sceneItemTransform"].(map[string]any)["positionX"].(float64))
	}
	return out
}

// hueShifts pulls the hue of every filter settings write a plan makes.
func hueShifts(t *testing.T, p *FXPlan) []float64 {
	t.Helper()
	var out []float64
	for _, r := range p.Requests {
		if r.Type != "SetSourceFilterSettings" {
			continue
		}
		out = append(out, r.Data["filterSettings"].(map[string]any)["hue_shift"].(float64))
	}
	return out
}

// The seam between two chunks is the whole reason the phase is returned
// rather than recomputed. Chunk two must pick the oscillation up exactly
// where chunk one put it down -- if it restarted from zero, the effect
// would visibly hitch four times a second.
func TestSustainChunksTileWithoutASeam(t *testing.T) {
	env := fxTestEnv()
	fx, err := ParseCamFX([]string{"jitter", "8"})
	if err != nil {
		t.Fatal(err)
	}
	first, phase, err := SustainChunk(fx, env, 0, FXSustainChunk)
	if err != nil {
		t.Fatal(err)
	}
	second, next, err := SustainChunk(fx, env, phase, FXSustainChunk)
	if err != nil {
		t.Fatal(err)
	}
	if want := FXJitterHz * FXSustainChunk.Seconds(); math.Abs(phase-want) > 1e-9 {
		t.Errorf("a chunk advanced %v turns, want %v", phase, want)
	}
	if math.Abs(next-2*phase) > 1e-9 {
		t.Errorf("two chunks advanced %v turns, want %v", next, 2*phase)
	}

	// the pose one sample past the end of chunk one IS the first pose of
	// chunk two: continuous, not restarted
	a, b := poseXs(t, first), poseXs(t, second)
	if len(a) < 4 || len(b) < 4 {
		t.Fatalf("chunk samples = %d/%d", len(a), len(b))
	}
	step := (a[len(a)-1] - a[len(a)-2])
	if got := b[0] - a[len(a)-1]; math.Abs(got-step) > 1 {
		t.Errorf("seam step %v, want about %v (the chunk restarted its phase)", got, step)
	}
	// a chunk is deliberately NOT whole cycles, so it must not land home
	if a[len(a)-1] == env.From.X {
		t.Error("a chunk that ends on the base pose is the old whole-cycle shape")
	}
}

// A chunk is the off latency: the driver only gets to notice a level
// dropped between two of them. This is the number that used to be three
// seconds, so it is worth pinning.
func TestSustainChunkIsShortEnoughToStopQuickly(t *testing.T) {
	if FXSustainChunk > 500*time.Millisecond {
		t.Errorf("chunk = %s: a tap on off waits out one of these", FXSustainChunk)
	}
	// and long enough to be worth a batch: a chunk under a couple of frames
	// is all round trip and no motion
	if FXSustainChunk < 100*time.Millisecond {
		t.Errorf("chunk = %s: mostly round trip", FXSustainChunk)
	}
}

// Home is the state a SHAKE found, and it is the only thing that ever puts
// the camera back: the pose read before the first chunk. The lamp has no
// home between drivers, for the reason below.
func TestSustainHomeRestoresWhatWasThere(t *testing.T) {
	env := fxTestEnv()
	jitter, _ := ParseCamFX([]string{"jitter", "6"})
	home, err := SustainHome(jitter, env)
	if err != nil {
		t.Fatal(err)
	}
	if got := poseXs(t, home); len(got) != 1 || got[0] != env.From.X {
		t.Errorf("jitter home = %v, want the base pose %v", got, env.From.X)
	}

	// The lamp's home is deliberately EMPTY. A level change stops one
	// driver and starts another, so a home that disabled the filter would
	// flash it off and back on under every tap of the pair; level 0 is what
	// darkens it, and that is the only moment it should.
	lava, _ := ParseCamFX([]string{"lava", "6"})
	home, err = SustainHome(lava, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(home.Requests) != 0 {
		t.Errorf("lava home = %+v, want nothing between two drivers", home.Requests)
	}

	// an unplaced cam has no pose to go home to, and saying so beats
	// writing a zero transform over whatever is there
	env.From = Transform{}
	if _, err := SustainHome(jitter, env); err == nil {
		t.Error("home over an unplaced cam must refuse")
	}
}

// The lamp rides its own filter of the same stock kind purple uses, so
// setup must create that filter and nothing else's.
func TestLavaSetupCreatesItsOwnFilter(t *testing.T) {
	env := fxTestEnv()
	env.FilterKinds = []string{FXFilterKind, FXPurpleFilterKind}
	fx, _ := ParseCamFX([]string{"lava", "10"})
	setup, err := SustainSetup(fx, env)
	if err != nil {
		t.Fatal(err)
	}
	var created, enabled bool
	for _, r := range setup.Requests {
		switch r.Type {
		case "CreateSourceFilter":
			if r.Data["filterName"] != FXLavaFilterName {
				t.Errorf("setup created %v, not the lamp's own filter", r.Data["filterName"])
			}
			if r.Data["filterKind"] != FXPurpleFilterKind {
				t.Errorf("setup used filter kind %v", r.Data["filterKind"])
			}
			created = true
		case "SetSourceFilterEnabled":
			enabled = r.Data["filterEnabled"] == true
		}
	}
	if !created || !enabled {
		t.Errorf("setup = %+v, want the filter created and enabled", setup.Requests)
	}
	// the filter comes up on the camera's own hue, so switching it on is
	// not itself a visible jump
	for _, r := range setup.Requests {
		if r.Type != "CreateSourceFilter" {
			continue
		}
		if hue := r.Data["filterSettings"].(map[string]any)["hue_shift"].(float64); hue != 0 {
			t.Errorf("the lamp came up already shifted %v degrees", hue)
		}
	}

	// and the capability probe still stands in front of it: no advertised
	// kinds means the effect refuses by name rather than failing mid-batch
	env.FilterKinds = nil
	if _, err := SustainSetup(fx, env); err == nil {
		t.Error("an obs advertising no filter kinds must refuse the lamp")
	}
}

// The lamp oscillates rather than running all the way round the wheel: a
// full rotation spends half its time with skin the colour of a bruise, and
// there is no graceful place to stop from the far side.
func TestLavaSwingsBothWaysAndScalesWithLevel(t *testing.T) {
	env := fxTestEnv()
	swing := func(level string) (lo, hi float64) {
		fx, err := ParseCamFX([]string{"lava", level})
		if err != nil {
			t.Fatal(err)
		}
		lo, hi = math.Inf(1), math.Inf(-1)
		phase := 0.0
		// a whole oscillation takes several chunks at this frequency
		for range int(math.Ceil(1 / (FXLavaHz * FXSustainChunk.Seconds()))) {
			var p *FXPlan
			p, phase, err = SustainChunk(fx, env, phase, FXSustainChunk)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range hueShifts(t, p) {
				lo, hi = math.Min(lo, h), math.Max(hi, h)
			}
		}
		return lo, hi
	}
	lo, hi := swing(strconv.Itoa(FXMaxLevel))
	if lo > -FXLavaMaxShift*0.9 || hi < FXLavaMaxShift*0.9 {
		t.Errorf("top level swings %v..%v, want about +/-%v", lo, hi, FXLavaMaxShift)
	}
	if math.Abs(lo+hi) > 1 {
		t.Errorf("the swing is lopsided: %v..%v", lo, hi)
	}
	smallLo, smallHi := swing("2")
	if smallHi >= hi || smallLo <= lo {
		t.Errorf("level 2 swings %v..%v, must be inside the top's %v..%v", smallLo, smallHi, lo, hi)
	}
}

// Only the effects that MOVE get a driver. A pixelate is a filter setting
// that stays where it is put, and spending a process on it would be a
// process re-writing the same value four times a second forever.
func TestOnlyMovingEffectsSustain(t *testing.T) {
	for _, kind := range []string{FXJitter, FXLava} {
		if !IsSustainFX(kind) {
			t.Errorf("%s moves, so it has to be driven", kind)
		}
		if !IsLevelFX(kind) {
			t.Errorf("%s is a magnitude, so it belongs on a stepper", kind)
		}
	}
	for _, kind := range []string{FXPixelate, FXPurple, FXMove, FXJump, FXShow, FXHide} {
		if IsSustainFX(kind) {
			t.Errorf("%s does not need driving", kind)
		}
	}
	// the lamp lands in a filter, so it needs the capability probe; the
	// shake is pure motion and probing filters for it is a wasted round trip
	if !IsFilterFX(FXLava) {
		t.Error("the lamp drives a filter, so it needs the probe")
	}
	if IsFilterFX(FXJitter) {
		t.Error("a shake touches no filter")
	}
	// the lamp's own filter, never purple's: two independently levelled
	// tiles sharing one filter would each overwrite the other
	if slices.Contains(levelStageNames(FXPurple), FXLavaFilterName) ||
		slices.Contains(levelStageNames(FXLava), FXPurpleFilterName) {
		t.Error("the lamp and purple must not share a filter")
	}
}

// Nobody driving means the span has to be self-contained: setup, something
// visible, and home. This is what a script step or a bare shell call gets.
func TestSustainOneShotEndsWhereItStarted(t *testing.T) {
	env := fxTestEnv()
	env.FilterKinds = []string{FXPurpleFilterKind}
	for _, kind := range SustainFXKinds {
		fx, err := ParseCamFX([]string{kind, "9"})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := BuildCamFXPlan(fx, env)
		if err != nil {
			t.Fatalf("%s one-shot: %v", kind, err)
		}
		if len(plan.Requests) < 8 {
			t.Errorf("%s one-shot = %d requests, want a span you can see", kind, len(plan.Requests))
		}
		// Nobody drives after a one-shot, so it owes the ending outright: a
		// shake back on its pose, a lamp switched off.
		last := plan.Requests[len(plan.Requests)-1]
		want := "SetSceneItemTransform"
		if kind == FXLava {
			want = "SetSourceFilterEnabled"
			if last.Data["filterEnabled"] != false {
				t.Errorf("the lamp was left on with nobody driving it: %+v", last)
			}
		}
		if last.Type != want {
			t.Errorf("%s one-shot ended on %s, want %s", kind, last.Type, want)
		}
	}
}

// A lamp turned off is real work -- its filter is state sitting on the
// camera -- where a shake turned off is just its driver leaving.
func TestLavaOffDisablesTheFilter(t *testing.T) {
	env := fxTestEnv()
	env.FilterKinds = []string{FXPurpleFilterKind}
	env.Filters = []string{FXLavaFilterName}
	off, _ := ParseCamFX([]string{"lava", "0"})
	plan, err := BuildCamFXPlan(off, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 1 || plan.Requests[0].Type != "SetSourceFilterEnabled" ||
		plan.Requests[0].Data["filterName"] != FXLavaFilterName {
		t.Errorf("lava off = %+v", plan.Requests)
	}
	// a lamp that was never on has nothing to disable, and says so rather
	// than inventing a filter to switch off
	env.Filters = nil
	plan, err = BuildCamFXPlan(off, env)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Requests) != 0 || !strings.Contains(plan.Summary, "no filter to disable") {
		t.Errorf("lava off with no filter = %+v %q", plan.Requests, plan.Summary)
	}
}
