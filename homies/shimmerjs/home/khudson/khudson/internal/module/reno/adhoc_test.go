package reno

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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

// freshInv installs a just-probed inventory, stamped against the stepping
// test clock.
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
	m.inventory.At = m.now().Add(-2 * time.Hour)
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

	m.inventory.At = m.now().Add(-2 * time.Hour)
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

// fxArgv is one level one-shot's argv, the thing park and unpark emit.
func fxArgv(m *Mod, kind string, level int) []string {
	return append([]string{m.exe, "reno", "obs", "cam"},
		renocore.CamFX{Kind: kind, Level: level}.Args()...)
}

// moveFX is the effect a placement tap builds.
func moveFX(preset string) renocore.CamFX {
	return renocore.CamFX{Kind: renocore.FXMove, Preset: preset,
		Dur: renocore.FXDefaultDur, Ease: renocore.FXDefaultEase}
}

// The camera going away takes the level effects with it, filters included.
// A driver put its own pose back on the way out, but a filter is state:
// hiding the camera used to leave the tint sitting on a source nobody was
// driving, invisible only because the item was disabled.
func TestHidingTheCameraParksTheEffects(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgWebcam, "br"})
	ctl(t, m, "apply-note", "ok", "applied")
	m.fxLevels = map[string]int{renocore.FXPixelate: 4, renocore.FXPurple: 3}

	_, effects := m.HandleActEffects([]string{actFXHide})
	var got [][]string
	for _, e := range effects {
		got = append(got, e.Argv)
	}
	want := [][]string{
		fxArgv(m, renocore.FXPixelate, 0),
		fxArgv(m, renocore.FXPurple, 0),
		append([]string{m.exe, "reno", "obs", "cam"}, renocore.CamFX{Kind: renocore.FXHide}.Args()...),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hide effects = %v,\nwant the filters off first, then the hide %v", got, want)
	}
	// and the tiles read off, because there is no tint on a camera that is
	// not there
	for _, kind := range []string{renocore.FXPixelate, renocore.FXPurple} {
		if lvl := m.fxLevels[kind]; lvl != 0 {
			t.Errorf("%s still reads %d over a hidden camera", kind, lvl)
		}
	}

	// the camera comes back and brings them with it, on the far side of the
	// placement that stages it
	_, effects = m.HandleActEffects([]string{actFXMove, "br"})
	got = nil
	for _, e := range effects {
		got = append(got, e.Argv)
	}
	want = [][]string{
		append([]string{m.exe, "reno", "obs", "cam"}, moveFX("br").Args()...),
		fxArgv(m, renocore.FXPixelate, 4),
		fxArgv(m, renocore.FXPurple, 3),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("return effects = %v,\nwant the placement first, then the levels %v", got, want)
	}
	if m.fxLevels[renocore.FXPixelate] != 4 || m.fxLevels[renocore.FXPurple] != 3 {
		t.Errorf("levels did not come back: %+v", m.fxLevels)
	}
}

// Selecting no camera at all parks them too, and the return rides the
// apply's note -- an effect fired at a camera the apply has not staged yet
// has nothing to land on.
func TestUnsettingTheCameraParksUntilTheApplyLands(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgScreen, "uuid-d"})
	ctl(t, m, "apply-note", "ok", "applied")
	m.HandleActEffects([]string{actCfgWebcam, "br"})
	ctl(t, m, "apply-note", "ok", "applied")
	m.fxLevels = map[string]int{renocore.FXPixelate: 4}

	_, effects := m.HandleActEffects([]string{actCfgWebcam, cfgDefault})
	if len(effects) != 2 || !slices.Equal(effects[0].Argv, fxArgv(m, renocore.FXPixelate, 0)) {
		t.Fatalf("effects = %+v, want the filter off before the apply", effects)
	}
	if !slices.Equal(effects[1].Argv, applyArgv(m)) {
		t.Fatalf("effects = %+v, want the apply second", effects)
	}
	// the apply landing on a camera-less config asserts nothing
	if _, done := ctl(t, m, "apply-note", "ok", "applied"); len(done) != 0 {
		t.Errorf("apply-note with no camera emitted %+v", done)
	}

	m.HandleActEffects([]string{actCfgWebcam, "br"})
	_, done := ctl(t, m, "apply-note", "ok", "applied")
	if len(done) != 1 || !slices.Equal(done[0].Argv, fxArgv(m, renocore.FXPixelate, 4)) {
		t.Fatalf("apply-note = %+v, want the level back", done)
	}
	if m.fxLevels[renocore.FXPixelate] != 4 {
		t.Errorf("level did not come back: %+v", m.fxLevels)
	}
}

