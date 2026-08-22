package bus

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// sceneFake is a fakeObs wired with a plausible OBS 32 environment.
// Displays never enumerate through OBS (the obs-websocket SEGV);
// fakeCGDisplays overrides the local CG seam.
func sceneFake() *fakeObs {
	return &fakeObs{
		version: "32.2.0",
		canDir:  true,
		kinds:   []string{"screen_capture", "macos-avcapture", "coreaudio_input_capture"},
		monitors: []renocore.Monitor{
			// OBS reports POINT geometry; labels match against it
			{Index: 0, Name: "Built-in Retina Display", Width: 1512, Height: 982},
		},
		canvasW: 1920, canvasH: 1080,
		scenes: []string{"Scene"},
		inputs: map[string]string{"Mic/Aux": "coreaudio_input_capture"},
		props: map[string][]renocore.PropertyItem{
			"reno-mic/device_id": {{Name: "MacBook Pro Microphone", Value: "uid-mbp"}},
			"reno-cam/device":    {{Name: "FaceTime HD Camera", Value: "uid-cam"}},
		},
		// every video source reports a size: the cam needs one too, or the
		// scale it is given is derived from nothing
		sizes: map[string][2]int{"reno-screen-0": {3024, 1964}, "reno-cam": {1920, 1080}},
	}
}

// fakeCGDisplays pins the local CG enumeration: one retina built-in,
// 1512x982 points / 3024x1964 pixels.
func fakeCGDisplays(t *testing.T) {
	t.Helper()
	old := localDisplays
	localDisplays = func() []cgdisplay.Display {
		return []cgdisplay.Display{{
			ID: 1, UUID: "uuid-built-in",
			X: 0, Y: 0, W: 1512, H: 982, PxW: 3024, PxH: 1964, Main: true,
		}}
	}
	t.Cleanup(func() { localDisplays = old })
}

// ProbeEnv ensures the reno scene + skeleton inputs, then enumerates
// through them.
func TestProbeEnv(t *testing.T) {
	fakeCGDisplays(t)
	obs := sceneFake()
	env, err := ProbeEnv(obs)
	if err != nil {
		t.Fatal(err)
	}
	if env.ScreenKind != "screen_capture" || env.CamKind != "macos-avcapture" {
		t.Errorf("kinds = %q/%q", env.ScreenKind, env.CamKind)
	}
	if env.DisplayProp != "display_uuid" || env.DisplayNumeric {
		t.Errorf("display prop = %q numeric=%v", env.DisplayProp, env.DisplayNumeric)
	}
	if !slices.Contains(obs.scenes, "reno") {
		t.Error("reno scene not ensured")
	}
	for _, skel := range []string{"reno-screen-0", "reno-mic", "reno-cam"} {
		if _, ok := obs.inputs[skel]; !ok {
			t.Errorf("skeleton %s not ensured", skel)
		}
	}
	if len(env.Displays) != 1 || env.Displays[0].Value != "uuid-built-in" {
		t.Errorf("displays = %+v", env.Displays)
	}
	if len(env.Mics) != 1 || len(env.Cams) != 1 {
		t.Errorf("mics/cams = %+v / %+v", env.Mics, env.Cams)
	}

	// second probe: skeletons exist, no duplicate creation
	creates := 0
	for _, op := range obs.ops {
		if strings.HasPrefix(op, "create-input:") {
			creates++
		}
	}
	if _, err := ProbeEnv(obs); err != nil {
		t.Fatal(err)
	}
	for _, op := range obs.ops {
		if strings.HasPrefix(op, "create-input:") {
			creates--
		}
	}
	if creates != 0 {
		t.Error("re-probe recreated skeleton inputs")
	}
}

