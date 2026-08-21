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

const recScript = `demo rec-tour
record
scene main

step intro
focus-session work

step cut
scene closeup
record pause
`

func TestAdhocRecordLifecycle(t *testing.T) {
	m, _ := testMod(t)

	// glass tap with nothing live: arms an ad hoc run + fires the record verb
	handled, effects := m.HandleActEffects([]string{actRecord})
	if !handled || len(effects) != 1 {
		t.Fatalf("record act: handled=%v effects=%+v", handled, effects)
	}
	if m.run == nil || m.run.Demo != renocore.AdhocName || m.run.ScriptPath != "" {
		t.Fatalf("ad hoc run: %+v", m.run)
	}
	rec := m.run.Recording
	if rec == nil || rec.IntentAt.IsZero() || !strings.Contains(rec.Directory, "workspace/adhoc/"+m.run.ID) {
		t.Fatalf("intent must journal write-ahead: %+v", rec)
	}
	argv := strings.Join(effects[0].Argv, " ")
	if !strings.HasPrefix(argv, "/inst/khudson reno record --run "+m.run.ID) {
		t.Errorf("record effect = %q", argv)
	}

	// the verb's handshake: re-arm is idempotent while unproven, then notes
	data, _, err := m.HandleCtl([]string{"record-open"})
	if err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	arm := data.(RecordOpen)
	if arm.Run != m.run.ID || arm.Directory != rec.Directory {
		t.Fatalf("re-arm mismatch: %+v", arm)
	}
	if _, _, err := m.HandleCtl([]string{"record-note", m.run.ID, "started", "31.0.2"}); err != nil {
		t.Fatal(err)
	}
	if !m.run.Recording.Active() || m.run.Recording.ObsVersion != "31.0.2" {
		t.Fatalf("started note: %+v", m.run.Recording)
	}

	// live recording: arm refuses, glass tap flips to the stop effect
	if _, _, err := m.HandleCtl([]string{"record-open"}); err == nil || !strings.Contains(err.Error(), "already recording") {
		t.Fatalf("arm over live: %v", err)
	}
	_, effects = m.HandleActEffects([]string{actRecord})
	if len(effects) != 1 || !strings.Contains(strings.Join(effects[0].Argv, " "), "reno obs stop") {
		t.Fatalf("record act while live must stop: %+v", effects)
	}

	// run stop appends the obs stop effect after the boundary
	_, effects, err = m.HandleCtl([]string{"stop"})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 || effects[0].Key != module.BoundaryKey || !strings.Contains(strings.Join(effects[1].Argv, " "), "obs stop") {
		t.Fatalf("stop effects = %+v", effects)
	}

	// the stop note lands even though the run finalized
	if _, _, err := m.HandleCtl([]string{"record-note", m.run.ID, "stopped", "/clips/x.mkv"}); err != nil {
		t.Fatal(err)
	}
	if m.run.Recording.Output != "/clips/x.mkv" || m.run.Recording.Active() {
		t.Fatalf("stop note: %+v", m.run.Recording)
	}

	// journal round-trips the recording state
	got, err := renocore.ReadRun(m.runsDir + "/" + m.run.ID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if got.Recording == nil || got.Recording.Output != "/clips/x.mkv" {
		t.Fatalf("journaled recording: %+v", got.Recording)
	}
}

func TestScriptedAutoRecord(t *testing.T) {
	m, _ := testMod(t)
	dir := t.TempDir()
	path := dir + "/rec.txtar"
	if err := writeFile(path, recScript); err != nil {
		t.Fatal(err)
	}
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	if m.run.Recording != nil {
		t.Fatal("arming must not record yet")
	}

	// first advance: record effect precedes the step's own actions
	_, effects := ctl(t, m, "next")
	if len(effects) != 2 {
		t.Fatalf("effects = %+v", effects)
	}
	if !strings.Contains(strings.Join(effects[0].Argv, " "), "reno record") {
		t.Errorf("record must lead the chain: %+v", effects[0])
	}
	if effects[1].Argv[2] != "focus-session" {
		t.Errorf("step action must follow: %+v", effects[1])
	}
	if m.run.Recording == nil || m.run.Recording.Scene != "main" {
		t.Fatalf("intent with header scene: %+v", m.run.Recording)
	}

	// scene/record step commands map onto the obs verb family
	_, effects = ctl(t, m, "next")
	if len(effects) != 2 {
		t.Fatalf("cut effects = %+v", effects)
	}
	if got := strings.Join(effects[0].Argv, " "); !strings.HasPrefix(got, "/inst/khudson reno obs scene closeup") {
		t.Errorf("scene argv = %q", got)
	}
	if got := strings.Join(effects[1].Argv, " "); !strings.HasPrefix(got, "/inst/khudson reno obs pause") {
		t.Errorf("pause argv = %q", got)
	}
}

// The transport tile carries every recording phase EXPLICITLY -- ready,
// arming, live, stopped-with-clip, failed -- so the pad never shows a
// healthy-looking default over a dead OBS.
func TestRecorderViewStates(t *testing.T) {
	m, _ := testMod(t)
	rows := mustRows(t, m, "recorder")
	// cold and unarmed, the transport offers ARM -- REC only appears once
	// the rig is up, so it never promises an instant start it cannot make
	if tile := padTile(rows, KeyActRecord); tile == nil || tile.Text != "ARM" || len(tile.Act) == 0 {
		t.Fatalf("idle recorder needs the transport tile: %+v", rows)
	}

	m.HandleActEffects([]string{actRecord})
	// the write-ahead is open and no note has landed: "starting", never a
	// claim that frames are going
	if tile := padTile(mustRows(t, m, "recorder"), KeyActRecord); tile == nil || tile.Value != "starting" {
		t.Errorf("opened-take state: %+v", tile)
	}
	ctl(t, m, "record-note", m.run.ID, "started", "31")
	rows = mustRows(t, m, "recorder")
	tile := padTile(rows, KeyActRecord)
	if tile == nil || tile.Text != "STOP" || len(tile.Act) == 0 {
		t.Errorf("live state needs the stop tile: %+v", rows)
	}
	if tile.Style != module.StyleWarn {
		t.Errorf("a live recording must read hot: %+v", tile)
	}
	ctl(t, m, "record-note", m.run.ID, "stopped", "/clips/a.mkv")
	if rows = mustRows(t, m, "recorder"); !rowsContain(rows, "a.mkv") {
		t.Errorf("stopped state shows the clip: %+v", rows)
	}

	m2, _ := testMod(t)
	m2.HandleActEffects([]string{actRecord})
	ctl(t, m2, "record-note", m2.run.ID, "failed", "dial: connection refused")
	if rows = mustRows(t, m2, "recorder"); !rowsContain(rows, "recording FAILED") {
		t.Errorf("failed state: %+v", rows)
	}
}

// padTile finds one pad button by key; readout rows never match.
func padTile(rows []module.Row, key string) *module.Row {
	for i := range rows {
		if rows[i].Kind == module.RowPad && rows[i].Key == key {
			return &rows[i]
		}
	}
	return nil
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

// TestRecordOpenWire pins the ctl payload shape the record verb decodes.
func TestRecordOpenWire(t *testing.T) {
	m, _ := testMod(t)
	data, _, err := m.HandleCtl([]string{"record-open"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var arm struct {
		Run       string `json:"run"`
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal(raw, &arm); err != nil || arm.Run == "" || arm.Directory == "" {
		t.Fatalf("wire shape: %s (%v)", raw, err)
	}
}

// A stop note for a run that is no longer current reconciles into that
// run's journal file instead of contaminating the new take.
func TestRecordNoteReconcilesOldRun(t *testing.T) {
	m, path := testMod(t)
	m.HandleActEffects([]string{actRecord})
	oldID := m.run.ID
	ctl(t, m, "record-note", oldID, "started", "31")
	ctl(t, m, "stop")

	// a scripted run replaces m.run before the old stop note lands
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	newID := m.run.ID
	if newID == oldID {
		t.Fatal("expected a fresh run")
	}
	ctl(t, m, "record-note", oldID, "stopped", "/clips/old.mkv")
	if m.run.Recording != nil && m.run.Recording.Output != "" {
		t.Fatalf("new run absorbed the old clip: %+v", m.run.Recording)
	}
	old, err := renocore.ReadRun(m.runsDir + "/" + oldID + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if old.Recording.Output != "/clips/old.mkv" {
		t.Fatalf("old journal not reconciled: %+v", old.Recording)
	}

	// arm scoped to a stale run refuses instead of hijacking the current
	if _, _, err := m.HandleCtl([]string{"record-open", oldID}); err == nil {
		t.Fatal("stale-run arm must refuse")
	}
}

// ctl transitions are deliberate: the glass retap gap must not swallow
// them into a silent ok.
func TestCtlTransitionsBypassGap(t *testing.T) {
	m, path := testMod(t)
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	m.HandleActEffects([]string{actNext})
	frozen := m.lastAct
	m.now = func() time.Time { return frozen.Add(100 * time.Millisecond) }
	if _, effects := ctl(t, m, "next"); len(effects) == 0 || m.run.Cursor != 1 {
		t.Fatalf("ctl next inside the gap must land: cursor=%d", m.run.Cursor)
	}
	// glass tap right after is still gap-guarded
	if _, effects := m.HandleActEffects([]string{actNext}); len(effects) != 0 || m.run.Cursor != 1 {
		t.Fatalf("glass retap must stay guarded: cursor=%d", m.run.Cursor)
	}
}

// A scripted cam step execs the same verb the pad tap does -- one core,
// correlated to its step like every other action.
func TestScriptedCamStep(t *testing.T) {
	m, base := testMod(t)
	path := filepath.Join(filepath.Dir(base), "cam.txtar")
	src := "demo camshow\nrecord\nwebcam br\n\nstep intro\ncam move big 600ms bounce\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	_, effects := ctl(t, m, "next")
	// the record arm leads the step's own actions
	if len(effects) != 2 {
		t.Fatalf("effects = %+v", effects)
	}
	argv := strings.Join(effects[1].Argv, " ")
	want := "/inst/khudson reno obs cam move big 600ms bounce --run " + m.run.ID + " --step 0 --act 0"
	if argv != want {
		t.Errorf("cam effect = %q, want %q", argv, want)
	}
}
