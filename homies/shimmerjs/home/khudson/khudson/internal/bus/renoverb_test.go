package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// renoLSFixture builds an ls tree: windows[i] = {id, handle, focused}.
func renoLSFixture(wins ...[3]any) string {
	var b strings.Builder
	b.WriteString(`[{"id":1,"is_focused":true,"tabs":[{"id":1,"is_focused":true,"windows":[`)
	for i, w := range wins {
		if i > 0 {
			b.WriteString(",")
		}
		vars := "{}"
		if w[1].(string) != "" {
			vars = fmt.Sprintf(`{"reno_handle":%q}`, w[1])
		}
		fmt.Fprintf(&b, `{"id":%d,"title":"t%d","is_focused":%v,"user_vars":%s}`, w[0], w[0], w[2], vars)
	}
	b.WriteString(`]}]}]`)
	return b.String()
}

// renoTestVerbs wires a RenoVerbs against a scripted kitten: each call
// consumes the next canned response; every argv is recorded.
func renoTestVerbs(t *testing.T, responses ...string) (*RenoVerbs, *[]string) {
	t.Helper()
	dir := t.TempDir()
	calls := &[]string{}
	i := 0
	v := &RenoVerbs{
		Socket:     "sock",
		EventsPath: filepath.Join(dir, "events.jsonl"),
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			*calls = append(*calls, strings.Join(args, " "))
			if i >= len(responses) {
				t.Fatalf("kitten called %d times, scripted %d: %v", i+1, len(responses), *calls)
			}
			r := responses[i]
			i++
			if strings.HasPrefix(r, "ERR:") {
				return nil, fmt.Errorf("%s", r[4:])
			}
			return []byte(r), nil
		},
		openStart: func(context.Context, []string) error { return nil },
		now:       func() time.Time { return time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC) },
	}
	return v, calls
}

