package state

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shimmerjs/krib/chord"
	"github.com/shimmerjs/krib/envelope"
)

func binding(key, cmd string) envelope.Entry {
	return envelope.Entry{Mode: "default", Keys: []chord.Chord{{Mods: []string{"cmd"}, Key: key}}, Cmd: cmd}
}

func envWith(entries ...envelope.Entry) *envelope.Envelope {
	return &envelope.Envelope{
		SchemaVersion: envelope.SchemaVersion,
		Kind:          envelope.KindBindings,
		Meta:          envelope.Meta{Sheet: "kitty"},
		Entries:       entries,
	}
}

func bindingsEnv(cmd string) *envelope.Envelope {
	return envWith(binding("w", cmd))
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

// Ids gone from the envelope are swept, not retained: lingering state would
// resurrect a stale firstSeen (and read as a value change) on re-add. An
// empty envelope is a degenerate scrape and sweeps nothing.
func TestObserveSweepsAbsentIDs(t *testing.T) {
	f := New()
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	f.Observe(envWith(binding("w", "close_window"), binding("t", "new_tab")), t0)
	f.RecordUse("default/cmd+t", t0)

	if !f.Observe(envWith(binding("w", "close_window")), t0.Add(time.Hour)) {
		t.Fatal("sweep should be dirty")
	}
	if _, ok := f.Entries["default/cmd+t"]; ok {
		t.Fatalf("absent id retained: %+v", f.Entries)
	}
	if _, ok := f.Entries["default/cmd+w"]; !ok {
		t.Fatal("live id swept")
	}

	// unchanged envelope stays clean after the sweep
	if f.Observe(envWith(binding("w", "close_window")), t0.Add(2*time.Hour)) {
		t.Fatal("post-sweep observation should not be dirty")
	}

	if f.Observe(envWith(), t0.Add(3*time.Hour)) {
		t.Fatal("empty envelope should not be dirty")
	}
	if len(f.Entries) != 1 {
		t.Fatalf("empty envelope wiped state: %+v", f.Entries)
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

// Concurrent instances (several kitty windows) serialize the load-modify-save
// cycle on the lock sidecar: no accept increment is lost.
func TestUpdateConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "krib", "kitty.json")
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	const writers = 16
	start := make(chan struct{})
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := Update(path, func(f *File) bool {
				f.Observe(bindingsEnv("close_window"), now)
				f.RecordUse("default/cmd+w", now)
				return true
			})
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := Load(path).Entries["default/cmd+w"].Accepts; got != writers {
		t.Fatalf("accepts = %d, want %d (lost updates)", got, writers)
	}
}

// A clean cycle (fn reports no change) writes no statefile.
func TestUpdateSkipsCleanRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "krib", "kitty.json")
	f, err := Update(path, func(*File) bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 0 {
		t.Fatalf("fresh file = %+v", f)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("clean cycle wrote the statefile: %v", err)
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
