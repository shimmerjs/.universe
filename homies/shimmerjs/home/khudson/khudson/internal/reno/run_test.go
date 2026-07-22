package reno

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunJournalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 22, 15, 0, 0, 0, time.UTC)
	r := &Run{
		Version: runVersion, ID: NewRunID(now), Demo: "widget-tour",
		ScriptPath: "/x/demo.txtar", ScriptHash: "abc",
		StartedAt: now, Cursor: CursorArmed,
		Steps: make([]StepActual, 3),
	}
	if err := WriteRun(dir, r); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRun(filepath.Join(dir, r.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Demo != "widget-tour" || got.Cursor != CursorArmed || len(got.Steps) != 3 {
		t.Errorf("round trip mangled: %+v", got)
	}
	if !got.Live() {
		t.Error("unfinalized run must be live")
	}
}

func TestWriteRunNoDir(t *testing.T) {
	if err := WriteRun("", &Run{ID: "x"}); err == nil {
		t.Fatal("empty dir must error loudly")
	}
}

func TestRecoverLatestPicksNewestLive(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	old := &Run{Version: runVersion, ID: "a-old", Demo: "d", StartedAt: base, Cursor: 0}
	newer := &Run{Version: runVersion, ID: "b-new", Demo: "d", StartedAt: base.Add(time.Hour), Cursor: 2}
	done := &Run{Version: runVersion, ID: "c-done", Demo: "d", StartedAt: base.Add(2 * time.Hour), StoppedAt: base.Add(3 * time.Hour)}
	for _, r := range []*Run{old, newer, done} {
		if err := WriteRun(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	got, errs := RecoverLatest(dir)
	if len(errs) != 0 {
		t.Fatalf("errs: %v", errs)
	}
	if got == nil || got.ID != "b-new" {
		t.Fatalf("got %+v", got)
	}

	if r, errs := RecoverLatest(filepath.Join(dir, "absent")); r != nil || errs != nil {
		t.Errorf("missing dir must be a quiet fresh start, got %v %v", r, errs)
	}
	if r, errs := RecoverLatest(""); r != nil || errs != nil {
		t.Errorf("empty dir must no-op, got %v %v", r, errs)
	}
}

func TestRecoverLatestSurfacesGarbage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "junk.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	live := &Run{Version: runVersion, ID: "ok", Demo: "d", StartedAt: time.Now()}
	if err := WriteRun(dir, live); err != nil {
		t.Fatal(err)
	}
	got, errs := RecoverLatest(dir)
	if got == nil || got.ID != "ok" {
		t.Fatalf("live run lost to garbage: %+v", got)
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "junk.json") {
		t.Errorf("garbage must surface: %v", errs)
	}
}

func TestSweepRuns(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	oldDone := &Run{Version: runVersion, ID: "old-done", StartedAt: now.Add(-40 * 24 * time.Hour), StoppedAt: now.Add(-35 * 24 * time.Hour)}
	freshDone := &Run{Version: runVersion, ID: "fresh-done", StartedAt: now.Add(-2 * time.Hour), StoppedAt: now.Add(-time.Hour)}
	oldLive := &Run{Version: runVersion, ID: "old-live", StartedAt: now.Add(-40 * 24 * time.Hour)}
	for _, r := range []*Run{oldDone, freshDone, oldLive} {
		if err := WriteRun(dir, r); err != nil {
			t.Fatal(err)
		}
	}
	SweepRuns(dir, now)
	for name, want := range map[string]bool{
		"old-done.json": false, "fresh-done.json": true, "old-live.json": true,
	} {
		_, err := os.Stat(filepath.Join(dir, name))
		if got := err == nil; got != want {
			t.Errorf("%s exists = %v, want %v", name, got, want)
		}
	}
	SweepRuns("", now) // must not panic
}
