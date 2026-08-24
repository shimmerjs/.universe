// Scene orchestration for the reno record verbs: probe the OBS
// environment (input kinds, displays, devices, canvas), and drive the
// khudson-owned "reno" scene to match a RecordConfig. The desired state
// is computed pure in internal/reno (BuildScenePlan, unit-tested); this
// file is the thin executor plus the enumeration the probe verb notes
// back into the module as menu inventory.
package bus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// skeleton inputs the probe ensures so list-properties can be
// enumerated: OBS only exposes device/display items through an existing
// input's properties. Created disabled inside the reno scene; the first
// screen slot doubles as the display probe.
func ensureSkeleton(obs ObsTransport, env *renocore.ObsEnv, name, kind string) error {
	if kind == "" {
		return nil
	}
	if _, ok := env.Inputs[name]; ok {
		return nil
	}
	if err := obs.CreateInput(renocore.SceneName, name, kind, nil, false); err != nil {
		return fmt.Errorf("create %s (%s): %w", name, kind, err)
	}
	env.Inputs[name] = kind
	return nil
}

// propItems tries each property name in order and returns the first
// non-empty enumeration -- the capability-probe discipline for the
// device property naming differences across input kinds. Nameless or
// valueless items are dropped (live-observed: the cam list leads with
// an empty entry); a menu row nobody can read selects nothing.
func propItems(obs ObsTransport, input string, props ...string) ([]renocore.PropertyItem, string) {
	for _, p := range props {
		items, err := obs.PropertyItems(input, p)
		if err != nil {
			continue
		}
		kept := items[:0]
		for _, it := range items {
			if it.Name != "" && it.Value != "" {
				kept = append(kept, it)
			}
		}
		if len(kept) > 0 {
			return kept, p
		}
	}
	return nil, ""
}

// ProbeEnv gathers the OBS environment a scene plan builds against,
// ensuring the reno scene and skeleton inputs exist first. Fresh at
// every apply -- nothing is trusted from an old inventory.
func ProbeEnv(obs ObsTransport) (renocore.ObsEnv, error) {
	var env renocore.ObsEnv
	kinds, err := obs.InputKinds()
	if err != nil {
		return env, fmt.Errorf("input kinds: %w", err)
	}
	env.ScreenKind = renocore.PickKind(kinds, "screen_capture", "display_capture")
	env.CamKind = renocore.PickKind(kinds, "macos-avcapture", "av_capture_input")
	// The html overlay's renderer. Probed here with the rest, because this
	// is where the question "what can this OBS do" is answered; the PAGE it
	// renders is written by the apply, and only when the apply wants one.
	env.LemmingsKind = renocore.PickKind(kinds, renocore.LemmingsKind)

	if env.CanvasW, env.CanvasH, env.FPS, err = obs.Canvas(); err != nil {
		return env, fmt.Errorf("canvas: %w", err)
	}
	// OBS's monitor list contributes labels only; env.Monitors is rebuilt
	// below from local CG in PIXEL dims (the crop-math space SCK sources
	// report)
	obsMonitors, err := obs.Monitors()
	if err != nil {
		return env, fmt.Errorf("monitors: %w", err)
	}
	if env.Scenes, err = obs.Scenes(); err != nil {
		return env, fmt.Errorf("scenes: %w", err)
	}
	if !slices.Contains(env.Scenes, renocore.SceneName) {
		if err := obs.CreateScene(renocore.SceneName); err != nil {
			return env, fmt.Errorf("create scene %s: %w", renocore.SceneName, err)
		}
		env.Scenes = append(env.Scenes, renocore.SceneName)
	}
	if env.Inputs, err = obs.Inputs(); err != nil {
		return env, fmt.Errorf("inputs: %w", err)
	}

	if err := ensureSkeleton(obs, &env, renocore.ScreenInputName(0), env.ScreenKind); err != nil {
		return env, err
	}
	if err := ensureSkeleton(obs, &env, renocore.MicInput, "coreaudio_input_capture"); err != nil {
		return env, err
	}
	if err := ensureSkeleton(obs, &env, renocore.CamInput, env.CamKind); err != nil {
		return env, err
	}

	if env.ScreenKind != "" {
		// displays enumerate LOCALLY via CoreGraphics, never through OBS:
		// GetInputPropertiesListPropertyItems on the SCK source's
		// display_uuid list SEGVs obs-websocket in OBS 32.2 (strlen(NULL),
		// crash-verified 2026-07-23 -- three matching .ips reports). SCK
		// selects by CGDisplay UUID, the legacy source by CGDirectDisplayID;
		// khudson always runs on the machine OBS captures.
		env.DisplayNumeric = env.ScreenKind != "screen_capture"
		env.DisplayProp = "display_uuid"
		if env.DisplayNumeric {
			env.DisplayProp = "display"
		}
		seen := map[string]int{}
		for i, d := range localDisplays() {
			val := d.UUID
			if env.DisplayNumeric {
				val = fmt.Sprint(d.ID)
			}
			name := displayLabel(obsMonitors, d, i)
			// identical monitors match identical OBS names; an ambiguous
			// label would make ResolveItem capture the wrong one silently
			// (arc-2 review #22), so repeats get a positional suffix
			seen[name]++
			if n := seen[name]; n > 1 {
				name = fmt.Sprintf("%s (%d)", name, n)
			}
			env.Displays = append(env.Displays, renocore.PropertyItem{Name: name, Value: val})
			env.Monitors = append(env.Monitors, renocore.Monitor{
				Index: i, Name: name, Width: d.PxW, Height: d.PxH,
			})
		}
	}
	env.Mics, _ = propItems(obs, renocore.MicInput, "device_id")
	if env.CamKind != "" {
		env.Cams, env.CamProp = propItems(obs, renocore.CamInput, "device", "device_id")
	}
	return env, nil
}

