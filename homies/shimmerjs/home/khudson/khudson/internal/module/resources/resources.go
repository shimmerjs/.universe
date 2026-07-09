// Package resources composes the cpumem, disk, and procs modules into one
// home-screen cluster. Row order is load-bearing for the dock's resources
// renderer: cpu and mem resource rows, the disk volume rows in config order,
// one divider, then the top-process kv rows. The parts stay registered
// standalone where schema'd.
package resources

import (
	"context"
	"fmt"
	"maps"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// Mod implements module.Module. The parts keep history rings across Poll
// calls.
type Mod struct {
	cpumem module.Module
	disk   module.Module
	procs  module.Module
}

// New returns the module singleton for the registry. Pass the same part
// instances the registry holds, so history rings and the per-poll execs
// are shared rather than duplicated.
func New(cpumem, disk, procs module.Module) *Mod {
	return &Mod{cpumem: cpumem, disk: disk, procs: procs}
}

func (*Mod) Name() string { return "resources" }

// Poll polls every part with the same params; each part ignores the keys
// it doesn't read (window, volumes, top). Procs polls first: its top exec
// measures whole-host cpu utilization, which feeds the cpumem part's cpu
// row (loadavg is not a utilization percent). A failed part degrades to a
// dim text row, matching disk's not-mounted rows; the whole poll errors
// only when all parts fail.
func (m *Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	pr, prErr := m.procs.Poll(ctx, params)
	cm, cmErr := m.cpumem.Poll(ctx, cpuParams(params, m.procs))
	dk, dkErr := m.disk.Poll(ctx, params)
	if cmErr != nil && dkErr != nil && prErr != nil {
		return module.Data{}, fmt.Errorf("cpumem: %w; disk: %w; procs: %w", cmErr, dkErr, prErr)
	}
	var rows []module.Row
	rows = append(rows, partRows(cm, cmErr, "cpu/mem")...)
	rows = append(rows, partRows(dk, dkErr, "disk")...)
	rows = append(rows, module.Row{Kind: module.RowDivider})
	rows = append(rows, partRows(pr, prErr, "procs")...)
	return module.Data{Title: "resources", Rows: rows}, nil
}

// utilSource is implemented by the procs part when its sampler measures
// whole-host cpu utilization alongside the process rows.
type utilSource interface{ Utilization() (float64, bool) }

// cpuParams augments params with the "cpu-util" fraction when the procs
// part measured one this poll; the caller's map stays untouched. Without a
// measurement cpumem keeps its loadavg fallback.
func cpuParams(params map[string]any, procs module.Module) map[string]any {
	src, ok := procs.(utilSource)
	if !ok {
		return params
	}
	util, ok := src.Utilization()
	if !ok {
		return params
	}
	out := make(map[string]any, len(params)+1)
	maps.Copy(out, params)
	out["cpu-util"] = util
	return out
}

// partRows is the part's rows, or one dim text row when its poll failed.
func partRows(d module.Data, err error, label string) []module.Row {
	if err != nil {
		return []module.Row{{Kind: module.RowText, Text: label + ": " + err.Error(), Style: module.StyleDim}}
	}
	return d.Rows
}
