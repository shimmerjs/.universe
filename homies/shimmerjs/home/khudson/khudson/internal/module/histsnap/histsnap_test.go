package histsnap

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

func testSeries() map[string]module.HistState {
	return map[string]module.HistState{
		"cpu":     {Cadence: time.Second, LastUnix: 1751900000, Samples: []float32{0.1, 0.5, 0.9}},
		"mem":     {Cadence: time.Second, LastUnix: 1751900000, Samples: []float32{0.3, 0.4}},
		"disk//":  {Cadence: 5 * time.Second, LastUnix: 1751899900, Samples: []float32{0.75}},
		"disk//x": {Cadence: 5 * time.Second, LastUnix: 1751899900, Samples: []float32{0.2, 0.2}},
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hist.snap")
	want := testSeries()
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %v, want 0600", fi.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if raw[0] != version {
		t.Errorf("first byte = %d, want version %d", raw[0], version)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

// TestEncodeStable pins byte-level stability: the same state (whatever map
// iteration order produced it) encodes to identical bytes, so flushes of
// unchanged history rewrite an identical file.
func TestEncodeStable(t *testing.T) {
	a, err := encode(testSeries())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for range 10 {
		b, err := encode(testSeries())
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		if !bytes.Equal(a, b) {
			t.Fatalf("encode is not byte-stable across runs (%d vs %d bytes)", len(a), len(b))
		}
	}

	// load -> save round trip is also byte-identical
	dir := t.TempDir()
	p1, p2 := filepath.Join(dir, "1.snap"), filepath.Join(dir, "2.snap")
	if err := Save(p1, testSeries()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := Load(p1)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Save(p2, loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if !bytes.Equal(b1, b2) {
		t.Error("save -> load -> save changed the bytes")
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "absent.snap"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(missing) = %v, want os.ErrNotExist", err)
	}
}

func TestLoadCorrupt(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"wrong version", append([]byte{version + 1}, 0x0a, 0x0b)},
		{"truncated gob", []byte{version, 0x1f}},
		{"garbage", []byte("not a snapshot at all")},
	} {
		path := filepath.Join(dir, tc.name)
		if err := os.WriteFile(path, tc.raw, 0o600); err != nil {
			t.Fatalf("%s: write: %v", tc.name, err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%s: Load succeeded, want error", tc.name)
		}
	}
}

func TestPrepareExpiryDrop(t *testing.T) {
	now := time.Unix(2000, 0)
	series := map[string]module.HistState{
		// 100 samples at 1s cover 100s; gap of exactly 100s = expired
		"expired": {Cadence: time.Second, LastUnix: 1900, Samples: make([]float32, 100)},
		// same shape one second younger survives
		"alive":        {Cadence: time.Second, LastUnix: 1901, Samples: make([]float32, 100)},
		"zero cadence": {Cadence: 0, LastUnix: 1999, Samples: []float32{0.5}},
		"no samples":   {Cadence: time.Second, LastUnix: 1999, Samples: nil},
	}
	got := Prepare(series, now)
	if _, ok := got["expired"]; ok {
		t.Error("entry older than its window survived Prepare")
	}
	if _, ok := got["zero cadence"]; ok {
		t.Error("zero-cadence entry survived Prepare")
	}
	if _, ok := got["no samples"]; ok {
		t.Error("empty entry survived Prepare")
	}
	if _, ok := got["alive"]; !ok {
		t.Fatal("in-window entry dropped")
	}
}

func TestPrepareGapPadding(t *testing.T) {
	// 10 samples at 5s cover 50s; a 23s gap pads 23/5 = 4 zero fillers and
	// advances the stamp by 20s, leaving the 3s remainder honest
	st := module.HistState{Cadence: 5 * time.Second, LastUnix: 1000, Samples: []float32{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}}
	got := Prepare(map[string]module.HistState{"cpu": st}, time.Unix(1023, 0))
	out, ok := got["cpu"]
	if !ok {
		t.Fatal("padded entry dropped")
	}
	if len(out.Samples) != 14 {
		t.Fatalf("len(Samples) = %d, want 10 + 4 fillers", len(out.Samples))
	}
	for i, v := range out.Samples[:10] {
		if v != 1 {
			t.Fatalf("sample %d = %v, want original 1", i, v)
		}
	}
	for i, v := range out.Samples[10:] {
		if v != 0 {
			t.Fatalf("filler %d = %v, want 0", i, v)
		}
	}
	if out.LastUnix != 1020 {
		t.Errorf("LastUnix = %d, want 1000 + 4*5", out.LastUnix)
	}

	// gap under one cadence: no fillers, entry untouched
	got = Prepare(map[string]module.HistState{"cpu": st}, time.Unix(1003, 0))
	if out := got["cpu"]; len(out.Samples) != 10 || out.LastUnix != 1000 {
		t.Errorf("sub-cadence gap padded: %d samples, stamp %d", len(out.Samples), out.LastUnix)
	}

	// future stamp (clock skew): kept as-is
	got = Prepare(map[string]module.HistState{"cpu": st}, time.Unix(900, 0))
	if out, ok := got["cpu"]; !ok || len(out.Samples) != 10 {
		t.Errorf("future-stamped entry mangled: %+v", out)
	}
}

func TestAge(t *testing.T) {
	now := time.Unix(2000, 0)
	series := map[string]module.HistState{
		"old": {LastUnix: 1000},
		"new": {LastUnix: 1970},
	}
	if got := Age(series, now); got != 30*time.Second {
		t.Errorf("Age = %v, want 30s (newest entry)", got)
	}
	if got := Age(nil, now); got != 0 {
		t.Errorf("Age(nil) = %v, want 0", got)
	}
	if got := Age(map[string]module.HistState{"future": {LastUnix: 3000}}, now); got != 0 {
		t.Errorf("Age(future) = %v, want 0", got)
	}
}

// fakePersistent is a module.Persistent test double.
type fakePersistent struct {
	mu   sync.Mutex
	snap map[string]module.HistState
}

func (f *fakePersistent) HistSnapshot() map[string]module.HistState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]module.HistState, len(f.snap))
	for k, v := range f.snap {
		out[k] = v
	}
	return out
}

