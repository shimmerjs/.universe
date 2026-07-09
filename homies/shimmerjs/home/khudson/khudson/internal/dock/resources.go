// Resources chrome renderer: the composed cpu/mem/disk cluster. The module
// emits leading RowResource rows (live metrics), a divider, then RowKV
// process rows; the renderer turns the resources into side-by-side live
// cells plus one full-width history sparkline each, and the processes into
// an aligned table.
package dock

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
)

func (m *model) renderResources(w config.Widget, rr rect) string {
	m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
	if e, bad := m.widgetErr[w.ID]; bad {
		return renderTitledBox(w.Title,
			[]string{chromeWarn.Render(" " + w.Render.Module + ": " + e)}, rr.w, rr.h)
	}
	d, ok := m.widgetData[w.ID]
	if !ok {
		return renderTitledBox(w.Title, []string{chromeDim.Render(" ...")}, rr.w, rr.h)
	}
	return renderTitledBox(w.Title, resourceClusterLines(d, rr.w-2, m.rowStyles()), rr.w, rr.h)
}

// resourceClusterLines lays the cluster out at cols wide: live cells for the
// leading resource rows, a history row per resource, then the remaining rows
// (divider, process table).
func resourceClusterLines(d module.Data, cols int, ss rowStyles) []string {
	if cols < 1 {
		return nil
	}
	rest := d.Rows
	var res []module.Row
	for len(rest) > 0 && rest[0].Kind == module.RowResource {
		res = append(res, rest[0])
		rest = rest[1:]
	}
	lines := resourceCellLines(res, cols, ss)
	for _, r := range res {
		if len(r.Series) == 0 {
			// no emitted history (disk, by design): the live cell stands
			continue
		}
		lines = append(lines, historyLine(r, cols, ss))
	}
	keyW, valW := kvColumns(rest)
	for _, r := range rest {
		switch r.Kind {
		case module.RowDivider:
			lines = append(lines, dividerLine(cols, ss))
		case module.RowKV:
			key := fitCell(r.Key, kvNameCap)
			pad := strings.Repeat(" ", max(keyW-lipgloss.Width(key), 0))
			lines = append(lines, ss.dim.Render(" "+key)+pad+"  "+
				ss.styleFor(r.Style).Render(alignFields(r.Value, valW)))
		case module.RowResource:
			// contract skew (a resource after the divider): still a resource
			lines = append(lines, resourceLine(r, cols, ss))
		default:
			lines = append(lines, ss.styleFor(r.Style).Render(" "+r.Text))
		}
	}
	return lines
}

// kvNameCap bounds the KV table's name column.
const kvNameCap = 24

// kvColumns measures the KV table: the name column width (capped at
// kvNameCap), and per-column widths of the space-split value fields, so the
// table aligns in the renderer while modules stay layout-blind.
func kvColumns(rows []module.Row) (keyW int, valW []int) {
	for _, r := range rows {
		if r.Kind != module.RowKV {
			continue
		}
		keyW = max(keyW, lipgloss.Width(fitCell(r.Key, kvNameCap)))
		for i, f := range strings.Fields(r.Value) {
			if i == len(valW) {
				valW = append(valW, 0)
			}
			valW[i] = max(valW[i], lipgloss.Width(f))
		}
	}
	return keyW, valW
}

// alignFields right-aligns each space-split field of v in its column width.
func alignFields(v string, valW []int) string {
	fields := strings.Fields(v)
	for i, f := range fields {
		if i < len(valW) {
			if p := valW[i] - lipgloss.Width(f); p > 0 {
				fields[i] = strings.Repeat(" ", p) + f
			}
		}
	}
	return strings.Join(fields, " ")
}

// cellGaugeCap keeps live-cell gauges compact snapshots: the gauge is one
// sample of the history spark beneath it, so the spark carries the width
// emphasis.
const cellGaugeCap = 24

// resourceCellLines renders res as side-by-side 3-line live cells: dim
// metric name, current gauge (capped at cellGaugeCap), bold current value.
// Cell widths derive from cols alone so every metric shares the geometry.
func resourceCellLines(res []module.Row, cols int, ss rowStyles) []string {
	n := len(res)
	if n == 0 {
		return nil
	}
	cellW := (cols - (n - 1)) / n
	if cellW < 5 {
		// too narrow for cells: one classic resource line per metric
		lines := make([]string, 0, n)
		for _, r := range res {
			lines = append(lines, resourceLine(r, cols, ss))
		}
		return lines
	}
	pad := func(s string) string {
		s = fitCell(s, cellW)
		if p := cellW - lipgloss.Width(s); p > 0 {
			s += strings.Repeat(" ", p)
		}
		return s
	}
	names := make([]string, n)
	gauges := make([]string, n)
	values := make([]string, n)
	for i, r := range res {
		name, _ := splitKeyHint(r.Key)
		names[i] = pad(ss.dim.Render(" " + name))
		gauges[i] = pad(" " + gaugeBar(r.Frac, min(cellW-2, cellGaugeCap), ss))
		values[i] = pad(ss.styleFor(r.Style).Bold(true).Render(" " + r.Value))
	}
	return []string{
		strings.Join(names, " "),
		strings.Join(gauges, " "),
		strings.Join(values, " "),
	}
}

// historyLine is one full-width history row: dim fixed-width label with the
// window hint when the module provides one in the Key, sparkline filling the
// rest. The fixed label width keeps every history spark starting at the same
// column.
func historyLine(r module.Row, cols int, ss rowStyles) string {
	name, hint := splitKeyHint(r.Key)
	prefix := fmt.Sprintf(" %-6s ", ansi.Truncate(name, 6, ""))
	if hint != "" {
		prefix = fmt.Sprintf(" %-6s (%s) ", ansi.Truncate(name, 6, ""), hint)
	}
	sparkW := cols - lipgloss.Width(prefix)
	if sparkW < 1 {
		return ss.dim.Render(fitCell(prefix, cols))
	}
	return ss.dim.Render(prefix) + spark(r.Series, sparkW, ss.heat)
}

// splitKeyHint splits a resource Key into the metric name and the optional
// window hint the module appends ("cpu 6h" -> "cpu", "6h").
func splitKeyHint(key string) (name, hint string) {
	name, hint, _ = strings.Cut(key, " ")
	return name, hint
}
