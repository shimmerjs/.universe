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
	KeyActArm      = "act:arm"
	KeyActWindow   = "act:obswindow"
	KeyActVCam     = "act:vcam"
	KeyActClip     = "act:clip"
	KeyActEndRun   = "act:endrun"
	KeyCfgScreens  = "cfg:screens"
	KeyCfgAudio    = "cfg:audio"
	KeyCfgWebcam   = "cfg:webcam"
	KeyFXMove      = "fx:move"
	KeyFXSize      = "fx:size"
	KeyFXHide      = "fx:hide"
	KeyFXLemmings  = "fx:lemmings"
	KeyFXLevel     = "fx:level"
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
	glyphArm    = "\U000F06AD" // nf-md-timer_sand_empty
	glyphWindow = "\U000F05B2" // nf-md-window_restore
	glyphMic    = "\U000F036C" // nf-md-microphone
	glyphMicOff = "\U000F036D" // nf-md-microphone_off
	glyphCam    = "\U000F0567" // nf-md-video
	glyphCamOff = "\U000F0568" // nf-md-video_off
	glyphScreen = "\U000F0379" // nf-md-monitor
	glyphScript = "\U000F0219" // nf-md-file_document
	// shared, not a film codepoint: an unverified glyph is a tofu, and this
	// tile carries a filename that says what it is anyway
	glyphClip = glyphCam
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
	// no clip here: a finished take is pressable, so by the header/tile
	// split it belongs on a tile (appendClip), not in this line
	//
	// what the live apply last did to the scene: the pad is a mixer, so
	// the readout has to say whether the knob you just turned reached OBS
	switch {
	case m.applyLive:
		add("scene", "applying...", module.StyleDim)
	case m.applyFail:
		add("scene", m.applyNote, module.StyleWarn)
	case m.applyNote != "":
		add("scene", m.applyNote, module.StyleDim)
	}
	// FX one-shots read back beside the scene state, not inside it: they
	// leave no desired state behind, so the only honest record of one is
	// what the last one did
	switch {
	case m.fxFail:
		add("fx", m.fxNote, module.StyleWarn)
	case m.fxNote != "":
		add("fx", m.fxNote, module.StyleDim)
	}
	if m.notesPath != "" {
		add("notes", filepath.Base(m.notesPath), module.StyleDim)
	}
	return append(rows, module.Row{Kind: module.RowSpans, Key: KeyHead, Spans: spans})
}

// transportTileH is the height the transport band asks the pad for: a frame
// around three interior rows, which buys the glyph a line of its own above
// the name and the state. Three and not more -- a glyph is one row tall
// however much room it gets, so a fourth interior row is a blank one. The
// transport is the pad's only weighted control: one target you hit without
// aiming, against choice bands you read before you press.
const transportTileH = 5

// Pad section names; the dock draws each as a labeled band of buttons.
const (
	secTransport = "transport"
	secScreens   = "screens"
	secMic       = "mic"
	secCam       = "cam"
	secFX        = "fx"
)

