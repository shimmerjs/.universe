package procs

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// psOut mirrors ps -Aceo pcpu=,pmem=,comm= -r: right-aligned floats,
// cpu-descending, comm may contain spaces, dups appear per pid.
const psOut = ` 42.3  4.5 WindowServer
 12.0  8.1 Google Chrome Helper (Renderer)
 12.0  0.4 kernel_task
  3.2  2.0 Google Chrome Helper (Renderer)
  1.0  0.5 mDNSResponder
  0.0  0.1 zsh
`

func TestName(t *testing.T) {
	if got := New().Name(); got != "procs" {
		t.Errorf("Name() = %q, want %q", got, "procs")
	}
}

// topOut mirrors /usr/bin/top -l 2 -o cpu -stats pid,command,cpu,mem: two
// samples, each a header block then a process table. The first sample's
// %CPU is since-boot and must be ignored; commands may contain spaces and
// truncate at the column width; MEM is absolute with a +/- delta marker.
const topOut = `Processes: 641 total, 13 running, 628 sleeping, 13671 threads
2026/07/06 23:26:06
Load Avg: 314.75, 297.12, 178.55
CPU usage: 16.22% user, 83.77% sys, 0.0% idle

PID    COMMAND          %CPU  MEM
99992  can              999.0 13G+
0      kernel_task      99.0  42M+

Processes: 641 total, 13 running, 628 sleeping, 13671 threads
2026/07/06 23:26:07
Load Avg: 314.75, 297.12, 178.55
CPU usage: 43.14% user, 48.0% sys, 8.85% idle

PID    COMMAND          %CPU  MEM
99992  can              289.7 13G+
0      kernel_task      28.5  42M+
653    WindowServer     15.6  1781M+
58034  Google Chrome He 6.5   281M-
99999  zombie           0.0   0B
777    can              50.0  1G
`

const totalMem = 36 * float64(1<<30)

func TestParseTopSecondSample(t *testing.T) {
	rows := parseTop(topOut, 10, totalMem)
	want := []struct{ key, value string }{
		{"can", "289.7%c 36.1%m"},
		{"kernel_task", "28.5%c 0.1%m"},
		{"windowserver", "15.6%c 4.8%m"},
		{"google chrome he", "6.5%c 0.8%m"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d (second sample, dup and 0.0/0.0 dropped): %+v",
			len(rows), len(want), rows)
	}
	for i, w := range want {
		r := rows[i]
		if r.Kind != module.RowKV || r.Key != w.key || r.Value != w.value {
			t.Errorf("rows[%d] = {%s %q %q}, want {kv %q %q}", i, r.Kind, r.Key, r.Value, w.key, w.value)
		}
	}
}

func TestParseTopLimitAndGarbage(t *testing.T) {
	if got := len(parseTop(topOut, 2, totalMem)); got != 2 {
		t.Errorf("parseTop(top=2) = %d rows, want 2", got)
	}
	if got := parseTop("no table here\n", 10, totalMem); got != nil {
		t.Errorf("parseTop(no header) = %+v, want nil (ps fallback)", got)
	}
	// zero total memory degrades mem to 0.0, never NaN; cpu still shows
	rows := parseTop(topOut, 10, 0)
	if len(rows) == 0 || rows[0].Value != "289.7%c 0.0%m" {
		t.Errorf("parseTop(total=0) rows[0] = %+v, want 289.7%%c 0.0%%m", rows)
	}
}

