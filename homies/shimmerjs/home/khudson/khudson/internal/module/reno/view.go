package reno

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// Row key conventions consumed by the dock's reno chrome renderer. The
// rows still render tolerably through the generic titled-box path (couch
// clients, tests), but geometry-aware treatment -- note wrapping, chip
// strips, big tap targets -- keys off these.
const (
	KeyChipStep    = "chip:step"
	KeyChipElapsed = "chip:elapsed"
	KeyChipInStep  = "chip:steptime"
	KeyNote        = "note"
	KeyNext        = "next"
	KeyActNext     = "act:next"
	KeyActPrev     = "act:prev"
)

// presenterData renders the presenter view. Caller holds mu.
func (m *Mod) presenterData() module.Data {
	d := module.Data{Title: "reno"}
	if m.stale != "" {
		d.Rows = append(d.Rows, module.Row{Kind: module.RowText, Text: m.stale, Style: module.StyleWarn})
	}
	if m.journalErr != "" {
		d.Rows = append(d.Rows, module.Row{Kind: module.RowText, Text: "journal: " + m.journalErr, Style: module.StyleWarn})
	}
	switch {
	case m.staged == nil && m.run == nil:
		d.Rows = append(d.Rows,
			module.Row{Kind: module.RowText, Text: "no script loaded", Style: module.StyleDim},
			module.Row{Kind: module.RowText, Text: "khudson reno load <script.txtar>", Style: module.StyleDim})
	case m.run == nil || !m.run.Live():
		if m.staged != nil {
			d.Title = "reno: " + m.staged.Name
			d.Rows = append(d.Rows, module.Row{Kind: module.RowKV, Key: "loaded",
				Value: fmt.Sprintf("%s (%d steps)", m.staged.Name, len(m.staged.Steps))})
		}
		if m.run != nil && !m.run.Live() {
			d.Rows = append(d.Rows, m.summaryRows()...)
		}
		if m.staged != nil {
			d.Rows = append(d.Rows, module.Row{Kind: module.RowText, Text: "khudson reno start to arm", Style: module.StyleDim})
		}
	case m.staged == nil || m.stale != "":
		// a live run with no usable script: a recovered ad hoc journal, or
		// a script that changed under the run. Every recovery branch must
		// render -- a panic here crash-loops the bus straight back into
		// the same journal.
		d.Title = "reno: " + m.run.Demo
		d.Rows = append(d.Rows,
			module.Row{Kind: module.RowKV, Key: "run", Value: m.run.ID, Style: module.StyleAccent},
			module.Row{Kind: module.RowText, Text: "live run without a usable script; khudson reno stop to close it", Style: module.StyleDim})
	default:
		d.Title = "reno: " + m.run.Demo
		d.Rows = append(d.Rows, m.liveRows()...)
	}
	return d
}

// liveRows is the mid-demo presenter body. Caller holds mu.
func (m *Mod) liveRows() []module.Row {
	now := m.now()
	var rows []module.Row
	cur := m.run.Cursor
	total := len(m.staged.Steps)

	stepLabel := "armed"
	if cur >= 0 && cur < total {
		stepLabel = fmt.Sprintf("%d/%d %s", cur+1, total, m.staged.Steps[cur].ID)
	}
	stepRow := module.Row{Kind: module.RowKV, Key: KeyChipStep, Value: stepLabel, Style: module.StyleAccent,
		Menu: m.gotoMenu()}
	rows = append(rows, stepRow)

	elapsed := module.Row{Kind: module.RowKV, Key: KeyChipElapsed, Value: "0:00"}
	if !m.run.ShowStartedAt.IsZero() {
		e := now.Sub(m.run.ShowStartedAt)
		elapsed.Value = fmtClock(e)
		if m.staged.Budget > 0 {
			elapsed.Value += "/" + fmtClock(m.staged.Budget)
			elapsed.PctHeat = heatFrac(float64(e) / float64(m.staged.Budget))
			if e > m.staged.Budget {
				elapsed.Value = fmtClock(e-m.staged.Budget) + " over"
				elapsed.Style = module.StyleWarn
			}
		}
	}
	rows = append(rows, elapsed)

	if cur >= 0 && cur < total {
		step := m.staged.Steps[cur]
		if cur < len(m.run.Steps) && !m.run.Steps[cur].EnteredAt.IsZero() {
			in := m.run.Steps[cur].Total + now.Sub(m.run.Steps[cur].EnteredAt)
			r := module.Row{Kind: module.RowKV, Key: KeyChipInStep, Value: fmtClock(in)}
			if step.Budget > 0 {
				r.Value = fmtClock(in) + "/" + fmtClock(step.Budget)
				r.PctHeat = heatFrac(float64(in) / float64(step.Budget))
				if in > step.Budget {
					r.Value = fmtClock(in-step.Budget) + " over"
					r.Style = module.StyleWarn
				}
			}
			rows = append(rows, r)
		}
		for _, line := range noteLines(step.Note) {
			rows = append(rows, module.Row{Kind: module.RowText, Key: KeyNote, Text: line})
		}
	} else {
		rows = append(rows, module.Row{Kind: module.RowText, Key: KeyNote,
			Text: "armed -- next enters step 1 and starts the clock", Style: module.StyleDim})
	}

	if next := cur + 1; next < total {
		rows = append(rows, module.Row{Kind: module.RowKV, Key: KeyNext, Value: m.staged.Steps[next].ID, Style: module.StyleDim})
	} else {
		rows = append(rows, module.Row{Kind: module.RowKV, Key: KeyNext, Value: "(end)", Style: module.StyleDim})
	}

	rows = append(rows,
		module.Row{Kind: module.RowText, Key: KeyActNext, Text: "next", Style: module.StyleHighlight, Act: []string{actNext}},
		module.Row{Kind: module.RowText, Key: KeyActPrev, Text: "prev", Style: module.StyleDim, Act: []string{actPrev}},
	)
	return rows
}

