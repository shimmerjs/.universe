package gesture

import (
	"testing"
	"time"
)

func TestCancelDiscardsIncompleteGesture(t *testing.T) {
	r := New(DefaultCalibration, CellMetrics{Cols: 100, Rows: 20, PanelW: DefaultCalibration.PanelW, PanelH: DefaultCalibration.PanelH}, Config{})
	now := time.Now()
	r.Frame(Frame{Time: now, Contacts: []Contact{{ID: 1, Tip: true, X: 100, Y: 100}}})
	if _, armed := r.Deadline(); !armed {
		t.Fatal("contact did not arm long press")
	}
	r.Cancel()
	if _, armed := r.Deadline(); armed {
		t.Fatal("canceled contact retained deadline")
	}
	if events := r.Tick(now.Add(time.Hour)); len(events) != 0 {
		t.Fatal("canceled contact generated long press")
	}
	if events := r.Frame(Frame{Time: now.Add(time.Second)}); len(events) != 0 {
		t.Fatal("disconnect generated release action")
	}
	if events := r.Frame(Frame{Time: now.Add(2 * time.Second), Contacts: []Contact{{ID: 1, Tip: true, X: 100, Y: 100}}}); len(events) != 1 {
		t.Fatal("new contact did not restart recognizer")
	}
}
