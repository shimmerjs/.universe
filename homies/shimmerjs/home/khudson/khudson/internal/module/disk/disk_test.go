package disk

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

const gib = uint64(1024 * 1024 * 1024)

type fakeFS struct {
	stats map[string]fsStat
	calls []string
}

func (f *fakeFS) statfs(path string) (fsStat, error) {
	f.calls = append(f.calls, path)
	st, ok := f.stats[path]
	if !ok {
		return fsStat{}, errors.New("no such file or directory")
	}
	return st, nil
}

func TestPollMultiVolumeConfigOrder(t *testing.T) {
	fs := &fakeFS{stats: map[string]fsStat{
		"/data": {Total: 4 * gib, Free: 1 * gib, Avail: 1 * gib},
		"/":     {Total: 2 * gib, Free: 1 * gib, Avail: 1 * gib},
	}}
	m := &Mod{fs: fs}
	params := map[string]any{"volumes": []any{"/data", "/"}}

	data, err := m.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(data.Rows), data.Rows)
	}
	wantKeys := []string{"/data 6h", "/ 6h"}
	for i, row := range data.Rows {
		if row.Key != wantKeys[i] || row.Kind != module.RowResource {
			t.Errorf("rows[%d] = {%s %s}, want {%s %s}", i, row.Kind, row.Key, module.RowResource, wantKeys[i])
		}
	}
	if got := fs.calls; len(got) != 2 || got[0] != "/data" || got[1] != "/" {
		t.Errorf("statfs calls = %v, want [/data /]", got)
	}
}

func TestPollRowShape(t *testing.T) {
	fs := &fakeFS{stats: map[string]fsStat{
		"/": {Total: 4 * gib, Free: 1 * gib, Avail: 512 * gib / 1024},
	}}
	m := &Mod{fs: fs}

	data, err := m.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("got %d rows, want 1: %+v", len(data.Rows), data.Rows)
	}
	res := data.Rows[0]
	if res.Kind != module.RowResource {
		t.Errorf("res.Kind = %q, want %q", res.Kind, module.RowResource)
	}
	if res.Frac != 0.75 {
		t.Errorf("res.Frac = %v, want 0.75", res.Frac)
	}
	if want := "3.0G/4.0G free 512M"; res.Value != want {
		t.Errorf("res.Value = %q, want %q", res.Value, want)
	}
	// the emitted series is the free-floor danger derived from the ring's
	// used-fraction: 0.75 used of 4G -> 1G superuser-free, floor 40 ->
	// 0.6 + 0.4*(1 - 1/40) = 0.99
	if len(res.Series) != 1 || math.Abs(res.Series[0]-0.99) > 1e-9 {
		t.Errorf("res.Series = %v, want [0.99] (free-floor danger history)", res.Series)
	}
	// raw pair: free space, and the WHOLE row (raw + pct) heats by absolute
	// free space vs the 40G floor: avail 0.5G -> 0.6 + 0.4*(1 - 0.5/40)
	if res.RawX != "512M" || res.RawY != " free" {
		t.Errorf("raw pair = %q + %q, want 512M + \" free\"", res.RawX, res.RawY)
	}
	wantHeat := 0.6 + 0.4*(1-0.5/40)
	if math.Abs(res.RawHeat-wantHeat) > 1e-9 || math.Abs(res.PctHeat-wantHeat) > 1e-9 {
		t.Errorf("RawHeat/PctHeat = %v/%v, want %v", res.RawHeat, res.PctHeat, wantHeat)
	}
	snap := m.HistSnapshot()
	if hs, ok := snap["disk//"]; !ok || len(hs.Samples) != 1 || hs.Samples[0] != 0.75 {
		t.Errorf("HistSnapshot = %+v, want the ring holding [0.75] (the ring keeps used-fraction)", snap)
	}
}

// The floor is the hot boundary, exclusive: AT the floor the row is still
// neutral (heatBucket 0), strictly under it warms, empty is loud, plenty
// of headroom decays quiet, and floor<=0 disables the ramp.
func TestFreeFloorDangerBoundaries(t *testing.T) {
	if d := freeFloorDanger(40, 40); d >= 0.6 {
		t.Errorf("at the floor: danger %v, want neutral (<0.6)", d)
	}
	if d := freeFloorDanger(39.9, 40); d < 0.6 || d >= 0.85 {
		t.Errorf("just under the floor: danger %v, want warn band", d)
	}
	if d := freeFloorDanger(0, 40); d != 1 {
		t.Errorf("empty: danger %v, want 1", d)
	}
	if d := freeFloorDanger(400, 40); d >= 0.1 {
		t.Errorf("10x headroom: danger %v, want near-quiet", d)
	}
	if d := freeFloorDanger(5, 0); d != 0 {
		t.Errorf("disabled floor: danger %v, want 0", d)
	}
}

