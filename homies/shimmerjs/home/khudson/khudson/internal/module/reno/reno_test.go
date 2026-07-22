package reno

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

var _ module.Module = (*Mod)(nil)
var _ module.EffectHandler = (*Mod)(nil)

const testScript = `demo tour
budget 10m

step one
note one.md
budget 1m
focus-session work

step two
budget 2m
open-url https://example.com/x
focus-app com.google.Chrome

step three
launch-session aux --cwd /tmp -- claude

-- one.md --
first line.
second line.
`

// testMod builds a Mod with a real journal dir, a stepping clock, and a
// loaded script. The clock advances past transitionGap on every read so
// consecutive transitions never trip the retap guard unless a test
// freezes it deliberately.
func testMod(t *testing.T) (*Mod, string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC)
	m := &Mod{runsDir: filepath.Join(dir, "runs"), exe: "/inst/khudson"}
	m.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	path := filepath.Join(dir, "tour.txtar")
	if err := os.WriteFile(path, []byte(testScript), 0o600); err != nil {
		t.Fatal(err)
	}
	return m, path
}

func ctl(t *testing.T, m *Mod, args ...string) (any, []module.Effect) {
	t.Helper()
	data, effects, err := m.HandleCtl(args)
	if err != nil {
		t.Fatalf("ctl %v: %v", args, err)
	}
	return data, effects
}

func TestLifecycleEndToEnd(t *testing.T) {
	m, path := testMod(t)
	ctl(t, m, "load", path)
	ctl(t, m, "start")

	if m.run == nil || m.run.Cursor != renocore.CursorArmed {
		t.Fatalf("armed run: %+v", m.run)
	}
	if _, _, err := m.HandleCtl([]string{"start"}); err == nil || !strings.Contains(err.Error(), "already live") {
		t.Errorf("double start: %v", err)
	}
	if _, _, err := m.HandleCtl([]string{"load", path}); err == nil || !strings.Contains(err.Error(), "stop it or load --replace") {
		t.Errorf("load over live: %v", err)
	}

	_, effects := ctl(t, m, "next") // armed -> step 0
	if m.run.Cursor != 0 {
		t.Fatalf("cursor = %d", m.run.Cursor)
	}
	if len(effects) != 1 {
		t.Fatalf("step one effects = %+v", effects)
	}
	wantArgv := []string{"/inst/khudson", "reno", "focus-session", "work", "--run", m.run.ID, "--step", "0", "--act", "0"}
	if got := strings.Join(effects[0].Argv, " "); got != strings.Join(wantArgv, " ") {
		t.Errorf("argv = %q", got)
	}
	if !strings.HasPrefix(effects[0].Key, m.run.ID+"/0/1/") {
		t.Errorf("key = %q", effects[0].Key)
	}
	if m.run.ShowStartedAt.IsZero() {
		t.Error("show clock must start on first advance")
	}

	_, effects = ctl(t, m, "next") // -> step 1, two effects
	if len(effects) != 2 || effects[0].Argv[2] != "open-url" || effects[1].Argv[2] != "focus-app" {
		t.Fatalf("step two effects = %+v", effects)
	}
	for _, e := range effects {
		if e.Argv[0] != "/inst/khudson" {
			t.Errorf("every effect head must be the exe identity (PATH flips mid-switch), got %q", e.Argv[0])
		}
	}

	_, effects = ctl(t, m, "goto", "three")
	if m.run.Cursor != 2 || len(effects) != 1 {
		t.Fatalf("goto three: cursor=%d effects=%+v", m.run.Cursor, effects)
	}
	got := strings.Join(effects[0].Argv, " ")
	if !strings.HasPrefix(got, "/inst/khudson reno launch-session aux --run "+m.run.ID) || !strings.HasSuffix(got, "--cwd /tmp -- claude") {
		t.Errorf("launch argv order (correlation before --): %q", got)
	}

	// walking off the end is a quiet no-op
	if _, effects = ctl(t, m, "next"); len(effects) != 0 || m.run.Cursor != 2 {
		t.Errorf("end walk-off: cursor=%d effects=%+v", m.run.Cursor, effects)
	}

	ctl(t, m, "stop")
	if m.run.Live() {
		t.Fatal("stop must finalize")
	}
	if m.run.Steps[2].Total <= 0 {
		t.Errorf("step actuals must accumulate: %+v", m.run.Steps)
	}
}