// localDisplays is the CG enumeration seam (tests fake it).
var localDisplays = cgdisplay.Active

// renoStateDir is where reno keeps what it owns on disk, and the seam
// tests point somewhere temporary. It has to be a seam: the overlay page
// is WRITTEN from here, and a test that resolved the real state root would
// be a test editing the machine it runs on.
var renoStateDir = func() (string, error) {
	p, err := paths.Resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Dir, "reno"), nil
}

// displayLabel names a CG display for menus: the OBS monitor whose
// point geometry matches, else a positional fallback.
func displayLabel(monitors []renocore.Monitor, d cgdisplay.Display, i int) string {
	for _, m := range monitors {
		if m.Width == d.W && m.Height == d.H {
			return m.Name
		}
	}
	main := ""
	if d.Main {
		main = " (main)"
	}
	return fmt.Sprintf("display %d %dx%d%s", i+1, d.W, d.H, main)
}

// ApplyRecordConfig drives the reno scene to match cfg and switches to
// it: probe fresh, plan pure, execute. Errors carry the failing stage.
//
// It also reports the enabled inputs that are not producing frames. A
// capture whose device went away, whose display selector stopped resolving,
// or whose stream died leaves a source sitting in the scene answering 0x0 --
// the scene composites to black and nothing else about the apply looks
// wrong. Callers escalate that rather than report ok over it.
func ApplyRecordConfig(obs ObsTransport, cfg renocore.RecordConfig) (dead []string, err error) {
	env, err := ProbeEnv(obs)
	if err != nil {
		return nil, err
	}
	if cfg.Lemmings {
		// written HERE and not in the probe: the page is a file on disk, and
		// an environment probe that wrote one every time it ran would be
		// touching the machine to answer a question about OBS. The plan
		// refuses by name if this did not land.
		dir, derr := renoStateDir()
		if derr != nil {
			return nil, fmt.Errorf("lemmings: %w", derr)
		}
		if env.LemmingsFile, err = renocore.WriteLemmingsPage(dir); err != nil {
			return nil, err
		}
	}
	plan, err := renocore.BuildScenePlan(cfg, env)
	if err != nil {
		return nil, err
	}
	items := make([]placedItem, 0, len(plan.Inputs))
	for _, in := range plan.Inputs {
		existingKind, exists := env.Inputs[in.Name]
		if exists && existingKind != in.Kind {
			// a reno-owned input of the wrong kind never converges by
			// settings writes (display_uuid on a legacy source); recreate
			// it -- hand-attached filters on it are lost, and that is the
			// documented cost of the reno-owned scene (arc-2 review #21)
			if err := obs.RemoveInput(in.Name); err != nil {
				return nil, fmt.Errorf("recreate %s (kind %s -> %s): %w", in.Name, existingKind, in.Kind, err)
			}
			exists = false
		}
		if !exists {
			if err := obs.CreateInput(plan.Scene, in.Name, in.Kind, in.Settings, in.Enabled); err != nil {
				return nil, fmt.Errorf("create %s: %w", in.Name, err)
			}
		} else if in.Settings != nil {
			if err := obs.SetInputSettings(in.Name, in.Settings); err != nil {
				return nil, fmt.Errorf("settings %s: %w", in.Name, err)
			}
		}
		id, err := obs.SceneItemID(plan.Scene, in.Name, true)
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", in.Name, err)
		}
		if err := obs.SetSceneItemEnabled(plan.Scene, id, in.Enabled); err != nil {
			return nil, fmt.Errorf("enable %s: %w", in.Name, err)
		}
		items = append(items, placedItem{in: in, id: id})
	}
	// Sizes before boxes: a stacked slot's row height comes from what its
	// capture reports, so every screen has to have settled before any one
	// of them can be placed.
	for i := range items {
		if err := items[i].measure(obs, plan.Scene); err != nil {
			return nil, err
		}
	}
	hangOverlays(items)
	if err := fitCanvas(obs, items, &env); err != nil {
		return nil, err
	}
	boxes := screenBoxes(items, env)
	for _, it := range items {
		live, err := applyTransform(obs, plan.Scene, it, boxes, env)
		if err != nil {
			return nil, err
		}
		if it.in.Enabled && !live {
			dead = append(dead, it.in.Name)
		}
	}
	for _, name := range plan.RemoveInputs {
		if err := obs.RemoveInput(name); err != nil {
			return nil, fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	for name, muted := range plan.Mutes {
		if err := obs.SetInputMute(name, muted); err != nil {
			return nil, fmt.Errorf("mute %s: %w", name, err)
		}
	}
	if err := applyStack(obs, plan.Scene, plan.Stack); err != nil {
		return nil, err
	}
	if err := obs.SetScene(plan.Scene); err != nil {
		return nil, fmt.Errorf("switch to %s: %w", plan.Scene, err)
	}
	return dead, nil
}

// hangOverlays gives every overlay the shape of the item it hangs on, once
// everything has been measured. An overlay sized from its own page would
// sit in a 16:9 box over a camera that is not, and be visibly the wrong
// rectangle on the one shot where it matters.
//
// An overlay whose host reported nothing keeps its own shape rather than
// inheriting a zero: a dead camera is reported dead elsewhere, and a box
// of no size is not an improvement on a box of the wrong one.
func hangOverlays(items []placedItem) {
	size := make(map[string][2]int, len(items))
	for _, it := range items {
		if it.w > 0 && it.h > 0 {
			size[it.in.Name] = [2]int{it.w, it.h}
		}
	}
	for i := range items {
		host := items[i].in.Overlay
		if host == "" {
			continue
		}
		if wh, ok := size[host]; ok {
			items[i].boxW, items[i].boxH = wh[0], wh[1]
		}
	}
}

// applyStack asserts the plan's stacking order: item stack[i] goes to
// index i, bottom-first. Assigning ascending indices in that order
// converges from any starting arrangement, because by the time index i is
// written, indices 0..i-1 already hold stack[0..i-1]. Items the plan does
// not name -- audio, anything hand-added to the scene -- float above the
// stack, which is where an overlay someone added by hand wants to be
// anyway.
//
// Then read the order back, because getting this wrong is silent: the
// webcam renders under a full-canvas screen capture and looks exactly like
// a webcam that was never enabled.
func applyStack(obs ObsTransport, scene string, stack []string) error {
	for i, name := range stack {
		id, err := obs.SceneItemID(scene, name, false)
		if err != nil {
			return fmt.Errorf("stack %s: %w", name, err)
		}
		if err := obs.SetSceneItemIndex(scene, id, i); err != nil {
			return fmt.Errorf("stack %s -> %d: %w", name, i, err)
		}
	}
	if len(stack) < 2 {
		return nil // nothing can occlude anything
	}
	order, err := obs.SceneItemOrder(scene)
	if err != nil {
		return fmt.Errorf("stack readback %s: %w", scene, err)
	}
	if got := filterKeep(order, stack); !slices.Equal(got, stack) {
		return fmt.Errorf("stack %s: want %v bottom-first, got %v", scene, stack, got)
	}
	return nil
}

// filterKeep returns order's elements that appear in keep, order preserved.
func filterKeep(order, keep []string) []string {
	out := make([]string, 0, len(keep))
	for _, name := range order {
		if slices.Contains(keep, name) {
			out = append(out, name)
		}
	}
	return out
}

// sourceSettle bounds the wait for a freshly-configured capture to report
// its dimensions. A capture answers 0x0 for a beat after it is created --
// SCK has to hand over a first frame -- and every geometric decision below
// depends on the answer, so this waits for it instead of guessing.
var (
	sourceSettleTries = 12
	sourceSettlePoll  = 250 * time.Millisecond
)

// liveSourceSize polls until the item's source reports real dimensions,
// answering 0,0 if it never does. A source still at 0x0 after this is not
// slow, it is dead: the device went away, the display selector stopped
// resolving, or the stream died and left the source sitting in the scene.
func liveSourceSize(obs ObsTransport, scene string, id int) (int, int, error) {
	for i := range sourceSettleTries {
		w, h, err := obs.ItemSourceSize(scene, id)
		if err != nil {
			return 0, 0, err
		}
		if w > 0 && h > 0 {
			return w, h, nil
		}
		if i < sourceSettleTries-1 {
			time.Sleep(sourceSettlePoll)
		}
	}
	return 0, 0, nil
}

// placedItem is a planned input that now exists in the scene, carrying the
// live source size its geometry is computed from. Audio items, and any
// capture that never produced a frame, stay at 0x0.
type placedItem struct {
	in   renocore.PlanInput
	id   int
	w, h int
	// boxW/boxH override what the box is shaped from; set only on an
	// overlay, from the item it hangs on.
	boxW, boxH int
}

// geometric reports whether the item has a position at all: webcam presets
// and screen slots do, audio does not.
func (p placedItem) geometric() bool { return p.in.Preset != "" || p.in.Slots > 0 }

// boxSize is what the item's BOX is shaped from, which is not always what
// the item renders. An overlay hangs on another item and takes its shape,
// so the two coincide over a camera of any aspect; the scale below still
// comes from the overlay's own size, because that is what OBS is actually
// fitting into the box.
func (p placedItem) boxSize() (int, int) {
	if p.boxW > 0 && p.boxH > 0 {
		return p.boxW, p.boxH
	}
	return p.w, p.h
}

// measure settles the item's source once; the crop math, the row heights,
// and the scale all read the answer.
func (p *placedItem) measure(obs ObsTransport, scene string) error {
	if !p.geometric() {
		return nil
	}
	w, h, err := liveSourceSize(obs, scene, p.id)
	if err != nil {
		return fmt.Errorf("source size %s: %w", p.in.Name, err)
	}
	p.w, p.h = w, h
	return nil
}

// layoutSize is the size the canvas geometry is computed from: what the
// capture reports, else the resolved monitor's geometry. A dead source
// still beats garbage for the crop and row math, and it keeps one dead
// screen from silently resizing the ones stacked with it -- reporting it
// dead is the caller's job.
func (p placedItem) layoutSize() (int, int) {
	if p.w > 0 && p.h > 0 {
		return p.w, p.h
	}
	if p.in.Monitor != nil {
		return p.in.Monitor.Width, p.in.Monitor.Height
	}
	return 0, 0
}

// fitCanvas resizes OBS's canvas to the composition and updates env to
// match, so the boxes computed next are computed against the canvas that
// will actually render them.
//
// The canvas follows the sources rather than the sources being squeezed
// into whatever canvas was there: the Edge is a strip of small text, and a
// strip resampled by any non-integer factor -- 0.985 was enough -- turns
// every glyph edge to mush. Sizing the frame to the content is the only way
// the legible thing lands on the pixel grid.
//
// This WRITES a profile-global OBS setting, so the canvas OBS had first is
// stashed before the first resize and put back on release. Only ever called
// for a non-empty config.
func fitCanvas(obs ObsTransport, items []placedItem, env *renocore.ObsEnv) error {
	slots := 0
	for _, it := range items {
		slots = max(slots, it.in.Slots)
	}
	if slots == 0 {
		return nil // no screens: nothing derives a canvas
	}
	sizes := make([][2]int, slots)
	for _, it := range items {
		if it.in.Slots == 0 || it.in.Slot >= slots {
			continue
		}
		lw, lh := it.layoutSize()
		w, h := renocore.EffectiveSize(lw, lh, it.in.Region)
		sizes[it.in.Slot] = [2]int{w, h}
	}
	w, h, ok := renocore.StackCanvas(sizes)
	if !ok {
		// a screen that never reported a size and resolved to no monitor
		// leaves nothing honest to size a canvas from; keep the one OBS has
		// rather than inventing a frame around a dead capture
		return nil
	}
	if w == env.CanvasW && h == env.CanvasH {
		return nil
	}
	stashCanvas(env.CanvasW, env.CanvasH)
	if err := obs.SetCanvas(int(w), int(h)); err != nil {
		return fmt.Errorf("canvas %dx%d: %w", int(w), int(h), err)
	}
	// Read the canvas back before ANY transform is written against it.
	// Resizing the base resolution makes OBS rescale every scene item to fit
	// the new frame, and that rescale is not finished when SetVideoSettings
	// returns: transforms written into the gap get multiplied by the ratio of
	// the two canvases afterwards. Live, a 720 -> 2200 resize left every box
	// at 0.328 of the size it was written at -- the whole composition shrunk
	// into the middle of the frame with every readout reporting success. The
	// round trip is the barrier.
	gotW, gotH, fps, err := obs.Canvas()
	if err != nil {
		return fmt.Errorf("canvas readback: %w", err)
	}
	if gotW != w || gotH != h {
		return fmt.Errorf("canvas %dx%d did not take (obs reports %vx%v)", int(w), int(h), gotW, gotH)
	}
	env.CanvasW, env.CanvasH, env.FPS = gotW, gotH, fps
	return nil
}

// canvasFile holds the canvas OBS had before reno first resized it. It is a
// FILE rather than memory because the two halves happen in different
// processes: the apply verb resizes, and the release verb -- minutes and a
// bus restart later -- is what puts it back.
const canvasFile = "canvas.json"

// stashedCanvas is one saved base resolution.
type stashedCanvas struct {
	W int `json:"w"`
	H int `json:"h"`
}

// stashCanvas records the canvas to go back to, once. Only the FIRST resize
// writes it: every later apply is reno resizing its own canvas, and taking
// that one would leave the restore putting back a frame reno itself chose.
//
// Best effort by construction -- a stash that could not be written costs the
// restore, and an apply that failed over it would cost the recording.
func stashCanvas(w, h float64) {
	if w <= 0 || h <= 0 {
		return // nothing honest to go back to
	}
	dir, err := renoStateDir()
	if err != nil {
		return
	}
	path := filepath.Join(dir, canvasFile)
	if _, err := os.Stat(path); err == nil {
		return
	}
	body, err := json.Marshal(stashedCanvas{W: int(w), H: int(h)})
	if err != nil {
		return
	}
	if err := writeFileAtomic(path, body, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "khudson reno: canvas stash: %v\n", err)
	}
}

