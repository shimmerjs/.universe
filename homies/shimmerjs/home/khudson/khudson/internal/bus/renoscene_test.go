package bus

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
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

// fakeDellAndEdge pins the two-display rig this records: a 4K dell and the
// Xeneon Edge strip, in pixel dims.
func fakeDellAndEdge(t *testing.T) {
	t.Helper()
	old := localDisplays
	localDisplays = func() []cgdisplay.Display {
		return []cgdisplay.Display{
			{ID: 1, UUID: "uuid-dell", W: 3840, H: 2160, PxW: 3840, PxH: 2160, Main: true},
			{ID: 2, UUID: "uuid-edge", W: 2560, H: 720, PxW: 2560, PxH: 720},
		}
	}
	t.Cleanup(func() { localDisplays = old })
}

// Two screens stack as rows, not columns. The edge goes UNDER the dell and
// both keep one shared width -- 2560 of the 3840 canvas, where the columns
// this replaced gave each of them 1920. Height is what binds: these
// displays are far wider than they are tall, so side-by-side spent width
// the composition could not use.
func TestApplyStacksScreensAsRows(t *testing.T) {
	fakeDellAndEdge(t)
	noSettleDelay(t)
	obs := sceneFake()
	obs.canvasW, obs.canvasH = 3840, 2160
	obs.sizes = map[string][2]int{
		"reno-screen-0": {3840, 2160},
		"reno-screen-1": {2560, 720},
	}
	cfg := renocore.RecordConfig{Screens: []renocore.ScreenSel{
		{Display: "uuid-dell"}, {Display: "uuid-edge"},
	}}
	dead, err := ApplyRecordConfig(obs, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 0 {
		t.Fatalf("live sources reported dead: %v", dead)
	}
	dell := obs.tfs[obs.itemIDs["reno/reno-screen-0"]]
	edge := obs.tfs[obs.itemIDs["reno/reno-screen-1"]]

	if dell.BoundsW != edge.BoundsW {
		t.Errorf("stacked rows must share a width: dell %v, edge %v", dell.BoundsW, edge.BoundsW)
	}
	if dell.BoundsW <= 1920 {
		t.Errorf("stacking must beat the 1920 the old columns gave: %v", dell.BoundsW)
	}
	// under, not beside, held apart by a gutter so the two read as two
	gutter := math.Round(dell.BoundsW * renocore.StackGutterFrac)
	if gap := edge.Y - (dell.Y + dell.BoundsH); math.Abs(gap-gutter) > 0.01 {
		t.Errorf("gap between rows = %v, want a %v gutter", gap, gutter)
	}
	if edge.X != dell.X {
		t.Errorf("rows must share a left edge: dell X %v, edge X %v", dell.X, edge.X)
	}
	// The canvas is now DERIVED from the composition, so the stack fills it
	// edge to edge: no bars, and the narrowest source at 1:1. That is the
	// whole point -- the Edge is small text, and any non-integer resample of
	// it turns every glyph to mush.
	if dell.X != 0 || edge.X != 0 || dell.Y != 0 {
		t.Errorf("stack must fill the derived canvas: dell %+v, edge %+v", dell, edge)
	}
	if obs.canvasW != 2560 || obs.canvasH != 2200 {
		t.Errorf("canvas = %vx%v, want 2560x2200", obs.canvasW, obs.canvasH)
	}
	if edge.BoundsW != 2560 || edge.BoundsH != 720 || edge.ScaleX != 1 {
		t.Errorf("edge must land at native 2560x720 scale 1: %+v", edge)
	}
	if bottom := edge.Y + edge.BoundsH; bottom != obs.canvasH {
		t.Errorf("stack bottom = %v, want the canvas height %v", bottom, obs.canvasH)
	}
	// each row is its source's own aspect, so nothing is letterboxed inside
	// its own box
	if ar := dell.BoundsW / dell.BoundsH; ar-3840.0/2160 > 0.001 {
		t.Errorf("dell row must keep its aspect: %+v", dell)
	}
	if ar := edge.BoundsW / edge.BoundsH; ar-2560.0/720 > 0.001 {
		t.Errorf("edge row must keep its aspect: %+v", edge)
	}
}

// A cropped screen is scaled by its CROP, not its sensor. The row it gets
// is sized to the crop's aspect, so scaling by the full source renders it
// short of its box by exactly the crop ratio -- a visible gap with every
// readout clean.
func TestApplyScalesCroppedSourceByItsCrop(t *testing.T) {
	fakeDellAndEdge(t)
	noSettleDelay(t)
	obs := sceneFake()
	obs.canvasW, obs.canvasH = 3840, 2160
	obs.sizes = map[string][2]int{"reno-screen-0": {3840, 2160}}
	cfg := renocore.RecordConfig{Screens: []renocore.ScreenSel{
		{Display: "uuid-dell", Region: &renocore.Region{X: 0, Y: 0, W: 1920, H: 1080}},
	}}
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}
	tf := obs.tfs[obs.itemIDs["reno/reno-screen-0"]]
	// The canvas is derived from what the canvas SEES, so a cropped screen
	// sizes the frame to its crop and lands 1:1. Scaling by the full sensor
	// instead would render it at half its box, which is the bug this guards.
	if obs.canvasW != 1920 || obs.canvasH != 1080 {
		t.Errorf("canvas = %vx%v, want the crop's 1920x1080", obs.canvasW, obs.canvasH)
	}
	if tf.ScaleX != 1 || tf.ScaleY != 1 {
		t.Errorf("crop scale = %v/%v, want 1", tf.ScaleX, tf.ScaleY)
	}
	if tf.CropR != 1920 || tf.CropB != 1080 {
		t.Errorf("the crop itself must still be applied: %+v", tf)
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

// The overlay takes the CAMERA's box, not its own page's shape. Its page
// is 16:9 and a camera need not be, so an overlay sized from itself would
// be visibly the wrong rectangle on the one shot where it matters.
func TestApplyHangsLemmingsOnTheCameraBox(t *testing.T) {
	fakeCGDisplays(t)
	noSettleDelay(t)
	fakeRenoState(t)
	obs := sceneFake()
	obs.kinds = append(obs.kinds, renocore.LemmingsKind)
	// a 4:3 camera, so a box shaped from the overlay's own 16:9 page would
	// come out visibly different
	obs.sizes[renocore.CamInput] = [2]int{1440, 1080}
	obs.sizes[renocore.LemmingsInput] = [2]int{renocore.LemmingsW, renocore.LemmingsH}

	cfg := renocore.RecordConfig{
		Screens:  []renocore.ScreenSel{{Display: "built-in"}},
		Webcam:   "br",
		Lemmings: true,
	}
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}
	camID, err := obs.SceneItemID("reno", renocore.CamInput, false)
	if err != nil {
		t.Fatal(err)
	}
	lemID, err := obs.SceneItemID("reno", renocore.LemmingsInput, false)
	if err != nil {
		t.Fatal(err)
	}
	cam, lem := obs.tfs[camID], obs.tfs[lemID]
	if cam.BoundsW == 0 {
		t.Fatal("the camera was never placed")
	}
	if lem.X != cam.X || lem.Y != cam.Y || lem.BoundsW != cam.BoundsW || lem.BoundsH != cam.BoundsH {
		t.Errorf("overlay box %+v does not sit on the camera's %+v", lem, cam)
	}
	// the SCALE still comes from the overlay's own page, because that is
	// what OBS is fitting into the box
	wantX, wantY := renocore.ScaleInner(cam.BoundsW, cam.BoundsH, renocore.LemmingsW, renocore.LemmingsH)
	if lem.ScaleX != wantX || lem.ScaleY != wantY {
		t.Errorf("overlay scale = %v,%v, want %v,%v from its own page size",
			lem.ScaleX, lem.ScaleY, wantX, wantY)
	}
	// and it renders over the camera, which is the whole point
	order := filterKeep(obs.order["reno"], []string{renocore.CamInput, renocore.LemmingsInput})
	if !slices.Equal(order, []string{renocore.CamInput, renocore.LemmingsInput}) {
		t.Errorf("stack bottom-first = %v; the overlay must be on top", order)
	}
	// the page it loads is real and on disk
	settings := obs.settings[renocore.LemmingsInput]
	path, _ := settings["local_file"].(string)
	if path == "" {
		t.Fatalf("overlay settings = %+v, want a local file", settings)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the overlay points at a page that is not there: %v", err)
	}
}

// The canvas is a profile-global setting reno writes to fit its own
// composition, so the release puts back the one OBS had first -- and only
// that one: every apply after the first is reno resizing its own frame.
func TestCanvasGoesBackOnRelease(t *testing.T) {
	fakeRenoState(t)
	fakeDellAndEdge(t)
	noSettleDelay(t)
	obs := sceneFake()
	obs.canvasW, obs.canvasH = 3840, 2160
	obs.sizes = map[string][2]int{"reno-screen-0": {3840, 2160}}
	cfg := renocore.RecordConfig{Screens: []renocore.ScreenSel{
		{Display: "uuid-dell", Region: &renocore.Region{X: 0, Y: 0, W: 1920, H: 1080}},
	}}
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}
	if obs.canvasW != 1920 {
		t.Fatalf("canvas = %v, want reno's 1920", obs.canvasW)
	}
	// a second apply at another size must not re-stash: the frame it finds
	// is the one reno itself left behind
	cfg.Screens[0].Region = &renocore.Region{X: 0, Y: 0, W: 1280, H: 720}
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}

	detail, err := restoreCanvas(obs)
	if err != nil {
		t.Fatal(err)
	}
	if obs.canvasW != 3840 || obs.canvasH != 2160 {
		t.Errorf("canvas = %vx%v, want OBS's own 3840x2160 back", obs.canvasW, obs.canvasH)
	}
	if !strings.Contains(detail, "3840x2160") {
		t.Errorf("detail = %q, want the restored size in it", detail)
	}
	// and it is forgotten: a second release must not put a stale frame back
	// on top of whatever is there by then
	obs.canvasW, obs.canvasH = 1280, 720
	if detail, err := restoreCanvas(obs); err != nil || detail != "" {
		t.Fatalf("second restore = %q, %v; want a no-op", detail, err)
	}
	if obs.canvasW != 1280 {
		t.Errorf("a forgotten stash was applied again: %v", obs.canvasW)
	}
}

