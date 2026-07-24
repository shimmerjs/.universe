package main

import (
	"encoding/hex"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// The MX Master 4's pointer report descriptor, read from the IORegistry
// (2026-07-23, pid 0xB042, direct BT). The decoder's offsets are pinned
// against it: 7 button bits + 9 pad bits, two int12s (X,Y), wheel int8,
// AC Pan int8, all inside report id 0x02; report id 0x11 is the HID++
// vendor node.
const mx4Descriptor = "05010902a10185020901a10095077501150025010509190129078102950981030501a49502750c1600f826ff073600f846ff07550d6513093009318106b4950175081580257f09388106050c0a38028106c0c00643ff0a0202a101851195137508150026ff000902810009029100c0"

// TestDescriptorPins re-derives the load-bearing layout facts from the
// descriptor bytes so a firmware update that reshapes the report fails
// here, not silently in the decoder.
func TestDescriptorPins(t *testing.T) {
	raw, err := hex.DecodeString(mx4Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	// report id 0x02 opens the pointer collection; 0x11 the vendor node
	if !strings.Contains(mx4Descriptor, "8502") || !strings.Contains(mx4Descriptor, "8511") {
		t.Fatal("report ids moved")
	}
	// button field: count 7 size 1 (9507 7501), padding count 9 (9509)
	for _, pin := range []string{"95077501", "9509", "750c", "1600f8", "26ff07", "1580257f", "0938", "0a3802"} {
		if !strings.Contains(mx4Descriptor, pin) {
			t.Errorf("descriptor pin %q missing", pin)
		}
	}
	if len(raw) != 111 {
		t.Errorf("descriptor length = %d", len(raw))
	}
}

func TestDecodeXY12(t *testing.T) {
	cases := []struct {
		b3, b4, b5 byte
		x, y       int
	}{
		{0x00, 0x00, 0x00, 0, 0},
		{0x01, 0x00, 0x00, 1, 0},
		{0xFF, 0x0F, 0x00, -1, 0},   // X = 0xFFF
		{0x00, 0x10, 0x00, 0, 1},    // Y low nibble of b4 high
		{0x00, 0xF0, 0xFF, 0, -1},   // Y = 0xFFF
		{0xFF, 0x07, 0x00, 2047, 0}, // X max
		{0x00, 0x08, 0x00, -2048, 0},
		{0x00, 0x00, 0x80, 0, -2048},
		{0x00, 0xF0, 0x7F, 0, 2047},
	}
	for _, c := range cases {
		x, y := decodeXY12(c.b3, c.b4, c.b5)
		if x != c.x || y != c.y {
			t.Errorf("decodeXY12(%02x %02x %02x) = %d,%d want %d,%d", c.b3, c.b4, c.b5, x, y, c.x, c.y)
		}
	}
}

// trackerHarness wires an inputTracker with hand-fired timers and a
// captured publish stream.
func trackerHarness() (*inputTracker, *[]LogiInput, *[]func()) {
	var mu sync.Mutex
	lines := &[]LogiInput{}
	fires := &[]func(){}
	t := &inputTracker{
		publish: func(v any) {
			mu.Lock()
			defer mu.Unlock()
			*lines = append(*lines, v.(LogiInput))
		},
		now: func() time.Time { return time.Unix(0, 42) },
		after: func(_ time.Duration, f func()) *time.Timer {
			*fires = append(*fires, f)
			return nil
		},
	}
	return t, lines, fires
}

func mouseFrame(buttons byte, dx, dy int, wheel, pan int8) []byte {
	x := uint16(dx) & 0x0FFF
	y := uint16(dy) & 0x0FFF
	return []byte{0x02, buttons, 0x00,
		byte(x), byte(x>>8) | byte(y<<4), byte(y >> 4),
		byte(wheel), byte(pan)}
}

func TestButtonEdgesPublishImmediately(t *testing.T) {
	tr, lines, fires := trackerHarness()
	tr.frame(mouseFrame(0b00000001, 0, 0, 0, 0)) // left down
	tr.frame(mouseFrame(0b00000101, 0, 0, 0, 0)) // middle joins
	tr.frame(mouseFrame(0b00000100, 0, 0, 0, 0)) // left up
	tr.frame(mouseFrame(0b00000000, 0, 0, 0, 0)) // middle up
	if len(*fires) != 0 {
		t.Errorf("button-only frames must not arm the flush timer")
	}
	if len(*lines) != 4 {
		t.Fatalf("lines = %+v", *lines)
	}
	if !reflect.DeepEqual((*lines)[0].Pressed, []string{"left"}) {
		t.Errorf("edge 0 = %+v", (*lines)[0])
	}
	l := (*lines)[1]
	if !reflect.DeepEqual(l.Held, []string{"left", "middle"}) || !reflect.DeepEqual(l.Pressed, []string{"middle"}) {
		t.Errorf("edge 1 = %+v", l)
	}
	l = (*lines)[2]
	if !reflect.DeepEqual(l.Released, []string{"left"}) || !reflect.DeepEqual(l.Held, []string{"middle"}) {
		t.Errorf("edge 2 = %+v", l)
	}
	// an unchanged buttons byte publishes nothing
	tr.frame(mouseFrame(0, 0, 0, 0, 0))
	if len(*lines) != 4 {
		t.Errorf("no-change frame published: %+v", (*lines)[4:])
	}
}

func TestMotionCoalesces(t *testing.T) {
	tr, lines, fires := trackerHarness()
	tr.frame(mouseFrame(0, 3, -2, 1, 0))
	tr.frame(mouseFrame(0, 5, 1, 0, -2))
	tr.frame(mouseFrame(0, -1, 0, 1, 0))
	if len(*lines) != 0 {
		t.Fatalf("motion must coalesce, got %+v", *lines)
	}
	if len(*fires) != 1 {
		t.Fatalf("exactly one flush timer must arm, got %d", len(*fires))
	}
	(*fires)[0]()
	if len(*lines) != 1 {
		t.Fatalf("flush lines = %+v", *lines)
	}
	l := (*lines)[0]
	if l.DX != 7 || l.DY != -1 || l.Wheel != 2 || l.Pan != -2 {
		t.Errorf("coalesced = %+v", l)
	}
	// flushed and idle: no timer armed until new motion
	tr.frame(mouseFrame(0, 0, 0, 0, 0))
	if len(*fires) != 1 {
		t.Errorf("idle frame re-armed the timer")
	}
	tr.frame(mouseFrame(0, 1, 0, 0, 0))
	if len(*fires) != 2 {
		t.Errorf("new motion must re-arm")
	}
}

func TestNonMouseFramesIgnored(t *testing.T) {
	tr, lines, fires := trackerHarness()
	tr.frame([]byte{0x11, 0xFF, 0x00, 0x00, 0x05}) // HID++ frame
	tr.frame([]byte{0x02, 0x01})                   // truncated
	if len(*lines) != 0 || len(*fires) != 0 {
		t.Errorf("non-mouse frames leaked: %+v %d", *lines, len(*fires))
	}
}

// The button byte's bit 7 is descriptor padding and must never register
// as a button.
func TestPaddingBitMasked(t *testing.T) {
	tr, lines, _ := trackerHarness()
	tr.frame(mouseFrame(0x80, 0, 0, 0, 0))
	if len(*lines) != 0 {
		t.Errorf("padding bit published an edge: %+v", *lines)
	}
}
