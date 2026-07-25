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
	KeyActEndRun   = "act:endrun"
	KeyCfgScreens  = "cfg:screens"
	KeyCfgAudio    = "cfg:audio"
	KeyCfgWebcam   = "cfg:webcam"
	KeyCfgScript   = "cfg:script"
	KeyNotes       = "notes"
	KeyHead        = "head"
)

// Recorder pad glyphs, nf-md from the HUD's Nerd Font. Escaped, not
// literal: the source stays ASCII and the codepoint stays greppable.
const (
	glyphRecord = "\U000F044A" // nf-md-record
	glyphStop   = "\U000F04DB" // nf-md-stop
	glyphProbe  = "\U000F0450" // nf-md-refresh
	glyphMic    = "\U000F036C" // nf-md-microphone
	glyphMicOff = "\U000F036D" // nf-md-microphone_off
	glyphCam    = "\U000F0567" // nf-md-video
	glyphScreen = "\U000F0379" // nf-md-monitor
	glyphScript = "\U000F0219" // nf-md-file_document
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

// recorderData is the recorder control pad: a header line of readouts
// over one tile per pressable control. States are EXPLICIT -- armed
// intent, live, stopped-with-clip, failed -- never a stale-looking
// healthy default (the OBS disconnect hazard from the design's P1
// preconditions). Readouts never become tiles: a button you cannot press
// is a lie about the surface.
func (m *Mod) recorderData() module.Data {
	d := module.Data{Title: "reno rec"}
	d.Rows = append(d.Rows, m.recorderHeadRows()...)
	d.Rows = append(d.Rows, m.recorderTiles()...)
	return d
}

// recorderHeadRows is the pad's readout header: the loud failures first,
// then one dense status line. Caller holds mu.
func (m *Mod) recorderHeadRows() []module.Row {
	var rows []module.Row
	if m.journalErr != "" {
		rows = append(rows, module.Row{Kind: module.RowText,
			Text: "journal: " + m.journalErr, Style: module.StyleWarn})
	}
	rec := m.recording()
	if rec != nil && rec.Failed != "" {
		rows = append(rows, module.Row{Kind: module.RowText,
			Text: "recording FAILED: " + rec.Failed, Style: module.StyleWarn})
	}

	var spans []module.Span
	add := func(key, value, style string) {
		if value == "" {
			return
		}
		if len(spans) > 0 {
			spans = append(spans, module.Span{Text: "   "})
		}
		spans = append(spans,
			module.Span{Text: key + " ", Style: module.StyleDim},
			module.Span{Text: value, Style: style})
	}
	switch {
	case m.run != nil && m.run.Live():
		add("run", m.run.ID, module.StyleAccent)
		add("demo", m.run.Demo, "")
		add("clips", m.workspace(), module.StyleDim)
	case m.staged != nil:
		add("loaded", m.staged.Name, "")
	default:
		add("run", "none -- record arms an ad hoc one", module.StyleDim)
	}
	if rec != nil && !rec.StoppedAt.IsZero() && rec.Output != "" {
		add("clip", filepath.Base(rec.Output), module.StyleAccent)
	}
	if m.notesPath != "" {
		add("notes", filepath.Base(m.notesPath), module.StyleDim)
	}
	return append(rows, module.Row{Kind: module.RowSpans, Key: KeyHead, Spans: spans})
}

// Pad section names; the dock draws each as a labeled band of buttons.
const (
	secTransport = "transport"
	secScreens   = "screens"
	secMic       = "mic"
	secCam       = "cam"
)

// recorderTiles is the pad's button set. Every CHOICE is its own button --
// each display, each microphone, each webcam preset -- so selecting one is
// a single tap on a lit target instead of a popover you aim at twice.
// Caller holds mu.
func (m *Mod) recorderTiles() []module.Row {
	tiles := []module.Row{m.recordTile()}

	// a scripted recording run reads its config FROM the script: the ad
	// hoc knobs would lie about what is about to record, so the script's
	// config shows as one unpressable readout instead (arc-2 review #6)
	if m.staged != nil && m.staged.Record &&
		m.run != nil && m.run.Live() && m.run.Demo == m.staged.Name {
		v := "from script"
		if !m.staged.RecordCfg.Empty() {
			v = m.staged.RecordCfg.Summary()
		} else if m.staged.Scene != "" {
			v = "scene " + m.staged.Scene
		}
		tiles = append(tiles, module.Row{Kind: module.RowPad, Section: secTransport,
			Key: KeyCfgScript, Glyph: glyphScript, Text: "SCRIPTED", Value: v,
			Style: module.StyleDim})
		return m.appendEndRun(tiles)
	}

	tiles = append(tiles, m.probeTile())
	tiles = m.appendEndRun(tiles)

	inv := m.inventory
	if inv == nil {
		// nothing probed yet: the OBS button above is the whole surface,
		// and saying so beats a band of buttons that select nothing
		return append(tiles, module.Row{Kind: module.RowPad, Section: secScreens,
			Key: "cfg:unprobed", Text: "probe OBS first", Style: module.StyleDim})
	}

	for _, it := range inv.Screens {
		t := module.Row{Kind: module.RowPad, Section: secScreens,
			Key: KeyCfgScreens + ":" + it.Value, Glyph: glyphScreen, Text: it.Name,
			Act: []string{actCfgScreen, it.Value}}
		for _, s := range m.pending.Screens {
			if s.Display == it.Value {
				t.Style = module.StyleAccent
				if s.Region != nil {
					t.Value = s.Region.String()
				}
				break
			}
		}
		tiles = append(tiles, t)
	}

	// "obs as-is" clears the knob (no scene reconciliation) while "MUTE"
	// forces every non-reno input silent -- different promises, so they
	// stay different buttons (arc-2 review #4, the webcam off/as-is split)
	tiles = append(tiles,
		m.choiceTile(secMic, KeyCfgAudio+":default", "obs as-is", glyphMic,
			actCfgAudio, cfgDefault, m.pending.Audio == ""),
		m.choiceTile(secMic, KeyCfgAudio+":none", "MUTE", glyphMicOff,
			actCfgAudio, renocore.AudioNone, m.pending.Audio == renocore.AudioNone))
	for _, it := range inv.Mics {
		tiles = append(tiles, m.choiceTile(secMic, KeyCfgAudio+":"+it.Value, deviceLabel(it.Name),
			glyphMic, actCfgAudio, it.Value, m.pending.Audio == it.Value))
	}

	tiles = append(tiles, m.choiceTile(secCam, KeyCfgWebcam+":default", "obs as-is",
		glyphCam, actCfgWebcam, cfgDefault, m.pending.Webcam == ""))
	for _, p := range renocore.WebcamPresets {
		tiles = append(tiles, m.choiceTile(secCam, KeyCfgWebcam+":"+p, p,
			glyphCam, actCfgWebcam, p, m.pending.Webcam == p))
	}
	return tiles
}

// deviceLabel trims the noise OBS reports in device names: a key is ~18
// cells wide, and "MacBook Pro Microphone" spends four of them saying
// what the MIC band already said.
func deviceLabel(name string) string {
	for _, suffix := range []string{" Microphone", " Camera", " Audio Device"} {
		if trimmed := strings.TrimSuffix(name, suffix); trimmed != name && trimmed != "" {
			return trimmed
		}
	}
	return name
}

// choiceTile is one button in a pick-one band: lit when it is the live
// selection, plain otherwise.
func (m *Mod) choiceTile(section, key, label, glyph, act, value string, on bool) module.Row {
	t := module.Row{Kind: module.RowPad, Section: section, Key: key,
		Glyph: glyph, Text: label, Act: []string{act, value}}
	if on {
		t.Style = module.StyleAccent
	}
	return t
}

// recordTile is the transport button: one tap arms an ad hoc run and
// starts OBS, or stops the recording that is live. Caller holds mu.
func (m *Mod) recordTile() module.Row {
	t := module.Row{Kind: module.RowPad, Section: secTransport, Key: KeyActRecord,
		Glyph: glyphRecord, Text: "REC", Value: "ready",
		Style: module.StyleHighlight, Act: []string{actRecord}}
	rec := m.recording()
	switch {
	case rec.Active():
		t.Glyph, t.Text = glyphStop, "STOP"
		t.Style, t.Value = module.StyleWarn, fmtClock(m.now().Sub(rec.StartedAt))
	case rec != nil && rec.Failed != "":
		t.Value = "retry"
	case rec != nil && rec.StoppedAt.IsZero():
		t.Value = "arming"
	}
	return t
}

// probeTile re-enumerates the OBS devices behind the choice bands. It also
// boots OBS, so it stays a deliberate tap and never an automatic poll.
func (m *Mod) probeTile() module.Row {
	t := module.Row{Kind: module.RowPad, Section: secTransport, Key: KeyActProbe,
		Glyph: glyphProbe, Text: "OBS", Value: "cold",
		Style: module.StyleHighlight, Act: []string{actProbe}}
	inv := m.inventory
	if inv == nil {
		return t
	}
	t.Value = inv.ObsVersion
	if m.now().Sub(inv.At) > invStale {
		t.Value = inv.ObsVersion + " stale"
		return t
	}
	t.Style = ""
	return t
}

// appendEndRun adds the close-the-run button, but only while one is live:
// an inert tile reads as a broken one, so the pad loses the button rather
// than greying it.
func (m *Mod) appendEndRun(tiles []module.Row) []module.Row {
	if m.run == nil || !m.run.Live() {
		return tiles
	}
	return append(tiles, module.Row{Kind: module.RowPad, Section: secTransport,
		Key: KeyActEndRun, Glyph: glyphStop, Text: "END RUN", Value: m.run.Demo,
		Style: module.StyleWarn, Act: []string{actStop}})
}

// recording is the live run's recording state, nil-safe for every
// no-run branch. Caller holds mu.
func (m *Mod) recording() *renocore.RecordingState {
	if m.run == nil {
		return nil
	}
	return m.run.Recording
}

func (m *Mod) pendingAudioLabel() string {
	if m.pending.Audio == "" {
		return "obs as-is"
	}
	if inv := m.inventory; inv != nil {
		return audioLabel(inv, m.pending.Audio)
	}
	return m.pending.Audio
}

func (m *Mod) pendingWebcamLabel() string {
	if m.pending.Webcam == "" {
		return "obs as-is"
	}
	return m.pending.Webcam
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
