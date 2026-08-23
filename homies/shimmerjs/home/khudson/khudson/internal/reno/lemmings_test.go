package reno

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func lemmingsEnv() ObsEnv {
	return ObsEnv{
		CanvasW: 1920, CanvasH: 1080,
		ScreenKind: "screen_capture", CamKind: "macos-avcapture",
		DisplayProp:  "display_uuid",
		LemmingsKind: LemmingsKind,
		LemmingsFile: "/state/reno/lemmings.html",
		Scenes:       []string{SceneName},
		Inputs:       map[string]string{},
	}
}

// The overlay goes ON TOP, and getting it wrong is silent: a source under
// the thing it overlays renders invisibly and looks exactly like a source
// that never loaded. Stack is bottom-first, so the overlay is last.
func TestLemmingsStacksOverTheCamera(t *testing.T) {
	env := lemmingsEnv()
	env.Displays = []PropertyItem{{Name: "built-in", Value: "uuid-a"}}
	p, err := BuildScenePlan(RecordConfig{
		Screens:  []ScreenSel{{Display: "built-in"}},
		Webcam:   "br",
		Lemmings: true,
	}, env)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ScreenInputName(0), CamInput, LemmingsInput}
	if !slices.Equal(p.Stack, want) {
		t.Errorf("stack = %v, want %v bottom-first", p.Stack, want)
	}
	in := planInput(t, p, LemmingsInput)
	if !in.Enabled || in.Kind != LemmingsKind {
		t.Errorf("overlay input = %+v", in)
	}
	// it hangs on the camera, which is what makes the two boxes coincide
	// over a camera of any aspect
	if in.Overlay != CamInput {
		t.Errorf("overlay hangs on %q, want %q", in.Overlay, CamInput)
	}
	if in.Preset != "br" {
		t.Errorf("overlay preset = %q, want the camera's", in.Preset)
	}
}

