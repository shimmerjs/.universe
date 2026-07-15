package gesture

import (
	"math"
	"time"
)

// Config tunes the recognizer; zero values take the defaults.
type Config struct {
	SlopCells  float64       // tap/drag slop radius in cells; default 1.5
	LongPress  time.Duration // hold threshold; default 450ms
	SwipeCells int           // min dominant-axis travel; default 6
}

func (c Config) withDefaults() Config {
	if c.SlopCells == 0 {
		c.SlopCells = 1.5
	}
	if c.LongPress == 0 {
		c.LongPress = 450 * time.Millisecond
	}
	if c.SwipeCells == 0 {
		c.SwipeCells = 6
	}
	return c
}

type state int

const (
	stateIdle    state = iota
	statePending       // finger down, under slop, long-press timer running
	stateHeld          // long-press fired, waiting for release
	stateDrag
	stateMulti
)

// Recognizer is the gesture state machine. Not safe for concurrent use;
// the bus owns exactly one per glass.
type Recognizer struct {
	cal   Calibration
	cells CellMetrics
	cfg   Config

	state     state
	primary   uint8
	start     Point
	last      Point
	startTime time.Time

	// centroid track while >=2 tip contacts are down; multiDropped marks a
	// finger having lifted mid-gesture so a re-landed pair re-anchors as a
	// new stroke
	multiStart   Point
	multiLast    Point
	multiDropped bool
}

// New builds a recognizer for one calibration + cell grid.
func New(cal Calibration, cells CellMetrics, cfg Config) *Recognizer {
	return &Recognizer{cal: cal, cells: cells, cfg: cfg.withDefaults()}
}

// Frame consumes one contact frame and returns the gestures it completes
// or advances. Multi-contact handling is fully separated: frames that
// never carry two tip contacts never reach the multi path.
func (r *Recognizer) Frame(f Frame) []Event {
	tips := tipContacts(f.Contacts)
	if len(tips) >= 2 || r.state == stateMulti {
		return r.frameMulti(tips)
	}
	return r.frameSingle(f, tips)
}

// Deadline reports when Tick must next run for a time-based transition
// (the long-press timer); ok is false when no timer is armed.
func (r *Recognizer) Deadline() (t time.Time, ok bool) {
	if r.state == statePending {
		return r.startTime.Add(r.cfg.LongPress), true
	}
	return time.Time{}, false
}

// Tick fires time-based transitions. Frames alone would delay long-press
// if the digitizer goes quiet under a perfectly still finger.
func (r *Recognizer) Tick(now time.Time) []Event {
	if r.state == statePending && now.Sub(r.startTime) >= r.cfg.LongPress {
		r.state = stateHeld
		return []Event{LongPress{Pos: r.start}}
	}
	return nil
}

func (r *Recognizer) frameSingle(f Frame, tips []Contact) []Event {
	switch r.state {
	case stateIdle:
		if len(tips) == 0 {
			return nil
		}
		c := tips[0]
		r.primary = c.ID
		r.start = r.point(c)
		r.last = r.start
		r.startTime = f.Time
		r.state = statePending
		// touch acknowledgment: every press opens with exactly one Press,
		// resolved later by the Tap/LongPress/DragStart classification
		return []Event{Press{Pos: r.start}}

	case statePending:
		c, ok := findContact(tips, r.primary)
		if !ok {
			r.state = stateIdle
			// release: a missed Tick can leave a due long-press unfired
			if f.Time.Sub(r.startTime) >= r.cfg.LongPress {
				return []Event{LongPress{Pos: r.start}}
			}
			return []Event{Tap{Pos: r.start}}
		}
		p := r.point(c)
		if r.beyondSlop(p) {
			r.state = stateDrag
			evs := []Event{DragStart{Start: r.start}}
			return append(evs, r.dragMove(p)...)
		}
		if f.Time.Sub(r.startTime) >= r.cfg.LongPress {
			r.state = stateHeld
			return []Event{LongPress{Pos: r.start}}
		}
		return nil

	case stateHeld:
		if _, ok := findContact(tips, r.primary); !ok {
			r.state = stateIdle
		}
		return nil

	case stateDrag:
		c, ok := findContact(tips, r.primary)
		if !ok {
			evs := []Event{DragEnd{Start: r.start, Pos: r.last}}
			if sw, ok := r.classifySwipe(r.start, r.last); ok {
				evs = append(evs, sw)
			}
			r.state = stateIdle
			return evs
		}
		return r.dragMove(r.point(c))
	}
	return nil
}

