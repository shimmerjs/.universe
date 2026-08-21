package reno

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

func testInventory() string {
	inv := renocore.ObsInventory{
		At:         time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC),
		ObsVersion: "32.2.0",
		Screens:    []renocore.PropertyItem{{Name: "Built-in", Value: "uuid-b"}, {Name: "DELL", Value: "uuid-d"}},
		Mics:       []renocore.PropertyItem{{Name: "MacBook Pro Microphone", Value: "uid-mbp"}},
		Cams:       []renocore.PropertyItem{{Name: "FaceTime HD", Value: "uid-cam"}},
	}
	raw, _ := json.Marshal(inv)
	return string(raw)
}

// freshInv installs an inventory whose age stays under invStale for the
// stepping test clock.
func freshInv(t *testing.T, m *Mod) {
	t.Helper()
	if _, _, err := m.HandleCtl([]string{"obs-inventory", testInventory()}); err != nil {
		t.Fatal(err)
	}
	m.inventory.At = m.now()
}

func TestRecordConfigCtl(t *testing.T) {
	m, _ := testMod(t)
	raw, _ := json.Marshal(renocore.RecordConfig{Audio: renocore.AudioNone, Webcam: "br"})
	if _, _, err := m.HandleCtl([]string{"record-config", string(raw)}); err != nil {
		t.Fatal(err)
	}
	if m.desired.Audio != renocore.AudioNone || m.desired.Webcam != "br" {
		t.Fatalf("pending = %+v", m.desired)
	}
	st, _ := ctl(t, m, "status")
	if !strings.Contains(st.(Status).Config, "audio none") {
		t.Errorf("status config = %q", st.(Status).Config)
	}
	if _, _, err := m.HandleCtl([]string{"record-config", `{"webcam":"sideways"}`}); err == nil {
		t.Error("bad preset must fail")
	}
	if _, _, err := m.HandleCtl([]string{"record-config", "not json"}); err == nil {
		t.Error("garbage must fail")
	}
	if _, _, err := m.HandleCtl([]string{"record-config", "--clear"}); err != nil {
		t.Fatal(err)
	}
	if !m.desired.Empty() {
		t.Errorf("clear left %+v", m.desired)
	}
}

