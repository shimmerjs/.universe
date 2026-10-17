package main

/*
#cgo LDFLAGS: -framework CoreGraphics
#include <CoreGraphics/CoreGraphics.h>
*/
import "C"

import "time"

// osIdle is aggregate session idle, not an application-delivery measurement.
func osIdle() time.Duration {
	s := C.CGEventSourceSecondsSinceLastEventType(
		C.kCGEventSourceStateCombinedSessionState, C.kCGAnyInputEventType)
	return time.Duration(float64(s) * float64(time.Second))
}

func osEventObservation(state, event int) (uint32, float64) {
	return uint32(C.CGEventSourceCounterForEventType(C.CGEventSourceStateID(state), C.CGEventType(event))),
		float64(C.CGEventSourceSecondsSinceLastEventType(C.CGEventSourceStateID(state), C.CGEventType(event)))
}