// restoreCanvas puts the pre-reno canvas back and forgets it, reporting what
// it did for the release summary ("" = there was nothing to restore).
//
// The forgetting is unconditional. A stash kept after a failed restore would
// be a canvas from some older session waiting to be applied on top of a
// later one, which is worse than the resize that is already visible.
func restoreCanvas(obs ObsTransport) (string, error) {
	dir, err := renoStateDir()
	if err != nil {
		return "", nil
	}
	path := filepath.Join(dir, canvasFile)
	body, err := os.ReadFile(path)
	if err != nil {
		return "", nil // never resized, or already restored
	}
	var saved stashedCanvas
	if err := json.Unmarshal(body, &saved); err != nil || saved.W <= 0 || saved.H <= 0 {
		os.Remove(path)
		return "", nil
	}
	os.Remove(path)
	w, h, _, err := obs.Canvas()
	if err != nil {
		return "", fmt.Errorf("canvas readback: %w", err)
	}
	if int(w) == saved.W && int(h) == saved.H {
		return "", nil
	}
	if err := obs.SetCanvas(saved.W, saved.H); err != nil {
		return "", fmt.Errorf("restore canvas %dx%d: %w", saved.W, saved.H, err)
	}
	return fmt.Sprintf("canvas back to %dx%d", saved.W, saved.H), nil
}

