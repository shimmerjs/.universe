package reno

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
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
	KeyActRecord   = "act:record"
	KeyActProbe    = "act:probe"
	KeyCfgScreens  = "cfg:screens"
	KeyCfgAudio    = "cfg:audio"
	KeyCfgWebcam   = "cfg:webcam"
	KeyNotes       = "notes"
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
		if m.notes != "" {
			// ad hoc notes without a script: the zero-ceremony presenter
			d.Title = "reno: notes"
			d.Rows = append(d.Rows, m.notesRows()...)
			break
		}
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
		// a live run with no usable script: a mid-flight ad hoc run (its
		// notes render here), a recovered ad hoc journal, or a script
		// that changed under the run. Every recovery branch must render
		// -- a panic here crash-loops the bus straight back into the
		// same journal.
		d.Title = "reno: " + m.run.Demo
		d.Rows = append(d.Rows,
			module.Row{Kind: module.RowKV, Key: "run", Value: m.run.ID, Style: module.StyleAccent})
		if m.stale == "" && m.notes != "" {
			d.Rows = append(d.Rows, m.notesRows()...)
			break
		}
		d.Rows = append(d.Rows,
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

// recorderData is the recorder view: run identity, the recording
// transport (one-tap ad hoc record / stop), and the journaled recording
// state. States are EXPLICIT -- armed intent, live, stopped-with-clip,
// failed -- never a stale-looking healthy default (the OBS disconnect
// hazard from the design's P1 preconditions).
func (m *Mod) recorderData() module.Data {
	d := module.Data{Title: "reno rec"}
	if m.journalErr != "" {
		d.Rows = append(d.Rows, module.Row{Kind: module.RowText, Text: "journal: " + m.journalErr, Style: module.StyleWarn})
	}
	if m.run != nil {
		d.Rows = append(d.Rows, m.recordingRows()...)
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
	d.Rows = append(d.Rows, module.Row{Kind: module.RowDivider})
	d.Rows = append(d.Rows, m.configRows()...)
	d.Rows = append(d.Rows, module.Row{Kind: module.RowDivider}, m.recordControlRow())
	return d
}

// configRows renders the next run's recording config with the menus
// that mutate it. Scripted recording runs read their config from the
// script; the pending knobs then show read-only so a leftover ad hoc
// config never looks armed.
func (m *Mod) configRows() []module.Row {
	// the script's config rows show only while the SCRIPTED run is live:
	// with no live scripted run, a record tap arms an AD HOC run with the
	// pending knobs, and displaying the script's config over that would
	// lie about what is about to record (arc-2 review #6)
	if m.staged != nil && m.staged.Record &&
		m.run != nil && m.run.Live() && m.run.Demo == m.staged.Name {
		v := "from script"
		if !m.staged.RecordCfg.Empty() {
			v = m.staged.RecordCfg.Summary()
		} else if m.staged.Scene != "" {
			v = "scene " + m.staged.Scene
		}
		return []module.Row{{Kind: module.RowKV, Key: "config", Value: v, Style: module.StyleDim}}
	}
	inv := m.inventory
	if inv == nil || m.now().Sub(inv.At) > invStale {
		label := "probe obs devices"
		if inv != nil {
			label = "re-probe obs devices (inventory stale)"
		}
		rows := m.pendingSummaryRows()
		return append(rows, module.Row{Kind: module.RowText, Key: KeyActProbe,
			Text: label, Style: module.StyleHighlight, Act: []string{actProbe}})
	}

	screens := module.Row{Kind: module.RowKV, Key: KeyCfgScreens, Value: "obs as-is",
		Menu: m.screenMenu(inv)}
	if len(m.pending.Screens) > 0 {
		var sel []string
		for _, s := range m.pending.Screens {
			sel = append(sel, screenLabel(inv, s))
		}
		screens.Value = strings.Join(sel, " + ")
		screens.Style = module.StyleAccent
	}
	audio := module.Row{Kind: module.RowKV, Key: KeyCfgAudio, Value: "obs as-is",
		Menu: m.audioMenu(inv)}
	if m.pending.Audio != "" {
		audio.Value = audioLabel(inv, m.pending.Audio)
		audio.Style = module.StyleAccent
	}
	webcam := module.Row{Kind: module.RowKV, Key: KeyCfgWebcam, Value: "obs as-is",
		Menu: m.webcamMenu()}
	if m.pending.Webcam != "" {
		webcam.Value = m.pending.Webcam
		webcam.Style = module.StyleAccent
	}
	rows := []module.Row{screens, audio, webcam}
	if m.notesPath != "" {
		rows = append(rows, module.Row{Kind: module.RowKV, Key: KeyNotes,
			Value: filepath.Base(m.notesPath), Style: module.StyleDim})
	}
	return rows
}

// pendingSummaryRows shows a config set before any probe (CLI-driven):
// honest even without menus.
func (m *Mod) pendingSummaryRows() []module.Row {
	if m.pending.Empty() {
		return nil
	}
	return []module.Row{{Kind: module.RowKV, Key: "config",
		Value: m.pending.Summary(), Style: module.StyleAccent}}
}

func (m *Mod) screenMenu(inv *renocore.ObsInventory) []module.Act {
	var items []module.Act
	for _, it := range inv.Screens {
		label := it.Name
		for _, s := range m.pending.Screens {
			if s.Display == it.Value {
				label = "[x] " + label
				break
			}
		}
		items = append(items, module.Act{Label: label,
			Argv: []string{actCfgScreen, it.Value}, Section: "screens"})
	}
	return items
}

func (m *Mod) audioMenu(inv *renocore.ObsInventory) []module.Act {
	items := []module.Act{
		{Label: "obs as-is", Argv: []string{actCfgAudio, cfgDefault}, Section: "audio"},
		{Label: "none (mute all)", Argv: []string{actCfgAudio, renocore.AudioNone}, Section: "audio"},
	}
	for _, it := range inv.Mics {
		items = append(items, module.Act{Label: it.Name,
			Argv: []string{actCfgAudio, it.Value}, Section: "devices"})
	}
	return items
}

// webcamMenu: "obs as-is" clears the knob (no scene reconciliation);
// "off" is a REAL preset that forces the reno-cam item disabled -- the
// two are different promises and must never share a menu row (arc-2
// review #4: an "off" that meant as-is could record a hot camera).
func (m *Mod) webcamMenu() []module.Act {
	items := []module.Act{{Label: "obs as-is", Argv: []string{actCfgWebcam, cfgDefault}, Section: "webcam"}}
	for _, p := range renocore.WebcamPresets {
		items = append(items, module.Act{Label: p,
			Argv: []string{actCfgWebcam, p}, Section: "webcam"})
	}
	return items
}

// screenLabel resolves a selection back to its display name for the
// row; the raw value stands in when the inventory moved on.
func screenLabel(inv *renocore.ObsInventory, s renocore.ScreenSel) string {
	for _, it := range inv.Screens {
		if it.Value == s.Display {
			if s.Region != nil {
				return it.Name + " (" + s.Region.String() + ")"
			}
			return it.Name
		}
	}
	return s.String()
}

func audioLabel(inv *renocore.ObsInventory, q string) string {
	if q == renocore.AudioNone {
		return "none (mute all)"
	}
	for _, it := range inv.Mics {
		if it.Value == q {
			return it.Name
		}
	}
	return q
}

// recordControlRow is the transport's one tap: record (arming an ad hoc
// run when nothing is live) or stop-recording while one is live.
func (m *Mod) recordControlRow() module.Row {
	if m.run != nil && m.run.Recording.Active() {
		return module.Row{Kind: module.RowText, Key: KeyActRecord, Text: "stop recording",
			Style: module.StyleWarn, Act: []string{actRecord}}
	}
	return module.Row{Kind: module.RowText, Key: KeyActRecord, Text: "record",
		Style: module.StyleHighlight, Act: []string{actRecord}}
}

// recordingRows renders the journaled recording state, explicitly per
// phase. Caller holds mu; m.run is non-nil.
func (m *Mod) recordingRows() []module.Row {
	rec := m.run.Recording
	if rec == nil {
		return nil
	}
	switch {
	case rec.Failed != "":
		return []module.Row{{Kind: module.RowText, Text: "recording FAILED: " + rec.Failed, Style: module.StyleWarn}}
	case rec.Active():
		return []module.Row{{Kind: module.RowKV, Key: "recording",
			Value: fmtClock(m.now().Sub(rec.StartedAt)), Style: module.StyleWarn}}
	case !rec.StoppedAt.IsZero():
		out := rec.Output
		if out == "" {
			out = "(no output path noted)"
		}
		return []module.Row{{Kind: module.RowKV, Key: "clip", Value: out, Style: module.StyleAccent}}
	default:
		return []module.Row{{Kind: module.RowKV, Key: "recording", Value: "armed (start pending)", Style: module.StyleDim}}
	}
}

// heatFrac clamps a budget fraction into PctHeat's live range; PctHeat
// zero means unset, so a just-entered step pins to a hair above zero.
func heatFrac(f float64) float64 {
	return min(max(f, 0.001), 1)
}

// notesRows renders the ad hoc notes body as KeyNote rows (the dock
// wraps them) under their source-file chip. Caller holds mu.
func (m *Mod) notesRows() []module.Row {
	rows := []module.Row{{Kind: module.RowKV, Key: KeyNotes,
		Value: filepath.Base(m.notesPath), Style: module.StyleDim}}
	for _, line := range noteLines(m.notes) {
		rows = append(rows, module.Row{Kind: module.RowText, Key: KeyNote, Text: line})
	}
	return rows
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
