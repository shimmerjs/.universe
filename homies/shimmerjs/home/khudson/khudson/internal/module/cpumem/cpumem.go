// Package cpumem is the home-screen cpu/memory module: load and memory
// utilization as RowResource clusters (current gauge + history sparkline).
// Sampling is subprocess-free except one vm_stat exec per poll (darwin has
// no public sysctl for the used-page counts).
package cpumem

import (
	"context"
	"encoding/binary"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	"golang.org/x/sys/unix"
)

// Mod implements module.Module and module.Persistent. The singleton keeps
// cpu/mem history rings across Poll calls; the rings are sized from
// params.window at render time and survive restarts via histsnap.
type Mod struct {
	mu  sync.Mutex
	cpu *module.Ring
	mem *module.Ring
	// snapshot bookkeeping: the cadence the rings were last filled at and
	// the newest sample's unix time
	cadence time.Duration
	last    int64
	now     func() time.Time // test seam; nil = time.Now
}

var (
	newOnce   sync.Once
	singleton *Mod
)

// New returns the module singleton for the registry -- one process-wide
// instance, so the histories main's hist-snapshot path restores are the
// same rings bus.Run's own registry.All() call polls.
func New() *Mod {
	newOnce.Do(func() { singleton = &Mod{} })
	return singleton
}

func (*Mod) Name() string { return "cpumem" }

func (m *Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	load1, err := loadAvg()
	if err != nil {
		return module.Data{}, err
	}
	used, total, err := memory(ctx)
	if err != nil {
		return module.Data{}, err
	}
	s := sample{
		load1:    load1,
		ncpu:     runtime.NumCPU(),
		usedGiB:  used,
		totalGiB: total,
	}
	// "cpu-util" is runtime-injected by the resources composite (the
	// whole-host busy fraction its top exec measured), never schema'd config
	if v, ok := params["cpu-util"].(float64); ok {
		s.util, s.hasUtil = v, true
	}
	return m.render(s, params), nil
}

// sample is one poll's readings. util is a measured whole-host busy
// fraction (0..1) when a composite supplies one; without it the cpu row
// degrades to loadavg, which is NOT a utilization percent (macOS 26 hosts
// report load averages in the hundreds).
type sample struct {
	load1    float64
	ncpu     int
	usedGiB  float64
	totalGiB float64
	util     float64
	hasUtil  bool
}

// render pushes the raw sample into the window-deep history rings and
// builds the rows; module.Resource clamps fractions and series values to
// 0..1 on emission, and the Value strings keep the unclamped human-units
// readings. Emitted Series is bucket-max downsampled: the rings hold the
// full window, so Series' newest-MaxSeries cap must never do the
// truncating.
func (m *Mod) render(s sample, params map[string]any) module.Data {
	histCap, hint := module.HistWindow(params)
	// measured utilization drives the cpu gauge, history, and Value; the
	// loadavg fallback labels itself as load -- never a fake percent. The
	// raw pair is ALWAYS load-over-cores (scheduler pressure): it may
	// legitimately disagree with a utilization percent, and that
	// disagreement is diagnostic.
	cpuFrac := 0.0
	if s.ncpu > 0 {
		cpuFrac = s.load1 / float64(s.ncpu)
	}
	cpuVal := fmt.Sprintf("load %.1f / %dc", s.load1, s.ncpu)
	if s.hasUtil {
		cpuFrac = s.util
		cpuVal = fmt.Sprintf("%.0f%%", s.util*100)
	}
	memFrac := 0.0
	if s.totalGiB > 0 {
		memFrac = s.usedGiB / s.totalGiB
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cpu = module.ResizeRing(m.cpu, histCap)
	m.mem = module.ResizeRing(m.mem, histCap)
	m.cpu.Push(cpuFrac)
	m.mem.Push(memFrac)
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	m.cadence = module.HistCadence(params)
	m.last = now().Unix()
	cpu := module.Resource("cpu "+hint, cpuFrac, module.BucketMax(m.cpu.Samples(), module.MaxSeries),
		cpuVal)
	// the whole row tones by ONE signal -- whatever drives the gauge
	// (measured utilization, or the loadavg fallback). The load pair stays
	// displayed as data, but it carries no alarm of its own: macOS load
	// EMAs run inflated (post-burst tails, QoS churn), and a red load
	// beside a neutral percent read as a contradiction on glass.
	cpu.RawX, cpu.RawY, cpu.RawHeat = fmt.Sprintf("%.1f", s.load1), fmt.Sprintf("/%d", s.ncpu), cpuFrac
	mem := module.Resource("mem "+hint, memFrac, module.BucketMax(m.mem.Samples(), module.MaxSeries),
		fmt.Sprintf("%.1f/%.0f GiB", s.usedGiB, s.totalGiB))
	mem.RawX, mem.RawY, mem.RawHeat = gibCompact(s.usedGiB), "/"+gibCompact(s.totalGiB)+"G", memFrac
	return module.Data{Title: "cpu / mem", Rows: []module.Row{cpu, mem}}
}

// gibCompact renders GiB for the tight raw column: one decimal under 10,
// whole numbers above.
func gibCompact(v float64) string {
	if v < 10 {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.0f", v)
}

// HistSnapshot implements module.Persistent: series "cpu" and "mem".
func (m *Mod) HistSnapshot() map[string]module.HistState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]module.HistState, 2)
	for name, r := range map[string]*module.Ring{"cpu": m.cpu, "mem": m.mem} {
		s := module.SnapRing(r)
		if len(s) == 0 {
			continue
		}
		out[name] = module.HistState{Cadence: m.cadence, LastUnix: m.last, Samples: s}
	}
	return out
}

// HistRestore implements module.Persistent: entries the module doesn't own
// are ignored. Restored rings are sized to their samples; the next poll's
// ResizeRing re-caps them to the configured window.
func (m *Mod) HistRestore(hist map[string]module.HistState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := hist["cpu"]; ok && len(st.Samples) > 0 {
		m.cpu = module.RestoreRing(st.Samples)
		m.cadence, m.last = st.Cadence, st.LastUnix
	}
	if st, ok := hist["mem"]; ok && len(st.Samples) > 0 {
		m.mem = module.RestoreRing(st.Samples)
		m.cadence, m.last = st.Cadence, st.LastUnix
	}
}

// loadAvg reads the 1-minute load average from the vm.loadavg sysctl:
// struct loadavg { fixpt_t ldavg[3]; long fscale; } -- three uint32
// fixed-point averages, 4 bytes padding, int64 scale.
func loadAvg() (float64, error) {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return 0, fmt.Errorf("sysctl vm.loadavg: %w", err)
	}
	if len(raw) < 24 {
		return 0, fmt.Errorf("sysctl vm.loadavg: short read (%d bytes)", len(raw))
	}
	fscale := binary.LittleEndian.Uint64(raw[16:24])
	if fscale == 0 {
		return 0, fmt.Errorf("sysctl vm.loadavg: zero fscale")
	}
	return float64(binary.LittleEndian.Uint32(raw[0:4])) / float64(fscale), nil
}

// memory returns (used GiB, total GiB): total from the hw.memsize sysctl,
// used from one vm_stat exec.
func memory(ctx context.Context) (float64, float64, error) {
	totalBytes, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, 0, fmt.Errorf("sysctl hw.memsize: %w", err)
	}
	out, err := exec.CommandContext(ctx, "vm_stat").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("vm_stat: %w", err)
	}
	used, err := module.VMStatUsedGiB(string(out))
	if err != nil {
		return 0, 0, err
	}
	const gib = 1024 * 1024 * 1024
	return used, float64(totalBytes) / gib, nil
}
