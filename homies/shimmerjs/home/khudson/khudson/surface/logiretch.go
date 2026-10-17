// Logiretch chrome renderer: the on-glass MX Master 4 behind the
// logiretch layout and the home-logi side column. The module publishes
// the mouse art, the mapping readbacks, and a link/battery footer as
// keyed rows (internal/module/logiretch view conventions); this renderer
// owns geometry -- the art keeps its own column with the mapping rows
// beside it when the region is wide enough, stacked under it when it is
// not, and the footer pins to the bottom edge. Read-only surface: the
// region consumes every tap.
package surface

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	logiretch "github.com/shimmerjs/khudson/khudson/internal/module/logiretch"
	"github.com/shimmerjs/khudson/khudson/provider"
)

const (
	// logiArtGutter separates the art column from the mapping rows.
	logiArtGutter = 3
	// logiRowsMinW is the narrowest mapping column worth splitting for:
	// "smartShift on (dev on)" and friends want this much before the
	// side-by-side layout beats stacking.
	logiRowsMinW = 28
)

func (m *model) renderLogiretch(w config.Widget, rr rect) string {
	m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
	if e, bad := m.widgetErr[w.ID]; bad {
		return renderTitledBox(w.Title,
			[]string{chromeWarn.Render(" " + w.Render.Module + ": " + e)}, rr.w, rr.h)
	}
	d, ok := m.widgetData[w.ID]
	if !ok {
		return renderTitledBox(w.Title, []string{chromeDim.Render(" ...")}, rr.w, rr.h)
	}
	ss := m.rowStyles()
	art, body, status := splitLogiRows(d)

	title := d.Title
	if title == "" {
		title = w.Title
	}
	if m.widgetStale[w.ID] {
		title += chromeDim.Faint(true).Render(" stale")
	}
	lines := []string{fitCellPad(chromeAccent.Bold(true).Render(" "+title), rr.w)}

	// cols 0 leaves the art uncropped: its width sets the column, and the
	// stacked fallback below is what handles a region too narrow for it
	artLines, _, _ := renderRows(module.Data{Rows: art}, 0, ss)
	artW := 0
	for _, l := range artLines {
		artW = max(artW, lipgloss.Width(l))
	}

	body = padLogiKeys(body)
	var bodyLines []string
	if colW := artW + logiArtGutter; artW > 0 && rr.w-colW >= logiRowsMinW {
		mapLines, _, _ := renderRows(module.Data{Rows: body}, rr.w-colW, ss)
		mapW := 0
		for _, l := range mapLines {
			mapW = max(mapW, lipgloss.Width(l))
		}
		// centre the art+readings cluster: on a full-panel region the pair
		// is far narrower than the glass, and pinning it left reads as a
		// widget that failed to fill rather than one composed to sit there
		lead := strings.Repeat(" ", max((rr.w-colW-mapW)/2, 0))
		for i := range max(len(artLines), len(mapLines)) {
			l, r := "", ""
			if i < len(artLines) {
				l = artLines[i]
			}
			if i < len(mapLines) {
				r = mapLines[i]
			}
			bodyLines = append(bodyLines, lead+fitCellPad(l, colW)+r)
		}
	} else {
		mapLines, _, _ := renderRows(module.Data{Rows: body}, rr.w, ss)
		bodyLines = append(append(bodyLines, artLines...), mapLines...)
	}
	// the footer owns the last row; the body takes what is left
	if bodyH := max(rr.h-2, 0); len(bodyLines) > bodyH {
		bodyLines = bodyLines[:bodyH]
	}
	lines = append(lines, bodyLines...)
	if status != nil {
		for len(lines) < rr.h-1 {
			lines = append(lines, "")
		}
		foot, _, _ := renderRows(module.Data{Rows: []module.Row{*status}}, rr.w, ss)
		lines = append(lines, foot...)
	}
	return fixedBlock(lines, rr.w, rr.h)
}

// padLogiKeys right-pads every reading's key to a common width so the
// values land in one column. The shared KV renderer sets no key column --
// each row is as wide as its own label -- which reads as ragged noise in
// a list of settings that all want comparing at a glance.
func padLogiKeys(rows []module.Row) []module.Row {
	keyW := 0
	for _, r := range rows {
		if r.Kind == module.RowKV {
			keyW = max(keyW, lipgloss.Width(r.Key))
		}
	}
	if keyW == 0 {
		return rows
	}
	out := make([]module.Row, len(rows))
	for i, r := range rows {
		if r.Kind == module.RowKV {
			r.Key += strings.Repeat(" ", keyW-lipgloss.Width(r.Key))
		}
		out[i] = r
	}
	return out
}

// splitLogiRows pulls the layout's three parts off the wire. The module's
// dividers only separate them for the generic titled-box path; here the
// geometry does that, so they drop.
func splitLogiRows(d module.Data) (art, body []module.Row, status *module.Row) {
	for i := range d.Rows {
		r := &d.Rows[i]
		switch {
		case r.Key == logiretch.KeyArt:
			art = append(art, *r)
		case r.Key == logiretch.KeyStatus:
			status = r
		case r.Kind == module.RowDivider:
		default:
			body = append(body, *r)
		}
	}
	return art, body, status
}