func TestMemBytes(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"13G+", 13 * float64(1<<30), true},
		{"281M-", 281 * float64(1<<20), true},
		{"8640K", 8640 * float64(1<<10), true},
		{"0B", 0, true},
		{"512", 512, true},
		{"", 0, false},
		{"junk", 0, false},
	}
	for _, c := range cases {
		got, ok := memBytes(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("memBytes(%q) = %v/%v, want %v/%v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestParseTopDedupOrder(t *testing.T) {
	rows := parse(psOut, 10)
	want := []struct{ key, value string }{
		{"windowserver", "42.3%c 4.5%m"},
		{"google chrome helper (renderer)", "12.0%c 8.1%m"},
		{"kernel_task", "12.0%c 0.4%m"},
		{"mdnsresponder", "1.0%c 0.5%m"},
		{"zsh", "0.0%c 0.1%m"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d (dup dropped): %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		r := rows[i]
		if r.Kind != module.RowKV || r.Key != w.key || r.Value != w.value {
			t.Errorf("rows[%d] = {%s %q %q}, want {kv %q %q}", i, r.Kind, r.Key, r.Value, w.key, w.value)
		}
	}
}

func TestParseTopLimit(t *testing.T) {
	if got := len(parse(psOut, 2)); got != 2 {
		t.Errorf("parse(top=2) = %d rows, want 2", got)
	}
	if got := len(parse(psOut, 0)); got != 0 {
		t.Errorf("parse(top=0) = %d rows, want 0", got)
	}
}

func TestParseTolerance(t *testing.T) {
	// entitlement-blocked ps drops the pmem column; comm-with-path takes
	// the basename; garbage lines are skipped
	out := `  5.0 WindowServer
  1.5 Google Chrome Helper (Renderer)
  2.0  1.0 /usr/sbin/mDNSResponder
USER PID COMMAND
   1.0
`
	rows := parse(out, 10)
	want := []struct{ key, value string }{
		{"windowserver", "5.0%c 0.0%m"},
		{"google chrome helper (renderer)", "1.5%c 0.0%m"},
		{"mdnsresponder", "2.0%c 1.0%m"},
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(rows), len(want), rows)
	}
	for i, w := range want {
		if rows[i].Key != w.key || rows[i].Value != w.value {
			t.Errorf("rows[%d] = {%q %q}, want {%q %q}", i, rows[i].Key, rows[i].Value, w.key, w.value)
		}
	}
}

// TestPSDataBlockedIsLoud pins the entitlement-blocked degrade: when every
// ps row filters out as 0.0/0.0 the fallback emits one dim text row, never
// a junk table.
func TestPSDataBlockedIsLoud(t *testing.T) {
	blocked := `  0.0 <defunct>
  0.0 ps
  0.0 zsh
`
	d := psData(blocked, 10)
	if len(d.Rows) != 1 {
		t.Fatalf("got %d rows, want 1 dim row: %+v", len(d.Rows), d.Rows)
	}
	r := d.Rows[0]
	if r.Kind != module.RowText || r.Style != module.StyleDim || r.Text != "process stats unavailable" {
		t.Errorf("row = %+v, want dim text %q", r, "process stats unavailable")
	}
	// rows with real percentages still form the table
	if d := psData(psOut, 10); len(d.Rows) != 5 || d.Rows[0].Kind != module.RowKV {
		t.Errorf("psData(psOut) = %+v, want the 5 kv rows", d.Rows)
	}
}

// TestParseUtilSecondSample pins the utilization source: the LAST "CPU
// usage" header wins (the first sample's accounting is since-boot), and
// garbage never yields a measurement.
func TestParseUtilSecondSample(t *testing.T) {
	util, ok := parseUtil(topOut)
	if !ok {
		t.Fatal("parseUtil found no CPU usage line")
	}
	idle := 8.85
	if want := (100 - idle) / 100; util != want {
		t.Errorf("util = %v, want %v (second sample, idle 8.85)", util, want)
	}
	if _, ok := parseUtil("no header\n"); ok {
		t.Error("parseUtil invented a measurement")
	}
	if _, ok := parseUtil("CPU usage: 1.0% user, 1.0% sys, 200.0% idle\n"); ok {
		t.Error("parseUtil accepted out-of-range idle")
	}
}

func TestUtilizationAccessor(t *testing.T) {
	m := New()
	if _, ok := m.Utilization(); ok {
		t.Error("fresh Mod reports a utilization")
	}
	m.setUtil(0.42, true)
	if util, ok := m.Utilization(); !ok || util != 0.42 {
		t.Errorf("Utilization() = %v/%v, want 0.42/true", util, ok)
	}
	m.setUtil(0, false)
	if _, ok := m.Utilization(); ok {
		t.Error("fallback poll did not clear the utilization")
	}
}

// TestPollLive proves the top exec against the running host: rows must
// carry real (non-zero) values, not the entitlement-blocked ps zeros, and
// the header sample must yield a whole-host utilization.
func TestPollLive(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only sampler")
	}
	if _, err := exec.LookPath("/usr/bin/top"); err != nil {
		t.Skipf("/usr/bin/top: %v", err)
	}
	// Poll shells out to bare `ps` too; in the nix checkPhase /usr/bin/top
	// exists (absolute path) but PATH carries no ps, so gate both.
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skipf("ps: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := New()
	data, err := m.Poll(ctx, nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if util, ok := m.Utilization(); !ok || util <= 0 || util > 1 {
		t.Errorf("Utilization() = %v/%v, want measured 0 < u <= 1", util, ok)
	} else {
		t.Logf("live utilization: %.1f%%", util*100)
	}
	if len(data.Rows) == 0 || len(data.Rows) > 10 {
		t.Fatalf("got %d rows, want 1..10", len(data.Rows))
	}
	for i, r := range data.Rows {
		if r.Kind != module.RowKV || r.Key == "" {
			t.Errorf("rows[%d] = %+v, want non-empty kv", i, r)
		}
		if strings.HasPrefix(r.Value, "0.0%c 0.0%m") {
			t.Errorf("rows[%d] = %q %q, want non-zero values", i, r.Key, r.Value)
		}
		t.Logf("live row %d: %-24s %s", i, r.Key, r.Value)
	}
}