// ApplyRecordConfig drives the full plan: create/settings, item
// enable, transform with crops, mute sweep, stale prune, scene switch.
func TestApplyRecordConfig(t *testing.T) {
	fakeCGDisplays(t)
	obs := sceneFake()
	obs.inputs["reno-screen-1"] = "screen_capture" // stale from a wider config
	cfg := renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "built-in", Region: &renocore.Region{X: 0, Y: 0, W: 1512, H: 982}}},
		Audio:   renocore.AudioNone,
		Webcam:  "br",
	}
	dead, err := ApplyRecordConfig(obs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 0 {
		t.Errorf("streaming sources reported dead: %v", dead)
	}
	if obs.ops[len(obs.ops)-1] != "scene:reno" {
		t.Errorf("last op must switch the scene: %v", obs.ops[len(obs.ops)-3:])
	}
	if got := obs.settings["reno-screen-0"]["display_uuid"]; got != "uuid-built-in" {
		t.Errorf("display setting = %v", got)
	}
	screenItem := obs.itemIDs["reno/reno-screen-0"]
	tf := obs.tfs[screenItem]
	if tf.CropL != 0 || tf.CropR != 3024-1512 || tf.CropB != 1964-982 {
		t.Errorf("screen crops = %+v", tf)
	}
	camItem := obs.itemIDs["reno/reno-cam"]
	if !obs.enabled[camItem] {
		t.Error("webcam item must be enabled")
	}
	if camTf := obs.tfs[camItem]; camTf.X <= 960 || camTf.Y <= 540 {
		t.Errorf("br cam transform = %+v", camTf)
	}
	if !obs.mutes["Mic/Aux"] {
		t.Errorf("audio none must mute foreign inputs: %v", obs.mutes)
	}
	if _, gone := obs.inputs["reno-screen-1"]; gone {
		t.Errorf("stale screen input not pruned: %v", obs.inputs)
	}
}

// A source that never reports a size falls back to the resolved monitor's
// geometry for the crop math -- and is reported DEAD. It is not merely
// unstreamed: a capture still at 0x0 after the settle records black, and an
// apply that returned clean over it is how 52 seconds of black got recorded
// with every readout on the pad saying ok.
func TestApplyRegionMonitorFallbackReportsDeadSource(t *testing.T) {
	fakeCGDisplays(t)
	noSettleDelay(t)
	obs := sceneFake()
	obs.sizes = nil // capture never comes up
	cfg := renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "built-in", Region: &renocore.Region{X: 100, Y: 100, W: 800, H: 600}}},
	}
	dead, err := ApplyRecordConfig(obs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	tf := obs.tfs[obs.itemIDs["reno/reno-screen-0"]]
	if tf.CropL != 100 || tf.CropR != 3024-900 {
		t.Errorf("fallback crops = %+v", tf)
	}
	if !slices.Contains(dead, "reno-screen-0") {
		t.Errorf("a source stuck at 0x0 must be reported dead, got %v", dead)
	}
	// and no scale is invented for it: a scale derived from nothing is the
	// zero that leaves the item rendering at zero size forever
	if tf.ScaleX != 0 || tf.ScaleY != 0 {
		t.Errorf("dead source got a scale: %+v", tf)
	}
}

// A live source gets an EXPLICIT scale. OBS derives a scale-inner scale only
// when the bounds are set, so an item first bounded against a 0x0 source
// keeps scale 0 after frames arrive and renders at zero size -- the scene
// composites to black with every source in it healthy (glass, 2026-08-21).
func TestApplyScalesLiveSourcesExplicitly(t *testing.T) {
	fakeCGDisplays(t)
	noSettleDelay(t)
	obs := sceneFake()
	cfg := renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "built-in"}},
		Webcam:  "br",
	}
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reno-screen-0", "reno-cam"} {
		tf := obs.tfs[obs.itemIDs["reno/"+name]]
		if tf.ScaleX <= 0 || tf.ScaleY <= 0 {
			t.Errorf("%s: scale must be sent explicitly, got %+v", name, tf)
		}
	}
}

// The webcam ends up ABOVE the screen capture whatever order the items
// were created in. OBS renders by item index and puts a new item on top,
// so a screen capture recreated after the cam covers the whole canvas and
// the PiP vanishes -- source healthy, transform correct, every readout ok
// (glass, 2026-08-22).
func TestApplyStacksWebcamOverScreen(t *testing.T) {
	fakeCGDisplays(t)
	noSettleDelay(t)
	obs := sceneFake()
	// the cam item exists first; the screen gets created last and lands on top
	if _, err := obs.SceneItemID("reno", "reno-cam", true); err != nil {
		t.Fatal(err)
	}
	cfg := renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "built-in"}},
		Webcam:  "bl",
	}
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}
	order := filterKeep(obs.order["reno"], []string{"reno-screen-0", "reno-cam"})
	if !slices.Equal(order, []string{"reno-screen-0", "reno-cam"}) {
		t.Errorf("stack bottom-first = %v; the cam must render over the screen", order)
	}
}

// An off webcam is not in the stack: there is no item to order, and an
// index write against a disabled one is a request spent on nothing.
func TestApplyStackSkipsDisabledWebcam(t *testing.T) {
	fakeCGDisplays(t)
	noSettleDelay(t)
	obs := sceneFake()
	cfg := renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "built-in"}},
		Webcam:  "off",
	}
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(obs.ops, "index:reno-cam=1") {
		t.Errorf("a disabled cam was stacked: %v", obs.ops)
	}
}

