package sysmon

import (
	"context"
	"fmt"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

const (
	loadOut    = "{ 1.23 4.56 7.89 }\n" // sysctl -n vm.loadavg
	ncpuOut    = "8\n"                  // sysctl -n hw.ncpu
	memsizeOut = "34359738368\n"        // sysctl -n hw.memsize, 32 GiB
	uptimeOut  = "23:26  up 12 days,  4:33, 3 users, load averages: 1.23 4.56 7.89\n"
)

// vmStatOut mirrors vm_stat: 16 KiB pages; active + wired + compressor =
// 819200 pages = 12.5 GiB used.
const vmStatOut = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                              218933.
Pages active:                            409600.
Pages inactive:                          401856.
Pages speculative:                         5265.
Pages throttled:                              0.
Pages wired down:                        245760.
Pages purgeable:                          12345.
"Translation faults":                 123456789.
Pages occupied by compressor:            163840.
Pages found in compressor:            987654321.
`

// dfOut mirrors df -h /: header then the root line; fields[3] is Avail,
// fields[4] Capacity.
const dfOut = `Filesystem      Size   Used  Avail Capacity iused      ifree %iused  Mounted on
/dev/disk3s1s1 926Gi  600Gi  278Gi    67% 1055222 2914396040    0%   /
`

// fakeRun serves canned stdout keyed by the joined argv; unlisted commands
// fail like a broken tool.
func fakeRun(outs map[string]string) runFunc {
	return func(_ context.Context, name string, args ...string) (string, error) {
		key := strings.Join(append([]string{name}, args...), " ")
		out, ok := outs[key]
		if !ok {
			return "", fmt.Errorf("%s: not wired", name)
		}
		return out, nil
	}
}

func allOuts() map[string]string {
	return map[string]string{
		"sysctl -n vm.loadavg": loadOut,
		"sysctl -n hw.ncpu":    ncpuOut,
		"sysctl -n hw.memsize": memsizeOut,
		"vm_stat":              vmStatOut,
		"df -h /":              dfOut,
		"uptime":               uptimeOut,
	}
}

func TestName(t *testing.T) {
	if got := New().Name(); got != "sysmon" {
		t.Errorf("Name() = %q, want %q", got, "sysmon")
	}
}

// TestPollRender pins the happy-path view model: three gauges with the exact
// label/caption formats, the divider, the dim uptime row.
func TestPollRender(t *testing.T) {
	m := New()
	m.run = fakeRun(allOuts())
	data, err := m.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "system" {
		t.Errorf("Title = %q, want %q", data.Title, "system")
	}
	want := []module.Row{
		{Kind: module.RowGauge, Key: "load", Frac: 1.23 / 8, Value: "1.23 / 8 cores"},
		{Kind: module.RowGauge, Key: "mem", Frac: 12.5 / 32, Value: "12.5 / 32 GiB"},
		{Kind: module.RowGauge, Key: "disk", Frac: 0.67, Value: "67% used, 278Gi free"},
		{Kind: module.RowDivider},
		{Kind: module.RowText, Text: strings.TrimSpace(uptimeOut), Style: module.StyleDim},
	}
	if len(data.Rows) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(data.Rows), len(want), data.Rows)
	}
	for i, w := range want {
		if !reflect.DeepEqual(data.Rows[i], w) {
			t.Errorf("rows[%d] = %+v, want %+v", i, data.Rows[i], w)
		}
	}
}

// TestPollDegrade pins the degrade contract: one failing probe yields one
// warn text row, the neighbor gauges still render, and the poll succeeds.
func TestPollDegrade(t *testing.T) {
	outs := allOuts()
	delete(outs, "vm_stat")
	m := New()
	m.run = fakeRun(outs)
	data, err := m.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v (a failed probe must not fail the poll)", err)
	}
	if len(data.Rows) != 5 {
		t.Fatalf("got %d rows, want 5: %+v", len(data.Rows), data.Rows)
	}
	r := data.Rows[1]
	if r.Kind != module.RowText || r.Style != module.StyleWarn || !strings.HasPrefix(r.Text, "mem: ") {
		t.Errorf("rows[1] = %+v, want warn text row with mem: prefix", r)
	}
	if data.Rows[0].Kind != module.RowGauge || data.Rows[2].Kind != module.RowGauge {
		t.Errorf("neighbor gauges lost: %+v", data.Rows)
	}
}

// TestPollAllProbesFail pins the floor: every probe failing still returns a
// view (three warn rows and the divider; the uptime row is skipped), never
// an error.
func TestPollAllProbesFail(t *testing.T) {
	m := New()
	m.run = fakeRun(nil)
	data, err := m.Poll(context.Background(), nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 4 {
		t.Fatalf("got %d rows, want 4: %+v", len(data.Rows), data.Rows)
	}
	for i, prefix := range []string{"load: ", "mem: ", "disk: "} {
		r := data.Rows[i]
		if r.Kind != module.RowText || r.Style != module.StyleWarn || !strings.HasPrefix(r.Text, prefix) {
			t.Errorf("rows[%d] = %+v, want warn %q row", i, r, prefix)
		}
	}
	if data.Rows[3].Kind != module.RowDivider {
		t.Errorf("rows[3] = %+v, want divider", data.Rows[3])
	}
}

func TestLoadAvg(t *testing.T) {
	ctx := context.Background()
	load, ncpu, err := loadAvg(ctx, fakeRun(allOuts()))
	if err != nil || load != 1.23 || ncpu != 8 {
		t.Errorf("loadAvg = %v/%v/%v, want 1.23/8/nil", load, ncpu, err)
	}
	// a failed core count keeps the load and reads 0 cores, not an error
	outs := allOuts()
	delete(outs, "sysctl -n hw.ncpu")
	load, ncpu, err = loadAvg(ctx, fakeRun(outs))
	if err != nil || load != 1.23 || ncpu != 0 {
		t.Errorf("loadAvg(no ncpu) = %v/%v/%v, want 1.23/0/nil", load, ncpu, err)
	}
	for name, out := range map[string]string{
		"empty braces": "{ }\n",
		"non-numeric":  "{ x y z }\n",
	} {
		if _, _, err := loadAvg(ctx, fakeRun(map[string]string{"sysctl -n vm.loadavg": out})); err == nil {
			t.Errorf("loadAvg accepted %s", name)
		}
	}
	if _, _, err := loadAvg(ctx, fakeRun(nil)); err == nil {
		t.Error("loadAvg swallowed the exec error")
	}
}

func TestMemory(t *testing.T) {
	ctx := context.Background()
	used, total, err := memory(ctx, fakeRun(allOuts()))
	if err != nil || used != 12.5 || total != 32 {
		t.Errorf("memory = %v/%v/%v, want 12.5/32/nil", used, total, err)
	}
	outs := allOuts()
	outs["vm_stat"] = "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 100.\n"
	if _, _, err := memory(ctx, fakeRun(outs)); err == nil {
		t.Error("memory accepted vm_stat without used-page counts")
	}
	outs = allOuts()
	outs["sysctl -n hw.memsize"] = "junk\n"
	if _, _, err := memory(ctx, fakeRun(outs)); err == nil {
		t.Error("memory accepted a non-numeric hw.memsize")
	}
}

func TestDisk(t *testing.T) {
	ctx := context.Background()
	pct, avail, err := disk(ctx, fakeRun(allOuts()))
	if err != nil || pct != 67 || avail != "278Gi" {
		t.Errorf("disk = %v/%q/%v, want 67/278Gi/nil", pct, avail, err)
	}
	for name, out := range map[string]string{
		"header only":  "Filesystem Size Used Avail Capacity Mounted on\n",
		"short fields": "Filesystem Size Used Avail Capacity Mounted on\n/dev/disk3 926Gi\n",
		"bad percent":  "Filesystem Size Used Avail Capacity Mounted on\n/dev/disk3 926Gi 600Gi 278Gi x% /\n",
	} {
		if _, _, err := disk(ctx, fakeRun(map[string]string{"df -h /": out})); err == nil {
			t.Errorf("disk accepted %s", name)
		}
	}
}

// TestPollLive proves the probes against the running host: all four rows
// plus the uptime line, gauges carrying real captions.
func TestPollLive(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin-only probes")
	}
	for _, tool := range []string{"sysctl", "vm_stat", "df", "uptime"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s: %v", tool, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	data, err := New().Poll(ctx, nil)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "system" || len(data.Rows) < 4 {
		t.Fatalf("data = %+v, want system title and the full row set", data)
	}
	for i, key := range []string{"load", "mem", "disk"} {
		r := data.Rows[i]
		if r.Kind != module.RowGauge || r.Key != key || r.Value == "" {
			t.Errorf("rows[%d] = %+v, want a live %s gauge", i, r, key)
		}
		t.Logf("live %s: frac=%.2f %s", key, r.Frac, r.Value)
	}
	if data.Rows[3].Kind != module.RowDivider {
		t.Errorf("rows[3] = %+v, want divider", data.Rows[3])
	}
}