// gotoMenu windows the step list around the cursor + goto offset: the
// overlay keeps only rows that fit and discards the tail, so the menu
// never publishes more than a window plus pagers. Caller holds mu.
func (m *Mod) gotoMenu() []module.Act {
	if m.staged == nil {
		return nil
	}
	total := len(m.staged.Steps)
	lo := max(m.cursor()-gotoWindow/2, 0) + m.gotoOff
	lo = min(max(lo, 0), max(total-gotoWindow, 0))
	hi := min(lo+gotoWindow, total)

	var items []module.Act
	if lo > 0 {
		items = append(items, module.Act{Label: fmt.Sprintf("earlier (%d)", lo),
			Argv: []string{actGotoWindow, "-1"}, Section: "steps"})
	}
	for i := lo; i < hi; i++ {
		items = append(items, module.Act{
			Label:   fmt.Sprintf("%d %s", i+1, m.staged.Steps[i].ID),
			Argv:    []string{actGoto, m.staged.Steps[i].ID},
			Section: "steps",
		})
	}
	if hi < total {
		items = append(items, module.Act{Label: fmt.Sprintf("later (%d)", total-hi),
			Argv: []string{actGotoWindow, "+1"}, Section: "steps"})
	}
	items = append(items, module.Act{Label: "stop demo", Argv: []string{actStop}, Destructive: true, Section: "run"})
	return items
}

// summaryRows shows the stopped run's per-step actuals against budgets.
// Caller holds mu.
func (m *Mod) summaryRows() []module.Row {
	rows := []module.Row{{Kind: module.RowKV, Key: "last run",
		Value: fmt.Sprintf("%s (%s)", m.run.ID, fmtClock(m.run.StoppedAt.Sub(m.run.StartedAt)))}}
	if m.staged == nil || m.run.Demo != m.staged.Name {
		return rows
	}
	for i, sa := range m.run.Steps {
		if i >= len(m.staged.Steps) || sa.Visits == 0 {
			continue
		}
		step := m.staged.Steps[i]
		r := module.Row{Kind: module.RowKV, Key: step.ID, Value: fmtClock(sa.Total), Style: module.StyleDim}
		if step.Budget > 0 {
			r.Value += "/" + fmtClock(step.Budget)
			r.PctHeat = heatFrac(float64(sa.Total) / float64(step.Budget))
		}
		rows = append(rows, r)
	}
	return rows
}

// recorderData is the P0 recorder shell: run identity + the clip
// workspace the run keys, with the OBS transport landing in P1. It keeps
// the layout, tab-flip, and view plumbing complete now so P1 only fills
// the transport in.
func (m *Mod) recorderData() module.Data {
	d := module.Data{Title: "reno rec"}
	if m.journalErr != "" {
		d.Rows = append(d.Rows, module.Row{Kind: module.RowText, Text: "journal: " + m.journalErr, Style: module.StyleWarn})
	}
	switch {
	case m.run != nil && m.run.Live():
		d.Rows = append(d.Rows,
			module.Row{Kind: module.RowKV, Key: "run", Value: m.run.ID, Style: module.StyleAccent},
			module.Row{Kind: module.RowKV, Key: "demo", Value: m.run.Demo},
			module.Row{Kind: module.RowKV, Key: "workspace", Value: m.workspace(), Style: module.StyleDim})
	case m.staged != nil:
		d.Rows = append(d.Rows, module.Row{Kind: module.RowKV, Key: "loaded", Value: m.staged.Name})
	default:
		d.Rows = append(d.Rows, module.Row{Kind: module.RowText, Text: "no script, no run", Style: module.StyleDim})
	}
	d.Rows = append(d.Rows,
		module.Row{Kind: module.RowDivider},
		module.Row{Kind: module.RowText, Text: "transport lands with OBS (P1)", Style: module.StyleDim},
		module.Row{Kind: module.RowText, Text: "ad hoc record lands with it", Style: module.StyleDim})
	return d
}

// workspace is where the live run's clips land (P1 creates it; the shell
// shows intent). Caller holds mu.
func (m *Mod) workspace() string {
	if m.runsDir == "" || m.run == nil {
		return "(no state root)"
	}
	root := filepath.Dir(m.runsDir) // <state>/reno
	return filepath.Join(root, "workspace", m.run.Demo, m.run.ID)
}

// heatFrac clamps a budget fraction into PctHeat's live range; PctHeat
// zero means unset, so a just-entered step pins to a hair above zero.
func heatFrac(f float64) float64 {
	return min(max(f, 0.001), 1)
}

// noteLines splits a note body for the wire: rows carry no embedded
// newlines (one line per row); the dock renderer re-joins and wraps to
// the region rect.
func noteLines(note string) []string {
	if note == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(note, "\n"), "\n")
	return lines
}
