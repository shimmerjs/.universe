package reno

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRecorderConfigRows(t *testing.T) {
	m, _ := testMod(t)

	// no inventory: a probe act row instead of dead menus
	d := m.recorderData()
	var probeRow *module.Row
	for i := range d.Rows {
		if d.Rows[i].Key == KeyActProbe {
			probeRow = &d.Rows[i]
		}
	}
	if probeRow == nil || probeRow.Act[0] != actProbe {
		t.Fatalf("no probe row without inventory: %+v", d.Rows)
	}

	freshInv(t, m)
	m.pending = renocore.RecordConfig{Screens: []renocore.ScreenSel{{Display: "uuid-d"}}, Audio: "uid-mbp"}
	d = m.recorderData()
	rows := map[string]module.Row{}
	for _, r := range d.Rows {
		rows[r.Key] = r
	}
	if got := rows[KeyCfgScreens].Value; got != "DELL" {
		t.Errorf("screens row = %q", got)
	}
	if got := rows[KeyCfgAudio].Value; got != "MacBook Pro Microphone" {
		t.Errorf("audio row = %q", got)
	}
	if got := rows[KeyCfgWebcam].Value; got != "obs as-is" {
		t.Errorf("webcam row = %q", got)
	}
	// menus: screens toggle marks the selected display
	var sawSelected bool
	for _, a := range rows[KeyCfgScreens].Menu {
		if a.Argv[0] != actCfgScreen {
			t.Errorf("screen menu argv = %v", a.Argv)
		}
		if strings.HasPrefix(a.Label, "[x] DELL") {
			sawSelected = true
		}
	}
	if !sawSelected {
		t.Errorf("selected display unmarked: %+v", rows[KeyCfgScreens].Menu)
	}
	if n := len(rows[KeyCfgAudio].Menu); n != 3 { // as-is, none, one device
		t.Errorf("audio menu = %d items", n)
	}
	// "obs as-is" (clear) plus every preset INCLUDING the real "off":
	// the two are different promises (as-is skips scene reconciliation;
	// off forces the cam item disabled)
	if n := len(rows[KeyCfgWebcam].Menu); n != len(renocore.WebcamPresets)+1 {
		t.Errorf("webcam menu = %d items", n)
	}
	var sawAsIs, sawOff bool
	for _, a := range rows[KeyCfgWebcam].Menu {
		switch a.Label {
		case "obs as-is":
			sawAsIs = a.Argv[1] == cfgDefault
		case "off":
			sawOff = a.Argv[1] == "off"
		}
	}
	if !sawAsIs || !sawOff {
		t.Errorf("webcam menu must carry distinct as-is and off items: %+v", rows[KeyCfgWebcam].Menu)
	}

	// a stale inventory falls back to the re-probe row
	m.inventory.At = m.now().Add(-2 * invStale)
	d = m.recorderData()
	found := false
	for _, r := range d.Rows {
		if r.Key == KeyActProbe && strings.Contains(r.Text, "stale") {
			found = true
		}
	}
	if !found {
		t.Errorf("stale inventory must offer re-probe: %+v", d.Rows)
	}
}

// The script's read-only config row shows ONLY while the scripted run
// is live: with the script merely staged, a record tap arms an AD HOC
// run with the pending knobs, so the ad hoc menus must show (displaying
// the script's config there would lie about what is about to record).
func TestRecorderConfigRowsScripted(t *testing.T) {
	m, _ := testMod(t)
	freshInv(t, m)
	dir := t.TempDir()
	path := filepath.Join(dir, "s.txtar")
	if err := os.WriteFile(path, []byte("demo show\nrecord\naudio none\nstep a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl(t, m, "load", path)

	// staged but not started: the ad hoc config rows show (a tap arms
	// ad hoc, not the script)
	d := m.recorderData()
	sawMenus := false
	for _, r := range d.Rows {
		if r.Key == KeyCfgScreens || r.Key == KeyCfgAudio || r.Key == KeyCfgWebcam {
			sawMenus = true
		}
		if r.Key == "config" {
			t.Errorf("staged-only script leaked its config row: %+v", r)
		}
	}
	if !sawMenus {
		t.Error("staged-only view must show the ad hoc config menus")
	}

	// live scripted run: the script's config shows read-only, menus gone
	ctl(t, m, "start")
	d = m.recorderData()
	sawScript := false
	for _, r := range d.Rows {
		if r.Key == KeyCfgScreens || r.Key == KeyCfgAudio || r.Key == KeyCfgWebcam {
			t.Errorf("scripted live view leaked config menus: %+v", r)
		}
		if r.Key == "config" && strings.Contains(r.Value, "audio none") {
			sawScript = true
		}
	}
	if !sawScript {
		t.Errorf("live scripted view must show the script config: %+v", d.Rows)
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
