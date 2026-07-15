package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/krib/chord"
	"github.com/shimmerjs/krib/envelope"
)

func bindingsEnv(cmd string) *envelope.Envelope {
	return &envelope.Envelope{
		SchemaVersion: envelope.SchemaVersion,
		Kind:          envelope.KindBindings,
		Meta:          envelope.Meta{Sheet: "kitty"},
		Entries: []envelope.Entry{
			{Mode: "default", Keys: []chord.Chord{{Mods: []string{"cmd"}, Key: "w"}}, Cmd: cmd},
		},
	}
}

func TestObserveTransitions(t *testing.T) {
	f := New()
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// first sight: firstSeen = since = now
	if !f.Observe(bindingsEnv("close_window"), t0) {
		t.Fatal("first observation should be dirty")
	}
	e := f.Entries["default/cmd+w"]
	if !e.FirstSeen.Equal(t0) || !e.Since.Equal(t0) {
		t.Fatalf("bootstrap entry = %+v", e)
	}

	// unchanged value: untouched, not dirty
	t1 := t0.Add(24 * time.Hour)
	if f.Observe(bindingsEnv("close_window"), t1) {
		t.Fatal("unchanged observation should not be dirty")
	}
	if e := f.Entries["default/cmd+w"]; !e.Since.Equal(t0) {
		t.Fatalf("unchanged value moved since: %+v", e)
	}

	// changed value: since moves, firstSeen stays
	t2 := t0.Add(48 * time.Hour)
	if !f.Observe(bindingsEnv("close_tab"), t2) {
		t.Fatal("changed observation should be dirty")
	}
	e = f.Entries["default/cmd+w"]
	if !e.Since.Equal(t2) || !e.FirstSeen.Equal(t0) {
		t.Fatalf("changed entry = %+v, want since=t2 firstSeen=t0", e)
	}
}

func TestRecordUse(t *testing.T) {
	f := New()
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	f.Observe(bindingsEnv("close_window"), now)

	f.RecordUse("default/cmd+w", now.Add(time.Hour))
	f.RecordUse("default/cmd+w", now.Add(2*time.Hour))
	e := f.Entries["default/cmd+w"]
	if e.Accepts != 2 || !e.LastUsed.Equal(now.Add(2*time.Hour)) {
		t.Fatalf("usage = %+v", e)
	}
	if !e.FirstSeen.Equal(now) || !e.Since.Equal(now) {
		t.Fatalf("usage clobbered observation fields: %+v", e)
	}

	// usage on a never-observed id still initializes sanely
	f.RecordUse("default/cmd+q", now)
	if e := f.Entries["default/cmd+q"]; e.Accepts != 1 || e.FirstSeen.IsZero() {
		t.Fatalf("unobserved usage = %+v", e)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "krib", "kitty.json")
	f := New()
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	f.Observe(bindingsEnv("close_window"), now)
	f.RecordUse("default/cmd+w", now)
	if err := f.Save(path); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	e := got.Entries["default/cmd+w"]
	if e.Accepts != 1 || !e.Since.Equal(now) || e.Hash == "" {
		t.Fatalf("roundtrip entry = %+v", e)
	}
	// atomic rewrite leaves no temp droppings
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range entries {
		if strings.HasPrefix(de.Name(), ".krib-state-") {
			t.Fatalf("stale temp file %s", de.Name())
		}
	}
}

// Corrupt, missing, or version-skewed statefiles degrade to empty -- no
// markers, never a crash.
func TestLoadDegrades(t *testing.T) {
	dir := t.TempDir()
	missing := Load(filepath.Join(dir, "nope.json"))
	if len(missing.Entries) != 0 || missing.Version != Version {
		t.Fatalf("missing = %+v", missing)
	}

	corrupt := filepath.Join(dir, "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := Load(corrupt); len(f.Entries) != 0 {
		t.Fatalf("corrupt = %+v", f)
	}

	skewed := filepath.Join(dir, "skewed.json")
	if err := os.WriteFile(skewed, []byte(`{"version": 99, "entries": {"x": {}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := Load(skewed); len(f.Entries) != 0 {
		t.Fatalf("skewed = %+v", f)
	}
}

func TestPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg-state")
	p, err := Path("kitty")
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/xdg-state/krib/kitty.json" {
		t.Fatalf("path = %q", p)
	}
	p, err = Path("")
	if err != nil {
		t.Fatal(err)
	}
	if p != "/tmp/xdg-state/krib/default.json" {
		t.Fatalf("default path = %q", p)
	}

	// sheet names arrive from envelope data: separators and dot segments
	// must fail resolution, never escape the krib state dir
	for _, bad := range []string{"../../evil", "a/b", `a\b`, ".", ".."} {
		if p, err := Path(bad); err == nil {
			t.Fatalf("Path(%q) = %q, want error", bad, p)
		}
	}
}
