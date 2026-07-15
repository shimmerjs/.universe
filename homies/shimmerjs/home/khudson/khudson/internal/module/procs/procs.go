// Package procs samples the top cpu consumers, emitting RowKV rows per the
// resources contract (Key = lowercase process name, Value = "NN.N%c MM.M%m",
// cpu then mem). Primary sampler is /usr/bin/top -- macOS 26 blocks ps's
// pcpu/pmem columns for non-child processes without the proc-info
// entitlement, while top is entitled; ps stays as the fallback when top
// fails. The same top exec's header also yields whole-host cpu utilization,
// cached on the singleton for the resources composite (see Utilization).
package procs

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	"golang.org/x/sys/unix"
)

// defaultTop bounds the emitted rows when params.top is absent.
const defaultTop = 10

// sampleEvery is the default floor between sampler execs; params
// "sampleEvery" (a duration string) overrides it. top -l 2 -s 1 holds ~1s
// wall and walks every process twice, so at the resources widget's 5s poll
// the exec alone is a ~20% duty cycle -- ticks between refreshes re-render
// from the cache with zero subprocesses, the same cadence-cache seam as
// dockmirror.
const sampleEvery = 15 * time.Second

// Mod implements module.Module. The singleton caches the sampler's result
// (rows OR error, plus the utilization) between cadence ticks; now and
// sample are test seams.
type Mod struct {
	mu      sync.Mutex
	now     func() time.Time
	sample  func(ctx context.Context, top int) (module.Data, float64, bool, error)
	last    time.Time
	lastTop int
	data    module.Data
	err     error
	util    float64
	hasUtil bool
}

// New returns the module singleton for the registry.
func New() *Mod { return &Mod{} }

func (*Mod) Name() string { return "procs" }

// Poll reruns the sampler at most once per cadence tick and reuses the
// cached result (rows OR error) between ticks -- a cached error is returned
// without an exec too, so a failing sampler cannot re-open the per-tick
// exec cost either. A changed top param busts the cache (the row cap is
// baked into the sampled rows).
func (m *Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	top := module.IntParam(params, "top", defaultTop)
	every := sampleEvery
	if s, ok := params["sampleEvery"].(string); ok {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			every = d
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now, sample := time.Now, samplePoll
	if m.now != nil {
		now = m.now
	}
	if m.sample != nil {
		sample = m.sample
	}
	if !m.last.IsZero() && now().Sub(m.last) < every && top == m.lastTop {
		return m.data, m.err
	}
	data, util, hasUtil, err := sample(ctx, top)
	if err != nil && ctx.Err() != nil {
		return data, err // timeout: leave m.last unset so the next poll retries
	}
	m.data, m.err = data, err
	m.util, m.hasUtil = util, hasUtil
	m.last, m.lastTop = now(), top
	return m.data, m.err
}

// samplePoll is the exec behind Poll: top primary, ps fallback (which
// carries no host-wide cpu line, so utilization reads unmeasured).
func samplePoll(ctx context.Context, top int) (module.Data, float64, bool, error) {
	if rows, util, ok, err := pollTop(ctx, top); err == nil && len(rows) > 0 {
		return module.Data{Title: "procs", Rows: rows}, util, ok, nil
	}
	data, err := pollPS(ctx, top)
	return data, 0, false, err
}

// Utilization reports the whole-host cpu busy fraction (0..1) measured by
// the last successful top poll; ok is false when that poll fell back to ps
// (ps carries no host-wide cpu line).
func (m *Mod) Utilization() (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.util, m.hasUtil
}

func (m *Mod) setUtil(util float64, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.util, m.hasUtil = util, ok
}

// pollTop samples via /usr/bin/top -- the absolute path, since PATH's top
// may be procps on nix-managed hosts. Two samples: the first reports
// since-boot cpu, so parseTop and parseUtil keep only the second. -s 1
// pins the between-sample delay so the exec fits the bus's 5s poll budget.
func pollTop(ctx context.Context, top int) ([]module.Row, float64, bool, error) {
	out, err := exec.CommandContext(ctx, "/usr/bin/top",
		"-l", "2", "-s", "1", "-o", "cpu", "-n", strconv.Itoa(top),
		"-stats", "pid,command,cpu,mem").Output()
	if err != nil {
		return nil, 0, false, fmt.Errorf("top: %w", err)
	}
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		total = 0
	}
	util, ok := parseUtil(string(out))
	return parseTop(string(out), top, float64(total)), util, ok, nil
}

// parseUtil extracts the busy fraction from the last "CPU usage: X% user,
// Y% sys, Z% idle" header line -- the second sample's; the first sample's
// cpu accounting is since-boot.
func parseUtil(out string) (float64, bool) {
	idle, ok := 0.0, false
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[0] != "CPU" || f[1] != "usage:" || f[7] != "idle" {
			continue
		}
		if v, err := strconv.ParseFloat(strings.TrimSuffix(f[6], "%"), 64); err == nil {
			idle, ok = v, true
		}
	}
	if !ok || idle < 0 || idle > 100 {
		return 0, false
	}
	return (100 - idle) / 100, true
}