func TestNotesCtl(t *testing.T) {
	m, _ := testMod(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("hello\n\nworld"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.HandleCtl([]string{"notes", path}); err != nil {
		t.Fatal(err)
	}
	if m.notes != "hello\n\nworld" || m.notesPath != path {
		t.Fatalf("notes = %q %q", m.notes, m.notesPath)
	}

	// the presenter renders scriptless notes as wrappable note rows
	d := m.presenterData()
	if d.Title != "reno: notes" {
		t.Errorf("title = %q", d.Title)
	}
	var noteRows int
	for _, r := range d.Rows {
		if r.Key == KeyNote {
			noteRows++
		}
	}
	if noteRows != 3 {
		t.Errorf("note rows = %d, want 3", noteRows)
	}

	if _, _, err := m.HandleCtl([]string{"notes", "relative.md"}); err == nil {
		t.Error("relative path must fail (crosses processes)")
	}
	if _, _, err := m.HandleCtl([]string{"notes", filepath.Join(dir, "missing.md")}); err == nil {
		t.Error("missing file must fail")
	}
	if _, _, err := m.HandleCtl([]string{"notes", "--clear"}); err != nil {
		t.Fatal(err)
	}
	if m.notes != "" || m.notesPath != "" {
		t.Error("clear left notes")
	}
}

func TestNotesSizeCap(t *testing.T) {
	m, _ := testMod(t)
	path := filepath.Join(t.TempDir(), "fat.md")
	if err := os.WriteFile(path, make([]byte, maxNotesBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.HandleCtl([]string{"notes", path}); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("fat notes must fail loudly: %v", err)
	}
}

// Ad hoc notes render inside a live scriptless run too (the mid-take
// presenter), not only at idle.
func TestNotesRenderDuringAdhocRun(t *testing.T) {
	m, _ := testMod(t)
	path := filepath.Join(t.TempDir(), "n.md")
	if err := os.WriteFile(path, []byte("say hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.HandleCtl([]string{"notes", path}); err != nil {
		t.Fatal(err)
	}
	if _, effects := m.HandleActEffects([]string{actRecord}); len(effects) != 1 {
		t.Fatal("record act must fire")
	}
	d := m.presenterData()
	var sawNote, sawRun bool
	for _, r := range d.Rows {
		if r.Key == KeyNote && r.Text == "say hi" {
			sawNote = true
		}
		if r.Key == "run" {
			sawRun = true
		}
	}
	if !sawNote || !sawRun {
		t.Errorf("mid-run notes view: note=%v run=%v rows=%+v", sawNote, sawRun, d.Rows)
	}
}

func TestCfgActs(t *testing.T) {
	m, _ := testMod(t)

	if handled, _ := m.HandleActEffects([]string{actCfgAudio, "uid-mbp"}); !handled || m.desired.Audio != "uid-mbp" {
		t.Fatalf("audio act: %+v", m.desired)
	}
	if _, _ = m.HandleActEffects([]string{actCfgAudio, cfgDefault}); m.desired.Audio != "" {
		t.Fatalf("audio default: %+v", m.desired)
	}
	if _, _ = m.HandleActEffects([]string{actCfgWebcam, "big"}); m.desired.Webcam != "big" {
		t.Fatalf("webcam act: %+v", m.desired)
	}
	// screen toggles membership
	m.HandleActEffects([]string{actCfgScreen, "uuid-b"})
	m.HandleActEffects([]string{actCfgScreen, "uuid-d"})
	if len(m.desired.Screens) != 2 {
		t.Fatalf("screens = %+v", m.desired.Screens)
	}
	m.HandleActEffects([]string{actCfgScreen, "uuid-b"})
	if len(m.desired.Screens) != 1 || m.desired.Screens[0].Display != "uuid-d" {
		t.Fatalf("toggle off = %+v", m.desired.Screens)
	}

	// probe fires the exec'd probe verb with a unique key
	handled, effects := m.HandleActEffects([]string{actProbe})
	if !handled || len(effects) != 1 {
		t.Fatalf("probe act: %v %+v", handled, effects)
	}
	if got := strings.Join(effects[0].Argv, " "); got != "/inst/khudson reno obs probe" {
		t.Errorf("probe argv = %q", got)
	}
	_, effects2 := m.HandleActEffects([]string{actProbe})
	if effects[0].Key == effects2[0].Key {
		t.Error("probe keys must be unique per emission")
	}
}

// Arm picks the script's config for scripted recording runs and the
// pending knobs for ad hoc -- a leftover ad hoc config must never
// hijack a show.
func TestArmConfigSelection(t *testing.T) {
	m, _ := testMod(t)
	m.desired = renocore.RecordConfig{Audio: renocore.AudioNone}
	if _, effects := m.HandleActEffects([]string{actRecord}); len(effects) != 1 {
		t.Fatal("record act must fire")
	}
	if cfg := m.run.Recording.Config; cfg == nil || cfg.Audio != renocore.AudioNone {
		t.Fatalf("ad hoc arm config = %+v", m.run.Recording.Config)
	}
	ctl(t, m, "stop")

	// scripted: header config wins, pending ignored
	dir := t.TempDir()
	path := filepath.Join(dir, "s.txtar")
	src := "demo show\nrecord\nscreen 1\nstep a\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	_, effects := m.HandleActEffects([]string{actNext})
	if len(effects) == 0 {
		t.Fatal("first advance must fire the record effect")
	}
	cfg := m.run.Recording.Config
	if cfg == nil || len(cfg.Screens) != 1 || cfg.Screens[0].Display != "1" || cfg.Audio != "" {
		t.Fatalf("scripted arm config = %+v", cfg)
	}
}

// The recorder pad publishes one PRESSABLE button per CHOICE -- each
// display, each mic, each webcam preset -- grouped into labeled sections.
// A popover would make selecting a device a two-aim gesture on glass.
func TestRecorderPadChoiceButtons(t *testing.T) {
	m, _ := testMod(t)

	// nothing probed: the device button is the whole surface, and the pad
	// says so rather than offering bands that select nothing
	d := m.recorderData()
	probe := padTile(d.Rows, KeyActProbe)
	if probe == nil || probe.Act[0] != actProbe || probe.Value != "not probed" {
		t.Fatalf("no probe button without inventory: %+v", d.Rows)
	}
	if padTile(d.Rows, "cfg:unprobed") == nil {
		t.Errorf("pre-probe pad must say what is missing: %+v", d.Rows)
	}

	freshInv(t, m)
	m.desired = renocore.RecordConfig{Screens: []renocore.ScreenSel{{Display: "uuid-d"}}, Audio: "uid-mbp"}
	d = m.recorderData()

	// one button per display, the selected one lit, tap = direct toggle
	sel := padTile(d.Rows, KeyCfgScreens+":uuid-d")
	if sel == nil || sel.Text != "DELL" || sel.Style != module.StyleAccent {
		t.Errorf("selected display button = %+v", sel)
	}
	if len(sel.Act) != 2 || sel.Act[0] != actCfgScreen || sel.Act[1] != "uuid-d" {
		t.Errorf("display button act = %v", sel.Act)
	}
	if off := padTile(d.Rows, KeyCfgScreens+":uuid-b"); off == nil || off.Style == module.StyleAccent {
		t.Errorf("unselected display must not read lit: %+v", off)
	}

	// mic band: "no mic", "mute all", then a button per device. Both
	// silence reno's mic; they differ in whether the REST of OBS audio
	// goes quiet, so they stay two buttons -- but the labels name that
	// scope rather than the old mechanism-flavoured "obs as-is".
	noMic := padTile(d.Rows, KeyCfgAudio+":default")
	mute := padTile(d.Rows, KeyCfgAudio+":none")
	dev := padTile(d.Rows, KeyCfgAudio+":uid-mbp")
	if noMic == nil || mute == nil || dev == nil {
		t.Fatalf("mic band incomplete: %+v", d.Rows)
	}
	if noMic.Text != "no mic" || mute.Text != "mute all" {
		t.Errorf("mic labels = %q / %q", noMic.Text, mute.Text)
	}
	if noMic.Act[1] != cfgDefault || mute.Act[1] != renocore.AudioNone || dev.Act[1] != "uid-mbp" {
		t.Errorf("mic acts = %v / %v / %v", noMic.Act, mute.Act, dev.Act)
	}
	if dev.Style != module.StyleAccent || noMic.Style == module.StyleAccent {
		t.Errorf("the live selection is the lit one: no-mic=%q dev=%q", noMic.Style, dev.Style)
	}
	// the band label says MIC; the key does not need to repeat it
	if dev.Text != "MacBook Pro" {
		t.Errorf("device label = %q, want the trimmed form", dev.Text)
	}
	for _, tile := range []*module.Row{noMic, mute, dev} {
		if tile.Section != secMic {
			t.Errorf("mic button %q in section %q", tile.Text, tile.Section)
		}
	}

	// cam band: the presets and NOTHING else. Unset and "off" both leave
	// reno-cam disabled, so a separate as-is tile was a second button for
	// an identical outcome.
	if got := padTile(d.Rows, KeyCfgWebcam+":default"); got != nil {
		t.Errorf("cam band must not carry an as-is tile: %+v", got)
	}
	for _, p := range renocore.WebcamPresets {
		tile := padTile(d.Rows, KeyCfgWebcam+":"+p)
		if tile == nil || tile.Act[1] != p || tile.Section != secCam {
			t.Errorf("webcam preset %q button = %+v", p, tile)
		}
	}

	// an old list keeps every button and reports its AGE, not an alarm: a
	// device list an hour old is not wrong, and a warning that fired on a
	// timer taught the eye to skip the one tile that carries a real one
	m.inventory.At = m.now().Add(-2 * invStale)
	d = m.recorderData()
	if probe = padTile(d.Rows, KeyActProbe); probe == nil || probe.Style == module.StyleWarn {
		t.Errorf("age alone must not raise an alarm: %+v", probe)
	}
	if probe.Value != "2h old" {
		t.Errorf("probe tile value = %q, want the age", probe.Value)
	}
	if padTile(d.Rows, KeyCfgAudio+":uid-mbp") == nil {
		t.Errorf("stale inventory must keep its device buttons: %+v", d.Rows)
	}
}

// Every switch restarts the bus. An in-memory-only inventory meant the pad
// came back COLD after every system update, with the device bands gone --
// which is the ad hoc surface, so losing it loses the feature.
func TestObsInventorySurvivesRestart(t *testing.T) {
	m, script := testMod(t)
	freshInv(t, m)
	if padTile(m.recorderData().Rows, KeyCfgAudio+":uid-mbp") == nil {
		t.Fatal("probed inventory must produce device buttons")
	}

	next := &Mod{now: m.now, stateDir: filepath.Dir(script)}
	next.recoverInventory()
	if next.inventory == nil {
		t.Fatal("inventory cache did not survive the restart")
	}
	if got := padTile(next.recorderData().Rows, KeyCfgAudio+":uid-mbp"); got == nil {
		t.Errorf("recovered inventory must rebuild the device buttons: %+v", next.recorderData().Rows)
	}
	// the tile reports the LIST's age, not OBS: a recovered cache is a
	// probed one, however old, so it must never read as never-probed
	if got := padTile(next.recorderData().Rows, KeyActProbe); got == nil || got.Value == "not probed" {
		t.Errorf("recovered pad must not read unprobed: %+v", got)
	}
}

// The script's read-only config shows ONLY while the scripted run is live:
// with the script merely staged, a record tap arms an AD HOC run with the
// pending knobs, so the ad hoc bands must show (displaying the script's
// config there would lie about what is about to record).
func TestRecorderConfigRowsScripted(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.txtar")
	if err := os.WriteFile(path, []byte("demo show\nrecord\naudio none\nstep a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl(t, m, "load", path)

	d := m.recorderData()
	if padTile(d.Rows, KeyCfgAudio+":default") == nil || padTile(d.Rows, KeyCfgWebcam+":off") == nil {
		t.Errorf("staged-only view must show the ad hoc bands: %+v", d.Rows)
	}
	if tile := padTile(d.Rows, KeyCfgScript); tile != nil {
		t.Errorf("staged-only script leaked its config tile: %+v", tile)
	}

	// live scripted run: the script's config shows read-only, bands gone
	ctl(t, m, "start")
	d = m.recorderData()
	for _, key := range []string{KeyCfgAudio + ":default", KeyCfgWebcam + ":default", KeyCfgScreens + ":uuid-d"} {
		if tile := padTile(d.Rows, key); tile != nil {
			t.Errorf("scripted live view leaked %s: %+v", key, tile)
		}
	}
	script := padTile(d.Rows, KeyCfgScript)
	if script == nil || !strings.Contains(script.Value, "audio none") {
		t.Errorf("live scripted view must show the script config: %+v", d.Rows)
	}
	if len(script.Act) > 0 || len(script.Menu) > 0 {
		t.Errorf("the script config is a readout, not a button: %+v", script)
	}
	if padTile(d.Rows, KeyActEndRun) == nil {
		t.Error("a live run must keep its end-run button")
	}
}

func TestObsInventoryCtlValidates(t *testing.T) {
	m, _ := testMod(t)
	if _, _, err := m.HandleCtl([]string{"obs-inventory", "junk"}); err == nil {
		t.Error("garbage inventory must fail")
	}
	if _, _, err := m.HandleCtl([]string{"obs-inventory"}); err == nil {
		t.Error("missing payload must fail")
	}
}

// A pad tile is a BUTTON. The only tile allowed to ignore a tap is the
// scripted-config readout, which exists to say the knobs are not yours to
// turn right now. Anything else that paints like a key and swallows the
// press is a lie about the surface -- the "probe OBS first" tile shipped
// exactly that way.
func TestEveryPadTileIsPressable(t *testing.T) {
	readouts := map[string]bool{KeyCfgScript: true}

	check := func(t *testing.T, what string, rows []module.Row) {
		t.Helper()
		var tiles int
		for _, r := range rows {
			if r.Kind != module.RowPad {
				continue
			}
			tiles++
			if readouts[r.Key] {
				continue
			}
			if len(r.Act) == 0 && len(r.Menu) == 0 {
				t.Errorf("%s: tile %q (%q) takes no tap", what, r.Key, r.Text)
			}
		}
		if tiles == 0 {
			t.Errorf("%s: pad rendered no tiles at all", what)
		}
	}

	m, _ := testMod(t)
	check(t, "cold", m.recorderData().Rows)

	freshInv(t, m)
	check(t, "probed", m.recorderData().Rows)

	m.inventory.At = m.now().Add(-2 * invStale)
	check(t, "stale", m.recorderData().Rows)

	m2, _ := testMod(t)
	m2.HandleActEffects([]string{actRecord})
	check(t, "recording", m2.recorderData().Rows)
}

// Screens are the one part of an OBS enumeration that changes without
// warning -- and the one part that needs no OBS to re-derive, because the
// selectors come from CoreGraphics and OBS only ever supplied the label.
// So a moved display REPAIRS itself, and what earns a warning is the thing
// repair cannot fix: a configured display that is no longer attached.
func TestMovedDisplaysHealAndOrphansWarn(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	// the fixture inventory enumerates uuid-b and uuid-d
	m.local = []cgdisplay.Display{{UUID: "uuid-b"}, {UUID: "uuid-d"}}
	if m.healDisplaysLocked() {
		t.Error("a matching enumeration must heal nothing")
	}
	if got := padTile(m.recorderData().Rows, KeyActProbe); got == nil || got.Style == module.StyleWarn {
		t.Fatalf("matching display set must read healthy: %+v", got)
	}

	// unplug one. Poll heals before rendering, so the list follows the
	// hardware and the surviving display KEEPS the name the probe gave it
	m.local = []cgdisplay.Display{{UUID: "uuid-b"}}
	if !m.healDisplaysLocked() {
		t.Fatal("an unplugged display must heal the cached list")
	}
	if n := len(m.inventory.Screens); n != 1 {
		t.Fatalf("healed list has %d screens, want 1", n)
	}
	if got := m.inventory.Screens[0]; got.Value != "uuid-b" || got.Name == "" {
		t.Errorf("healed screen = %+v, want uuid-b with its probed label", got)
	}
	if got := padTile(m.recorderData().Rows, KeyActProbe); got == nil || got.Style == module.StyleWarn {
		t.Errorf("a healed list must not keep warning: %+v", got)
	}

	// but a SELECTED display that went away is not repairable -- it keeps a
	// tile of its own so the config cannot silently point at nothing
	m.desired.Screens = []renocore.ScreenSel{{Display: "uuid-d"}}
	orphan := padTile(m.recorderData().Rows, KeyCfgScreens+":uuid-d")
	if orphan == nil || len(orphan.Act) == 0 {
		t.Fatalf("an orphaned selection needs a live tile: %+v", m.recorderData().Rows)
	}
	if orphan.Style != module.StyleWarn || orphan.Value != "not attached" {
		t.Errorf("orphaned screen tile = %+v, want a warning", orphan)
	}
	// and tapping it clears the selection
	m.handleCfgActLocked(orphan.Act)
	if len(m.desired.Screens) != 0 {
		t.Errorf("tapping an orphan must drop it: %+v", m.desired.Screens)
	}

	// a brand new display arrives with a synthesized label, not a blank
	m.local = []cgdisplay.Display{{UUID: "uuid-b"}, {UUID: "uuid-new", W: 1920, H: 1080}}
	if !m.healDisplaysLocked() {
		t.Fatal("a swapped-in display must heal the cached list")
	}
	var found bool
	for _, s := range m.inventory.Screens {
		if s.Value == "uuid-new" {
			found = s.Name != ""
		}
	}
	if !found {
		t.Errorf("new display missing a label: %+v", m.inventory.Screens)
	}
}

// No enumeration is not evidence of a change: a bare test bus, or a CG
// call that came back empty, must not paint the pad red.
func TestNoEnumerationIsNotAMove(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.local = nil
	if got := padTile(m.recorderData().Rows, KeyActProbe); got == nil || got.Style == module.StyleWarn {
		t.Errorf("absent enumeration must not read as moved: %+v", got)
	}
}

// applyArgv is the live-apply effect argv one config change emits.
func applyArgv(m *Mod) []string { return []string{m.exe, "reno", "obs", "apply"} }

// A config tap drives OBS right then: the pad is a mixer, not a form.
func TestCfgActAppliesLive(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	handled, effects := m.HandleActEffects([]string{actCfgScreen, "uuid-d"})
	if !handled {
		t.Fatal("screen tap unhandled")
	}
	if len(effects) != 1 {
		t.Fatalf("effects = %+v, want one apply", effects)
	}
	if got := effects[0].Argv; !slices.Equal(got, applyArgv(m)) {
		t.Errorf("argv = %v, want %v", got, applyArgv(m))
	}
	// the CLI seam is the same core, so it applies identically
	m2, _ := testMod(t)
	raw, _ := json.Marshal(renocore.RecordConfig{Webcam: "br"})
	_, effects = ctl(t, m2, "record-config", string(raw))
	if len(effects) != 1 || !slices.Equal(effects[0].Argv, applyArgv(m2)) {
		t.Errorf("record-config effects = %+v, want one apply", effects)
	}
}

// An untouched pad means untouched OBS: no apply, and the readout says
// so rather than implying a scene got torn down.
func TestEmptyConfigAppliesNothing(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgWebcam, "br"})
	if _, effects := m.HandleActEffects([]string{actCfgWebcam, cfgDefault}); len(effects) != 0 {
		t.Fatalf("clearing to empty emitted %+v", effects)
	}
	if !m.desired.Empty() {
		t.Fatalf("pending = %+v", m.desired)
	}
	if m.applyFail || !strings.Contains(m.applyNote, "unconfigured") {
		t.Errorf("note = %q fail=%v", m.applyNote, m.applyFail)
	}
}

// A burst of taps costs two applies, not one per tap: the second and
// later changes ride the dirty latch and the completing apply re-fires
// once with whatever the config ended up as.
func TestApplyCoalescesBurst(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	_, effects := m.HandleActEffects([]string{actCfgScreen, "uuid-d"})
	if len(effects) != 1 {
		t.Fatalf("first tap = %+v, want one apply", effects)
	}
	for _, argv := range [][]string{
		{actCfgScreen, "uuid-b"},
		{actCfgAudio, "uid-mbp"},
		{actCfgWebcam, "br"},
	} {
		if _, e := m.HandleActEffects(argv); len(e) != 0 {
			t.Fatalf("%v during an in-flight apply emitted %+v", argv, e)
		}
	}
	if !m.applyDirty {
		t.Fatal("changes under an in-flight apply must set the dirty latch")
	}

	// the verb starts: it reads the CURRENT config, so every queued tap
	// is already covered and the latch clears here
	req, _, err := m.HandleCtl([]string{"apply-begin"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := req.(ApplyRequest).Config
	if cfg == nil || len(cfg.Screens) != 2 || cfg.Audio != "uid-mbp" || cfg.Webcam != "br" {
		t.Fatalf("apply-begin config = %+v", cfg)
	}
	if m.applyDirty {
		t.Error("apply-begin must clear the dirty latch: it read the live config")
	}
	// nothing moved under it, so completing does not re-fire
	if _, effects = ctl(t, m, "apply-note", "ok", "screen x2"); len(effects) != 0 {
		t.Fatalf("clean completion re-fired: %+v", effects)
	}
	if m.applyLive || m.applyNote != "screen x2" {
		t.Errorf("live=%v note=%q", m.applyLive, m.applyNote)
	}

	// a tap AFTER begin but BEFORE the note is the case the latch exists
	// for: it must produce exactly one follow-up apply
	m.HandleActEffects([]string{actCfgScreen, "uuid-d"})
	m.HandleCtl([]string{"apply-begin"})
	m.HandleActEffects([]string{actCfgWebcam, "big"})
	_, effects = ctl(t, m, "apply-note", "ok", "done")
	if len(effects) != 1 || !slices.Equal(effects[0].Argv, applyArgv(m)) {
		t.Fatalf("dirty completion = %+v, want one follow-up apply", effects)
	}
}

// The apply-begin ctl can be lost (bus restart, killed child). The latch
// must not hold in-flight forever and silently stop applying taps.
func TestApplyLatchUnwedges(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	if _, effects := m.HandleActEffects([]string{actCfgScreen, "uuid-d"}); len(effects) != 1 {
		t.Fatal("first tap must apply")
	}
	if _, effects := m.HandleActEffects([]string{actCfgWebcam, "br"}); len(effects) != 0 {
		t.Fatal("second tap must latch, not apply")
	}
	m.applyAt = m.applyAt.Add(-applyWedge - time.Second)
	_, effects := m.HandleActEffects([]string{actCfgWebcam, "big"})
	if len(effects) != 1 {
		t.Fatalf("a wedged apply must not block later taps: %+v", effects)
	}
}

// A failed apply surfaces on glass and never leaves the latch set.
func TestApplyNoteFailure(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgScreen, "uuid-d"})
	ctl(t, m, "apply-begin")
	if _, effects := ctl(t, m, "apply-note", "error", "display uuid-d: no such display"); len(effects) != 0 {
		t.Fatalf("failed apply re-fired: %+v", effects)
	}
	if m.applyLive {
		t.Error("a failed apply must clear the in-flight latch")
	}
	if !m.applyFail || !strings.Contains(m.applyNote, "no such display") {
		t.Errorf("note = %q fail=%v", m.applyNote, m.applyFail)
	}
	if _, _, err := m.HandleCtl([]string{"apply-note", "sideways"}); err == nil {
		t.Error("unknown outcome must fail")
	}
	if _, _, err := m.HandleCtl([]string{"apply-note"}); err == nil {
		t.Error("missing outcome must fail")
	}
}

// The config handed to the verb outlives the module lock, so it must not
// alias the pending screens: a toggle mid-apply would corrupt it.
func TestApplyBeginDeepCopies(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	region := renocore.Region{X: 10, Y: 20, W: 300, H: 400}
	m.desired = renocore.RecordConfig{Screens: []renocore.ScreenSel{{Display: "uuid-d", Region: &region}}}
	req, _, err := m.HandleCtl([]string{"apply-begin"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := req.(ApplyRequest).Config
	m.toggleScreenLocked("uuid-d")
	m.desired.Screens = append(m.desired.Screens, renocore.ScreenSel{Display: "uuid-b"})
	region.X = 999
	if len(cfg.Screens) != 1 || cfg.Screens[0].Display != "uuid-d" {
		t.Fatalf("screens aliased the pending slice: %+v", cfg.Screens)
	}
	if cfg.Screens[0].Region.X != 10 {
		t.Errorf("region aliased: %+v", cfg.Screens[0].Region)
	}
}

// Desired state that evaporates on a bus restart is a bug: every switch
// restarts the bus, and the pad losing your selection mid-setup reads as
// the pad ignoring you.
func TestConfigSurvivesRestart(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgScreen, "uuid-d"})
	m.HandleActEffects([]string{actCfgAudio, "uid-mbp"})
	m.HandleActEffects([]string{actCfgWebcam, "br"})

	// a fresh Mod over the same state dir is what a bus restart looks like
	next := &Mod{stateDir: m.stateDir, runsDir: m.runsDir, exe: m.exe, now: m.now}
	next.recoverConfig()
	if len(next.desired.Screens) != 1 || next.desired.Screens[0].Display != "uuid-d" {
		t.Errorf("screens lost across restart: %+v", next.desired.Screens)
	}
	if next.desired.Audio != "uid-mbp" || next.desired.Webcam != "br" {
		t.Errorf("recovered = %+v", next.desired)
	}

	// recovery must not re-apply: a starting bus reaching out to mutate a
	// scene nobody asked it to touch is worse than a stale one
	if next.applyLive {
		t.Error("recovery must not mark an apply in flight")
	}

	// an empty posture recovers as empty rather than as junk
	if _, _, err := m.HandleCtl([]string{"record-config", "--clear"}); err != nil {
		t.Fatal(err)
	}
	cleared := &Mod{stateDir: m.stateDir, runsDir: m.runsDir, exe: m.exe, now: m.now}
	cleared.recoverConfig()
	if !cleared.desired.Empty() {
		t.Errorf("cleared posture came back as %+v", cleared.desired)
	}
}

// A corrupt cache degrades to empty instead of wedging the boot.
func TestConfigCacheCorrupt(t *testing.T) {
	m, _ := testMod(t)
	if err := os.WriteFile(filepath.Join(m.stateDir, configFile), []byte("{["), 0o600); err != nil {
		t.Fatal(err)
	}
	m.recoverConfig()
	if !m.desired.Empty() {
		t.Errorf("garbage cache produced %+v", m.desired)
	}
}

// An FX move is a one-shot, not a config apply: it fires the cam verb
// directly. It still carries the placement into the pending config --
// the cam band would otherwise stay lit on a corner the camera left.
func TestFXMoveTapFiresTheCamVerb(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgWebcam, "br"})

	handled, effects := m.HandleActEffects([]string{actFXMove, "big"})
	if !handled || len(effects) != 1 {
		t.Fatalf("handled=%v effects=%+v", handled, effects)
	}
	want := []string{"/inst/khudson", "reno", "obs", "cam", "move", "big", "500ms", "smooth"}
	if !slices.Equal(effects[0].Argv, want) {
		t.Errorf("argv = %v", effects[0].Argv)
	}
	if m.desired.Webcam != "big" {
		t.Errorf("pending webcam = %q", m.desired.Webcam)
	}
	// each tap is its own deliberate one-shot, never a debounce candidate
	_, again := m.HandleActEffects([]string{actFXMove, "tl"})
	if len(again) != 1 || again[0].Key == effects[0].Key {
		t.Errorf("keys collided: %v", again)
	}
	if _, e := m.HandleActEffects([]string{actFXMove, "sideways"}); len(e) != 0 {
		t.Errorf("bad preset fired %v", e)
	}
}

// A camera that was never configured has no item to animate, so the
// first tap goes through the ordinary apply and lands the device.
func TestFXMoveOnAnUnconfiguredCamApplies(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	_, effects := m.HandleActEffects([]string{actFXMove, "big"})
	if len(effects) != 1 || !slices.Contains(effects[0].Argv, "apply") {
		t.Fatalf("effects = %+v", effects)
	}
	if m.desired.Webcam != "big" {
		t.Errorf("pending webcam = %q", m.desired.Webcam)
	}
}

func TestFXHideTap(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	// nothing staged: hiding an absent camera is a no-op, not an error
	if handled, effects := m.HandleActEffects([]string{actFXHide}); !handled || len(effects) != 0 {
		t.Fatalf("handled=%v effects=%+v", handled, effects)
	}

	m.HandleActEffects([]string{actCfgWebcam, "br"})
	_, effects := m.HandleActEffects([]string{actFXHide})
	if len(effects) != 1 || !slices.Contains(effects[0].Argv, "hide") {
		t.Fatalf("effects = %+v", effects)
	}
	if m.desired.Webcam != "off" {
		t.Errorf("pending webcam = %q", m.desired.Webcam)
	}
}

// The FX readout is its own: an fx note must never clear the config
// reconciler's in-flight latch, or a queued config change would strand.
func TestFXNoteIsNotTheApplyLatch(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgWebcam, "br"})
	if !m.applyLive {
		t.Fatal("config tap must leave an apply in flight")
	}
	ctl(t, m, "fx-note", "ok", "move big 500ms smooth")
	if !m.applyLive {
		t.Error("fx-note cleared the apply latch")
	}
	if !strings.Contains(headText(m.recorderHeadRows()), "move big") {
		t.Errorf("head = %q", headText(m.recorderHeadRows()))
	}

	ctl(t, m, "fx-note", "error", "obs cold")
	if !m.fxFail {
		t.Error("error note must read as a failure")
	}
	if _, _, err := m.HandleCtl([]string{"fx-note", "maybe"}); err == nil {
		t.Error("unknown outcome must fail")
	}
}