func TestActsAndRetapGuard(t *testing.T) {
	m, path := testMod(t)
	ctl(t, m, "load", path)
	ctl(t, m, "start")

	handled, effects := m.HandleActEffects([]string{actNext})
	if !handled || len(effects) != 1 || m.run.Cursor != 0 {
		t.Fatalf("next act: handled=%v cursor=%d", handled, m.run.Cursor)
	}

	// freeze the clock just past the last transition: a retap inside
	// transitionGap must not advance
	frozen := m.lastAct
	m.now = func() time.Time { return frozen.Add(100 * time.Millisecond) }
	if handled, effects = m.HandleActEffects([]string{actNext}); !handled || len(effects) != 0 || m.run.Cursor != 0 {
		t.Fatalf("retap advanced: cursor=%d effects=%+v", m.run.Cursor, effects)
	}
	m.now = func() time.Time { frozen = frozen.Add(time.Second); return frozen }
	if _, effects = m.HandleActEffects([]string{actNext}); len(effects) != 2 || m.run.Cursor != 1 {
		t.Fatalf("post-gap next: cursor=%d", m.run.Cursor)
	}

	// prev re-enters step 0: a fresh visit, fresh keys
	_, effects = m.HandleActEffects([]string{actPrev})
	if m.run.Cursor != 0 || len(effects) != 1 {
		t.Fatalf("prev: cursor=%d", m.run.Cursor)
	}
	if !strings.HasPrefix(effects[0].Key, m.run.ID+"/0/2/") {
		t.Errorf("revisit must re-key by visit: %q", effects[0].Key)
	}

	if handled, _ := m.HandleActEffects([]string{"panel:fold", "x"}); handled {
		t.Error("foreign argv must not be handled")
	}
	if handled, _ := m.HandleActEffects([]string{actGoto, "nope"}); !handled {
		t.Error("stale goto id is handled (no-op), not exec fall-through")
	}

	handled, _ = m.HandleActEffects([]string{actStop})
	if !handled || m.run.Live() {
		t.Fatal("act stop must finalize")
	}
}

