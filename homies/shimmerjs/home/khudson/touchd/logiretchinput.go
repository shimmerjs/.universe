// logiretch input activity: the standard mouse reports (report id 0x02)
// interleave with HID++ frames on the shared vendor handle, so button,
// wheel, and motion activity is observable with NOTHING diverted and no
// device behavior change -- the logiretch viewer's live-activation feed.
//
// Layout pinned from the device's own report descriptor (IORegistry,
// 2026-07-23, MX Master 4 pid 0xB042 over direct BT; see the test's
// descriptor hex): byte 1 carries buttons 1-7 as bits 0-6 (bit 7 +
// byte 2 are const padding), bytes 3-5 pack X,Y as two little-endian
// int12s, byte 6 is the wheel int8, byte 7 the AC Pan (thumbwheel) int8.
//
// Cost posture (constant-cost invariant): parsing is O(1) per frame;
// button EDGES publish immediately (human-rate); wheel/pan/motion
// accumulate and flush on a one-shot 100ms timer armed only while
// activity is pending -- an idle mouse runs zero timers and publishes
// nothing.
package main

import (
	"sync"
	"time"
)

// mouseReportID is the standard pointer report on the shared handle.
const mouseReportID = 0x02

// inputFlushEvery is the coalescing window for wheel/pan/motion lines.
const inputFlushEvery = 100 * time.Millisecond

// logiButtonNames maps report bits 0-6. Bits 5-6 exist in the descriptor
// beyond the standard five; their physical identity (gesture / force
// panel candidates) is pinned by logiretch-capture observation.
var logiButtonNames = []string{"left", "right", "middle", "back", "forward", "btn6", "btn7"}

// LogiInput is one activity line on logiretch.sock. Wire shape PINNED:
//
//	{"t": ns, "kind": "input", "held": [names], "pressed": [names],
//	 "released": [names], "wheel": n, "pan": n, "dx": n, "dy": n}
//
// Button edge lines carry held/pressed/released and no deltas; coalesced
// motion lines carry the deltas accumulated over the flush window and no
// button fields. Never retained.
type LogiInput struct {
	T        int64    `json:"t"`
	Kind     string   `json:"kind"`
	Held     []string `json:"held,omitempty"`
	Pressed  []string `json:"pressed,omitempty"`
	Released []string `json:"released,omitempty"`
	Wheel    int      `json:"wheel,omitempty"`
	Pan      int      `json:"pan,omitempty"`
	DX       int      `json:"dx,omitempty"`
	DY       int      `json:"dy,omitempty"`
}

// inputTracker coalesces 0x02 frames into activity lines. frame() is
// called from the conn reader under its mu and must never block: every
// publish path is the broadcaster's non-blocking offer.
type inputTracker struct {
	publish func(v any)
	now     func() time.Time
	// after is the one-shot flush timer seam (tests fire it by hand).
	after func(d time.Duration, f func()) *time.Timer

	mu     sync.Mutex
	held   byte
	wheel  int
	pan    int
	dx, dy int
	armed  bool
}

func newInputTracker(env Env) *inputTracker {
	return &inputTracker{publish: env.Publish, now: time.Now, after: time.AfterFunc}
}

// frame consumes one raw report; non-0x02 frames are ignored.
func (t *inputTracker) frame(frame []byte) {
	if len(frame) < 8 || frame[0] != mouseReportID {
		return
	}
	buttons := frame[1] & 0x7F
	dx, dy := decodeXY12(frame[3], frame[4], frame[5])
	wheel := int(int8(frame[6]))
	pan := int(int8(frame[7]))

	var edge *LogiInput
	t.mu.Lock()
	if buttons != t.held {
		edge = &LogiInput{
			T: t.now().UnixNano(), Kind: "input",
			Held:     buttonNames(buttons),
			Pressed:  buttonNames(buttons &^ t.held),
			Released: buttonNames(t.held &^ buttons),
		}
		t.held = buttons
	}
	t.wheel += wheel
	t.pan += pan
	t.dx += dx
	t.dy += dy
	if !t.armed && (t.wheel != 0 || t.pan != 0 || t.dx != 0 || t.dy != 0) {
		t.armed = true
		t.after(inputFlushEvery, t.flush)
	}
	t.mu.Unlock()
	if edge != nil {
		t.publish(*edge)
	}
}

// flush publishes the accumulated deltas and disarms; a new frame re-arms.
func (t *inputTracker) flush() {
	t.mu.Lock()
	line := LogiInput{T: t.now().UnixNano(), Kind: "input",
		Wheel: t.wheel, Pan: t.pan, DX: t.dx, DY: t.dy}
	t.wheel, t.pan, t.dx, t.dy = 0, 0, 0, 0
	t.armed = false
	t.mu.Unlock()
	if line.Wheel != 0 || line.Pan != 0 || line.DX != 0 || line.DY != 0 {
		t.publish(line)
	}
}

func buttonNames(bits byte) []string {
	var out []string
	for i, name := range logiButtonNames {
		if bits&(1<<i) != 0 {
			out = append(out, name)
		}
	}
	return out
}

// decodeXY12 unpacks the descriptor's two little-endian int12s from
// bytes 3-5: X = b3 | (b4 & 0x0F)<<8, Y = b4>>4 | b5<<4, sign-extended.
func decodeXY12(b3, b4, b5 byte) (int, int) {
	x := int(b3) | int(b4&0x0F)<<8
	y := int(b4>>4) | int(b5)<<4
	if x >= 2048 {
		x -= 4096
	}
	if y >= 2048 {
		y -= 4096
	}
	return x, y
}
