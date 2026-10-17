package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestBoundaryErrorsPreserveNativeAndProtocolCodes(t *testing.T) {
	for _, tt := range []struct {
		err  error
		kind string
		code int
	}{
		{errors.New("IOHIDDeviceSetReport failed: (0xE00002BC)"), "native_code_in_message", 0xE00002BC},
		{fmt.Errorf("reply: %w", hidppError(8)), "hidpp_device_error", 8},
	} {
		kind, code := classify(tt.err)
		if kind != tt.kind || code == nil || *code != tt.code {
			t.Fatalf("%s %v", kind, code)
		}
	}
	kind, _ := classify(context.DeadlineExceeded)
	if kind != "timeout" {
		t.Fatal(kind)
	}
}
func TestBoundaryRecorderRecordsNewErrorAndNewEpisode(t *testing.T) {
	var events []boundaryEvent
	r := newBoundaryRecorder(func(e boundaryEvent) { events = append(events, e) })
	for range 4 {
		r.Observe("open", errors.New("first"), "backoff")
	}
	r.Observe("open", errors.New("second"), "backoff")
	r.Observe("open", nil, "resume")
	r.Observe("open", errors.New("first"), "backoff")
	if len(events) != 6 || events[5].Error != "first" {
		t.Fatal(events)
	}
}