func readEvents(t *testing.T, v *RenoVerbs) []renoEvent {
	t.Helper()
	b, err := os.ReadFile(v.EventsPath)
	if err != nil {
		t.Fatalf("events journal: %v", err)
	}
	var out []renoEvent
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e renoEvent
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

func TestFocusSessionConfirmed(t *testing.T) {
	corr := RenoCorr{Run: "r1", Step: 2, Act: 0}
	v, calls := renoTestVerbs(t,
		renoLSFixture([3]any{12, "work", false}),
		"", // focus-window
		renoLSFixture([3]any{12, "work", true}),
	)
	if err := v.FocusSession(context.Background(), "work", corr); err != nil {
		t.Fatalf("focus: %v", err)
	}
	if (*calls)[1] != "focus-window --match id:12" {
		t.Errorf("calls = %v", *calls)
	}
	ev := readEvents(t, v)
	if len(ev) != 1 || ev[0].Outcome != "ok" || ev[0].Run != "r1" || ev[0].Step != 2 {
		t.Errorf("events = %+v", ev)
	}
}

func TestFocusSessionMissAndAmbiguous(t *testing.T) {
	v, calls := renoTestVerbs(t, renoLSFixture([3]any{12, "", false}))
	err := v.FocusSession(context.Background(), "work", RenoCorr{Step: -1, Act: -1})
	if err == nil || !strings.Contains(err.Error(), "no window carries") {
		t.Fatalf("miss: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("miss must not act: %v", *calls)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "miss" {
		t.Errorf("events = %+v", ev)
	}

	v2, calls2 := renoTestVerbs(t, renoLSFixture([3]any{12, "work", false}, [3]any{13, "work", false}))
	err = v2.FocusSession(context.Background(), "work", RenoCorr{})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "window 12") || !strings.Contains(err.Error(), "window 13") {
		t.Fatalf("ambiguous: %v", err)
	}
	if len(*calls2) != 1 {
		t.Fatalf("ambiguous must not act: %v", *calls2)
	}
}

func TestFocusSessionUnconfirmed(t *testing.T) {
	v, _ := renoTestVerbs(t,
		renoLSFixture([3]any{12, "work", false}),
		"",
		renoLSFixture([3]any{12, "work", false}), // still not focused
	)
	err := v.FocusSession(context.Background(), "work", RenoCorr{})
	if err == nil || !strings.Contains(err.Error(), "not focused") {
		t.Fatalf("unconfirmed: %v", err)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "unconfirmed" {
		t.Errorf("events = %+v", ev)
	}
}

func TestLaunchSession(t *testing.T) {
	// ensure-semantics: one already-planted window is success (prev/goto
	// re-enter launch steps), never a mid-show failure
	v, calls := renoTestVerbs(t, renoLSFixture([3]any{12, "aux", false}))
	if err := v.LaunchSession(context.Background(), "aux", "", false, nil, RenoCorr{}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("ensure must not launch again: %v", *calls)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "ok" || !strings.Contains(ev[0].Detail, "already planted") {
		t.Fatalf("ensure event = %+v", ev)
	}

	// two planted windows is the ambiguity every later focus trips on
	v1, calls1 := renoTestVerbs(t, renoLSFixture([3]any{12, "aux", false}, [3]any{13, "aux", false}))
	err := v1.LaunchSession(context.Background(), "aux", "", false, nil, RenoCorr{})
	if err == nil || !strings.Contains(err.Error(), "already planted 2 times") {
		t.Fatalf("dup: %v", err)
	}
	if len(*calls1) != 1 {
		t.Fatalf("dup must not launch: %v", *calls1)
	}

	// happy path: launch argv shape + post-ls confirmation
	v2, calls2 := renoTestVerbs(t,
		renoLSFixture([3]any{12, "", false}),
		"", // launch
		renoLSFixture([3]any{12, "", false}, [3]any{20, "aux", false}),
	)
	if err := v2.LaunchSession(context.Background(), "aux", "/tmp", true, []string{"claude"}, RenoCorr{Run: "r2"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	want := "launch --type tab --var reno_handle=aux --cwd /tmp --keep-focus claude"
	if (*calls2)[1] != want {
		t.Errorf("launch argv = %q, want %q", (*calls2)[1], want)
	}
	ev := readEvents(t, v2)
	if ev[0].Outcome != "ok" || !strings.Contains(ev[0].Detail, "window 20") {
		t.Errorf("events = %+v", ev)
	}
}

func TestOpenURL(t *testing.T) {
	v, _ := renoTestVerbs(t)
	var opened []string
	v.openStart = func(_ context.Context, argv []string) error {
		opened = append(opened, strings.Join(argv, " "))
		return nil
	}
	if err := v.OpenURL(context.Background(), "https://x/y", RenoCorr{}); err != nil {
		t.Fatal(err)
	}
	if len(opened) != 1 || opened[0] != "open https://x/y" {
		t.Errorf("opened = %v", opened)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "ok" {
		t.Errorf("events = %+v", ev)
	}
}

// A verb whose action lands but whose journal write fails must exit
// nonzero: the events journal backs wait guards and post-mortems, so a
// silent miss is worse than a dead step (deliberate delta from
// claudeverb's swallowed logf errors).
func TestJournalFailureFailsTheVerb(t *testing.T) {
	v, _ := renoTestVerbs(t)
	blocker := filepath.Join(t.TempDir(), "flat")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	v.EventsPath = filepath.Join(blocker, "events.jsonl") // parent is a file: writes fail
	err := v.OpenURL(context.Background(), "https://x", RenoCorr{})
	if err == nil || !strings.Contains(err.Error(), "events journal") {
		t.Fatalf("journal failure must fail the verb: %v", err)
	}
}

// fakeObs records transport ops; scripted failures by op name. The
// scene-plan surface serves canned environment data (renoscene_test.go
// wires it; the base fake answers empty).
type fakeObs struct {
	ops     []string
	vcam    bool
	fail    map[string]error
	canDir  bool
	version string
	output  string

	kinds     []string
	monitors  []renocore.Monitor
	canvasW   float64
	canvasH   float64
	canvasFPS float64
	scenes    []string

	filterKinds  []string
	srcFilters   map[string][]string
	batches      [][]renocore.FXRequest
	batchBudgets []time.Duration
	inputs       map[string]string                  // name -> kind
	props        map[string][]renocore.PropertyItem // "input/prop" -> items
	sizes        map[string][2]int                  // input -> source w,h
	itemIDs      map[string]int                     // "scene/source" -> id
	enabled      map[int]bool
	tfs          map[int]renocore.Transform
	mutes        map[string]bool
	settings     map[string]map[string]any
}

func (f *fakeObs) op(name string) error {
	f.ops = append(f.ops, name)
	if f.fail != nil {
		return f.fail[name]
	}
	return nil
}
func (f *fakeObs) Version() string                     { return f.version }
func (f *fakeObs) CanSetRecordDirectory() bool         { return f.canDir }
func (f *fakeObs) SetRecordDirectory(dir string) error { return f.op("dir:" + dir) }
func (f *fakeObs) SetScene(name string) error          { return f.op("scene:" + name) }
func (f *fakeObs) StartRecord() error                  { return f.op("start") }
func (f *fakeObs) StopRecord() (string, error)         { return f.output, f.op("stop") }
func (f *fakeObs) PauseRecord() error                  { return f.op("pause") }
func (f *fakeObs) ResumeRecord() error                 { return f.op("resume") }
func (f *fakeObs) Close() error                        { return f.op("close") }

func (f *fakeObs) InputKinds() ([]string, error) { return f.kinds, f.op("kinds") }
func (f *fakeObs) Monitors() ([]renocore.Monitor, error) {
	return f.monitors, f.op("monitors")
}
func (f *fakeObs) Canvas() (float64, float64, float64, error) {
	return f.canvasW, f.canvasH, f.canvasFPS, f.op("canvas")
}
func (f *fakeObs) Scenes() ([]string, error) { return f.scenes, f.op("scenes") }
func (f *fakeObs) CreateScene(name string) error {
	f.scenes = append(f.scenes, name)
	return f.op("create-scene:" + name)
}
func (f *fakeObs) Inputs() (map[string]string, error) {
	if f.inputs == nil {
		f.inputs = map[string]string{}
	}
	return f.inputs, f.op("inputs")
}
func (f *fakeObs) CreateInput(scene, name, kind string, settings map[string]any, enabled bool) error {
	if f.inputs == nil {
		f.inputs = map[string]string{}
	}
	f.inputs[name] = kind
	if f.settings == nil {
		f.settings = map[string]map[string]any{}
	}
	f.settings[name] = settings
	return f.op("create-input:" + name)
}
func (f *fakeObs) SetInputSettings(name string, settings map[string]any) error {
	if f.settings == nil {
		f.settings = map[string]map[string]any{}
	}
	f.settings[name] = settings
	return f.op("settings:" + name)
}
func (f *fakeObs) SetInputMute(name string, muted bool) error {
	if f.mutes == nil {
		f.mutes = map[string]bool{}
	}
	f.mutes[name] = muted
	return f.op(fmt.Sprintf("mute:%s=%v", name, muted))
}
func (f *fakeObs) RemoveInput(name string) error {
	delete(f.inputs, name)
	return f.op("remove:" + name)
}
func (f *fakeObs) PropertyItems(input, prop string) ([]renocore.PropertyItem, error) {
	return f.props[input+"/"+prop], f.op("props:" + input + "/" + prop)
}
func (f *fakeObs) SceneItemID(scene, source string, ensure bool) (int, error) {
	if id, ok := f.itemIDs[scene+"/"+source]; ok {
		return id, f.op("item:" + source)
	}
	if !ensure {
		return 0, fmt.Errorf("no item %s in %s", source, scene)
	}
	if f.itemIDs == nil {
		f.itemIDs = map[string]int{}
	}
	id := len(f.itemIDs) + 1
	f.itemIDs[scene+"/"+source] = id
	return id, f.op("item:" + source)
}
func (f *fakeObs) SetSceneItemEnabled(scene string, id int, enabled bool) error {
	if f.enabled == nil {
		f.enabled = map[int]bool{}
	}
	f.enabled[id] = enabled
	return f.op(fmt.Sprintf("enable:%d=%v", id, enabled))
}
func (f *fakeObs) ItemSourceSize(scene string, id int) (int, int, error) {
	for src, itemID := range f.itemIDs {
		if itemID == id {
			wh := f.sizes[strings.TrimPrefix(src, scene+"/")]
			return wh[0], wh[1], f.op(fmt.Sprintf("size:%d", id))
		}
	}
	return 0, 0, f.op(fmt.Sprintf("size:%d", id))
}
func (f *fakeObs) SetSceneItemTransform(scene string, id int, tr renocore.Transform) error {
	if f.tfs == nil {
		f.tfs = map[int]renocore.Transform{}
	}
	f.tfs[id] = tr
	return f.op(fmt.Sprintf("transform:%d", id))
}

func (f *fakeObs) ItemTransform(scene string, id int) (renocore.Transform, error) {
	return f.tfs[id], f.op(fmt.Sprintf("get-transform:%d", id))
}

func (f *fakeObs) FilterKinds() ([]string, error) { return f.filterKinds, f.op("filter-kinds") }

func (f *fakeObs) SourceFilters(source string) ([]string, error) {
	return f.srcFilters[source], f.op("filters:" + source)
}

func (f *fakeObs) RunFrameBatch(id string, reqs []renocore.FXRequest, budget time.Duration) error {
	f.batches = append(f.batches, reqs)
	f.batchBudgets = append(f.batchBudgets, budget)
	return f.op("batch")
}

// scriptedCtl returns arm data then records notes. record-phase is skipped
// deliberately: it is readout decoration on the way to the first frame, not
// a journal note, and folding it into the same list would let a UI tweak
// break every assertion about what the run actually recorded.
func scriptedCtl(t *testing.T, dir string) (CtlCall, *[][]string) {
	t.Helper()
	notes := &[][]string{}
	return func(args []string) (json.RawMessage, error) {
		if args[0] == "record-open" {
			raw, _ := json.Marshal(RecordOpen{Run: "r9", Directory: dir, Scene: "main"})
			return raw, nil
		}
		if args[0] == "record-phase" {
			return nil, nil
		}
		*notes = append(*notes, args)
		return nil, nil
	}, notes
}

func TestRecordVerbFlow(t *testing.T) {
	v, _ := renoTestVerbs(t)
	dir := filepath.Join(t.TempDir(), "ws")
	ctl, notes := scriptedCtl(t, dir)
	obs := &fakeObs{canDir: true, version: "31.0.2"}
	if err := v.Record(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatalf("record: %v", err)
	}
	want := []string{"dir:" + dir, "scene:main", "start", "close"}
	if strings.Join(obs.ops, ",") != strings.Join(want, ",") {
		t.Errorf("ops = %v, want %v", obs.ops, want)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("workspace dir must exist: %v", err)
	}
	if len(*notes) != 1 || (*notes)[0][0] != "record-note" || (*notes)[0][1] != "r9" || (*notes)[0][2] != "started" || (*notes)[0][3] != "31.0.2" {
		t.Errorf("notes = %v", *notes)
	}
	ev := readEvents(t, v)
	if ev[0].Outcome != "ok" || ev[0].Run != "r9" {
		t.Errorf("event = %+v", ev[0])
	}

	// start failure notes back as failed and exits nonzero
	v2, _ := renoTestVerbs(t)
	ctl2, notes2 := scriptedCtl(t, dir)
	obs2 := &fakeObs{canDir: true, fail: map[string]error{"start": fmt.Errorf("no output active")}}
	if err := v2.Record(func() (ObsTransport, error) { return obs2, nil }, ctl2, RenoCorr{}); err == nil {
		t.Fatal("failed start must fail the verb")
	}
	if len(*notes2) != 1 || (*notes2)[0][1] != "r9" || (*notes2)[0][2] != "failed" {
		t.Errorf("failure note = %v", *notes2)
	}
}

func TestObsOpStopNotesOutput(t *testing.T) {
	v, _ := renoTestVerbs(t)
	ctl, notes := scriptedCtl(t, "")
	obs := &fakeObs{output: "/clips/take1.mkv"}
	if err := v.ObsOp("stop", "", func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{Run: "r9", Step: -1, Act: -1}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if len(*notes) != 1 || (*notes)[0][1] != "r9" || (*notes)[0][2] != "stopped" || (*notes)[0][3] != "/clips/take1.mkv" {
		t.Errorf("notes = %v", *notes)
	}
	if err := v.ObsOp("scene", "wide", func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatalf("scene: %v", err)
	}
	if obs.ops[len(obs.ops)-2] != "scene:wide" {
		t.Errorf("ops = %v", obs.ops)
	}
}

func TestChromeTab(t *testing.T) {
	v, _ := renoTestVerbs(t)
	restore := osaRun
	t.Cleanup(func() { osaRun = restore })

	var gotArgs []string
	osaRun = func(_ context.Context, script string, args ...string) (string, error) {
		if !strings.Contains(script, "active tab index") {
			t.Errorf("unexpected script: %.60s", script)
		}
		gotArgs = args
		return "window 2 tab 5", nil
	}
	if err := v.ChromeTab(context.Background(), "slides.example", RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatal(err)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "slides.example" {
		t.Errorf("args = %v", gotArgs)
	}
	ev := readEvents(t, v)
	if ev[0].Outcome != "ok" || ev[0].Detail != "window 2 tab 5" {
		t.Errorf("event = %+v", ev[0])
	}

	osaRun = func(context.Context, string, ...string) (string, error) {
		return "", fmt.Errorf("execution error: no chrome tab matches nope (-2700)")
	}
	err := v.ChromeTab(context.Background(), "nope", RenoCorr{})
	if err == nil {
		t.Fatal("miss must fail the verb")
	}
	if ev := readEvents(t, v); ev[len(ev)-1].Outcome != "miss" {
		t.Errorf("miss event = %+v", ev[len(ev)-1])
	}
}

// applyCtl serves one apply-begin payload then records the notes.
func applyCtl(t *testing.T, cfg *renocore.RecordConfig, beginErr error) (CtlCall, *[][]string) {
	t.Helper()
	notes := &[][]string{}
	return func(args []string) (json.RawMessage, error) {
		if args[0] == "apply-begin" {
			if beginErr != nil {
				return nil, beginErr
			}
			raw, _ := json.Marshal(ApplyRequest{Config: cfg})
			return raw, nil
		}
		*notes = append(*notes, args)
		return nil, nil
	}, notes
}

func TestApplyConfigDrivesTheScene(t *testing.T) {
	fakeCGDisplays(t)
	v, _ := renoTestVerbs(t)
	cfg := &renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "built-in"}},
		Audio:   renocore.AudioNone,
	}
	ctl, notes := applyCtl(t, cfg, nil)
	obs := sceneFake()
	if err := v.ApplyConfig(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if obs.ops[len(obs.ops)-2] != "scene:reno" {
		t.Errorf("apply must switch to the reno scene: %v", obs.ops)
	}
	if len(*notes) != 1 || (*notes)[0][0] != "apply-note" || (*notes)[0][1] != "ok" || (*notes)[0][2] != cfg.Summary() {
		t.Errorf("notes = %v", *notes)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "ok" || ev[0].Detail != cfg.Summary() {
		t.Errorf("event = %+v", ev[0])
	}
}

// An empty config must not even dial: an untouched pad never seizes OBS.
func TestApplyConfigEmptyNeverDials(t *testing.T) {
	v, _ := renoTestVerbs(t)
	ctl, notes := applyCtl(t, nil, nil)
	dial := func() (ObsTransport, error) { t.Fatal("empty config must not dial"); return nil, nil }
	if err := v.ApplyConfig(dial, ctl, RenoCorr{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(*notes) != 1 || (*notes)[0][1] != "ok" || !strings.Contains((*notes)[0][2], "unconfigured") {
		t.Errorf("notes = %v", *notes)
	}
}

// OBS down between takes is the ordinary state, not a fault: the note
// says ok and the verb exits zero, so the pad never cries wolf.
func TestApplyConfigColdObsIsNotAFailure(t *testing.T) {
	v, _ := renoTestVerbs(t)
	ctl, notes := applyCtl(t, &renocore.RecordConfig{Audio: renocore.AudioNone}, nil)
	dial := func() (ObsTransport, error) { return nil, fmt.Errorf("connection refused") }
	if err := v.ApplyConfig(dial, ctl, RenoCorr{}); err != nil {
		t.Fatalf("cold obs must not fail the verb: %v", err)
	}
	if len(*notes) != 1 || (*notes)[0][1] != "ok" || !strings.Contains((*notes)[0][2], "cold") {
		t.Errorf("notes = %v", *notes)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "ok" {
		t.Errorf("event = %+v", ev[0])
	}
}

func TestApplyConfigFailureNotesBack(t *testing.T) {
	fakeCGDisplays(t)
	v, _ := renoTestVerbs(t)
	// a display the local enumeration cannot resolve: the plan refuses
	ctl, notes := applyCtl(t, &renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: "no-such-display"}},
	}, nil)
	obs := sceneFake()
	if err := v.ApplyConfig(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{}); err == nil {
		t.Fatal("an unresolvable display must fail the verb")
	}
	if len(*notes) != 1 || (*notes)[0][1] != "error" {
		t.Errorf("notes = %v", *notes)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "error" {
		t.Errorf("event = %+v", ev[0])
	}
}

// A begin that never lands leaves OBS untouched and is journaled: there
// is no config to drive, so guessing one would be worse than failing.
func TestApplyConfigBeginFailure(t *testing.T) {
	v, _ := renoTestVerbs(t)
	ctl, notes := applyCtl(t, nil, fmt.Errorf("ctl closed"))
	dial := func() (ObsTransport, error) { t.Fatal("must not dial without a config"); return nil, nil }
	if err := v.ApplyConfig(dial, ctl, RenoCorr{}); err == nil {
		t.Fatal("a failed begin must fail the verb")
	}
	if len(*notes) != 0 {
		t.Errorf("nothing to note back: %v", *notes)
	}
	if ev := readEvents(t, v); ev[0].Outcome != "error" || !strings.Contains(ev[0].Detail, "begin") {
		t.Errorf("event = %+v", ev[0])
	}
}

// open-clip is the pad's one file-opening verb, so the containment check is
// the whole verb. A prefix test on the UNRESOLVED path is the failure worth
// naming: a symlink sitting inside the workspace passes it while pointing
// anywhere on the disk, and the tap that follows it comes from a published
// bus argv, not a hand-typed one.
func TestOpenClipRefusesAnythingOutsideTheWorkspace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := filepath.Join(home, "Library", "Application Support", "khudson", "reno", "workspace")
	if err := os.MkdirAll(filepath.Join(ws, "demo", "r1"), 0o700); err != nil {
		t.Fatal(err)
	}
	take := filepath.Join(ws, "demo", "r1", "take.mkv")
	if err := os.WriteFile(take, []byte("frames"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(home, "secrets.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	escape := filepath.Join(ws, "demo", "r1", "escape.mkv")
	if err := os.Symlink(outside, escape); err != nil {
		t.Fatal(err)
	}

	open := func(t *testing.T, clip string) (opened []string, err error) {
		t.Helper()
		v, _ := renoTestVerbs(t)
		v.openStart = func(_ context.Context, argv []string) error {
			opened = argv
			return nil
		}
		return opened, v.OpenClip(context.Background(), clip, RenoCorr{Step: -1, Act: -1})
	}

	// a real take opens, and the RESOLVED path is what gets handed over
	v, _ := renoTestVerbs(t)
	var got []string
	v.openStart = func(_ context.Context, argv []string) error { got = argv; return nil }
	if err := v.OpenClip(context.Background(), take, RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatalf("open-clip on a real take: %v", err)
	}
	if len(got) != 2 || got[0] != "open" || filepath.Base(got[1]) != "take.mkv" {
		t.Errorf("opened %v, want open <take.mkv>", got)
	}

	for _, tc := range []struct{ what, clip string }{
		{"a path plainly outside the workspace", outside},
		{"a parent-dir escape", filepath.Join(ws, "..", "..", "secrets.txt")},
		{"a symlink inside the workspace pointing out of it", escape},
		{"a path that does not exist", filepath.Join(ws, "demo", "r1", "ghost.mkv")},
	} {
		opened, err := open(t, tc.clip)
		if err == nil {
			t.Errorf("%s must be refused: %s", tc.what, tc.clip)
		}
		if opened != nil {
			t.Errorf("%s reached `open`: %v", tc.what, opened)
		}
	}
}

// The virtual camera is the one thing that can hold the camera with nothing
// on the pad accounting for it, so the verb behind its tile has to report
// what it found in every case -- including the two where there is nothing to
// stop, which are exactly the cases a silent verb would leave looking idle.
func TestVirtualCamReadsBackAndStops(t *testing.T) {
	call := func(t *testing.T, op string, obs *fakeObs, dialErr error) [][]string {
		t.Helper()
		v, _ := renoTestVerbs(t)
		ctl, notes := scriptedCtl(t, "")
		dial := func() (ObsTransport, error) {
			if dialErr != nil {
				return nil, dialErr
			}
			return obs, nil
		}
		if err := v.VirtualCam(op, dial, ctl, RenoCorr{Step: -1, Act: -1}); err != nil {
			t.Fatalf("vcam %s: %v", op, err)
		}
		return *notes
	}

	// running, and stop takes it down
	obs := &fakeObs{vcam: true}
	if got := call(t, "stop", obs, nil); len(got) != 1 || got[0][1] != "off" {
		t.Errorf("stop on a live vcam noted %v, want off", got)
	}
	if !slices.Contains(obs.ops, "vcam-stop") {
		t.Errorf("stop never reached the output: %v", obs.ops)
	}

	// running, and status reports without touching it -- a readout that
	// stopped things would make the CLI seam unusable for a check
	obs = &fakeObs{vcam: true}
	if got := call(t, "status", obs, nil); len(got) != 1 || got[0][1] != "on" {
		t.Errorf("status on a live vcam noted %v, want on", got)
	}
	if slices.Contains(obs.ops, "vcam-stop") {
		t.Errorf("status must not stop the output: %v", obs.ops)
	}

	// idle: still a note. Nothing to stop is a result, not a reason to
	// leave the tile showing whatever it showed before
	if got := call(t, "stop", &fakeObs{}, nil); len(got) != 1 || got[0][1] != "off" {
		t.Errorf("idle vcam noted %v, want off", got)
	}

	// cold OBS is a definite off, not an unknown: the virtual camera output
	// is OBS's, so no OBS means nothing is running it
	if got := call(t, "stop", nil, fmt.Errorf("connection refused")); len(got) != 1 ||
		got[0][1] != "off" || got[0][2] != "obs cold" {
		t.Errorf("cold obs noted %v, want off/obs cold", got)
	}

	// a status read that failed is UNKNOWN on glass, never a clean idle
	v, _ := renoTestVerbs(t)
	ctl, notes := scriptedCtl(t, "")
	broken := &fakeObs{fail: map[string]error{"vcam-status": fmt.Errorf("socket gone")}}
	if err := v.VirtualCam("stop", func() (ObsTransport, error) { return broken, nil }, ctl, RenoCorr{}); err == nil {
		t.Fatal("a failed status read must fail the verb")
	}
	if got := *notes; len(got) != 1 || got[0][1] != "error" {
		t.Errorf("failed read noted %v, want an error note", got)
	}

	// and the op is checked before anything is dialled
	v2, _ := renoTestVerbs(t)
	ctl2, notes2 := scriptedCtl(t, "")
	if err := v2.VirtualCam("start", func() (ObsTransport, error) {
		t.Fatal("an unknown op must not dial")
		return nil, nil
	}, ctl2, RenoCorr{}); err == nil {
		t.Fatal("vcam start must be refused -- nothing here ever starts one")
	}
	if len(*notes2) != 0 {
		t.Errorf("a refused op must not note a state: %v", *notes2)
	}
}

func (f *fakeObs) VirtualCamActive() (bool, error) { return f.vcam, f.op("vcam-status") }
func (f *fakeObs) StopVirtualCam() error {
	f.vcam = false
	return f.op("vcam-stop")
}