func TestPollNotMountedDimText(t *testing.T) {
	fs := &fakeFS{stats: map[string]fsStat{
		"/": {Total: 2 * gib, Free: 1 * gib, Avail: 1 * gib},
	}}
	m := &Mod{fs: fs}
	params := map[string]any{"volumes": []any{"/Volumes/gone", "/"}}

	data, err := m.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll should not error on unmounted volume: %v", err)
	}
	if len(data.Rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(data.Rows), data.Rows)
	}
	miss := data.Rows[0]
	if miss.Kind != module.RowText || miss.Style != module.StyleDim {
		t.Errorf("missing volume row = {%s %s}, want dim text", miss.Kind, miss.Style)
	}
	if want := "/Volumes/gone: not mounted"; miss.Text != want {
		t.Errorf("missing volume text = %q, want %q", miss.Text, want)
	}
	if data.Rows[1].Kind != module.RowResource || data.Rows[1].Key != "/ 6h" {
		t.Errorf("mounted volume row missing: %+v", data.Rows[1:])
	}
}

// blockingFS models a dead network mount: statfs hangs until released.
type blockingFS struct {
	release chan struct{}
}

func (b *blockingFS) statfs(string) (fsStat, error) {
	<-b.release
	return fsStat{Total: 2 * gib, Free: gib, Avail: gib}, nil
}

func TestPollCancelledCtxReturnsPromptly(t *testing.T) {
	fs := &blockingFS{release: make(chan struct{})}
	defer close(fs.release)
	m := &Mod{fs: fs}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := m.Poll(ctx, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Poll = %v, want ctx deadline error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Poll did not return after ctx expiry")
	}
}

func TestPollHistoryAccumulates(t *testing.T) {
	fs := &fakeFS{stats: map[string]fsStat{
		"/": {Total: 2 * gib, Free: 1 * gib, Avail: 1 * gib},
	}}
	m := &Mod{fs: fs}

	for i := range 3 {
		if _, err := m.Poll(context.Background(), nil); err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
	}
	if _, err := m.Poll(context.Background(), nil); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// display Series is dropped by design; the RING (hist-snapshot
	// persistence) keeps accumulating
	if got := len(m.HistSnapshot()["disk//"].Samples); got != 4 {
		t.Errorf("ring length after 4 polls = %d, want 4", got)
	}
}

// TestPollWindowParam pins the window plumbing: params.window sizes the
// per-volume rings (window / assumed 5s cadence) and lands in the row key
// as the hint; the ring, not Series' newest-MaxSeries cap, bounds the
// history.
func TestPollWindowParam(t *testing.T) {
	fs := &fakeFS{stats: map[string]fsStat{
		"/": {Total: 2 * gib, Free: 1 * gib, Avail: 1 * gib},
	}}
	m := &Mod{fs: fs}
	params := map[string]any{"window": "30s"} // capacity 6

	var data module.Data
	for i := range 8 {
		var err error
		data, err = m.Poll(context.Background(), params)
		if err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
	}
	row := data.Rows[0]
	if row.Key != "/ 30s" {
		t.Errorf("row key = %q, want %q", row.Key, "/ 30s")
	}
	if got := len(m.HistSnapshot()["disk//"].Samples); got != 6 {
		t.Errorf("ring length = %d, want ring capacity 6", got)
	}
	_ = data
}

// TestNewSingleton pins the sharing contract: bus.Run's registry.All()
// and main's hist-snapshot path must resolve the same instance or restored
// rings would feed a module nobody polls.
func TestNewSingleton(t *testing.T) {
	if New() != New() {
		t.Fatal("New() returned distinct instances")
	}
	var _ module.Persistent = New()
}

