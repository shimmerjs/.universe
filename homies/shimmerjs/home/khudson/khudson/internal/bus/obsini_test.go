package bus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The file belongs to OBS. khudson owns exactly the keys it names, so
// everything else -- unknown sections, unknown keys, ordering, the giant
// base64 DockState blob -- has to come back byte-identical.
func TestApplyIniSettingsPreservesTheRest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user.ini")
	before := strings.Join([]string{
		"[General]",
		"Pre19Defaults=false",
		"ConfirmOnExit=true",
		"",
		"[BasicWindow]",
		"PreviewEnabled=true",
		"DockState=AAAA/wAAAAD9AAAAAgAAAAAAAAEY",
		"SysTrayEnabled=false",
		"",
		"[Appearance]",
		"FontScale=10",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := applyIniSettings(path, []iniSetting{
		{"BasicWindow", "SysTrayEnabled", "true"},        // in place
		{"BasicWindow", "SysTrayMinimizeToTray", "true"}, // appended to section
		{"General", "ConfirmOnExit", "false"},            // in place, first section
		{"Output", "Mode", "Simple"},                     // new section
	})
	if err != nil || !changed {
		t.Fatalf("apply: changed=%v err=%v", changed, err)
	}
	got := string(mustRead(t, path))

	for _, want := range []string{
		"SysTrayEnabled=true",
		"SysTrayMinimizeToTray=true",
		"ConfirmOnExit=false",
		"[Output]\nMode=Simple",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// untouched content survives verbatim
	for _, keep := range []string{
		"Pre19Defaults=false",
		"PreviewEnabled=true",
		"DockState=AAAA/wAAAAD9AAAAAgAAAAAAAAEY",
		"[Appearance]",
		"FontScale=10",
	} {
		if !strings.Contains(got, keep) {
			t.Errorf("clobbered %q in:\n%s", keep, got)
		}
	}
	// the in-place edit must not duplicate the key or leave the old value
	if n := strings.Count(got, "SysTrayEnabled="); n != 1 {
		t.Errorf("SysTrayEnabled appears %d times:\n%s", n, got)
	}
	if strings.Contains(got, "ConfirmOnExit=true") {
		t.Errorf("stale value survived:\n%s", got)
	}
	// the appended key landed inside BasicWindow, not after [Appearance]
	if idx, app := strings.Index(got, "SysTrayMinimizeToTray"), strings.Index(got, "[Appearance]"); idx > app {
		t.Errorf("appended key escaped its section:\n%s", got)
	}
}

// Re-seeding an already-correct file must not rewrite it: a no-op write
// would churn mtime every launch and make real drift unreadable.
func TestApplyIniSettingsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.ini")
	settings := []iniSetting{{"BasicWindow", "SysTrayEnabled", "true"}}
	if changed, err := applyIniSettings(path, settings); err != nil || !changed {
		t.Fatalf("first apply: changed=%v err=%v", changed, err)
	}
	first := mustRead(t, path)
	changed, err := applyIniSettings(path, settings)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("second apply reported a change")
	}
	if got := mustRead(t, path); string(got) != string(first) {
		t.Errorf("second apply rewrote the file:\n%s\n---\n%s", first, got)
	}
}

// A key present in a LATER section must not be mistaken for ours: both
// [SimpleOutput] and [AdvOut] carry RecEncoder, and writing the wrong one
// silently configures an output OBS is not using.
func TestApplyIniSettingsSectionScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "basic.ini")
	before := strings.Join([]string{
		"[SimpleOutput]",
		"RecEncoder=x264",
		"",
		"[AdvOut]",
		"RecEncoder=none",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(before), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := applyIniSettings(path, []iniSetting{{"AdvOut", "RecEncoder", "obs_x264"}}); err != nil {
		t.Fatal(err)
	}
	got := string(mustRead(t, path))
	if !strings.Contains(got, "[SimpleOutput]\nRecEncoder=x264") {
		t.Errorf("wrote the wrong section:\n%s", got)
	}
	if !strings.Contains(got, "[AdvOut]\nRecEncoder=obs_x264") {
		t.Errorf("target section not set:\n%s", got)
	}
}

func TestProfileDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "user.ini")
	if err := os.WriteFile(path, []byte("[BasicWindow]\nProfileDir=decoy\n\n[Basic]\nProfile=Untitled\nProfileDir=Untitled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := profileDir(path); got != "Untitled" {
		t.Errorf("profileDir = %q -- must read [Basic], not the first match anywhere", got)
	}
	if got := profileDir(filepath.Join(t.TempDir(), "absent.ini")); got != "" {
		t.Errorf("absent user.ini = %q", got)
	}
}

// Seeding must never touch a LIVE OBS: it holds the config in memory and
// writes its own copy on exit, so an edit behind its back is reverted,
// not merely ignored.
func TestSeedIniSkipsRunningObs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user.ini")
	if err := os.WriteFile(path, []byte("[BasicWindow]\nSysTrayEnabled=false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := &ObsBoot{ConfigDir: dir, running: func() bool { return true }}
	if err := b.seedIni(path); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, path)); strings.Contains(got, "SysTrayEnabled=true") {
		t.Errorf("seeded against a running OBS:\n%s", got)
	}

	b.running = func() bool { return false }
	if err := b.seedIni(path); err != nil {
		t.Fatal(err)
	}
	if got := string(mustRead(t, path)); !strings.Contains(got, "SysTrayEnabled=true") {
		t.Errorf("stopped OBS must seed:\n%s", got)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
