// Package disk reports per-volume usage via syscall.Statfs -- no
// subprocesses. Per volume: one RowResource cluster (current used-fraction
// gauge + history sparkline). Stateful singleton: history rings persist
// across Poll calls.
package disk

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// fsStat is the statfs subset the module consumes, in bytes.
type fsStat struct {
	Total uint64
	Free  uint64 // superuser free; drives the used fraction
	Avail uint64 // available to unprivileged users
}

// statfser abstracts syscall.Statfs so tests can fake volumes.
type statfser interface {
	statfs(path string) (fsStat, error)
}

type sysStatfs struct{}

func (sysStatfs) statfs(path string) (fsStat, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return fsStat{}, err
	}
	bs := uint64(st.Bsize)
	return fsStat{Total: st.Blocks * bs, Free: st.Bfree * bs, Avail: st.Bavail * bs}, nil
}

// Mod implements module.Module and module.Persistent. Wire it as New()
// (or &Mod{} in tests): copying a Mod value would fork the history rings.
type Mod struct {
	mu   sync.Mutex
	fs   statfser
	hist map[string]*module.Ring
	// snapshot bookkeeping: per-volume unix time of the newest sample (an
	// unmounted volume stops pushing) and the cadence of the last poll
	last    map[string]int64
	cadence time.Duration
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

func (*Mod) Name() string { return "disk" }

func (m *Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	histCap, hint := module.HistWindow(params)
	m.mu.Lock()
	fs := m.fs
	if fs == nil {
		fs = sysStatfs{}
	}
	if m.hist == nil {
		m.hist = map[string]*module.Ring{}
	}
	if m.last == nil {
		m.last = map[string]int64{}
	}
	m.cadence = module.HistCadence(params)
	m.mu.Unlock()

	var rows []module.Row
	for _, vol := range volumes(params) {
		st, err := statfsCtx(ctx, fs, vol)
		if err != nil {
			if ctx.Err() != nil {
				return module.Data{}, err
			}
			rows = append(rows, module.Row{Kind: module.RowText, Text: vol + ": not mounted", Style: module.StyleDim})
			continue
		}
		used := uint64(0)
		if st.Total > st.Free {
			used = st.Total - st.Free
		}
		frac := 0.0
		if st.Total > 0 {
			frac = float64(used) / float64(st.Total)
		}
		m.mu.Lock()
		r := module.ResizeRing(m.hist[vol], histCap)
		m.hist[vol] = r
		r.Push(frac)
		// the ring keeps recording used-fraction (stable persisted unit);
		// the EMITTED series is the free-floor danger derived from it, so
		// the card's dot grid heats on the same rule as the row's numbers.
		// The historical transform only knows superuser-free (avail is not
		// in the ring); close enough for a trend line.
		floor := float64(module.IntParam(params, "free-floor", defaultFreeFloorGiB))
		totalGiB := float64(st.Total) / gibF
		series := module.BucketMax(r.Samples(), module.MaxSeries)
		for i, s := range series {
			series[i] = freeFloorDanger((1-s)*totalGiB, floor)
		}
		now := time.Now
		if m.now != nil {
			now = m.now
		}
		m.last[vol] = now().Unix()
		m.mu.Unlock()
		row := module.Resource(vol+" "+hint, frac, series,
			fmt.Sprintf("%s/%s free %s", human(used), human(st.Total), human(st.Avail)))
		// the whole row heats by ABSOLUTE free space (user-available):
		// neutral above the floor, warming below it, loud toward empty. A
		// mostly-full big disk with real headroom stays quiet; the percent
		// column still DISPLAYS used-fraction, it just ramps by PctHeat.
		danger := freeFloorDanger(float64(st.Avail)/gibF, floor)
		row.RawHeat, row.PctHeat = danger, danger
		row.RawX, row.RawY = human(st.Avail), " free"
		rows = append(rows, row)
	}
	return module.Data{Title: "disk", Rows: rows}, nil
}

// defaultFreeFloorGiB is the free-space floor (params "free-floor", GiB):
// the volume reads neutral until free space drops toward it.
const defaultFreeFloorGiB = 40

// gibF converts statfs bytes to GiB for the danger math (the test file
// owns the package's `gib` name).
const gibF = float64(1 << 30)

// freeFloorDanger maps free GiB to the 0..1 ramp fraction: NEUTRAL at and
// above the floor (peaking just under the warn threshold, decaying toward
// zero with headroom), then warm-to-loud climbing linearly to 1.0 at empty
// below it -- "not hot until under the floor", boundary included. A
// non-positive floor disables the ramp entirely.
func freeFloorDanger(freeGiB, floorGiB float64) float64 {
	if floorGiB <= 0 {
		return 0
	}
	if freeGiB >= floorGiB {
		return 0.55 * floorGiB / max(freeGiB, floorGiB)
	}
	return min(0.6+0.4*(1-max(freeGiB, 0)/floorGiB), 1)
}

// histPrefix namespaces disk series in the shared snapshot: "disk/<vol>".
const histPrefix = "disk/"

// HistSnapshot implements module.Persistent: one "disk/<vol>" series per
// tracked volume.
func (m *Mod) HistSnapshot() map[string]module.HistState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]module.HistState, len(m.hist))
	for vol, r := range m.hist {
		s := module.SnapRing(r)
		if len(s) == 0 {
			continue
		}
		out[histPrefix+vol] = module.HistState{Cadence: m.cadence, LastUnix: m.last[vol], Samples: s}
	}
	return out
}