// A level tapped while the camera was away is the newer intent: it outranks
// whatever was parked, and the return must not overwrite it.
func TestATapWhileParkedOutranksTheParkedLevel(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgWebcam, "br"})
	ctl(t, m, "apply-note", "ok", "applied")
	m.fxLevels = map[string]int{renocore.FXPixelate: 4}
	m.HandleActEffects([]string{actFXHide})

	m.HandleActEffects([]string{actFXLevel, renocore.FXPixelate, levelUp})
	m.HandleActEffects([]string{actFXMove, "br"})
	if lvl := m.fxLevels[renocore.FXPixelate]; lvl != 1 {
		t.Errorf("level = %d, want the 1 that was tapped, not the parked 4", lvl)
	}
}

// A level effect is a remote, not a toggle: each plus walks the level one
// step and fires that level, and the tiles carry it back to glass.
func TestFXLevelPairWalksTheLevel(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)

	step := padTile(m.recorderTiles(), KeyFXLevel+":pixelate")
	if step == nil || len(step.Ends) != 2 {
		t.Fatal("pixelate needs one remote with two ends")
	}
	// at rest the minus has nowhere to go and says so; the plus does not
	if step.Value != "off" || !step.Ends[0].Dim || step.Ends[1].Dim {
		t.Errorf("at rest: %+v", step)
	}

	handled, effects := m.HandleActEffects([]string{actFXLevel, "pixelate", levelUp})
	if !handled || len(effects) != 1 {
		t.Fatalf("handled=%v effects=%+v", handled, effects)
	}
	want := []string{"/inst/khudson", "reno", "obs", "cam", "pixelate", "1"}
	if !slices.Equal(effects[0].Argv, want) {
		t.Errorf("argv = %v", effects[0].Argv)
	}
	wantReadout := fmt.Sprintf("1/%d", renocore.FXMaxLevel)
	if got := padTile(m.recorderTiles(), KeyFXLevel+":pixelate").Value; got != wantReadout {
		t.Errorf("readout = %q", got)
	}

	// walk to the top: the plus stops firing and dims rather than
	// pretending there is somewhere left to go
	for range renocore.FXMaxLevel {
		m.HandleActEffects([]string{actFXLevel, "pixelate", levelUp})
	}
	if lvl := m.fxLevels["pixelate"]; lvl != renocore.FXMaxLevel {
		t.Fatalf("level = %d", lvl)
	}
	handled, effects = m.HandleActEffects([]string{actFXLevel, "pixelate", levelUp})
	if !handled || len(effects) != 0 {
		t.Errorf("past the top: handled=%v effects=%+v", handled, effects)
	}
	if end := padTile(m.recorderTiles(), KeyFXLevel+":pixelate").Ends[1]; !end.Dim {
		t.Error("a plus at the top must dim")
	}

	// down again, and the two effects keep their own levels
	m.HandleActEffects([]string{actFXLevel, "pixelate", levelDown})
	if lvl := m.fxLevels["pixelate"]; lvl != renocore.FXMaxLevel-1 {
		t.Errorf("after a minus: %d", lvl)
	}
	if lvl := m.fxLevels["purple"]; lvl != 0 {
		t.Errorf("purple moved with pixelate: %d", lvl)
	}
	if _, e := m.HandleActEffects([]string{actFXLevel, "wiggle", levelUp}); len(e) != 0 {
		t.Errorf("unknown level fx fired %v", e)
	}
}