// recorderTiles is the pad's button set. Every CHOICE is its own button --
// each display, each microphone, each webcam preset -- so selecting one is
// a single tap on a lit target instead of a popover you aim at twice.
// Caller holds mu.
func (m *Mod) recorderTiles() []module.Row {
	// the take sits beside the transport that made it, and ahead of the
	// scripted branch below: a scripted run produces a clip too
	tiles := m.appendClip([]module.Row{m.transportTile()})

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

	tiles = append(tiles, m.probeTile(), m.obsWindowTile(), m.vcamTile())
	tiles = m.appendEndRun(tiles)

	inv := m.inventory
	if inv == nil {
		// nothing probed yet: say so where the device bands would be, but
		// say it on a LIVE button -- imperative text on a dead tile is the
		// pad telling you to tap something that ignores taps
		return append(tiles, module.Row{Kind: module.RowPad, Section: secScreens,
			Key: "cfg:unprobed", Glyph: glyphProbe, Text: "probe OBS first",
			Style: module.StyleHighlight, Act: []string{actProbe}})
	}

	for _, it := range inv.Screens {
		t := module.Row{Kind: module.RowPad, Section: secScreens,
			Key: KeyCfgScreens + ":" + it.Value, Glyph: glyphScreen, Text: it.Name,
			Act: []string{actCfgScreen, it.Value}}
		for _, s := range m.desired.Screens {
			// same resolver as the plan, so a display selected by name lights
			// its own tile instead of looking unselected next to a stray one
			if resolvesTo(inv.Screens, s.Display, it.Value) {
				t.Style = module.StyleAccent
				if s.Region != nil {
					t.Value = s.Region.String()
				}
				break
			}
		}
		tiles = append(tiles, t)
	}
	// A CONFIGURED display that is no longer attached keeps a tile of its
	// own. The enumeration self-heals to what is present, so without this
	// the selection would quietly vanish from glass while still sitting in
	// the config, and the recording would come up short with nothing on the
	// pad having said so. Tapping it drops the selection. This is the alarm
	// the old enumeration-age warning was reaching for and missing: not
	// "the list is old" but "what you asked to record is not there".
	for _, s := range m.desired.Screens {
		// Resolvable is the test, NOT exact-value equality. The plan resolves
		// a selector by value, name, index or unique name substring, so a
		// config written from the CLI (`--screen XENEON`) is perfectly good
		// and used to render here as "not attached" beside the very display
		// it selects.
		if _, err := renocore.ResolveItem(inv.Screens, s.Display, "display"); err == nil {
			continue
		}
		tiles = append(tiles, module.Row{Kind: module.RowPad, Section: secScreens,
			Key: KeyCfgScreens + ":" + s.Display, Glyph: glyphScreen,
			Text: shortSel(s.Display), Value: "not attached", Style: module.StyleWarn,
			Act: []string{actCfgScreen, s.Display}})
	}

	// Both audio knobs silence reno's own mic; they differ in what they do
	// to the REST of OBS. "no mic" leaves desktop audio and any hand-added
	// input alone, "mute all" forces every audio input silent. That is a
	// real difference worth two buttons -- but it is a difference about
	// scope, so the labels say scope instead of the old "obs as-is", which
	// named a mechanism nobody can see.
	tiles = append(tiles,
		m.choiceTile(secMic, KeyCfgAudio+":default", "no mic", glyphMicOff,
			actCfgAudio, cfgDefault, m.desired.Audio == ""),
		m.choiceTile(secMic, KeyCfgAudio+":none", "mute all", glyphMicOff,
			actCfgAudio, renocore.AudioNone, m.desired.Audio == renocore.AudioNone))
	for _, it := range inv.Mics {
		tiles = append(tiles, m.choiceTile(secMic, KeyCfgAudio+":"+it.Value, deviceLabel(it.Name),
			glyphMic, actCfgAudio, it.Value, m.desired.Audio == it.Value))
	}

	// The cam band has NO as-is tile. Unset and "off" both end with a
	// disabled reno-cam -- the only difference is whether the device and
	// transform were pre-set, which nothing on glass can show. One dead
	// camera needs one button, and "off" is the one that leaves the cam
	// staged so a later preset tap is instant.
	for _, p := range renocore.WebcamPresets {
		tiles = append(tiles, m.choiceTile(secCam, KeyCfgWebcam+":"+p, p,
			glyphCam, actCfgWebcam, p, m.desired.Webcam == p))
	}

	// The FX band is the same placements arrived at by an eased path
	// instead of a jump cut, so it reads off the same selection -- a move
	// that landed IS the current placement.
	tiles = append(tiles, m.slidePad(), m.sizeStepper(),
		m.choiceTile(secFX, KeyFXMove+":"+renocore.CamBig, "slide big",
			glyphCam, actFXMove, renocore.CamBig, m.desired.Webcam == renocore.CamBig),
		module.Row{Kind: module.RowPad, Section: secFX,
			Key: KeyFXHide, Glyph: glyphCamOff, Text: "hide",
			Act: []string{actFXHide}},
		m.lemmingsTile())

	// The level effects each get ONE remote: the name and its reading, with
	// a minus and a plus welded either side. They are allowed on the pad
	// where a toggle was not, because the level is khudson's own number
	// rather than a filter setting nothing can read back, so the element
	// showing it is showing the truth.
	for _, kind := range renocore.LevelFXKinds {
		tiles = append(tiles, m.levelStepper(kind))
	}
	return tiles
}

