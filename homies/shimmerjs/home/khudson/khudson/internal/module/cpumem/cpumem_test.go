package cpumem

import (
	"context"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

func TestRenderClamp(t *testing.T) {
	m := &Mod{} // New() is the process-wide singleton; tests isolate

	d := m.render(sample{load1: 30, ncpu: 4, usedGiB: 40, totalGiB: 36}, nil)
	if got := last(t, d.Rows[0]); got != 1 {
		t.Errorf("overloaded cpu sample = %v, want 1", got)
	}
	if got := last(t, d.Rows[1]); got != 1 {
		t.Errorf("overcommitted mem sample = %v, want 1", got)
	}
	if d.Rows[0].Frac != 1 || d.Rows[1].Frac != 1 {
		t.Errorf("overloaded fracs = %v/%v, want 1/1", d.Rows[0].Frac, d.Rows[1].Frac)
	}

	d = m.render(sample{load1: -1, ncpu: 4, usedGiB: -1, totalGiB: 36}, nil)
	if got := last(t, d.Rows[0]); got != 0 {
		t.Errorf("negative cpu sample = %v, want 0", got)
	}
	if got := last(t, d.Rows[1]); got != 0 {
		t.Errorf("negative mem sample = %v, want 0", got)
	}

	// zero denominators degrade to 0, never NaN
	d = m.render(sample{load1: 3, ncpu: 0, usedGiB: 10, totalGiB: 0}, nil)
	if got := last(t, d.Rows[0]); got != 0 {
		t.Errorf("ncpu-0 cpu sample = %v, want 0", got)
	}
	if got := last(t, d.Rows[1]); got != 0 {
		t.Errorf("total-0 mem sample = %v, want 0", got)
	}
}

func TestRenderRowShape(t *testing.T) {
	m := &Mod{}
	var d module.Data
	for range 3 {
		d = m.render(sample{load1: 4.56, ncpu: 12, usedGiB: 20.5, totalGiB: 36}, nil)
	}
	if d.Title != "cpu / mem" {
		t.Errorf("Title = %q, want %q", d.Title, "cpu / mem")
	}
	if len(d.Rows) != 2 {
		t.Fatalf("len(Rows) = %d, want 2", len(d.Rows))
	}
	cpu, mem := d.Rows[0], d.Rows[1]
	if cpu.Kind != module.RowResource || cpu.Key != "cpu 6h" {
		t.Errorf("cpu row = kind %q key %q, want resource/%q", cpu.Kind, cpu.Key, "cpu 6h")
	}
	if cpu.Value != "load 4.6 / 12c" {
		t.Errorf("cpu.Value = %q, want %q", cpu.Value, "load 4.6 / 12c")
	}
	if cpu.Frac != last(t, cpu) {
		t.Errorf("cpu.Frac = %v, want newest series sample %v", cpu.Frac, last(t, cpu))
	}
	if len(cpu.Series) != 3 {
		t.Errorf("len(cpu.Series) = %d, want 3 (one per render)", len(cpu.Series))
	}
	if mem.Kind != module.RowResource || mem.Key != "mem 6h" {
		t.Errorf("mem row = kind %q key %q, want resource/%q", mem.Kind, mem.Key, "mem 6h")
	}
	if mem.Value != "20.5/36 GiB" {
		t.Errorf("mem.Value = %q, want %q", mem.Value, "20.5/36 GiB")
	}
	if mem.Frac != last(t, mem) {
		t.Errorf("mem.Frac = %v, want newest series sample %v", mem.Frac, last(t, mem))
	}
	if len(mem.Series) != 3 {
		t.Errorf("len(mem.Series) = %d, want 3 (one per render)", len(mem.Series))
	}
}

// TestRenderValueScale pins the cpu Value semantics: a measured utilization
// renders as a whole percent (multiplied by 100 exactly once) and drives
// Frac; without one the row degrades to an honest loadavg label, never a
// fake percent.
func TestRenderValueScale(t *testing.T) {
	d := (&Mod{}).render(sample{load1: 422.6, ncpu: 12, util: 0.35, hasUtil: true,
		usedGiB: 10, totalGiB: 36}, nil)
	if got := d.Rows[0].Value; got != "35%" {
		t.Errorf("cpu.Value = %q, want %q", got, "35%")
	}
	if got := d.Rows[0].Frac; got != 0.35 {
		t.Errorf("cpu.Frac = %v, want util 0.35 (not load/ncpu)", got)
	}

	load := 4.2
	d = (&Mod{}).render(sample{load1: load, ncpu: 12, usedGiB: 10, totalGiB: 36}, nil)
	if got := d.Rows[0].Value; got != "load 4.2 / 12c" {
		t.Errorf("fallback cpu.Value = %q, want %q", got, "load 4.2 / 12c")
	}
	if got := d.Rows[0].Frac; got != load/12 {
		t.Errorf("fallback cpu.Frac = %v, want load/ncpu %v", got, load/12)
	}
}

// TestPollUtilParam pins the composite injection point: params["cpu-util"]
// overrides the loadavg fallback through the real Poll path.
func TestPollUtilParam(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only samplers")
	}
	if _, err := exec.LookPath("vm_stat"); err != nil {
		t.Skipf("vm_stat: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := (&Mod{}).Poll(ctx, map[string]any{"cpu-util": 0.42})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	want := "42%"
	if got := d.Rows[0].Value; got != want {
		t.Errorf("cpu.Value = %q, want %q", got, want)
	}
	if got := d.Rows[0].Frac; got != 0.42 {
		t.Errorf("cpu.Frac = %v, want injected 0.42", got)
	}
}

// TestRenderWindowParam pins the window plumbing: params.window sizes the
// rings (window / assumed 5s cadence) and lands in the row keys as the
// hint; deep history reaches emission bucket-downsampled, not truncated.
func TestRenderWindowParam(t *testing.T) {
	m := &Mod{}
	params := map[string]any{"window": "1m"} // capacity 12
	var d module.Data
	for range 15 {
		d = m.render(sample{load1: 6, ncpu: 12, usedGiB: 18, totalGiB: 36}, params)
	}
	if got := d.Rows[0].Key; got != "cpu 1m" {
		t.Errorf("cpu key = %q, want %q", got, "cpu 1m")
	}
	if got := len(d.Rows[0].Series); got != 12 {
		t.Errorf("len(cpu.Series) = %d, want ring capacity 12", got)
	}

	// same singleton re-polled with a wider window keeps its history
	d = m.render(sample{load1: 6, ncpu: 12, usedGiB: 18, totalGiB: 36}, nil)
	if got := d.Rows[0].Key; got != "cpu 6h" {
		t.Errorf("cpu key after window change = %q, want %q", got, "cpu 6h")
	}
	if got := len(d.Rows[0].Series); got != 13 {
		t.Errorf("len(cpu.Series) after resize = %d, want 13 (12 kept + 1)", got)
	}
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

// TestRenderCadenceParam pins cadence sizing: the scheduler-injected
// poll-interval sizes the ring as window/cadence (30s at 1s = 30, not the
// assumed-5s fallback's 6), while emission stays bucket-capped at MaxSeries.
func TestRenderCadenceParam(t *testing.T) {
	m := &Mod{}
	params := map[string]any{"window": "30s", "poll-interval": time.Second}
	var d module.Data
	for range 40 {
		d = m.render(sample{load1: 6, ncpu: 12, usedGiB: 18, totalGiB: 36}, params)
	}
	if got := len(d.Rows[0].Series); got != 30 {
		t.Errorf("len(cpu.Series) = %d, want ring capacity 30 (window/real cadence)", got)
	}

	// a window-deep ring emits at most MaxSeries samples regardless
	m = &Mod{}
	params = map[string]any{"window": "6h", "poll-interval": time.Second} // capacity 21600
	for range module.MaxSeries + 30 {
		d = m.render(sample{load1: 6, ncpu: 12, usedGiB: 18, totalGiB: 36}, params)
	}
	if got := len(d.Rows[0].Series); got != module.MaxSeries {
		t.Errorf("len(cpu.Series) = %d, want bucket-max cap %d", got, module.MaxSeries)
	}
}

// TestHistSnapshotRestore proves the module.Persistent round trip: a
// snapshot restores into a fresh instance byte-for-byte and history keeps
// accumulating after the restore.
func TestHistSnapshotRestore(t *testing.T) {
	clock := func() time.Time { return time.Unix(5000, 0) }
	m := &Mod{now: clock}
	params := map[string]any{"window": "1m", "poll-interval": 5 * time.Second}
	for _, util := range []float64{0.25, 0.5, 0.75} {
		m.render(sample{load1: 1, ncpu: 8, util: util, hasUtil: true, usedGiB: 18, totalGiB: 36}, params)
	}

	snap := m.HistSnapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot series = %d, want cpu+mem: %+v", len(snap), snap)
	}
	cpu := snap["cpu"]
	if cpu.Cadence != 5*time.Second || cpu.LastUnix != 5000 {
		t.Errorf("cpu meta = {%v %d}, want {5s 5000}", cpu.Cadence, cpu.LastUnix)
	}
	if len(cpu.Samples) != 3 || cpu.Samples[0] != 0.25 || cpu.Samples[2] != 0.75 {
		t.Errorf("cpu samples = %v, want [0.25 0.5 0.75]", cpu.Samples)
	}

	m2 := &Mod{now: clock}
	m2.HistRestore(snap)
	snap2 := m2.HistSnapshot()
	if len(snap2) != 2 {
		t.Fatalf("restored snapshot series = %d, want 2", len(snap2))
	}
	for _, name := range []string{"cpu", "mem"} {
		a, b := snap[name], snap2[name]
		if a.Cadence != b.Cadence || a.LastUnix != b.LastUnix || len(a.Samples) != len(b.Samples) {
			t.Fatalf("%s: restore changed the series: %+v vs %+v", name, a, b)
		}
		for i := range a.Samples {
			if a.Samples[i] != b.Samples[i] {
				t.Fatalf("%s: sample %d = %v, want %v", name, i, b.Samples[i], a.Samples[i])
			}
		}
	}

	// restored history keeps growing through render
	d := m2.render(sample{load1: 1, ncpu: 8, util: 1, hasUtil: true, usedGiB: 18, totalGiB: 36}, params)
	if got := len(d.Rows[0].Series); got != 4 {
		t.Errorf("len(cpu.Series) after restore+render = %d, want 4 (3 restored + 1)", got)
	}
	if got := last(t, d.Rows[0]); got != 1 {
		t.Errorf("newest sample after restore = %v, want 1", got)
	}

	// entries the module doesn't own are ignored
	m3 := &Mod{}
	m3.HistRestore(map[string]module.HistState{
		"disk//": {Cadence: time.Second, LastUnix: 1, Samples: []float32{0.1}},
	})
	if got := m3.HistSnapshot(); len(got) != 0 {
		t.Errorf("foreign series restored: %+v", got)
	}
}

// TestSampleLive proves the vm.loadavg struct parse and the vm_stat exec
// against the running host.
func TestSampleLive(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only samplers")
	}
	if _, err := exec.LookPath("vm_stat"); err != nil {
		t.Skipf("vm_stat: %v", err)
	}
	load1, err := loadAvg()
	if err != nil {
		t.Fatalf("loadAvg: %v", err)
	}
	if load1 < 0 || load1 > 1024 {
		t.Fatalf("loadAvg = %v, want a sane load average", load1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	used, total, err := memory(ctx)
	if err != nil {
		t.Fatalf("memory: %v", err)
	}
	if total <= 0 || used <= 0 || used > total {
		t.Fatalf("memory = %.1f/%.1f GiB, want 0 < used <= total", used, total)
	}
}

func last(t *testing.T, r module.Row) float64 {
	t.Helper()
	if len(r.Series) == 0 {
		t.Fatal("empty series")
	}
	return r.Series[len(r.Series)-1]
}