// frameMulti tracks the centroid while >=2 tips are down and classifies a
// two-finger swipe once everything lifts. A drag interrupted by a second
// finger is closed cleanly first.
func (r *Recognizer) frameMulti(tips []Contact) []Event {
	if len(tips) >= 2 {
		cen := r.centroid(tips)
		var evs []Event
		if r.state != stateMulti {
			if r.state == stateDrag {
				evs = append(evs, DragEnd{Start: r.start, Pos: r.last})
			}
			r.state = stateMulti
			r.multiStart = cen
		} else if r.multiDropped {
			// a finger lifted and re-landed: new stroke, new anchor
			r.multiStart = cen
		}
		r.multiDropped = false
		r.multiLast = cen
		return evs
	}
	if len(tips) == 0 {
		r.multiDropped = false
		var evs []Event
		if sw, ok := r.classifySwipe(r.multiStart, r.multiLast); ok {
			evs = append(evs, TwoFingerSwipe{Dir: sw.Dir, Start: sw.Start, End: sw.End, Cells: sw.Cells})
		}
		r.state = stateIdle
		return evs
	}
	// one finger still down: hold until it lifts, no tail drag
	r.multiDropped = true
	return nil
}

// dragMove emits the per-frame move plus a Wheel when the frame crossed
// cell boundaries -- the per-frame coalescing review M8 wants before
// anything is forwarded into a scraped region.
func (r *Recognizer) dragMove(p Point) []Event {
	evs := []Event{DragMove{Start: r.start, Pos: p, DX: p.PX - r.last.PX, DY: p.PY - r.last.PY}}
	if dc, dr := p.Col-r.last.Col, p.Row-r.last.Row; dc != 0 || dr != 0 {
		evs = append(evs, Wheel{Pos: p, DeltaCols: dc, DeltaRows: dr})
	}
	r.last = p
	return evs
}

func (r *Recognizer) beyondSlop(p Point) bool {
	sx := r.cfg.SlopCells * r.cells.CellW()
	sy := r.cfg.SlopCells * r.cells.CellH()
	return math.Abs(float64(p.PX-r.start.PX)) > sx || math.Abs(float64(p.PY-r.start.PY)) > sy
}

func (r *Recognizer) classifySwipe(start, end Point) (Swipe, bool) {
	dc, dr := end.Col-start.Col, end.Row-start.Row
	adc, adr := abs(dc), abs(dr)
	if adc >= adr {
		if adc < r.cfg.SwipeCells {
			return Swipe{}, false
		}
		dir := Right
		if dc < 0 {
			dir = Left
		}
		return Swipe{Dir: dir, Start: start, End: end, Cells: adc}, true
	}
	if adr < r.cfg.SwipeCells {
		return Swipe{}, false
	}
	dir := Down
	if dr < 0 {
		dir = Up
	}
	return Swipe{Dir: dir, Start: start, End: end, Cells: adr}, true
}

func (r *Recognizer) point(c Contact) Point {
	px, py := r.cal.PanelPx(c.X, c.Y)
	col, row := r.cells.Cell(px, py)
	return Point{PX: px, PY: py, Col: col, Row: row}
}

func (r *Recognizer) centroid(tips []Contact) Point {
	var sx, sy int
	for _, c := range tips {
		px, py := r.cal.PanelPx(c.X, c.Y)
		sx += px
		sy += py
	}
	px, py := sx/len(tips), sy/len(tips)
	col, row := r.cells.Cell(px, py)
	return Point{PX: px, PY: py, Col: col, Row: row}
}

func tipContacts(cs []Contact) []Contact {
	var tips []Contact
	for _, c := range cs {
		if c.Tip {
			tips = append(tips, c)
		}
	}
	return tips
}

func findContact(cs []Contact, id uint8) (Contact, bool) {
	for _, c := range cs {
		if c.ID == id {
			return c, true
		}
	}
	return Contact{}, false
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
