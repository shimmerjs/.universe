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

	if env.CanvasW, env.CanvasH, err = obs.Canvas(); err != nil {
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
		for i, d := range localDisplays() {
			val := d.UUID
			if env.DisplayNumeric {
				val = fmt.Sprint(d.ID)
			}
			name := displayLabel(obsMonitors, d, i)
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
func ApplyRecordConfig(obs ObsTransport, cfg renocore.RecordConfig) error {
	env, err := ProbeEnv(obs)
	if err != nil {
		return err
	}
	plan, err := renocore.BuildScenePlan(cfg, env)
	if err != nil {
		return err
	}
	for _, in := range plan.Inputs {
		if _, ok := env.Inputs[in.Name]; !ok {
			if err := obs.CreateInput(plan.Scene, in.Name, in.Kind, in.Settings, in.Enabled); err != nil {
				return fmt.Errorf("create %s: %w", in.Name, err)
			}
		} else if in.Settings != nil {
			if err := obs.SetInputSettings(in.Name, in.Settings); err != nil {
				return fmt.Errorf("settings %s: %w", in.Name, err)
			}
		}
		id, err := obs.SceneItemID(plan.Scene, in.Name, true)
		if err != nil {
			return fmt.Errorf("item %s: %w", in.Name, err)
		}
		if err := obs.SetSceneItemEnabled(plan.Scene, id, in.Enabled); err != nil {
			return fmt.Errorf("enable %s: %w", in.Name, err)
		}
		if err := applyTransform(obs, plan.Scene, id, in, env); err != nil {
			return err
		}
	}
	for _, name := range plan.RemoveInputs {
		if err := obs.RemoveInput(name); err != nil {
			return fmt.Errorf("remove stale %s: %w", name, err)
		}
	}
	for name, muted := range plan.Mutes {
		if err := obs.SetInputMute(name, muted); err != nil {
			return fmt.Errorf("mute %s: %w", name, err)
		}
	}
	if err := obs.SetScene(plan.Scene); err != nil {
		return fmt.Errorf("switch to %s: %w", plan.Scene, err)
	}
	return nil
}

// applyTransform positions one planned item: webcam presets and screen
// slots; audio items (no slot, no preset) have no geometry.
func applyTransform(obs ObsTransport, scene string, id int, in renocore.PlanInput, env renocore.ObsEnv) error {
	switch {
	case in.Preset != "":
		if err := obs.SetSceneItemTransform(scene, id, renocore.CamTransform(env.CanvasW, env.CanvasH, in.Preset)); err != nil {
			return fmt.Errorf("transform %s: %w", in.Name, err)
		}
	case in.Slots > 0:
		w, h, err := obs.ItemSourceSize(scene, id)
		if err != nil {
			return fmt.Errorf("source size %s: %w", in.Name, err)
		}
		if w == 0 && in.Monitor != nil {
			// source not streaming yet (first capture pre-TCC): the
			// resolved monitor's geometry stands in for the crop math
			w, h = in.Monitor.Width, in.Monitor.Height
		}
		tf, cropped := renocore.ItemTransform(env.CanvasW, env.CanvasH, in.Slot, in.Slots, w, h, in.Region)
		if in.Region != nil && !cropped {
			fmt.Fprintf(os.Stderr, "khudson reno: %s reports no source size yet; region %s skipped this run\n", in.Name, in.Region)
		}
		if err := obs.SetSceneItemTransform(scene, id, tf); err != nil {
			return fmt.Errorf("transform %s: %w", in.Name, err)
		}
	}
	return nil
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
