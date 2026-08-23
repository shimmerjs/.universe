// Reno chrome renderer: the presenter and recorder views behind the reno
// strip tab. The module publishes text and structure (keyed rows,
// internal/module/reno view conventions); this renderer owns geometry --
// notes wrap to the region rect, timer chips ride the heat ramp, and the
// live view grows big prev/next tap bands (tap-only-safe: no holds, no
// chords). Long-press anywhere opens the goto menu the module windows
// around the cursor.
package dock

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	reno "github.com/shimmerjs/khudson/khudson/internal/module/reno"
)

// Band widths for the live presenter's tap zones. Next is the show's one
// load-bearing control, so it gets the fatter band.
const (
	renoPrevBandW = 8
	renoNextBandW = 14
)

func (m *model) renderReno(w config.Widget, rr rect) string {
	if e, bad := m.widgetErr[w.ID]; bad {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return renderTitledBox(w.Title,
			[]string{chromeWarn.Render(" " + w.Render.Module + ": " + e)}, rr.w, rr.h)
	}
	d, ok := m.widgetData[w.ID]
	if !ok {
		m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
		return renderTitledBox(w.Title, []string{chromeDim.Render(" ...")}, rr.w, rr.h)
	}
	if view, _ := w.Render.Params["view"].(string); view == "recorder" {
		return m.renderRenoRecorder(w.ID, d, rr)
	}
	return m.renderRenoPresenter(w.ID, d, rr)
}

// renderRenoRecorder is the recorder control pad: the module's readout
// rows become the header, its pad rows the button grid. The split is the
// contract -- anything you can press is a tile, anything you can only
// read is a header line.
func (m *model) renderRenoRecorder(id string, d module.Data, rr rect) string {
	var head module.Data
	var tiles []module.Row
	for _, r := range d.Rows {
		if r.Kind == module.RowPad || r.Kind == module.RowStepper {
			tiles = append(tiles, r)
			continue
		}
		head.Rows = append(head.Rows, r)
	}
	lines := []string{fitCellPad(chromeAccent.Bold(true).Render(" "+d.Title), rr.w)}
	// the header never eats the grid: readouts cap at a third of the glass
	body, _, _ := renderChromeRows(head, rr.w, max(rr.h/3, 1), m.rowStyles())
	lines = append(lines, body...)
	return m.renderPad(id, lines, tiles, rr)
}

// renderRenoFlat is the generic keyed-row view: the recorder transport
// and the presenter's off-show states (empty/loaded/stopped) render
// their rows through the shared row renderer under a title line. Row
// acts stay live -- the recorder's one-tap record control resolves by
// row geometry exactly like a home widget (the review caught the first
// cut painting the control but consuming every tap).
func (m *model) renderRenoFlat(id string, d module.Data, rr rect) string {
	ss := m.rowStyles()
	lines := []string{fitCellPad(chromeAccent.Bold(true).Render(" "+d.Title), rr.w)}
	body, acts, menus := renderChromeRows(d, rr.w, rr.h-1, ss)
	lines = append(lines, body...)
	m.hits = append(m.hits, hitRegion{
		area: rr,
		do: func(_, y int) {
			if i := y - rr.y - 1; i >= 0 && i < len(acts) && len(acts[i]) > 0 {
				m.sendRowAct(id, acts[i])
			}
		},
		longPress: func(x, y int) {
			if i := y - rr.y - 1; i >= 0 && i < len(menus) && len(menus[i]) > 0 {
				m.menuOpener(id, menus[i])(x, y)
			}
		},
	})
	return fixedBlock(lines, rr.w, rr.h)
}

// renoPieces is the live view's structure pulled off the wire rows.
type renoPieces struct {
	step, elapsed, inStep *module.Row
	next                  string
	note                  []string
	warn                  []string
	actNext, actPrev      []string
	menu                  []module.Act
}

func splitRenoRows(d module.Data) renoPieces {
	var p renoPieces
	for i := range d.Rows {
		r := &d.Rows[i]
		switch r.Key {
		case reno.KeyChipStep:
			p.step = r
			p.menu = r.Menu
		case reno.KeyChipElapsed:
			p.elapsed = r
		case reno.KeyChipInStep:
			p.inStep = r
		case reno.KeyNext:
			p.next = r.Value
		case reno.KeyNote:
			p.note = append(p.note, r.Text)
		case reno.KeyActNext:
			p.actNext = r.Act
		case reno.KeyActPrev:
			p.actPrev = r.Act
		case "":
			if r.Style == module.StyleWarn && r.Text != "" {
				// keyless module alarms (stale recovery, journal failure)
				// must not vanish behind the live layout
				p.warn = append(p.warn, r.Text)
			}
		}
	}
	return p
}