// parseTop turns top -l output into RowKV rows, capped at top. Only the
// last "PID COMMAND %CPU MEM" table counts (the first sample's cpu is
// since-boot). Rows whose cpu and mem both display as 0.0 are junk and
// dropped; first hit per name wins (top -o cpu sorts descending, so dups
// keep their hottest line). Unparsable lines are skipped, not fatal.
func parseTop(out string, top int, totalMemBytes float64) []module.Row {
	lines := strings.Split(out, "\n")
	start := -1
	for i, line := range lines {
		if f := strings.Fields(line); len(f) > 0 && f[0] == "PID" {
			start = i + 1
		}
	}
	if start < 0 {
		return nil
	}
	var rows []module.Row
	seen := map[string]bool{}
	for _, line := range lines[start:] {
		if len(rows) >= top {
			break
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		if _, err := strconv.Atoi(f[0]); err != nil {
			continue
		}
		cpu, err := strconv.ParseFloat(f[len(f)-2], 64)
		if err != nil {
			continue
		}
		memB, ok := memBytes(f[len(f)-1])
		if !ok {
			continue
		}
		mem := 0.0
		if totalMemBytes > 0 {
			mem = memB / totalMemBytes * 100
		}
		if cpu < 0.05 && mem < 0.05 {
			continue
		}
		key := strings.ToLower(strings.Join(f[1:len(f)-2], " "))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, module.KV(key, fmt.Sprintf("%.1f%%c %.1f%%m", cpu, mem)))
	}
	return rows
}

// memBytes parses a top MEM cell ("13G+", "8640K", "281M-", "0B"): a number,
// a B/K/M/G/T unit, and an optional +/- delta marker.
func memBytes(s string) (float64, bool) {
	s = strings.TrimRight(s, "+-")
	if s == "" {
		return 0, false
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'B':
		s = s[:len(s)-1]
	case 'K':
		s, mult = s[:len(s)-1], 1<<10
	case 'M':
		s, mult = s[:len(s)-1], 1<<20
	case 'G':
		s, mult = s[:len(s)-1], 1<<30
	case 'T':
		s, mult = s[:len(s)-1], 1<<40
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n * mult, true
}

// pollPS is the fallback sampler.
func pollPS(ctx context.Context, top int) (module.Data, error) {
	// -c: bare executable names; -r: cpu-descending, so parse keeps each
	// name's hottest line
	out, err := exec.CommandContext(ctx, "ps", "-Aceo", "pcpu=,pmem=,comm=", "-r").Output()
	// without the proc-info entitlement macOS 26 ps exits 1 (and omits
	// the pmem column) while still streaming rows; non-empty output wins
	// over the exit status
	if err != nil && len(out) == 0 {
		return module.Data{}, fmt.Errorf("ps: %w", err)
	}
	return psData(string(out), top), nil
}

// psData turns ps output into the fallback Data. Entitlement-blocked ps
// zeroes every row; when the filter leaves nothing, one dim text row says
// so instead of a junk table.
func psData(out string, top int) module.Data {
	rows := parse(out, top)
	if len(rows) == 0 {
		rows = []module.Row{{Kind: module.RowText, Text: "process stats unavailable", Style: module.StyleDim}}
	}
	return module.Data{Title: "procs", Rows: rows}
}

// parse turns ps rows into RowKV rows, capped at top. First hit per name
// wins: ps -r sorts cpu-descending, so dups and ties keep their first
// (hottest) line. Rows whose cpu and mem both display as 0.0 are junk
// (entitlement-blocked ps zeroes every row) and dropped. Unparsable lines
// are skipped, not fatal.
func parse(out string, top int) []module.Row {
	var rows []module.Row
	seen := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		if len(rows) >= top {
			break
		}
		cpu, mem, comm, ok := parseLine(line)
		if !ok || (cpu < 0.05 && mem < 0.05) {
			continue
		}
		key := strings.ToLower(filepath.Base(comm))
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, module.KV(key, fmt.Sprintf("%.1f%%c %.1f%%m", cpu, mem)))
	}
	return rows
}

// parseLine splits "pcpu pmem comm"; comm may contain spaces. The pmem
// column is optional: entitlement-blocked ps omits it entirely, so a
// non-numeric second field starts comm and mem reads 0.
func parseLine(line string) (float64, float64, string, bool) {
	first, rest := cutField(strings.TrimSpace(line))
	cpu, err := strconv.ParseFloat(first, 64)
	if err != nil || rest == "" {
		return 0, 0, "", false
	}
	second, after := cutField(rest)
	if mem, err := strconv.ParseFloat(second, 64); err == nil {
		return cpu, mem, after, after != ""
	}
	return cpu, 0, rest, true
}

// cutField splits the leading whitespace-delimited field from the rest.
func cutField(s string) (string, string) {
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimLeft(s[i:], " \t")
}