// HistRestore implements module.Persistent: entries without the disk
// prefix are ignored. Restored rings are sized to their samples; the next
// poll's ResizeRing re-caps them to the configured window.
func (m *Mod) HistRestore(hist map[string]module.HistState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, st := range hist {
		vol, ok := strings.CutPrefix(name, histPrefix)
		if !ok || vol == "" || len(st.Samples) == 0 {
			continue
		}
		if m.hist == nil {
			m.hist = map[string]*module.Ring{}
		}
		if m.last == nil {
			m.last = map[string]int64{}
		}
		m.hist[vol] = module.RestoreRing(st.Samples)
		m.last[vol] = st.LastUnix
		m.cadence = st.Cadence
	}
}

// statfsCtx bounds one statfs by ctx: a dead network mount can block the
// syscall indefinitely, and no lock may be held across it. The buffered
// chan lets a timed-out goroutine finish without leaking on send.
func statfsCtx(ctx context.Context, fs statfser, path string) (fsStat, error) {
	type result struct {
		st  fsStat
		err error
	}
	ch := make(chan result, 1)
	go func() {
		st, err := fs.statfs(path)
		ch <- result{st, err}
	}()
	select {
	case r := <-ch:
		return r.st, r.err
	case <-ctx.Done():
		return fsStat{}, ctx.Err()
	}
}

// volumes reads params.volumes ([]string, default ["/"]), tolerating the
// []any shape the JSON-decoded config delivers.
func volumes(params map[string]any) []string {
	var vols []string
	switch raw := params["volumes"].(type) {
	case []string:
		for _, s := range raw {
			if s != "" {
				vols = append(vols, s)
			}
		}
	case []any:
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				vols = append(vols, s)
			}
		}
	}
	if len(vols) == 0 {
		return []string{"/"}
	}
	return vols
}

// human formats bytes df-style: binary units, one decimal below 10.
func human(b uint64) string {
	v := float64(b)
	for _, suf := range []string{"B", "K", "M", "G", "T", "P"} {
		if v < 1000 || suf == "P" {
			if v < 10 && suf != "B" {
				return fmt.Sprintf("%.1f%s", v, suf)
			}
			return fmt.Sprintf("%.0f%s", v, suf)
		}
		v /= 1024
	}
	return fmt.Sprintf("%dB", b)
}