// lemmingsTile toggles the html overlay that rides on the camera's box. It
// is a plain toggle rather than a level because the page animates itself:
// there is no magnitude khudson holds, so there is nothing for a stepper to
// report, and a stepper reporting a number it made up is the tile that lies.
//
// It dims with no camera visible, because the overlay hangs on the
// camera's box and there is none -- the same honesty the size pair keeps.
// Still pressable there: the config knob is real, and the overlay appears
// with the camera when one is placed. Caller holds mu.
func (m *Mod) lemmingsTile() module.Row {
	t := module.Row{Kind: module.RowPad, Section: secFX,
		Key: KeyFXLemmings, Glyph: glyphCam, Text: "lemmings",
		Act: []string{actLemmings}}
	switch {
	case !m.desired.Lemmings:
		t.Value = "off"
		t.Style = module.StyleDim
	case m.desired.Webcam == "" || m.desired.Webcam == "off":
		t.Value = "no cam"
		t.Style = module.StyleDim
	default:
		t.Value = "on"
		t.Style = module.StyleAccent
	}
	return t
}

// slidePad is the four corner placements arranged where they point. It
// replaced four buttons in a row whose labels were the only thing saying
// which corner was which -- put them in the corners and there is nothing
// left to read. Each press is an eased slide, so the element is one control
// with four destinations rather than four controls.
//
// The corner the camera is already in is dim: an end that would move
// nothing must not look like one that would, and dropping it would leave a
// hole where a corner ought to be. Caller holds mu.
func (m *Mod) slidePad() module.Row {
	at, grown := renocore.SplitPlacement(m.desired.Webcam)
	t := module.Row{Kind: module.RowDpad, Section: secFX,
		Key: KeyFXMove, Glyph: glyphCam, MinHeight: 4,
		Text: "slide", Value: placementValue(m.desired.Webcam)}
	for _, c := range renocore.CamCorners {
		// the slide keeps the size the camera already has, so the d-pad
		// moves it and the pair beside it resizes it, and neither surprises
		// you by doing the other's job
		preset := renocore.Placement(c, grown)
		t.Ends = append(t.Ends, module.End{
			Label: c, Key: KeyFXMove + ":" + preset, Dim: c == at,
			Argv: []string{actFXMove, preset},
		})
	}
	if at != "" {
		t.Style = module.StyleAccent
	}
	return t
}

// sizeStepper grows and shrinks whichever corner the camera is in. Both
// ends dim when it is in none -- off, big, or never placed -- because
// there is genuinely nothing to resize, and a live-looking button that
// silently does nothing is the thing this pad refuses to have. Caller
// holds mu.
func (m *Mod) sizeStepper() module.Row {
	at, grown := renocore.SplitPlacement(m.desired.Webcam)
	value := "--"
	switch {
	case at == "":
	case grown:
		value = "large"
	default:
		value = "small"
	}
	end := func(label string, want bool) module.End {
		e := module.End{Label: label, Key: KeyFXSize + ":" + label,
			Dim: at == "" || grown == want}
		if !e.Dim {
			e.Argv = []string{actFXMove, renocore.Placement(at, want)}
		}
		return e
	}
	return module.Row{Kind: module.RowStepper, Section: secFX,
		Key: KeyFXSize, Glyph: glyphCam, MinHeight: 4,
		Text: "size", Value: value,
		Ends: []module.End{end("-", false), end("+", true)}}
}

// placementValue is the slide pad's readout: where the camera is, in the
// vocabulary the rest of the surface uses.
func placementValue(preset string) string {
	switch preset {
	case "":
		return "unset"
	case "off":
		return "hidden"
	}
	return preset
}