// An OBS reno never resized has nothing to put back.
func TestReleaseWithoutAResizeRestoresNothing(t *testing.T) {
	fakeRenoState(t)
	obs := sceneFake()
	detail, err := restoreCanvas(obs)
	if err != nil || detail != "" {
		t.Fatalf("restore = %q, %v; want a no-op", detail, err)
	}
	for _, op := range obs.ops {
		if strings.HasPrefix(op, "canvas:") {
			t.Errorf("resized an OBS reno never touched: %v", obs.ops)
		}
	}
}

// fakeRenoState points the reno state dir at a temp dir, so the overlay
// page is written there rather than into the state root of the machine
// running the tests.
func fakeRenoState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := renoStateDir
	renoStateDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { renoStateDir = old })
	return dir
}

// End to end: one screen plus a placed webcam grows the canvas by the band,
// puts the picture at the top of it, and leaves the cam below the picture
// instead of over it.
func TestApplyReservesACamBandForOneScreen(t *testing.T) {
	fakeCGDisplays(t)
	noSettleDelay(t)
	obs := sceneFake()
	// the Edge alone: a 2560x720 strip
	obs.sizes["reno-screen-0"] = [2]int{2560, 720}
	cfg := renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "built-in"}},
		Webcam:  "br",
	}
	if _, err := ApplyRecordConfig(obs, cfg); err != nil {
		t.Fatal(err)
	}
	wantH := 720 + renocore.CamBand(720)
	if obs.canvasW != 2560 || obs.canvasH != wantH {
		t.Fatalf("canvas = %vx%v, want 2560x%v", obs.canvasW, obs.canvasH, wantH)
	}
	screenID, err := obs.SceneItemID("reno", "reno-screen-0", false)
	if err != nil {
		t.Fatal(err)
	}
	camID, err := obs.SceneItemID("reno", renocore.CamInput, false)
	if err != nil {
		t.Fatal(err)
	}
	screen, cam := obs.tfs[screenID], obs.tfs[camID]
	if screen.Y != 0 || screen.BoundsH != 720 {
		t.Errorf("screen = %+v, want it at the top of the canvas", screen)
	}
	if cam.Y < screen.Y+screen.BoundsH {
		t.Errorf("cam at y=%v is over a picture ending at %v", cam.Y, screen.Y+screen.BoundsH)
	}

	// two screens already have gutters and letterboxing to sit in, so no band
	fakeDellAndEdge(t)
	obs2 := sceneFake()
	obs2.sizes["reno-screen-0"] = [2]int{3840, 2160}
	obs2.sizes["reno-screen-1"] = [2]int{2560, 720}
	cfg2 := renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "1"}, {Display: "2"}},
		Webcam:  "br",
	}
	if _, err := ApplyRecordConfig(obs2, cfg2); err != nil {
		t.Fatal(err)
	}
	sizes := [][2]int{{3840, 2160}, {2560, 720}}
	wantW, wantH2, _ := renocore.StackCanvas(sizes)
	if obs2.canvasW != wantW || obs2.canvasH != wantH2 {
		t.Errorf("stacked canvas = %vx%v, want %vx%v (no band)",
			obs2.canvasW, obs2.canvasH, wantW, wantH2)
	}
}
