package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"

import "time"

// osIdle is how long the macOS event system thinks it has been since ANY
// user input reached it -- the OTHER half of the wedge signal. magicbusd
// counts HID reports below the pipeline; this asks the pipeline what it
// believes. The two disagreeing is the whole diagnosis.
//
// CombinedSessionState rather than HIDSystemState: the question is what the
// session's event stream has seen, which is what goes dead in a wedge,
// not what the HID layer alone accepted.
func osIdle() time.Duration {
	s := C.CGEventSourceSecondsSinceLastEventType(
		C.kCGEventSourceStateCombinedSessionState, C.kCGAnyInputEventType)
	return time.Duration(float64(s) * float64(time.Second))
}