// screenBoxes lays every screen slot out over the canvas as a stacked row.
// Slot count comes from the plan rather than the item count so a partial
// apply cannot silently renumber the stack.
func screenBoxes(items []placedItem, env renocore.ObsEnv) []renocore.Transform {
	slots := 0
	for _, it := range items {
		slots = max(slots, it.in.Slots)
	}
	if slots == 0 {
		return nil
	}
	aspects := make([]float64, slots)
	for _, it := range items {
		if it.in.Slots == 0 || it.in.Slot >= slots {
			continue
		}
		lw, lh := it.layoutSize()
		w, h := renocore.EffectiveSize(lw, lh, it.in.Region)
		if w > 0 && h > 0 {
			aspects[it.in.Slot] = float64(w) / float64(h)
		}
	}
	return renocore.StackBoxes(env.CanvasW, env.CanvasH, aspects)
}

// applyTransform positions one placed item: webcam presets and screen
// slots; audio items have no geometry. It reports whether the item's
// source is producing frames, because an apply that placed everything
// perfectly over dead captures still records black.
func applyTransform(obs ObsTransport, scene string, p placedItem, boxes []renocore.Transform, env renocore.ObsEnv) (live bool, err error) {
	in, id := p.in, p.id
	if !p.geometric() {
		return true, nil // audio: no geometry, nothing to be dead about
	}
	var tf renocore.Transform
	switch {
	case in.Preset != "":
		bw, bh := p.boxSize()
		tf = renocore.CamTransform(env.CanvasW, env.CanvasH, in.Preset, bw, bh)
	case in.Slot < len(boxes):
		lw, lh := p.layoutSize()
		var cropped bool
		tf, cropped = renocore.ItemTransform(boxes[in.Slot], lw, lh, in.Region)
		if in.Region != nil && !cropped {
			fmt.Fprintf(os.Stderr, "khudson reno: %s reports no source size yet; region %s skipped this run\n", in.Name, in.Region)
		}
	default:
		return false, fmt.Errorf("transform %s: slot %d of %d has no canvas box", in.Name, in.Slot, in.Slots)
	}
	// The scale is denominated in what the canvas sees -- the crop, when
	// there is one. Scaling a cropped source by its full sensor size renders
	// it short of its box by exactly the crop ratio.
	ew, eh := renocore.EffectiveSize(p.w, p.h, in.Region)
	tf.ScaleX, tf.ScaleY = renocore.ScaleInner(tf.BoundsW, tf.BoundsH, ew, eh)
	if err := obs.SetSceneItemTransform(scene, id, tf); err != nil {
		return false, fmt.Errorf("transform %s: %w", in.Name, err)
	}
	if p.w == 0 {
		return false, nil
	}
	// Read the scale back rather than trusting the write. A zero scale is the
	// difference between a scene that records and one that records black, and
	// nothing else about the apply looks any different -- so this is the one
	// place worth a round trip. The explicit scale above has been enough in
	// every case tested against live OBS 32.2; the second write is the belt,
	// and it is cheap because it only ever runs when the first did not take.
	got, err := obs.ItemTransform(scene, id)
	if err != nil {
		return false, fmt.Errorf("transform readback %s: %w", in.Name, err)
	}
	if got.ScaleX > 0 && got.ScaleY > 0 {
		return true, nil
	}
	if err := obs.SetSceneItemTransform(scene, id, tf); err != nil {
		return false, fmt.Errorf("rescale %s: %w", in.Name, err)
	}
	got, err = obs.ItemTransform(scene, id)
	if err != nil {
		return false, fmt.Errorf("transform readback %s: %w", in.Name, err)
	}
	// still zero after two writes: the item will render at zero size, which
	// is indistinguishable on glass from a dead capture, so call it one
	return got.ScaleX > 0 && got.ScaleY > 0, nil
}