// No camera, no box to hang on -- and the source goes DOWN rather than
// being placed somewhere arbitrary. Left enabled it is a page still
// rendering over a scene nobody asked it to.
func TestLemmingsWithoutACameraGoesDown(t *testing.T) {
	for _, webcam := range []string{"", "off"} {
		env := lemmingsEnv()
		env.Inputs[LemmingsInput] = LemmingsKind
		p, err := BuildScenePlan(RecordConfig{Webcam: webcam, Lemmings: true, Audio: AudioNone}, env)
		if err != nil {
			t.Fatalf("webcam %q: %v", webcam, err)
		}
		if slices.Contains(p.Stack, LemmingsInput) {
			t.Errorf("webcam %q: overlay stacked with nothing to hang on", webcam)
		}
		if in := planInput(t, p, LemmingsInput); in.Enabled {
			t.Errorf("webcam %q: overlay left enabled", webcam)
		}
	}

	// and switching it off takes down a source a previous posture left up
	env := lemmingsEnv()
	env.Inputs[LemmingsInput] = LemmingsKind
	p, err := BuildScenePlan(RecordConfig{Webcam: "br"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if in := planInput(t, p, LemmingsInput); in.Enabled {
		t.Error("lemmings off must disable the source, not ignore it")
	}

	// with no such source ever created there is nothing to say
	env2 := lemmingsEnv()
	p, err = BuildScenePlan(RecordConfig{Webcam: "br"}, env2)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range p.Inputs {
		if in.Name == LemmingsInput {
			t.Errorf("a never-created overlay must not enter the plan: %+v", in)
		}
	}
}

// obs-browser is bundled with OBS, but nix owns none of this OBS. The kind
// is capability-probed like every filter effect, and refuses by name --
// there is no second thing here that renders html to fall back to.
func TestLemmingsRefusesWithoutTheBrowserSource(t *testing.T) {
	env := lemmingsEnv()
	env.LemmingsKind = ""
	_, err := BuildScenePlan(RecordConfig{Webcam: "br", Lemmings: true}, env)
	if err == nil || !strings.Contains(err.Error(), LemmingsKind) {
		t.Errorf("err = %v, want it name the missing kind", err)
	}

	// and a page nobody wrote is the same class of refusal: an overlay
	// pointed at nothing renders a blank source over the camera
	env = lemmingsEnv()
	env.LemmingsFile = ""
	if _, err := BuildScenePlan(RecordConfig{Webcam: "br", Lemmings: true}, env); err == nil {
		t.Error("an unwritten page must refuse rather than load nothing")
	}
}

// Local file, never a url. That distinction IS the no-network guarantee:
// an overlay that fetched anything would render differently, or not at
// all, depending on what the wifi is doing mid-demo.
func TestLemmingsLoadsFromDiskAndNotTheNetwork(t *testing.T) {
	s := LemmingsSettings("/state/reno/lemmings.html")
	if s["is_local_file"] != true {
		t.Errorf("is_local_file = %v, want true", s["is_local_file"])
	}
	if s["local_file"] != "/state/reno/lemmings.html" {
		t.Errorf("local_file = %v", s["local_file"])
	}
	if _, ok := s["url"]; ok {
		t.Error("a url setting is a way for the overlay to reach the network")
	}
	// the page itself must be self-contained for the same reason
	for _, ref := range []string{"http://", "https://", "//fonts.", "src="} {
		if strings.Contains(lemmingsPage, ref) {
			t.Errorf("the page references %q -- it must fetch nothing", ref)
		}
	}
	// transparent, or the overlay is a grey rectangle over the camera
	css, _ := s["css"].(string)
	if !strings.Contains(css, "rgba(0, 0, 0, 0)") {
		t.Errorf("css = %q, want a transparent background", css)
	}
}

// The browser source points at this path, so rewriting the file under a
// live source is how the overlay blinks out mid-demo on an apply that
// changed nothing about it.
func TestWriteLemmingsPageSkipsAnIdenticalWrite(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteLemmingsPage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, LemmingsFile) {
		t.Errorf("path = %q", path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != lemmingsPage {
		t.Error("the written page is not the embedded one")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := WriteLemmingsPage(dir); err != nil {
		t.Fatal(err)
	}
	again, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(info.ModTime()) {
		t.Error("an identical page was rewritten; a live source would have reloaded")
	}

	// a page that HAS changed is written, or the overlay would be pinned to
	// whatever the first build ever put there
	if err := os.WriteFile(path, []byte("<html>stale</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteLemmingsPage(dir); err != nil {
		t.Fatal(err)
	}
	body, _ = os.ReadFile(path)
	if string(body) != lemmingsPage {
		t.Error("a stale page survived")
	}

	// nowhere to write is an error, not a silent empty path
	if _, err := WriteLemmingsPage(""); err == nil {
		t.Error("no state dir must refuse")
	}
}

// The overlay is reno's, so release disables it with everything else --
// walking away with a page rendering over an idle scene is the mirror of
// walking away with the camera light on.
func TestLemmingsIsARenoInput(t *testing.T) {
	if !IsRenoInput(LemmingsInput) {
		t.Error("release must reach the overlay")
	}
	// and it shows in the posture the pad and the journal report
	cfg := RecordConfig{Webcam: "br", Lemmings: true}
	if !strings.Contains(cfg.Summary(), "lemmings") {
		t.Errorf("summary = %q", cfg.Summary())
	}
	// on its own it is a configured posture: an overlay with no other knob
	// set still means the reno scene, not "obs as-is"
	if (RecordConfig{Lemmings: true}).Empty() {
		t.Error("lemmings alone must not read as an empty config")
	}
}

func planInput(t *testing.T, p *Plan, name string) PlanInput {
	t.Helper()
	for _, in := range p.Inputs {
		if in.Name == name {
			return in
		}
	}
	t.Fatalf("no %s input in the plan", name)
	return PlanInput{}
}