// levelStepper is one level effect's remote. Each end dims at its stop: an
// end that cannot move the level any further must not look like one that
// can, and it must not disappear either -- the other end would slide across
// under the thumb already coming down on it.
func (m *Mod) levelStepper(kind string) module.Row {
	level := m.fxLevels[kind]
	key := KeyFXLevel + ":" + kind
	// two interior lines: the name and its reading each get one. Folded onto
	// a single line the ends leave so little middle that the name crops to
	// four cells, and "purp 12/20" is not a control anyone can read.
	t := module.Row{Kind: module.RowStepper, Section: secFX,
		Key: key, Glyph: glyphCam, MinHeight: 4,
		Text: kind, Value: levelValue(level),
		Ends: []module.End{
			{Label: "-", Key: key + ":" + levelDown, Dim: level <= 0,
				Argv: []string{actFXLevel, kind, levelDown}},
			{Label: "+", Key: key + ":" + levelUp, Dim: level >= renocore.FXMaxLevel,
				Argv: []string{actFXLevel, kind, levelUp}},
		}}
	if level > 0 {
		t.Style = module.StyleAccent
	}
	return t
}

// levelValue is the readout both halves of a pair carry.
func levelValue(level int) string {
	if level <= 0 {
		return "off"
	}
	return fmt.Sprintf("%d/%d", level, renocore.FXMaxLevel)
}

// resolvesTo reports whether selector picks out the display with this
// value, through the same resolution the plan uses.
func resolvesTo(items []renocore.PropertyItem, selector, value string) bool {
	it, err := renocore.ResolveItem(items, selector, "display")
	return err == nil && it.Value == value
}

