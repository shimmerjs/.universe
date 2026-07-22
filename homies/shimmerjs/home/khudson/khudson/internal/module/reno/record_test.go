package reno

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

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
	data, _, err := m.HandleCtl([]string{"record-arm"})
	if err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	arm := data.(ArmResult)
	if arm.Run != m.run.ID || arm.Directory != rec.Directory {
		t.Fatalf("re-arm mismatch: %+v", arm)
	}
	if _, _, err := m.HandleCtl([]string{"record-note", "started", "31.0.2"}); err != nil {
		t.Fatal(err)
	}
	if !m.run.Recording.Active() || m.run.Recording.ObsVersion != "31.0.2" {
		t.Fatalf("started note: %+v", m.run.Recording)
	}

	// live recording: arm refuses, glass tap flips to the stop effect
	if _, _, err := m.HandleCtl([]string{"record-arm"}); err == nil || !strings.Contains(err.Error(), "already recording") {
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
	if _, _, err := m.HandleCtl([]string{"record-note", "stopped", "/clips/x.mkv"}); err != nil {
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

func TestRecorderViewStates(t *testing.T) {
	m, _ := testMod(t)
	rows := mustRows(t, m, "recorder")
	if !actRowPresent(rows, "record") {
		t.Fatalf("idle recorder needs the record act: %+v", rows)
	}

	m.HandleActEffects([]string{actRecord})
	if rows = mustRows(t, m, "recorder"); !rowsContain(rows, "armed (start pending)") {
		t.Errorf("armed state: %+v", rows)
	}
	ctl(t, m, "record-note", "started", "31")
	if rows = mustRows(t, m, "recorder"); !actRowPresent(rows, "stop recording") {
		t.Errorf("live state needs the stop act: %+v", rows)
	}
	ctl(t, m, "record-note", "stopped", "/clips/a.mkv")
	if rows = mustRows(t, m, "recorder"); !rowsContain(rows, "/clips/a.mkv") {
		t.Errorf("stopped state shows the clip: %+v", rows)
	}

	m2, _ := testMod(t)
	m2.HandleActEffects([]string{actRecord})
	ctl(t, m2, "record-note", "failed", "dial: connection refused")
	if rows = mustRows(t, m2, "recorder"); !rowsContain(rows, "recording FAILED") {
		t.Errorf("failed state: %+v", rows)
	}
}

func actRowPresent(rows []module.Row, label string) bool {
	for _, r := range rows {
		if r.Key == KeyActRecord && r.Text == label {
			return true
		}
	}
	return false
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o600)
}

// TestArmResultWire pins the ctl payload shape the record verb decodes.
func TestArmResultWire(t *testing.T) {
	m, _ := testMod(t)
	data, _, err := m.HandleCtl([]string{"record-arm"})
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