// headText flattens the recorder head row's spans.
func headText(rows []module.Row) string {
	var b strings.Builder
	for _, r := range rows {
		for _, s := range r.Spans {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

// The FX band draws the same placements as an eased path, lit off the
// same selection: a move that landed IS the current placement.
func TestRecorderFXBand(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgWebcam, "big"})
	rows := m.recorderData().Rows

	for _, p := range renocore.FXPlacements() {
		tile := padTile(rows, KeyFXMove+":"+p)
		if tile == nil || tile.Section != secFX {
			t.Fatalf("no fx tile for %q", p)
		}
		if lit := tile.Style == module.StyleAccent; lit != (p == "big") {
			t.Errorf("%s lit=%v", p, lit)
		}
	}
	if padTile(rows, KeyFXHide) == nil {
		t.Error("fx band needs a hide button")
	}
	// "off" is a visibility state, not a place to slide to
	if padTile(rows, KeyFXMove+":off") != nil {
		t.Error("off must not be an fx target")
	}
}

// Standby is phase one on its own button, and it reports its own state:
// the whole point of splitting the phases is that the slow half becomes
// The transport is ONE button cycling ARM -> REC -> STOP. Each face only
// appears when it can honour its name: REC never shows unless recording
// starts now, because ARM already paid for the OBS launch and the scene.
func TestTransportCyclesArmRecStop(t *testing.T) {
	m, _ := testMod(t)

	// unarmed: the button offers to bring the rig up, not to record
	tile := padTile(m.recorderData().Rows, KeyActRecord)
	if tile == nil || tile.Text != "ARM" || tile.Act[0] != actArm {
		t.Fatalf("cold pad must offer ARM: %+v", tile)
	}

	handled, effects := m.handleCfgActLocked([]string{actArm})
	if !handled || len(effects) != 1 {
		t.Fatalf("arm tap = %v, %+v", handled, effects)
	}
	if want := []string{m.exe, "reno", "obs", "arm"}; !slices.Equal(effects[0].Argv, want) {
		t.Errorf("arm argv = %v, want %v", effects[0].Argv, want)
	}
	if got := padTile(m.recorderData().Rows, KeyActRecord); got.Value != "arming..." {
		t.Errorf("in-flight arm must say so: %+v", got)
	}
	// one at a time: a second tap during a cold OBS launch is impatience
	if _, again := m.handleCfgActLocked([]string{actArm}); len(again) != 0 {
		t.Errorf("second tap fired a duplicate arm: %+v", again)
	}

	// armed: the face becomes REC and means it
	if err := m.noteArmLocked([]string{"ok", "obs 32.2.0, screen built-in"}); err != nil {
		t.Fatal(err)
	}
	got := padTile(m.recorderData().Rows, KeyActRecord)
	if got.Text != "REC" || got.Act[0] != actRecord || got.Value != "ready" {
		t.Fatalf("armed transport = %+v, want a live REC", got)
	}

	// rolling: the stop square, and stopping leaves the rig ARMED so the
	// next take is instant
	m.HandleActEffects([]string{actRecord})
	ctl(t, m, "record-note", m.run.ID, "started", "32.2.0")
	if got := padTile(m.recorderData().Rows, KeyActRecord); got.Text != "STOP" {
		t.Fatalf("rolling transport = %+v, want STOP", got)
	}
	ctl(t, m, "record-note", m.run.ID, "stopped", "/clips/take1.mkv")
	if !m.armed() {
		t.Error("stopping a take must leave the rig armed")
	}

	// a failed arm reports the reason and stays on ARM
	if err := m.noteArmLocked([]string{"error", "obs did not answer"}); err != nil {
		t.Fatal(err)
	}
	got = padTile(m.recorderData().Rows, KeyActRecord)
	if got.Text != "ARM" || got.Style != module.StyleWarn || got.Value != "obs did not answer" {
		t.Errorf("failed arm = %+v, want ARM with the reason", got)
	}
}

// END RUN is the unarm: it releases the scene and the virtual camera with
// it, because the failure worth designing against is walking away with the
// camera light still on.
func TestEndRunReleasesTheRig(t *testing.T) {
	m, _ := testMod(t)
	if err := m.noteArmLocked([]string{"ok", "obs 32.2.0"}); err != nil {
		t.Fatal(err)
	}
	m.HandleActEffects([]string{actRecord}) // opens an ad hoc run
	ctl(t, m, "record-note", m.run.ID, "started", "32.2.0")
	ctl(t, m, "record-note", m.run.ID, "stopped", "/clips/take1.mkv")

	_, effects := m.HandleActEffects([]string{actStop})
	var released bool
	for _, e := range effects {
		if slices.Equal(e.Argv, []string{m.exe, "reno", "obs", "release"}) {
			released = true
		}
	}
	if !released {
		t.Fatalf("END RUN must release the rig: %+v", effects)
	}
	// and the verb's note unarms, so the transport falls back to ARM
	if err := m.noteArmLocked([]string{"released", "2 sources released"}); err != nil {
		t.Fatal(err)
	}
	if m.armed() {
		t.Error("a released rig must read unarmed")
	}
	if got := padTile(m.recorderData().Rows, KeyActRecord); got.Text != "ARM" {
		t.Errorf("released transport = %+v, want ARM", got)
	}
}

// The window tile reports where OBS's window actually is. It is allowed to
// show state precisely because kAXHidden reads back -- an error resolves to
// unknown rather than a guess.
func TestObsWindowTile(t *testing.T) {
	m, _ := testMod(t)
	if got := padTile(m.recorderData().Rows, KeyActWindow); got == nil || got.Value != "?" {
		t.Fatalf("unqueried window tile = %+v", got)
	}
	handled, effects := m.handleCfgActLocked([]string{actWindow})
	if !handled || len(effects) != 1 {
		t.Fatalf("window tap = %v, %+v", handled, effects)
	}
	want := []string{m.exe, "reno", "obs", "window", "toggle"}
	if !slices.Equal(effects[0].Argv, want) {
		t.Errorf("window argv = %v, want %v", effects[0].Argv, want)
	}
	for _, tc := range []struct{ note, value, style string }{
		{"hidden", "hidden", module.StyleDim},
		{"shown", "shown", module.StyleAccent},
	} {
		if err := m.noteWindowLocked([]string{tc.note}); err != nil {
			t.Fatal(err)
		}
		got := padTile(m.recorderData().Rows, KeyActWindow)
		if got.Value != tc.value || got.Style != tc.style {
			t.Errorf("after %q: %+v", tc.note, got)
		}
	}
	// an error is not a state: OBS may not even be running
	if err := m.noteWindowLocked([]string{"error", "not running"}); err != nil {
		t.Fatal(err)
	}
	if got := padTile(m.recorderData().Rows, KeyActWindow); got.Value != "unknown" || got.Style != module.StyleWarn {
		t.Errorf("errored window tile = %+v", got)
	}
	if err := m.noteWindowLocked([]string{"sideways"}); err == nil {
		t.Error("an unknown state must be rejected")
	}
}

func TestFmtAge(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{-time.Second, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{3 * time.Hour, "3h"},
		{50 * time.Hour, "2d"},
	} {
		if got := fmtAge(tc.d); got != tc.want {
			t.Errorf("fmtAge(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