// shortSel labels a display selector that has no name left to show: the
// display is unplugged, so the probe's label went with it and only the raw
// UUID or index remains. A UUID's head is enough to tell two apart.
func shortSel(sel string) string {
	if len(sel) > 8 {
		return sel[:8]
	}
	return sel
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

// transportTile is the one button the whole transport cycles through:
// ARM -> REC -> STOP. Each face only appears when it can honour its own
// name, which is the point -- REC never shows unless recording starts NOW,
// because ARM already paid for the OBS launch and the scene. A button that
// says record and then thinks for two minutes is the thing this replaces.
//
// STOP ends the take and leaves the rig ARMED, so the next take is instant;
// unarming is END RUN's job (it releases the scene and the devices with it).
// Caller holds mu.
func (m *Mod) transportTile() module.Row {
	t := module.Row{Kind: module.RowPad, Section: secTransport, Key: KeyActRecord,
		Style: module.StyleHighlight, MinHeight: transportTileH}
	rec := m.recording()

	// rolling: the classic stop square, and the take's clock on its face
	if rec.Active() {
		t.Glyph, t.Text, t.Act = glyphStop, "STOP", []string{actRecord}
		t.Style, t.Value = module.StyleWarn, fmtClock(m.now().Sub(rec.StartedAt))
		return t
	}
	// mid-flight: the take is open, or a verb is working toward the first
	// frame. Still the stop face -- whatever happens next, stopping is the
	// move -- but never a claim that frames are going
	if rec != nil && rec.StoppedAt.IsZero() && rec.Failed == "" {
		t.Glyph, t.Text, t.Act = glyphStop, "STOP", []string{actRecord}
		t.Value = "starting"
		if m.recPhase != "" {
			t.Value = m.recPhase + "..."
		}
		return t
	}
	// armed: the record circle, and it means it
	if m.armed() {
		t.Glyph, t.Text, t.Act = glyphRecord, "REC", []string{actRecord}
		t.Value = "ready"
		if rec != nil && rec.Failed != "" {
			t.Value = "retry"
		}
		return t
	}
	// unarmed: bring the rig up first
	t.Glyph, t.Text, t.Act = glyphArm, "ARM", []string{actArm}
	switch {
	case m.armLive:
		t.Value, t.Style = "arming...", module.StyleDim
	case m.armFail:
		t.Value, t.Style = m.armNote, module.StyleWarn
	default:
		t.Value = "obs cold"
	}
	return t
}

// armed reports whether the rig is up: OBS running and the scene matching
// the configured posture. It is the ARM verb's confirmation, cleared by the
// release END RUN performs -- never inferred from age, because an OBS that
// quit under us leaves no trace here and a guess would put REC on a button
// that cannot record. Caller holds mu.
func (m *Mod) armed() bool { return m.armNote != "" && !m.armFail }

// obsWindowTile shows and hides OBS's own window. It earns a state value
// where the pixelate control could not: kAXHidden reads back, so the tile
// reports where the window actually is rather than where the last tap
// meant to put it. Caller holds mu.
func (m *Mod) obsWindowTile() module.Row {
	t := module.Row{Kind: module.RowPad, Section: secTransport, Key: KeyActWindow,
		Glyph: glyphWindow, Text: "obs window", Act: []string{actWindow}}
	switch {
	case m.winLive:
		t.Value, t.Style = "...", module.StyleDim
	case m.winFail:
		// not running, or the AX grant is gone: either way the honest
		// answer is that we do not know, not a guess
		t.Value, t.Style = "unknown", module.StyleWarn
	case m.winState == "hidden":
		t.Value, t.Style = "hidden", module.StyleDim
	case m.winState == "shown":
		t.Value, t.Style = "shown", module.StyleAccent
	default:
		t.Value = "?"
	}
	return t
}

// vcamTile reports OBS's virtual camera and takes it down on a tap. Release
// already stops it, but stopping is not SHOWING it -- and the case this tile
// exists for is the one Release cannot help with: an output nobody here
// started, holding the camera right now, with the rest of the pad reporting a
// clean idle rig. Tapping is safe in every state; the verb never starts one.
// Caller holds mu.
func (m *Mod) vcamTile() module.Row {
	t := module.Row{Kind: module.RowPad, Section: secTransport, Key: KeyActVCam,
		Glyph: glyphCamOff, Text: "vcam", Act: []string{actVCam}}
	switch {
	case m.vcamLive:
		t.Value, t.Style = "...", module.StyleDim
	case m.vcamFail:
		// unknown beats a guess: "idle" here would be the exact lie the
		// tile was added to catch
		t.Value, t.Style = "unknown", module.StyleWarn
	case m.vcamState == "on":
		// the loudest state on the pad: something has the camera and it is
		// not anything reno put there. The value stays one word because a
		// tile is ~12 interior cells and a cropped value is its own lie
		t.Glyph, t.Text = glyphCam, "VCAM"
		t.Value, t.Style = "LIVE", module.StyleWarn
	case m.vcamState == "off":
		t.Value, t.Style = "off", module.StyleDim
	default:
		t.Value = "?"
	}
	return t
}

// probeTile re-enumerates the devices the choice bands are drawn from. It
// also boots OBS, so it stays a deliberate tap and never an automatic poll.
//
// It reports the DEVICE LIST, not OBS -- whether OBS is up and configured
// is standby's question, and the tile used to answer both badly. There is
// deliberately no age warning: a list an hour old is not wrong, and the
// hour timer that used to warn here fired constantly, meant nothing you
// could act on, and taught you to ignore the one tile that also carries
// the real alarm. The age shows as plain information; identity is what
// escalates, per displaysMoved. Caller holds mu.
func (m *Mod) probeTile() module.Row {
	t := module.Row{Kind: module.RowPad, Section: secTransport, Key: KeyActProbe,
		Glyph: glyphProbe, Text: "devices", Value: "not probed",
		Style: module.StyleHighlight, Act: []string{actProbe}}
	inv := m.inventory
	if inv == nil {
		return t
	}
	if displaysMoved(inv, m.local) {
		// the enumeration is not merely old: its screen selectors point at
		// displays that are not there any more, so a recording built from
		// it would capture the wrong thing or nothing
		t.Text, t.Value, t.Style = "RE-PROBE", "displays moved", module.StyleWarn
		return t
	}
	t.Value = fmtAge(m.now().Sub(inv.At)) + " old"
	return t
}

// appendClip adds the open-the-take button, present only while a stopped
// recording has an output and gone with it -- an inert tile reads as a broken
// one, the same rule appendEndRun follows. Today it hands the file to the OS;
// it is also the way into the P4 scrub surface when that lands.
//
// The path rides in the argv, and open-clip is what refuses anything
// resolving outside the workspace root. Caller holds mu.
func (m *Mod) appendClip(tiles []module.Row) []module.Row {
	rec := m.recording()
	if rec == nil || rec.StoppedAt.IsZero() || rec.Output == "" {
		return tiles
	}
	return append(tiles, module.Row{Kind: module.RowPad, Section: secTransport,
		Key: KeyActClip, Glyph: glyphClip, Text: "clip",
		Value: filepath.Base(rec.Output), Style: module.StyleAccent,
		Act: []string{actOpenClip, rec.Output}})
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
