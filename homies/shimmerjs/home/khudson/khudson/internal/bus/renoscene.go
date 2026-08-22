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
	"slices"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
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
	plan, err := renocore.BuildScenePlan(cfg, env)
	if err != nil {
		return nil, err
	}
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
		live, err := applyTransform(obs, plan.Scene, id, in, env)
		if err != nil {
			return nil, err
		}
		if in.Enabled && !live {
			dead = append(dead, in.Name)
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

// applyTransform positions one planned item: webcam presets and screen
// slots; audio items (no slot, no preset) have no geometry. It reports
// whether the item's source is producing frames, because an apply that
// placed everything perfectly over dead captures still records black.
func applyTransform(obs ObsTransport, scene string, id int, in renocore.PlanInput, env renocore.ObsEnv) (live bool, err error) {
	if in.Preset == "" && in.Slots == 0 {
		return true, nil // audio: no geometry, nothing to be dead about
	}
	// one settle serves the crop math and the scale both
	w, h, err := liveSourceSize(obs, scene, id)
	if err != nil {
		return false, fmt.Errorf("source size %s: %w", in.Name, err)
	}
	var tf renocore.Transform
	if in.Preset != "" {
		tf = renocore.CamTransform(env.CanvasW, env.CanvasH, in.Preset)
	} else {
		cw, ch := w, h
		if cw == 0 && in.Monitor != nil {
			// dead, but the resolved monitor's geometry still beats garbage
			// for the crop math -- reporting the source is the caller's job
			cw, ch = in.Monitor.Width, in.Monitor.Height
		}
		var cropped bool
		tf, cropped = renocore.ItemTransform(env.CanvasW, env.CanvasH, in.Slot, in.Slots, cw, ch, in.Region)
		if in.Region != nil && !cropped {
			fmt.Fprintf(os.Stderr, "khudson reno: %s reports no source size yet; region %s skipped this run\n", in.Name, in.Region)
		}
	}
	tf.ScaleX, tf.ScaleY = renocore.ScaleInner(tf.BoundsW, tf.BoundsH, w, h)
	if err := obs.SetSceneItemTransform(scene, id, tf); err != nil {
		return false, fmt.Errorf("transform %s: %w", in.Name, err)
	}
	if w == 0 {
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
