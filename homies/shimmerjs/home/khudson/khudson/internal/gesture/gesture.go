// Package gesture turns digitizer contact frames into HUD gestures: tap,
// long-press, drag, swipe, per-frame-coalesced wheel, and (multitouch
// permitting) two-finger swipes. Pure state machine: no goroutines, no
// clock of its own -- the caller feeds Frames and Ticks.
package gesture

import (
	"math"
	"time"
)

// Contact is one digitizer slot in a frame; X/Y are raw digitizer units.
type Contact struct {
	ID  uint8
	Tip bool
	X   uint16
	Y   uint16
}

// Frame is one digitizer report with wall time attached by touchd.
type Frame struct {
	Contacts []Contact
	Time     time.Time
}

// Calibration maps digitizer units to panel pixels: HID units -> panel px,
// never CGDisplay bounds -- digitizer units never enter CG space under
// HID-direct.
type Calibration struct {
	MaxX, MaxY     uint16 // digitizer logical maxima
	PanelW, PanelH int    // panel pixels
}

// DefaultCalibration uses the Edge mouse-collection maxima from the ioreg
// descriptor dump; the digitizer collection's maxima may differ.
var DefaultCalibration = Calibration{MaxX: 16383, MaxY: 9599, PanelW: 2560, PanelH: 720}

// PanelPx scales one contact position to panel pixels, clamped.
func (c Calibration) PanelPx(x, y uint16) (px, py int) {
	px = int(math.Round(float64(x) * float64(c.PanelW-1) / float64(c.MaxX)))
	py = int(math.Round(float64(y) * float64(c.PanelH-1) / float64(c.MaxY)))
	return clamp(px, 0, c.PanelW-1), clamp(py, 0, c.PanelH-1)
}

// CellMetrics maps panel pixels to dock cells; the dock reports Cols/Rows
// to the bus on connect and resize.
type CellMetrics struct {
	Cols, Rows     int
	PanelW, PanelH int
}

// CellW is the width of one cell in panel px.
func (m CellMetrics) CellW() float64 { return float64(m.PanelW) / float64(max(m.Cols, 1)) }

// CellH is the height of one cell in panel px.
func (m CellMetrics) CellH() float64 { return float64(m.PanelH) / float64(max(m.Rows, 1)) }

// Cell maps panel px to a dock cell, clamped to the grid.
func (m CellMetrics) Cell(px, py int) (col, row int) {
	col = clamp(int(float64(px)/m.CellW()), 0, max(m.Cols-1, 0))
	row = clamp(int(float64(py)/m.CellH()), 0, max(m.Rows-1, 0))
	return col, row
}

// Point is one position in both coordinate systems.
type Point struct {
	PX, PY   int // panel px
	Col, Row int // dock cells
}

// Direction of a swipe.
type Direction string

// Swipe directions.
const (
	Left  Direction = "left"
	Right Direction = "right"
	Up    Direction = "up"
	Down  Direction = "down"
)

// Event is a recognized gesture.
type Event interface{ event() }

// Press: primary contact down -- the immediate touch acknowledgment, fired
// before classification. Every touch opens with exactly one Press; the
// eventual Tap/LongPress/DragStart is the press's resolution (docks restyle
// the pressed element between the two).
type Press struct{ Pos Point }

// Tap: down-up under slop, under the long-press hold.
type Tap struct{ Pos Point }

// LongPress: held past the threshold under slop.
type LongPress struct{ Pos Point }

// DragStart opens a continuous drag once motion exceeds slop.
type DragStart struct{ Start Point }

// DragMove is emitted once per frame during a drag; DX/DY are px deltas
// since the previous move.
type DragMove struct {
	Start, Pos Point
	DX, DY     int
}

// DragEnd closes a drag; a qualifying Swipe follows it in the same batch.
type DragEnd struct{ Start, Pos Point }

// Swipe is classified at release: dominant axis, travel >= SwipeCells.
type Swipe struct {
	Dir        Direction
	Start, End Point
	Cells      int
}

// Wheel is cell-boundary crossings coalesced per frame during a drag; a
// fast flick crossing three rows in one frame is one event with DeltaRows
// 3.
type Wheel struct {
	Pos                  Point
	DeltaCols, DeltaRows int
}

// TwoFingerSwipe is the tray gesture; only ever emitted when frames carry
// two or more tip contacts, so single-touch input never fires it.
type TwoFingerSwipe struct {
	Dir        Direction
	Start, End Point
	Cells      int
}

func (Press) event()          {}
func (Tap) event()            {}
func (LongPress) event()      {}
func (DragStart) event()      {}
func (DragMove) event()       {}
func (DragEnd) event()        {}
func (Swipe) event()          {}
func (Wheel) event()          {}
func (TwoFingerSwipe) event() {}

func clamp(v, lo, hi int) int {
	return min(max(v, lo), hi)
}