// A hand-run cam verb moves the pad with it. One core: the CLI seam and
// the tiles cannot drift, or the level a shell set would leave the pad
// reading off over a purple camera.
func TestFXNoteAdoptsTheCLIsLevel(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	ctl(t, m, "fx-note", "ok", "purple", "4")
	if lvl := m.fxLevels["purple"]; lvl != 4 {
		t.Errorf("adopted purple = %d", lvl)
	}
	// a parenthetical from the plan must not throw the parse off
	ctl(t, m, "fx-note", "ok", "purple", "0", "(no", "filter", "to", "disable)")
	if lvl := m.fxLevels["purple"]; lvl != 0 {
		t.Errorf("adopted purple off = %d", lvl)
	}
	// and a non-level effect carries no level to adopt
	m.fxLevels["pixelate"] = 2
	ctl(t, m, "fx-note", "ok", "move", "big", "500ms", "smooth")
	if lvl := m.fxLevels["pixelate"]; lvl != 2 {
		t.Errorf("a move moved a level: %d", lvl)
	}
}

// A level survives the bus: the filter stays in OBS's scene collection, so
// a pad that came back saying off would be lying about a purple camera.
func TestFXLevelsRecoverAcrossABus(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actFXLevel, "purple", levelUp})
	m.HandleActEffects([]string{actFXLevel, "purple", levelUp})

	next := &Mod{stateDir: m.stateDir, runsDir: m.runsDir, exe: m.exe, now: m.now}
	next.recoverInventory()
	next.recoverFXLevels()
	if lvl := next.fxLevels["purple"]; lvl != 2 {
		t.Errorf("recovered purple = %d", lvl)
	}
	wantReadout := fmt.Sprintf("2/%d", renocore.FXMaxLevel)
	if got := padTile(next.recorderTiles(), KeyFXLevel+":purple").Value; got != wantReadout {
		t.Errorf("recovered readout = %q", got)
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

// The four corner slides are ONE element with its presses where they
// point, in the order the renderer draws them: top-left, top-right,
// bottom-left, bottom-right. Nine buttons in a row became a d-pad, a size
// pair, and the one placement that is not a corner.
func TestRecorderFXBand(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgWebcam, "br"})
	rows := m.recorderData().Rows

	pad := padTile(rows, KeyFXMove)
	if pad == nil || pad.Kind != module.RowDpad || pad.Section != secFX {
		t.Fatalf("no slide d-pad: %+v", pad)
	}
	if len(pad.Ends) != len(renocore.CamCorners) {
		t.Fatalf("d-pad has %d ends, want one per corner", len(pad.Ends))
	}
	for i, c := range renocore.CamCorners {
		e := pad.Ends[i]
		if e.Label != c {
			t.Errorf("end %d is %q, want %q -- the renderer draws them in this order", i, e.Label, c)
		}
		// the corner the camera is already in moves nothing, so it is dim
		// rather than a live-looking button that does not budge
		if want := c == "br"; e.Dim != want {
			t.Errorf("end %s dim=%v, want %v", c, e.Dim, want)
		}
		if e.Dim {
			continue
		}
		if got := strings.Join(e.Argv, " "); got != actFXMove+" "+c {
			t.Errorf("end %s argv = %q", c, got)
		}
	}
	if pad.Value != "br" {
		t.Errorf("d-pad reads %q, want the current placement", pad.Value)
	}

	// the size pair grows and shrinks whichever corner it is in, keeping
	// the corner -- the d-pad moves, this resizes, neither does the other's
	// job
	size := padTile(rows, KeyFXSize)
	if size == nil || size.Kind != module.RowStepper || len(size.Ends) != 2 {
		t.Fatalf("no size stepper: %+v", size)
	}
	if size.Value != "small" {
		t.Errorf("size reads %q over br, want small", size.Value)
	}
	if !size.Ends[0].Dim {
		t.Error("already small: the shrink end must be dim")
	}
	if got := strings.Join(size.Ends[1].Argv, " "); got != actFXMove+" br+" {
		t.Errorf("grow argv = %q, want the same corner grown", got)
	}

	// and the d-pad slides at the size the camera already has
	m.HandleActEffects([]string{actCfgWebcam, "br+"})
	pad = padTile(m.recorderData().Rows, KeyFXMove)
	for _, e := range pad.Ends {
		if e.Dim {
			continue
		}
		if got := strings.Join(e.Argv, " "); !strings.HasSuffix(got, "+") {
			t.Errorf("slide from a grown corner = %q, want it kept grown", got)
		}
	}

	// off a corner there is nothing to resize, and the pair says so rather
	// than offering a press that would do nothing
	m.HandleActEffects([]string{actCfgWebcam, renocore.CamBig})
	rows = m.recorderData().Rows
	size = padTile(rows, KeyFXSize)
	if !size.Ends[0].Dim || !size.Ends[1].Dim || size.Value != "--" {
		t.Errorf("size over big = %+v, want both ends dim", size)
	}
	// big is not a corner, so it keeps a button of its own, lit like any
	// other choice
	big := padTile(rows, KeyFXMove+":"+renocore.CamBig)
	if big == nil || big.Style != module.StyleAccent {
		t.Errorf("big tile = %+v, want it lit", big)
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
	// and it asks the pad for its weight on every face -- without the ask the
	// dock splits the grid evenly and the transport is just another choice tile
	if tile.MinHeight != transportTileH {
		t.Errorf("transport MinHeight = %d, want %d", tile.MinHeight, transportTileH)
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
	if _, err := m.noteArmLocked([]string{"ok", "obs 32.2.0, screen built-in"}); err != nil {
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
	if _, err := m.noteArmLocked([]string{"error", "obs did not answer"}); err != nil {
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
	if _, err := m.noteArmLocked([]string{"ok", "obs 32.2.0"}); err != nil {
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
	if _, err := m.noteArmLocked([]string{"released", "2 sources released"}); err != nil {
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

// The virtual camera earns a tile because GetVirtualCamStatus reads back.
// Release already stops it, but stopping is not showing it -- and the state
// this tile exists for is an output nobody here started, holding the camera
// while the rest of the pad reports a clean idle rig. That state has to be
// the loudest thing on the band, and it has to be one tap from gone.
func TestVCamTile(t *testing.T) {
	m, _ := testMod(t)
	if got := padTile(m.recorderData().Rows, KeyActVCam); got == nil || got.Value != "?" {
		t.Fatalf("unqueried vcam tile = %+v", got)
	}
	handled, effects := m.handleCfgActLocked([]string{actVCam})
	if !handled || len(effects) != 1 {
		t.Fatalf("vcam tap = %v, %+v", handled, effects)
	}
	// stop, never start: there is no case where this rig wants one running,
	// which is what makes the tile safe to hit in any state
	want := []string{m.exe, "reno", "obs", "vcam", "stop"}
	if !slices.Equal(effects[0].Argv, want) {
		t.Errorf("vcam argv = %v, want %v", effects[0].Argv, want)
	}
	if _, again := m.handleCfgActLocked([]string{actVCam}); len(again) != 0 {
		t.Errorf("second tap fired a duplicate sweep: %+v", again)
	}

	if err := m.noteVCamLocked([]string{"on", "holding the camera"}); err != nil {
		t.Fatal(err)
	}
	got := padTile(m.recorderData().Rows, KeyActVCam)
	if got.Text != "VCAM" || got.Style != module.StyleWarn || got.Value != "LIVE" {
		t.Errorf("a live virtual cam must read as an alarm: %+v", got)
	}
	if err := m.noteVCamLocked([]string{"off", "stopped"}); err != nil {
		t.Fatal(err)
	}
	if got := padTile(m.recorderData().Rows, KeyActVCam); got.Value != "off" || got.Style != module.StyleDim {
		t.Errorf("stopped vcam tile = %+v", got)
	}
	// a failed read is unknown, never the clean idle it would be mistaken for
	if err := m.noteVCamLocked([]string{"error", "socket gone"}); err != nil {
		t.Fatal(err)
	}
	if got := padTile(m.recorderData().Rows, KeyActVCam); got.Value != "unknown" || got.Style != module.StyleWarn {
		t.Errorf("errored vcam tile = %+v", got)
	}
	if err := m.noteVCamLocked([]string{"maybe"}); err == nil {
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

// The overlay is scene state, so its tile is a plain toggle driving the
// same reconcile every other knob on this pad drives -- not an FX one-shot.
// It is a toggle and not a level because the page animates itself: there is
// no magnitude khudson holds, and a stepper reporting a number it invented
// is the tile that lies.
func TestRecorderLemmingsTile(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	m.HandleActEffects([]string{actCfgWebcam, "br"})

	tile := padTile(m.recorderData().Rows, KeyFXLemmings)
	if tile == nil || tile.Section != secFX || tile.Kind != module.RowPad {
		t.Fatalf("no lemmings tile: %+v", tile)
	}
	if tile.Value != "off" {
		t.Errorf("lemmings starts at %q, want off", tile.Value)
	}

	// let the webcam tap's apply land first, or the latch defers this one
	ctl(t, m, "apply-note", "ok", "scene ok")
	handled, effects := m.HandleActEffects(tile.Act)
	if !handled {
		t.Fatal("the lemmings tap must be handled in-process")
	}
	if !m.desired.Lemmings {
		t.Error("the tap must set the config knob")
	}
	// scene state, so it goes through the ordinary live apply rather than
	// an fx one-shot
	if len(effects) != 1 || !strings.Contains(strings.Join(effects[0].Argv, " "), "obs apply") {
		t.Fatalf("lemmings effects = %+v, want one apply", effects)
	}
	if tile = padTile(m.recorderData().Rows, KeyFXLemmings); tile.Value != "on" ||
		tile.Style != module.StyleAccent {
		t.Errorf("lit tile = %+v", tile)
	}

	// with no camera there is no box to hang on, and the tile says so
	// rather than claiming an overlay nobody can see
	ctl(t, m, "apply-note", "ok", "scene ok")
	m.HandleActEffects([]string{actCfgWebcam, "off"})
	if tile = padTile(m.recorderData().Rows, KeyFXLemmings); tile.Value != "no cam" {
		t.Errorf("lemmings over a dead cam = %q, want it said so", tile.Value)
	}

	// and tapping again turns it off
	m.HandleActEffects(tile.Act)
	if m.desired.Lemmings {
		t.Error("the tile must toggle")
	}
}

// The pad resolves a selector the way the PLAN does, not by exact value.
// A config written from the CLI (`--screen XENEON`) is perfectly good, and
// used to render as a warn "not attached" tile beside the very display it
// selects -- while its own tile sat unlit.
func TestScreensBandResolvesSelectorsLikeThePlan(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	// the inventory names are "Built-in" and "DELL"; select by substring
	m.desired.Screens = []renocore.ScreenSel{{Display: "DELL"}}
	rows := m.recorderData().Rows

	if tile := padTile(rows, KeyCfgScreens+":uuid-d"); tile == nil ||
		tile.Style != module.StyleAccent {
		t.Errorf("a display selected by name must light its own tile: %+v", tile)
	}
	if tile := padTile(rows, KeyCfgScreens+":DELL"); tile != nil {
		t.Errorf("a resolvable selector must not also read as not attached: %+v", tile)
	}

	// a selector that resolves to NOTHING is still the loud alarm it was
	m.desired.Screens = []renocore.ScreenSel{{Display: "uuid-gone"}}
	rows = m.recorderData().Rows
	tile := padTile(rows, KeyCfgScreens+":uuid-gone")
	if tile == nil || tile.Value != "not attached" || tile.Style != module.StyleWarn {
		t.Errorf("an unresolvable selector must stay the alarm: %+v", tile)
	}
}