// Probe is the `khudson reno obs probe` verb: ensure OBS, enumerate the
// environment, note the inventory into the module over ctl (the
// recorder view's menu source), and print a human summary.
func (v *RenoVerbs) Probe(dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	obs, err := dial()
	if err != nil {
		return v.Act(corr, "obs", "probe", "error", err.Error(), err)
	}
	defer obs.Close()
	env, err := ProbeEnv(obs)
	if err != nil {
		return v.Act(corr, "obs", "probe", "error", err.Error(), err)
	}
	inv := renocore.ObsInventory{
		At:         v.now(),
		ObsVersion: obs.Version(),
		Screens:    env.Displays,
		Mics:       env.Mics,
		Cams:       env.Cams,
		Monitors:   env.Monitors,
	}
	raw, err := json.Marshal(inv)
	if err != nil {
		return v.Act(corr, "obs", "probe", "error", err.Error(), err)
	}
	if _, err := ctl([]string{"obs-inventory", string(raw)}); err != nil {
		return v.Act(corr, "obs", "probe", "unconfirmed",
			"probed but the inventory note failed: "+err.Error(), err)
	}
	detail := fmt.Sprintf("obs %s: %d displays, %d mics, %d cams",
		inv.ObsVersion, len(inv.Screens), len(inv.Mics), len(inv.Cams))
	fmt.Println(detail)
	for _, it := range inv.Screens {
		fmt.Printf("  display: %s\n", it.Name)
	}
	for _, it := range inv.Mics {
		fmt.Printf("  mic: %s\n", it.Name)
	}
	for _, it := range inv.Cams {
		fmt.Printf("  cam: %s\n", it.Name)
	}
	return v.Act(corr, "obs", "probe", "ok", detail, nil)
}

// EnsureDeadline bounds verbs that may cold-launch OBS (record, obs
// ensure, obs probe): Gatekeeper's first-open confirmation and the app
// start ride inside it, so it is minutes where the plain verb budget is
// seconds.
const EnsureDeadline = 120 * time.Second