// A stack the server did not take fails the apply. Ordering is the one
// thing here with no visible symptom short of watching the pixels.
func TestApplyStackReadbackMismatchFails(t *testing.T) {
	fakeCGDisplays(t)
	noSettleDelay(t)
	obs := sceneFake()
	cfg := renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "built-in"}},
		Webcam:  "bl",
	}
	// first apply builds the items, then the order is pinned against the
	// writes: the server accepts every index and changes nothing
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}
	obs.order["reno"] = []string{"reno-cam", "reno-screen-0"}
	obs.orderFrozen = true
	if _, err := ApplyRecordConfig(obs, cfg); err == nil || !strings.Contains(err.Error(), "bottom-first") {
		t.Errorf("a stack the server ignored must fail the apply, got %v", err)
	}
}

// noSettleDelay strips the inter-poll sleep so a dead-source test costs
// nothing; the retry count still exercises the loop.
func noSettleDelay(t *testing.T) {
	t.Helper()
	tries, poll := sourceSettleTries, sourceSettlePoll
	sourceSettleTries, sourceSettlePoll = 3, 0
	t.Cleanup(func() { sourceSettleTries, sourceSettlePoll = tries, poll })
}

// The record verb applies a journaled config before starting: arm ->
// probe -> plan -> apply -> record directory -> start.
func TestRecordVerbAppliesConfig(t *testing.T) {
	fakeCGDisplays(t)
	v, _ := renoTestVerbs(t)
	dir := filepath.Join(t.TempDir(), "ws")
	cfg := &renocore.RecordConfig{Audio: renocore.AudioNone}
	ctl := func(args []string) (json.RawMessage, error) {
		if args[0] == "record-open" {
			raw, _ := json.Marshal(RecordOpen{Run: "r1", Directory: dir, Config: cfg})
			return raw, nil
		}
		return nil, nil
	}
	obs := sceneFake()
	if err := v.Record(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatalf("record: %v", err)
	}
	joined := strings.Join(obs.ops, ",")
	sceneAt := strings.LastIndex(joined, "scene:reno")
	startAt := strings.LastIndex(joined, ",start")
	if sceneAt == -1 || startAt == -1 || sceneAt > startAt {
		t.Errorf("scene switch must precede start: %v", obs.ops)
	}
	if !obs.mutes["Mic/Aux"] {
		t.Errorf("config not applied: %v", obs.mutes)
	}
}

// A config apply failure notes back as failed and fails the verb.
func TestRecordVerbConfigFailureNotes(t *testing.T) {
	fakeCGDisplays(t)
	v, _ := renoTestVerbs(t)
	dir := filepath.Join(t.TempDir(), "ws")
	cfg := &renocore.RecordConfig{Audio: "no-such-device"}
	var notes [][]string
	ctl := func(args []string) (json.RawMessage, error) {
		if args[0] == "record-open" {
			raw, _ := json.Marshal(RecordOpen{Run: "r1", Directory: dir, Config: cfg})
			return raw, nil
		}
		if args[0] == "record-phase" {
			return nil, nil // readout decoration, not a journal note
		}
		notes = append(notes, args)
		return nil, nil
	}
	obs := sceneFake()
	err := v.Record(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{})
	if err == nil {
		t.Fatal("unresolvable device must fail the verb")
	}
	if len(notes) != 1 || notes[0][2] != "failed" {
		t.Errorf("failure note = %v", notes)
	}
	if slices.Contains(obs.ops, "start") {
		t.Error("recording must not start on a failed config")
	}
}

// Probe notes the inventory into the module over ctl.
func TestProbeVerbNotesInventory(t *testing.T) {
	fakeCGDisplays(t)
	v, _ := renoTestVerbs(t)
	var got renocore.ObsInventory
	calls := 0
	ctl := func(args []string) (json.RawMessage, error) {
		calls++
		if args[0] != "obs-inventory" {
			return nil, fmt.Errorf("unexpected ctl %v", args)
		}
		if err := json.Unmarshal([]byte(args[1]), &got); err != nil {
			return nil, err
		}
		return nil, nil
	}
	obs := sceneFake()
	if err := v.Probe(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if calls != 1 || got.ObsVersion != "32.2.0" || len(got.Screens) != 1 || len(got.Mics) != 1 || len(got.Cams) != 1 {
		t.Errorf("inventory = %+v (calls=%d)", got, calls)
	}
	ev := readEvents(t, v)
	last := ev[len(ev)-1]
	if last.Verb != "obs" || last.Outcome != "ok" {
		t.Errorf("event = %+v", last)
	}
}