func (f *fakePersistent) HistRestore(map[string]module.HistState) {}

func (f *fakePersistent) set(snap map[string]module.HistState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap = snap
}

func TestFlushMergesModules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hist.snap")
	cm := &fakePersistent{snap: map[string]module.HistState{
		"cpu": {Cadence: time.Second, LastUnix: 10, Samples: []float32{0.1}},
		"mem": {Cadence: time.Second, LastUnix: 10, Samples: []float32{0.2}},
	}}
	dk := &fakePersistent{snap: map[string]module.HistState{
		"disk//": {Cadence: time.Second, LastUnix: 10, Samples: []float32{0.3}},
	}}
	if err := Flush(path, []module.Persistent{cm, dk}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("merged series = %d, want 3: %+v", len(got), got)
	}
	for _, name := range []string{"cpu", "mem", "disk//"} {
		if _, ok := got[name]; !ok {
			t.Errorf("series %q missing from flush", name)
		}
	}
}

// TestFlushEmptySkipsWrite: a flush before any poll must not clobber an
// existing snapshot with an empty one.
func TestFlushEmptySkipsWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hist.snap")
	if err := Save(path, testSeries()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	before, _ := os.ReadFile(path)
	if err := Flush(path, []module.Persistent{&fakePersistent{}}); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("empty flush rewrote the snapshot")
	}
}

// TestFlushLoop drives the loop with a hand-fed tick channel (fake clock):
// a tick flushes, and ctx cancellation flushes once more before returning.
func TestFlushLoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hist.snap")
	fp := &fakePersistent{snap: map[string]module.HistState{
		"cpu": {Cadence: time.Second, LastUnix: 10, Samples: []float32{0.1}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		FlushLoop(ctx, path, []module.Persistent{fp}, tick)
	}()

	tick <- time.Unix(100, 0)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if got, err := Load(path); err == nil && len(got) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tick did not produce a snapshot")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// mutate state, then cancel: the shutdown flush must capture it
	fp.set(map[string]module.HistState{
		"cpu": {Cadence: time.Second, LastUnix: 20, Samples: []float32{0.1, 0.9}},
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("FlushLoop did not return on ctx cancel")
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load after shutdown flush: %v", err)
	}
	if st := got["cpu"]; st.LastUnix != 20 || len(st.Samples) != 2 {
		t.Errorf("shutdown flush stale: %+v", st)
	}
}
