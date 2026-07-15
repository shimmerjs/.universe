// Package sysmon is the local system monitor module: load, memory, disk,
// uptime from stock darwin tools. The reference implementation for the
// module contract -- pure data mapper, loud errors, ctx-bounded shell-outs.
package sysmon

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// Mod implements module.Module. run is the exec test seam; nil means the
// real subprocess.
type Mod struct {
	run runFunc
}

// New returns the module singleton for the registry.
func New() *Mod { return &Mod{} }

func (*Mod) Name() string { return "sysmon" }

func (m *Mod) Poll(ctx context.Context, _ map[string]any) (module.Data, error) {
	run := runCmd
	if m.run != nil {
		run = m.run
	}
	var rows []module.Row

	if load, ncpu, err := loadAvg(ctx, run); err == nil {
		frac := 0.0
		if ncpu > 0 {
			frac = load / float64(ncpu)
		}
		rows = append(rows, module.Gauge("load", frac, fmt.Sprintf("%.2f / %d cores", load, ncpu)))
	} else {
		rows = append(rows, module.Row{Kind: module.RowText, Text: "load: " + err.Error(), Style: module.StyleWarn})
	}

	if used, total, err := memory(ctx, run); err == nil {
		rows = append(rows, module.Gauge("mem", used/total,
			fmt.Sprintf("%.1f / %.0f GiB", used, total)))
	} else {
		rows = append(rows, module.Row{Kind: module.RowText, Text: "mem: " + err.Error(), Style: module.StyleWarn})
	}

	if usedPct, avail, err := disk(ctx, run); err == nil {
		rows = append(rows, module.Gauge("disk", usedPct/100,
			fmt.Sprintf("%.0f%% used, %s free", usedPct, avail)))
	} else {
		rows = append(rows, module.Row{Kind: module.RowText, Text: "disk: " + err.Error(), Style: module.StyleWarn})
	}

	rows = append(rows, module.Row{Kind: module.RowDivider})
	if up, err := run(ctx, "uptime"); err == nil {
		rows = append(rows, module.Row{Kind: module.RowText, Text: strings.TrimSpace(up), Style: module.StyleDim})
	}

	return module.Data{Title: "system", Rows: rows}, nil
}

// runFunc is the exec seam shape: run one command, return its stdout.
type runFunc func(ctx context.Context, name string, args ...string) (string, error)

func runCmd(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

// loadAvg parses `sysctl -n vm.loadavg` ("{ 1.23 4.56 7.89 }") and core
// count.
func loadAvg(ctx context.Context, run runFunc) (float64, int, error) {
	out, err := run(ctx, "sysctl", "-n", "vm.loadavg")
	if err != nil {
		return 0, 0, err
	}
	fields := strings.Fields(strings.Trim(strings.TrimSpace(out), "{}"))
	if len(fields) < 1 {
		return 0, 0, fmt.Errorf("vm.loadavg: unexpected %q", out)
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, err
	}
	ncpuOut, err := run(ctx, "sysctl", "-n", "hw.ncpu")
	if err != nil {
		return load, 0, nil
	}
	ncpu, _ := strconv.Atoi(strings.TrimSpace(ncpuOut))
	return load, ncpu, nil
}

// memory returns (used GiB, total GiB) from hw.memsize and vm_stat.
func memory(ctx context.Context, run runFunc) (float64, float64, error) {
	memOut, err := run(ctx, "sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, 0, err
	}
	totalBytes, err := strconv.ParseFloat(strings.TrimSpace(memOut), 64)
	if err != nil {
		return 0, 0, err
	}
	vmOut, err := run(ctx, "vm_stat")
	if err != nil {
		return 0, 0, err
	}
	used, err := module.VMStatUsedGiB(vmOut)
	if err != nil {
		return 0, 0, err
	}
	const gib = 1024 * 1024 * 1024
	return used, totalBytes / gib, nil
}

// disk returns (used percent, available human) for /.
func disk(ctx context.Context, run runFunc) (float64, string, error) {
	out, err := run(ctx, "df", "-h", "/")
	if err != nil {
		return 0, "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, "", fmt.Errorf("df: unexpected output")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 5 {
		return 0, "", fmt.Errorf("df: unexpected fields %q", lines[len(lines)-1])
	}
	pct, err := strconv.ParseFloat(strings.TrimSuffix(fields[4], "%"), 64)
	if err != nil {
		return 0, "", err
	}
	return pct, fields[3], nil
}
