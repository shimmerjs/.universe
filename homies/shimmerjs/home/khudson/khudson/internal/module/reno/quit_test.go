package reno

import (
	"strings"
	"testing"

	"github.com/shimmerjs/khudson/khudson/provider"
)

// quit-begin closes the run exactly like stop does, but emits NOTHING that
// touches OBS. The quit verb does the release and the record stop itself,
// over the one connection it is about to close, and a second pair of
// children would race it to an OBS already on its way out.
func TestQuitBeginClosesTheRunWithoutObsEffects(t *testing.T) {
	m, path := testMod(t)
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	// arm the rig and open a take, so both of stop's OBS effects would fire
	if _, _, err := m.HandleCtl([]string{"arm-note", "ok", "obs 32.2"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.HandleCtl([]string{"record-open"}); err != nil {
		t.Fatal(err)
	}
	runID := m.run.ID

	data, effects := ctl(t, m, "quit-begin")
	req, ok := data.(QuitRequest)
	if !ok {
		t.Fatalf("quit-begin reply = %T, want QuitRequest", data)
	}
	if req.Run != runID || !req.Recording {
		t.Errorf("quit-begin = %+v, want run %s with an open take", req, runID)
	}
	if len(effects) != 1 || effects[0].Key != module.BoundaryKey {
		t.Fatalf("quit-begin effects = %+v, want the boundary alone", effects)
	}
	if m.run.Live() {
		t.Error("quit-begin must close the run")
	}
	if m.armed() {
		t.Error("quit-begin must leave the rig unarmed -- the app is going down")
	}
}

// The case the verb mostly runs in: a hand-driven session with no script
// and no run at all. There is nothing to close, but the rig still has to
// come down, so quit-begin must answer rather than refuse the way stop does.
func TestQuitBeginWithNoRun(t *testing.T) {
	m, _ := testMod(t)
	if _, _, err := m.HandleCtl([]string{"arm-note", "ok", "obs 32.2"}); err != nil {
		t.Fatal(err)
	}
	data, effects := ctl(t, m, "quit-begin")
	req := data.(QuitRequest)
	if req.Run != "" || req.Recording {
		t.Errorf("quit-begin with no run = %+v, want empty", req)
	}
	if len(effects) != 0 {
		t.Errorf("no run to cut, so no boundary: %+v", effects)
	}
	if m.armed() {
		t.Error("quit-begin must unarm even with no run live")
	}
	// and stop still refuses, so the two verbs stay distinguishable
	if _, _, err := m.HandleCtl([]string{"stop"}); err == nil || !strings.Contains(err.Error(), "no live run") {
		t.Errorf("stop with no run = %v, want a refusal", err)
	}
}

// Regression guard on the shared finalize: stop must keep emitting the
// release and the record stop that quit deliberately withholds.
func TestStopStillEmitsItsObsEffects(t *testing.T) {
	m, path := testMod(t)
	ctl(t, m, "load", path)
	ctl(t, m, "start")
	if _, _, err := m.HandleCtl([]string{"arm-note", "ok", "obs 32.2"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.HandleCtl([]string{"record-open"}); err != nil {
		t.Fatal(err)
	}
	_, effects := ctl(t, m, "stop")
	if len(effects) != 3 || effects[0].Key != module.BoundaryKey {
		t.Fatalf("stop effects = %+v, want boundary + release + record stop", effects)
	}
	if got := strings.Join(effects[1].Argv, " "); !strings.Contains(got, "obs release") {
		t.Errorf("stop effect 1 = %q, want the release", got)
	}
	if got := strings.Join(effects[2].Argv, " "); !strings.Contains(got, "obs stop") {
		t.Errorf("stop effect 2 = %q, want the record stop", got)
	}
}
