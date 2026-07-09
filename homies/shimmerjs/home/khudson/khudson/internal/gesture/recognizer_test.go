package gesture

import (
	"testing"
	"time"
)

// unit calibration: digitizer units == panel px, cells 10x40 px
func testRecognizer() *Recognizer {
	cal := Calibration{MaxX: 2559, MaxY: 719, PanelW: 2560, PanelH: 720}
	cells := CellMetrics{Cols: 256, Rows: 18, PanelW: 2560, PanelH: 720}
	return New(cal, cells, Config{})
}

func frame(t time.Time, cs ...Contact) Frame {
	return Frame{Contacts: cs, Time: t}
}

func down(id uint8, x, y uint16) Contact { return Contact{ID: id, Tip: true, X: x, Y: y} }

func TestTap(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()

	if evs := r.Frame(frame(t0, down(1, 100, 100))); len(evs) != 0 {
		t.Fatalf("down: got %v, want none", evs)
	}
	evs := r.Frame(frame(t0.Add(100 * time.Millisecond)))
	if len(evs) != 1 {
		t.Fatalf("up: got %v, want one Tap", evs)
	}
	tap, ok := evs[0].(Tap)
	if !ok {
		t.Fatalf("got %T, want Tap", evs[0])
	}
	if tap.Pos.Col != 10 || tap.Pos.Row != 2 {
		t.Fatalf("tap at %d,%d, want 10,2", tap.Pos.Col, tap.Pos.Row)
	}
}

func TestLongPressViaTick(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()
	r.Frame(frame(t0, down(1, 100, 100)))

	dl, armed := r.Deadline()
	if !armed || dl != t0.Add(450*time.Millisecond) {
		t.Fatalf("deadline %v armed=%v, want t0+450ms", dl, armed)
	}
	if evs := r.Tick(t0.Add(200 * time.Millisecond)); len(evs) != 0 {
		t.Fatalf("early tick fired %v", evs)
	}
	evs := r.Tick(t0.Add(500 * time.Millisecond))
	if len(evs) != 1 {
		t.Fatalf("got %v, want one LongPress", evs)
	}
	if _, ok := evs[0].(LongPress); !ok {
		t.Fatalf("got %T, want LongPress", evs[0])
	}
	// release after long-press is silent
	if evs := r.Frame(frame(t0.Add(600 * time.Millisecond))); len(evs) != 0 {
		t.Fatalf("release after long-press fired %v", evs)
	}
}

func TestLongPressOnLateRelease(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()
	r.Frame(frame(t0, down(1, 100, 100)))
	// no Tick ran; release past the threshold still means long-press
	evs := r.Frame(frame(t0.Add(600 * time.Millisecond)))
	if len(evs) != 1 {
		t.Fatalf("got %v, want one LongPress", evs)
	}
	if _, ok := evs[0].(LongPress); !ok {
		t.Fatalf("got %T, want LongPress", evs[0])
	}
}

func TestDragSwipeAndWheel(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()
	r.Frame(frame(t0, down(1, 100, 100)))

	// 200px right: past slop (15px), 20 cols crossed in one frame
	evs := r.Frame(frame(t0.Add(50*time.Millisecond), down(1, 300, 100)))
	if len(evs) != 3 {
		t.Fatalf("got %v, want DragStart+DragMove+Wheel", evs)
	}
	if _, ok := evs[0].(DragStart); !ok {
		t.Fatalf("got %T, want DragStart", evs[0])
	}
	mv, ok := evs[1].(DragMove)
	if !ok || mv.DX != 200 {
		t.Fatalf("got %#v, want DragMove DX=200", evs[1])
	}
	wh, ok := evs[2].(Wheel)
	if !ok || wh.DeltaCols != 20 || wh.DeltaRows != 0 {
		t.Fatalf("got %#v, want Wheel DeltaCols=20", evs[2])
	}

	// release: DragEnd then Swipe right (20 cells >= 6)
	evs = r.Frame(frame(t0.Add(100 * time.Millisecond)))
	if len(evs) != 2 {
		t.Fatalf("got %v, want DragEnd+Swipe", evs)
	}
	sw, ok := evs[1].(Swipe)
	if !ok || sw.Dir != Right || sw.Cells != 20 {
		t.Fatalf("got %#v, want Swipe right 20 cells", evs[1])
	}
}

