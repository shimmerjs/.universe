package reno

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if m.pending.Audio != renocore.AudioNone || m.pending.Webcam != "br" {
		t.Fatalf("pending = %+v", m.pending)
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
	if !m.pending.Empty() {
		t.Errorf("clear left %+v", m.pending)
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

	if handled, _ := m.HandleActEffects([]string{actCfgAudio, "uid-mbp"}); !handled || m.pending.Audio != "uid-mbp" {
		t.Fatalf("audio act: %+v", m.pending)
	}
	if _, _ = m.HandleActEffects([]string{actCfgAudio, cfgDefault}); m.pending.Audio != "" {
		t.Fatalf("audio default: %+v", m.pending)
	}
	if _, _ = m.HandleActEffects([]string{actCfgWebcam, "big"}); m.pending.Webcam != "big" {
		t.Fatalf("webcam act: %+v", m.pending)
	}
	// screen toggles membership
	m.HandleActEffects([]string{actCfgScreen, "uuid-b"})
	m.HandleActEffects([]string{actCfgScreen, "uuid-d"})
	if len(m.pending.Screens) != 2 {
		t.Fatalf("screens = %+v", m.pending.Screens)
	}
	m.HandleActEffects([]string{actCfgScreen, "uuid-b"})
	if len(m.pending.Screens) != 1 || m.pending.Screens[0].Display != "uuid-d" {
		t.Fatalf("toggle off = %+v", m.pending.Screens)
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
	m.pending = renocore.RecordConfig{Audio: renocore.AudioNone}
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

	// nothing probed: the OBS button is the whole surface, and the pad
	// says so rather than offering bands that select nothing
	d := m.recorderData()
	probe := padTile(d.Rows, KeyActProbe)
	if probe == nil || probe.Act[0] != actProbe || probe.Value != "cold" {
		t.Fatalf("no probe button without inventory: %+v", d.Rows)
	}
	if padTile(d.Rows, "cfg:unprobed") == nil {
		t.Errorf("pre-probe pad must say what is missing: %+v", d.Rows)
	}

	freshInv(t, m)
	m.pending = renocore.RecordConfig{Screens: []renocore.ScreenSel{{Display: "uuid-d"}}, Audio: "uid-mbp"}
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

	// mic band: as-is, MUTE, then a button per device. "obs as-is" clears
	// the knob while MUTE forces every input silent -- different promises,
	// so they stay different buttons
	asIs := padTile(d.Rows, KeyCfgAudio+":default")
	mute := padTile(d.Rows, KeyCfgAudio+":none")
	dev := padTile(d.Rows, KeyCfgAudio+":uid-mbp")
	if asIs == nil || mute == nil || dev == nil {
		t.Fatalf("mic band incomplete: %+v", d.Rows)
	}
	if asIs.Act[1] != cfgDefault || mute.Act[1] != renocore.AudioNone || dev.Act[1] != "uid-mbp" {
		t.Errorf("mic acts = %v / %v / %v", asIs.Act, mute.Act, dev.Act)
	}
	if dev.Style != module.StyleAccent || asIs.Style == module.StyleAccent {
		t.Errorf("the live selection is the lit one: as-is=%q dev=%q", asIs.Style, dev.Style)
	}
	// the band label says MIC; the key does not need to repeat it
	if dev.Text != "MacBook Pro" {
		t.Errorf("device label = %q, want the trimmed form", dev.Text)
	}
	for _, tile := range []*module.Row{asIs, mute, dev} {
		if tile.Section != secMic {
			t.Errorf("mic button %q in section %q", tile.Text, tile.Section)
		}
	}

	// cam band: as-is plus every preset, "off" among them -- an as-is that
	// meant off could record a hot camera (arc-2 review #4)
	if got := padTile(d.Rows, KeyCfgWebcam+":default"); got == nil || got.Style != module.StyleAccent {
		t.Errorf("webcam as-is must be lit with no preset set: %+v", got)
	}
	for _, p := range renocore.WebcamPresets {
		tile := padTile(d.Rows, KeyCfgWebcam+":"+p)
		if tile == nil || tile.Act[1] != p || tile.Section != secCam {
			t.Errorf("webcam preset %q button = %+v", p, tile)
		}
	}

	// a stale inventory keeps its buttons but says so on the OBS key: a
	// cached enumeration beats booting cold with nothing selectable
	m.inventory.At = m.now().Add(-2 * invStale)
	d = m.recorderData()
	if probe = padTile(d.Rows, KeyActProbe); probe == nil || !strings.Contains(probe.Value, "stale") {
		t.Errorf("stale inventory must offer re-probe: %+v", probe)
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
	if got := padTile(next.recorderData().Rows, KeyActProbe); got == nil || got.Value != "32.2.0" {
		t.Errorf("recovered pad must not read cold: %+v", got)
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
	if padTile(d.Rows, KeyCfgAudio+":default") == nil || padTile(d.Rows, KeyCfgWebcam+":default") == nil {
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
// warning. A dock unplugged mid-setup leaves an inventory whose clock has
// not run out but whose selectors point at displays that are gone, so the
// pad checks display IDENTITY, not age.
func TestDisplayChangeOutranksTheStalenessClock(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	// the fixture inventory enumerates uuid-b and uuid-d
	m.local = []cgdisplay.Display{{UUID: "uuid-b"}, {UUID: "uuid-d"}}
	if got := padTile(m.recorderData().Rows, KeyActProbe); got == nil || got.Value != "32.2.0" {
		t.Fatalf("matching display set must read healthy: %+v", got)
	}

	// unplug one: fresh clock, wrong screens
	m.local = []cgdisplay.Display{{UUID: "uuid-b"}}
	probe := padTile(m.recorderData().Rows, KeyActProbe)
	if probe == nil || probe.Style != module.StyleWarn {
		t.Errorf("a moved display set must read hot: %+v", probe)
	}
	if probe.Value != "displays moved" {
		t.Errorf("probe tile value = %q, want the reason", probe.Value)
	}
	// the screen buttons stay live -- the selection is still yours to
	// clear -- but must not read healthy
	screen := padTile(m.recorderData().Rows, KeyCfgScreens+":uuid-d")
	if screen == nil || len(screen.Act) == 0 {
		t.Fatalf("screen button went dead: %+v", screen)
	}
	if screen.Style != module.StyleWarn || screen.Value != "re-probe" {
		t.Errorf("stale screen button = %+v, want a warning", screen)
	}

	// swapped for a different display: same count, different identity
	m.local = []cgdisplay.Display{{UUID: "uuid-b"}, {UUID: "uuid-new"}}
	if got := padTile(m.recorderData().Rows, KeyActProbe); got == nil || got.Style != module.StyleWarn {
		t.Errorf("a swapped display must read hot: %+v", got)
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
