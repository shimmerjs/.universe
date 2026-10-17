package renocontrol

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// fxFake is a scene fake with the cam already staged: an item, a pose,
// and a canvas frame rate.
func fxFake() *fakeObs {
	f := sceneFake()
	f.canvasFPS = 60
	f.itemIDs = map[string]int{renocore.SceneName + "/" + renocore.CamInput: 4}
	f.tfs = map[int]renocore.Transform{4: {X: 24, Y: 24, BoundsW: 384, BoundsH: 216}}
	return f
}

// fxCtl records the notes the verb sends back.
func fxCtl(notes *[][]string) CtlCall {
	return func(args []string) (json.RawMessage, error) {
		*notes = append(*notes, args)
		return nil, nil
	}
}

// A sustained effect fired as a script step runs no span of its own: the
// bus that scheduled it adopts the level off the note and drives it. Doing
// both starts the effect twice with a driver spawn's stillness in the seam.
func TestScriptedSustainLeavesItToTheDriver(t *testing.T) {
	v, _ := renoTestVerbs(t)
	obs := fxFake()
	var notes [][]string
	fx, err := renocore.ParseCamFX([]string{"jitter", "8"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.CamFX(fx, obsDial(obs), fxCtl(&notes), RenoCorr{Run: "r1", Step: 2, Act: 0}); err != nil {
		t.Fatal(err)
	}
	if len(obs.batches) != 0 || len(obs.ops) != 0 {
		t.Errorf("the verb touched OBS: batches=%v ops=%v", obs.batches, obs.ops)
	}
	if len(notes) != 1 || notes[0][1] != "ok" || notes[0][2] != "jitter 8" {
		t.Fatalf("notes = %v, want one ok the supervisor can adopt", notes)
	}
}

// Off is not driven: level 0 stops a driver, and the work of putting the
// filter back has to happen here.
func TestScriptedSustainOffStillRuns(t *testing.T) {
	v, _ := renoTestVerbs(t)
	obs := fxFake()
	var notes [][]string
	fx, err := renocore.ParseCamFX([]string{"lava", "0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.CamFX(fx, obsDial(obs), fxCtl(&notes), RenoCorr{Run: "r1", Step: 2, Act: 0}); err != nil {
		t.Fatal(err)
	}
	if len(obs.ops) == 0 {
		t.Error("switching the lamp off is real work and must reach OBS")
	}
}

// A hand-run one is still a visible span: no run correlation means no bus
// scheduled it, so there is nothing to hand the effect off to.
func TestHandRunSustainStillRunsItsSpan(t *testing.T) {
	v, _ := renoTestVerbs(t)
	obs := fxFake()
	var notes [][]string
	fx, err := renocore.ParseCamFX([]string{"jitter", "8"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.CamFX(fx, obsDial(obs), fxCtl(&notes), RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatal(err)
	}
	if len(obs.batches) != 1 {
		t.Fatalf("%d batches, want the one-shot span", len(obs.batches))
	}
}

func TestCamFXVerbShipsOneBatch(t *testing.T) {
	v, _ := renoTestVerbs(t)
	obs := fxFake()
	var notes [][]string
	fx, err := renocore.ParseCamFX([]string{"move", "big", "200ms", "linear"})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.CamFX(fx, obsDial(obs), fxCtl(&notes), RenoCorr{Run: "r1", Step: 2, Act: 0}); err != nil {
		t.Fatal(err)
	}
	if len(obs.batches) != 1 {
		t.Fatalf("%d batches", len(obs.batches))
	}
	// the whole path is ONE batch, not a request per frame over the wire
	if got := len(obs.batches[0]); got < 20 {
		t.Errorf("batch carries %d requests", got)
	}
	if obs.batchBudgets[0] <= fx.Dur {
		t.Errorf("budget %s must exceed the move itself", obs.batchBudgets[0])
	}
	// a move reads the current pose, and never enumerates devices
	if !hasOp(obs, "get-transform:4") {
		t.Errorf("ops = %v", obs.ops)
	}
	if hasOp(obs, "kinds") || hasOp(obs, "monitors") {
		t.Errorf("an fx one-shot must not probe the environment: %v", obs.ops)
	}
	if len(notes) != 1 || notes[0][0] != "fx-note" || notes[0][1] != "ok" {
		t.Errorf("notes = %v", notes)
	}
}

// An fx never conjures the cam item: a scene without one fails with the
// fix in the message, and the failure notes back for the pad readout.
func TestCamFXWithoutTheCamItem(t *testing.T) {
	v, _ := renoTestVerbs(t)
	obs := sceneFake()
	obs.canvasFPS = 60
	var notes [][]string
	fx, _ := renocore.ParseCamFX([]string{"jump", "br"})
	err := v.CamFX(fx, obsDial(obs), fxCtl(&notes), RenoCorr{Step: -1, Act: -1})
	if err == nil || !strings.Contains(err.Error(), "webcam preset") {
		t.Fatalf("err = %v", err)
	}
	if len(obs.batches) != 0 {
		t.Errorf("nothing may ship: %v", obs.batches)
	}
	if len(notes) != 1 || notes[0][1] != "error" {
		t.Errorf("notes = %v", notes)
	}
}

// Turning off an effect that was never on sends nothing at all -- and
// still reports ok, because the camera is already how it was asked to be.
func TestCamFXEmptyPlanSkipsTheBatch(t *testing.T) {
	v, _ := renoTestVerbs(t)
	obs := fxFake()
	var notes [][]string
	fx, _ := renocore.ParseCamFX([]string{"pixelate", "off"})
	if err := v.CamFX(fx, obsDial(obs), fxCtl(&notes), RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatal(err)
	}
	if len(obs.batches) != 0 {
		t.Errorf("batches = %v", obs.batches)
	}
	if notes[0][1] != "ok" || !strings.Contains(notes[0][2], "no filter") {
		t.Errorf("notes = %v", notes)
	}
}

// The filter capability probe is the only guard in front of a plugin-shaped
// effect, so it runs before the batch and refuses by name.
func TestCamFXPixelateProbes(t *testing.T) {
	v, _ := renoTestVerbs(t)
	obs := fxFake()
	obs.filterKinds = []string{"color_filter"}
	var notes [][]string
	fx, _ := renocore.ParseCamFX([]string{"pixelate", "on"})
	err := v.CamFX(fx, obsDial(obs), fxCtl(&notes), RenoCorr{Step: -1, Act: -1})
	if err == nil || !strings.Contains(err.Error(), renocore.FXFilterKind) {
		t.Fatalf("err = %v", err)
	}

	obs = fxFake()
	obs.filterKinds = []string{renocore.FXFilterKind}
	if err := v.CamFX(fx, obsDial(obs), fxCtl(&notes), RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatal(err)
	}
	if len(obs.batches) != 1 || obs.batches[0][0].Type != "CreateSourceFilter" {
		t.Fatalf("batch = %+v", obs.batches)
	}
}

// A dead OBS fails the verb: unlike a config knob there is no desired
// state to hold, so a silent success would be a lie about the scene.
func TestCamFXDialFailure(t *testing.T) {
	v, _ := renoTestVerbs(t)
	var notes [][]string
	fx, _ := renocore.ParseCamFX([]string{"hide"})
	dial := func() (ObsTransport, error) { return nil, errors.New("connection refused") }
	if err := v.CamFX(fx, dial, fxCtl(&notes), RenoCorr{Step: -1, Act: -1}); err == nil {
		t.Fatal("dial failure must fail the verb")
	}
	if len(notes) != 1 || notes[0][1] != "error" {
		t.Errorf("notes = %v", notes)
	}
}

// obsDial hands the verb a fixed fake.
func obsDial(f *fakeObs) func() (ObsTransport, error) {
	return func() (ObsTransport, error) { return f, nil }
}

func hasOp(f *fakeObs, want string) bool {
	for _, op := range f.ops {
		if op == want {
			return true
		}
	}
	return false
}