func TestShortDragNoSwipe(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()
	r.Frame(frame(t0, down(1, 100, 100)))
	r.Frame(frame(t0.Add(50*time.Millisecond), down(1, 140, 100))) // 4 cols
	evs := r.Frame(frame(t0.Add(100 * time.Millisecond)))
	if len(evs) != 1 {
		t.Fatalf("got %v, want DragEnd only", evs)
	}
	if _, ok := evs[0].(DragEnd); !ok {
		t.Fatalf("got %T, want DragEnd", evs[0])
	}
}

func TestWheelCoalescedPerFrame(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()
	r.Frame(frame(t0, down(1, 100, 100)))
	// 100px down in one frame: rows 2 -> 5, one Wheel with DeltaRows=3
	evs := r.Frame(frame(t0.Add(50*time.Millisecond), down(1, 100, 200)))
	var wheels []Wheel
	for _, ev := range evs {
		if w, ok := ev.(Wheel); ok {
			wheels = append(wheels, w)
		}
	}
	if len(wheels) != 1 || wheels[0].DeltaRows != 3 {
		t.Fatalf("got %#v, want one Wheel DeltaRows=3", wheels)
	}
}

func TestTwoFingerSwipe(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()

	r.Frame(frame(t0, down(1, 1000, 500), down(2, 1100, 500)))
	r.Frame(frame(t0.Add(50*time.Millisecond), down(1, 1000, 200), down(2, 1100, 200)))
	evs := r.Frame(frame(t0.Add(100 * time.Millisecond)))
	if len(evs) != 1 {
		t.Fatalf("got %v, want one TwoFingerSwipe", evs)
	}
	sw, ok := evs[0].(TwoFingerSwipe)
	if !ok || sw.Dir != Up {
		t.Fatalf("got %#v, want TwoFingerSwipe up", evs[0])
	}
}

// A 2->1->2 tip sequence is two strokes: the re-landed pair re-anchors at
// its own centroid, so a chained right-then-left pair of swipes classifies
// the second stroke from its own origin instead of swallowing it as a
// net-zero of the first.
func TestTwoFingerRelandReanchors(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()

	// stroke 1: swipe right 60 cells
	r.Frame(frame(t0, down(1, 1000, 500), down(2, 1100, 500)))
	r.Frame(frame(t0.Add(50*time.Millisecond), down(1, 1600, 500), down(2, 1700, 500)))
	// one finger lifts, the other stays down
	r.Frame(frame(t0.Add(100*time.Millisecond), down(1, 1600, 500)))
	// stroke 2: the pair re-lands and swipes left back to the origin
	r.Frame(frame(t0.Add(150*time.Millisecond), down(1, 1600, 500), down(2, 1700, 500)))
	r.Frame(frame(t0.Add(200*time.Millisecond), down(1, 1000, 500), down(2, 1100, 500)))
	evs := r.Frame(frame(t0.Add(250 * time.Millisecond)))
	if len(evs) != 1 {
		t.Fatalf("got %v, want one TwoFingerSwipe", evs)
	}
	sw, ok := evs[0].(TwoFingerSwipe)
	if !ok || sw.Dir != Left {
		t.Fatalf("got %#v, want TwoFingerSwipe left (not a swallowed net-zero)", evs[0])
	}
}

func TestSecondFingerClosesDrag(t *testing.T) {
	r := testRecognizer()
	t0 := time.Now()
	r.Frame(frame(t0, down(1, 100, 100)))
	r.Frame(frame(t0.Add(30*time.Millisecond), down(1, 300, 100)))
	// second finger lands mid-drag: drag closes, multi takes over
	evs := r.Frame(frame(t0.Add(60*time.Millisecond), down(1, 300, 100), down(2, 400, 100)))
	if len(evs) != 1 {
		t.Fatalf("got %v, want DragEnd", evs)
	}
	if _, ok := evs[0].(DragEnd); !ok {
		t.Fatalf("got %T, want DragEnd", evs[0])
	}
}

func TestPanelPxMapping(t *testing.T) {
	c := DefaultCalibration
	px, py := c.PanelPx(0, 0)
	if px != 0 || py != 0 {
		t.Fatalf("origin mapped to %d,%d", px, py)
	}
	px, py = c.PanelPx(16383, 9599)
	if px != 2559 || py != 719 {
		t.Fatalf("max mapped to %d,%d, want 2559,719", px, py)
	}
}

func TestCellMapping(t *testing.T) {
	m := CellMetrics{Cols: 256, Rows: 18, PanelW: 2560, PanelH: 720}
	col, row := m.Cell(2559, 719)
	if col != 255 || row != 17 {
		t.Fatalf("bottom-right mapped to %d,%d, want 255,17", col, row)
	}
}