func TestJournalRecovery(t *testing.T) {
	m, path := testMod(t)
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	ctl(t, m, "next")
	ctl(t, m, "next")
	runID := m.run.ID

	// a fresh module (bus restart) recovers cursor + staged script
	m2 := &Mod{runsDir: m.runsDir, exe: "x", now: time.Now}
	m2.recover()
	if m2.run == nil || m2.run.ID != runID || m2.run.Cursor != 1 {
		t.Fatalf("recovery: %+v", m2.run)
	}
	if m2.staged == nil || m2.staged.Name != "tour" {
		t.Fatalf("script not restaged: %+v", m2.staged)
	}
	if m2.stale != "" {
		t.Fatalf("clean recovery must not be stale: %s", m2.stale)
	}

	// edit the script under the run: recovery goes loud-stale, transitions refuse
	if err := os.WriteFile(path, []byte(testScript+"\nstep extra\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m3 := &Mod{runsDir: m.runsDir, exe: "x", now: time.Now}
	m3.recover()
	if m3.stale == "" || !strings.Contains(m3.stale, "hash mismatch") {
		t.Fatalf("stale = %q", m3.stale)
	}
	if _, effects := m3.HandleActEffects([]string{actNext}); len(effects) != 0 {
		t.Error("stale run must not fire effects")
	}
}

func TestStartRefusedWithoutJournal(t *testing.T) {
	m, path := testMod(t)
	m.runsDir = "" // bare-test-bus convention: journal writes error
	ctl(t, m, "load", path)
	if _, _, err := m.HandleCtl([]string{"start"}); err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("start without a journal dir must refuse: %v", err)
	}
	if m.run != nil {
		t.Error("refused start must not leave a run")
	}
}

func TestPresenterView(t *testing.T) {
	m, path := testMod(t)

	d, err := m.Poll(context.Background(), map[string]any{"view": "presenter"})
	if err != nil {
		t.Fatal(err)
	}
	if !rowsContain(d.Rows, "no script loaded") {
		t.Errorf("empty view: %+v", d.Rows)
	}

	ctl(t, m, "load", path)
	ctl(t, m, "start")
	ctl(t, m, "next")

	d, _ = m.Poll(context.Background(), map[string]any{"view": "presenter"})
	if d.Title != "reno: tour" {
		t.Errorf("title = %q", d.Title)
	}
	var haveStep, haveNote, haveNext, haveActs, haveMenu bool
	for _, r := range d.Rows {
		switch r.Key {
		case KeyChipStep:
			haveStep = strings.Contains(r.Value, "1/3 one")
			haveMenu = len(r.Menu) > 0
		case KeyNote:
			haveNote = haveNote || strings.Contains(r.Text, "first line")
		case KeyNext:
			haveNext = r.Value == "two"
		case KeyActNext:
			haveActs = len(r.Act) == 1 && r.Act[0] == actNext
		}
	}
	if !haveStep || !haveNote || !haveNext || !haveActs || !haveMenu {
		t.Errorf("live view missing pieces: step=%v note=%v next=%v acts=%v menu=%v\n%+v",
			haveStep, haveNote, haveNext, haveActs, haveMenu, d.Rows)
	}

	d, _ = m.Poll(context.Background(), map[string]any{"view": "recorder"})
	var haveRecordAct bool
	for _, r := range d.Rows {
		if r.Key == KeyActRecord && len(r.Act) == 1 && r.Act[0] == actRecord {
			haveRecordAct = true
		}
	}
	if !haveRecordAct {
		t.Errorf("recorder transport missing the record act: %+v", d.Rows)
	}
}

func TestGotoMenuWindows(t *testing.T) {
	m, _ := testMod(t)
	var src strings.Builder
	src.WriteString("demo big\n")
	for i := range 40 {
		fmt := "step s%02d\n"
		src.WriteString(strings.ReplaceAll(fmt, "%02d", twoDigits(i)))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txtar")
	if err := os.WriteFile(path, []byte(src.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	ctl(t, m, "goto", "s20")

	menu := m.gotoMenu()
	if len(menu) > gotoWindow+3 {
		t.Fatalf("menu too big for the overlay: %d items", len(menu))
	}
	var pagers, stops int
	for _, it := range menu {
		switch it.Argv[0] {
		case actGotoWindow:
			pagers++
		case actStop:
			stops++
			if !it.Destructive {
				t.Error("stop must be destructive-gated")
			}
		}
	}
	if pagers != 2 || stops != 1 {
		t.Errorf("mid-list window wants both pagers + stop: %+v", menu)
	}

	// paging shifts the window in-process
	if handled, _ := m.HandleActEffects([]string{actGotoWindow, "+1"}); !handled {
		t.Fatal("goto-window act must be handled")
	}
	m.mu.Lock()
	first := m.gotoMenu()[0].Label
	m.mu.Unlock()
	if !strings.HasPrefix(first, "earlier") {
		t.Errorf("shifted window keeps the earlier pager, got %q", first)
	}
}

func twoDigits(i int) string {
	return string([]byte{byte('0' + i/10), byte('0' + i%10)})
}

func rowsContain(rows []module.Row, sub string) bool {
	for _, r := range rows {
		if strings.Contains(r.Text, sub) || strings.Contains(r.Value, sub) {
			return true
		}
	}
	return false
}

// A recovered journal without a usable script (ad hoc runs, moved
// scripts) must render, not panic: pollNative has no recover, so a
// panicking view crash-loops the bus straight back into the same
// journal.
func TestRecoveredScriptlessRunRenders(t *testing.T) {
	dir := t.TempDir()
	adhoc := &renocore.Run{Version: 1, ID: "r-adhoc", Demo: "adhoc",
		StartedAt: time.Now(), Cursor: 0, Steps: make([]renocore.StepActual, 1)}
	if err := renocore.WriteRun(dir, adhoc); err != nil {
		t.Fatal(err)
	}
	m := &Mod{runsDir: dir, exe: "x", now: time.Now}
	m.recover()
	if m.run == nil || m.staged != nil {
		t.Fatalf("recover: run=%v staged=%v", m.run, m.staged)
	}
	for _, view := range []string{"presenter", "recorder"} {
		d, err := m.Poll(context.Background(), map[string]any{"view": view})
		if err != nil {
			t.Fatalf("%s poll: %v", view, err)
		}
		if len(d.Rows) == 0 {
			t.Fatalf("%s: no rows", view)
		}
	}
	if !rowsContain(mustRows(t, m, "presenter"), "without a usable script") {
		t.Error("presenter must say why the run is inert")
	}

	// transitions refuse but do not panic; stop closes it out
	if _, effects := m.HandleActEffects([]string{actNext}); len(effects) != 0 {
		t.Error("scriptless run must not fire effects")
	}
	handled, effects := m.HandleActEffects([]string{actStop})
	if !handled || m.run.Live() {
		t.Fatal("stop must close a scriptless run")
	}
	if len(effects) != 1 || effects[0].Key != module.BoundaryKey {
		t.Fatalf("stop must return the boundary, got %+v", effects)
	}
	// stopped with nothing staged still renders
	if _, err := m.Poll(context.Background(), map[string]any{"view": "presenter"}); err != nil {
		t.Fatal(err)
	}
}

func mustRows(t *testing.T, m *Mod, view string) []module.Row {
	t.Helper()
	d, err := m.Poll(context.Background(), map[string]any{"view": view})
	if err != nil {
		t.Fatal(err)
	}
	return d.Rows
}

// Lifecycle boundaries ride the effect stream: stop and a fresh start
// both emit the marker that invalidates queued chains bus-side.
func TestLifecycleBoundaries(t *testing.T) {
	m, path := testMod(t)
	ctl(t, m, "load", path)
	_, effects := ctl(t, m, "start")
	if len(effects) != 1 || effects[0].Key != module.BoundaryKey {
		t.Fatalf("start boundary: %+v", effects)
	}
	_, effects = ctl(t, m, "stop")
	if len(effects) != 1 || effects[0].Key != module.BoundaryKey {
		t.Fatalf("stop boundary: %+v", effects)
	}
	// load --replace over a live run cuts it too
	ctl(t, m, "start")
	_, effects = ctl(t, m, "load", path, "--replace")
	if len(effects) != 1 || effects[0].Key != module.BoundaryKey {
		t.Fatalf("replace boundary: %+v", effects)
	}
}