// TestPollCadenceParam pins cadence sizing: the scheduler-injected
// poll-interval sizes the ring as window/cadence (30s at 1s = 30, not the
// assumed-5s fallback's 6).
func TestPollCadenceParam(t *testing.T) {
	fs := &fakeFS{stats: map[string]fsStat{
		"/": {Total: 2 * gib, Free: 1 * gib, Avail: 1 * gib},
	}}
	m := &Mod{fs: fs}
	params := map[string]any{"window": "30s", "poll-interval": time.Second}

	var data module.Data
	for i := range 40 {
		var err error
		data, err = m.Poll(context.Background(), params)
		if err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
	}
	if got := len(m.HistSnapshot()["disk//"].Samples); got != 30 {
		t.Errorf("ring length = %d, want ring capacity 30 (window/real cadence)", got)
	}
	_ = data
}

// TestHistSnapshotRestore proves the module.Persistent round trip: per-
// volume "disk/<vol>" series restore into a fresh instance and history
// keeps accumulating after the restore.
func TestHistSnapshotRestore(t *testing.T) {
	fs := &fakeFS{stats: map[string]fsStat{
		"/":     {Total: 4 * gib, Free: 1 * gib, Avail: 1 * gib},
		"/data": {Total: 2 * gib, Free: 1 * gib, Avail: 1 * gib},
	}}
	clock := func() time.Time { return time.Unix(7000, 0) }
	m := &Mod{fs: fs, now: clock}
	params := map[string]any{"volumes": []any{"/", "/data"}, "poll-interval": 5 * time.Second}
	for i := range 3 {
		if _, err := m.Poll(context.Background(), params); err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
	}

	snap := m.HistSnapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot series = %d, want disk// + disk//data: %+v", len(snap), snap)
	}
	root, ok := snap["disk//"]
	if !ok {
		t.Fatalf("snapshot missing disk//: %+v", snap)
	}
	if root.Cadence != 5*time.Second || root.LastUnix != 7000 {
		t.Errorf("disk// meta = {%v %d}, want {5s 7000}", root.Cadence, root.LastUnix)
	}
	if len(root.Samples) != 3 || root.Samples[0] != 0.75 {
		t.Errorf("disk// samples = %v, want three 0.75", root.Samples)
	}
	if _, ok := snap["disk//data"]; !ok {
		t.Errorf("snapshot missing disk//data: %+v", snap)
	}

	m2 := &Mod{fs: fs, now: clock}
	m2.HistRestore(snap)
	snap2 := m2.HistSnapshot()
	if len(snap2) != 2 {
		t.Fatalf("restored snapshot series = %d, want 2: %+v", len(snap2), snap2)
	}
	if got := snap2["disk//"]; got.Cadence != root.Cadence || got.LastUnix != root.LastUnix || len(got.Samples) != 3 {
		t.Errorf("restore changed disk//: %+v vs %+v", got, root)
	}

	// restored history keeps growing through Poll
	data, err := m2.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll after restore: %v", err)
	}
	if got := len(m2.HistSnapshot()["disk//"].Samples); got != 4 {
		t.Errorf("ring length after restore+poll = %d, want 4 (3 restored + 1)", got)
	}
	_ = data

	// foreign and malformed entries are ignored
	m3 := &Mod{fs: fs}
	m3.HistRestore(map[string]module.HistState{
		"cpu":    {Cadence: time.Second, LastUnix: 1, Samples: []float32{0.1}},
		"disk/":  {Cadence: time.Second, LastUnix: 1, Samples: []float32{0.1}},
		"disk/x": {Cadence: time.Second, LastUnix: 1, Samples: nil},
	})
	if got := m3.HistSnapshot(); len(got) != 0 {
		t.Errorf("foreign/malformed series restored: %+v", got)
	}
}

func TestVolumesParam(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		want   []string
	}{
		{"nil params", nil, []string{"/"}},
		{"missing key", map[string]any{}, []string{"/"}},
		{"empty list", map[string]any{"volumes": []any{}}, []string{"/"}},
		{"json shape", map[string]any{"volumes": []any{"/a", "/b"}}, []string{"/a", "/b"}},
		{"typed shape", map[string]any{"volumes": []string{"/a"}}, []string{"/a"}},
		{"blank entries dropped", map[string]any{"volumes": []any{"", "/a"}}, []string{"/a"}},
	} {
		got := volumes(tc.params)
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s: volumes = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestHuman(t *testing.T) {
	for _, tc := range []struct {
		in   uint64
		want string
	}{
		{0, "0B"},
		{512, "512B"},
		{812 * gib, "812G"},
		{92 * gib, "92G"},
		{1843 * gib, "1.8T"},
		{5 * 1024, "5.0K"},
	} {
		if got := human(tc.in); got != tc.want {
			t.Errorf("human(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