// renderRenoPresenter draws the live show view; without published
// next/prev acts (no live run) it falls back to the notes view when
// note rows ride the wire (ad hoc notes, no script -- prose still wants
// wrapping), else the flat view.
func (m *model) renderRenoPresenter(id string, d module.Data, rr rect) string {
	p := splitRenoRows(d)
	if p.actNext == nil || p.step == nil {
		if len(p.note) > 0 {
			return m.renderRenoNotes(d, rr)
		}
		return m.renderRenoFlat(id, d, rr)
	}
	ss := m.rowStyles()

	// header: step chip + timer chips left, next-step preview right
	left := chromeAccent.Bold(true).Render(" "+p.step.Value+" ") +
		renoChip(p.elapsed, ss) + renoChip(p.inStep, ss)
	right := ""
	if p.next != "" {
		right = chromeDim.Render("next: " + p.next + " ")
	}
	gap := max(rr.w-lipgloss.Width(left)-lipgloss.Width(right), 0)
	header := fitCellPad(left+strings.Repeat(" ", gap)+right, rr.w)

	// body: prev band | note wrapped to the middle | next band
	bodyH := rr.h - 1
	noteW := max(rr.w-renoPrevBandW-renoNextBandW-2, 1)
	var wrapped []string
	for _, wline := range renoWrap(p.warn, noteW) {
		wrapped = append(wrapped, chromeWarn.Render(wline))
	}
	wrapped = append(wrapped, renoWrap(p.note, noteW)...)
	prev := renoBand("<", renoPrevBandW, bodyH, chromeDim)
	next := renoBand(">", renoNextBandW, bodyH, chromeHighlight.Bold(true))
	lines := make([]string, 0, bodyH)
	for y := range bodyH {
		note := ""
		if y < len(wrapped) {
			note = wrapped[y]
		}
		lines = append(lines, prev[y]+" "+fitCellPad(note, noteW)+" "+next[y])
	}

	openMenu := m.menuOpener(id, p.menu)
	m.hits = append(m.hits,
		hitRegion{area: rect{rr.x, rr.y + 1, renoPrevBandW, bodyH},
			do:       func(int, int) { m.sendRowAct(id, p.actPrev) },
			pressKey: "reno:prev", longPress: openMenu},
		hitRegion{area: rect{rr.x + rr.w - renoNextBandW, rr.y + 1, renoNextBandW, bodyH},
			do:       func(int, int) { m.sendRowAct(id, p.actNext) },
			pressKey: "reno:next", longPress: openMenu},
		hitRegion{area: rr, do: consumeTap, longPress: openMenu},
	)
	return fixedBlock(append([]string{header}, lines...), rr.w, rr.h)
}

// renderRenoNotes is the scriptless-notes body: title, the non-note
// rows (run chip, notes-file chip, warnings), then the note text
// wrapped full-width. No tap bands -- there is no cursor to move.
func (m *model) renderRenoNotes(d module.Data, rr rect) string {
	ss := m.rowStyles()
	lines := []string{fitCellPad(chromeAccent.Bold(true).Render(" "+d.Title), rr.w)}
	var head module.Data
	for _, r := range d.Rows {
		if r.Key != reno.KeyNote {
			head.Rows = append(head.Rows, r)
		}
	}
	body, _, _ := renderChromeRows(head, rr.w, len(head.Rows), ss)
	lines = append(lines, body...)
	p := splitRenoRows(d)
	noteW := max(rr.w-2, 1)
	for _, wline := range renoWrap(p.note, noteW) {
		if len(lines) >= rr.h {
			break
		}
		lines = append(lines, fitCellPad(" "+wline, rr.w))
	}
	m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
	return fixedBlock(lines, rr.w, rr.h)
}

// renoChip renders one timer chip through the heat ramp: warn style wins
// (overtime), else PctHeat picks the ramp bucket, else plain.
func renoChip(r *module.Row, ss rowStyles) string {
	if r == nil {
		return ""
	}
	st := textHeat(r.PctHeat, ss)
	if r.Style == module.StyleWarn {
		st = ss.warn
	}
	return " " + st.Render(r.Key[len("chip:"):]+" "+r.Value)
}

// renoWrap wordwraps the note body (module rows carry unwrapped lines;
// the dock owns geometry). Paragraph structure survives: each source
// line wraps independently.
func renoWrap(noteLines []string, w int) []string {
	var out []string
	for _, line := range noteLines {
		if line == "" {
			out = append(out, "")
			continue
		}
		for wrapped := range strings.SplitSeq(ansi.Wordwrap(line, w, ""), "\n") {
			out = append(out, wrapped)
		}
	}
	return out
}

// renoBand is one vertical tap band: the glyph centered at mid-height,
// bare elsewhere -- a fat target that reads as a control without
// shouting over the notes.
func renoBand(glyph string, w, h int, style lipgloss.Style) []string {
	lines := make([]string, h)
	for y := range lines {
		s := ""
		if y == h/2 {
			pad := (w - lipgloss.Width(glyph)) / 2
			s = strings.Repeat(" ", max(pad, 0)) + style.Render(glyph)
		}
		lines[y] = fitCellPad(s, w)
	}
	return lines
}
