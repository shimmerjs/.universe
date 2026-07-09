package main

import (
	"encoding/binary"
	"fmt"
)

// Frame is one parsed digitizer report, the unit broadcast on touch.sock.
// Wire format is ndjson, one frame per line; khudson mirrors this shape:
//
//	{"t": <unix ns>, "scan": n, "count": n, "contacts": [{"id": n, "tip": bool, "x": n, "y": n}]}
//
// t is host receive time in unix nanoseconds (-replay rebases recorded
// timestamps to now, preserving deltas). scan is the device scan time (LE16,
// wraps). count is the device-reported contact count. contacts carries the
// first count finger slots plus any later slot with its tip switch set; a tip
// false contact is a lift. x/y are raw digitizer units (x 0-16383, y 0-9599);
// mapping HID units -> panel px is the bus's job.
type Frame struct {
	T        int64     `json:"t"`
	Scan     uint16    `json:"scan"`
	Count    uint8     `json:"count"`
	Contacts []Contact `json:"contacts"`
}

type Contact struct {
	ID  uint8  `json:"id"`
	Tip bool   `json:"tip"`
	X   uint16 `json:"x"`
	Y   uint16 `json:"y"`
}

const (
	maxContacts = 10
	// report ID + 10 slots of [flags][X le16][Y le16] + scan LE16 + count
	touchReportLen = 1 + maxContacts*5 + 2 + 1
)

// parseTouchReport decodes input report 0x0D per the descriptor: 10 finger
// collections of [tip(1bit)+pad(3)+contactID(4bit)][X le16][Y le16] at offset
// 1+i*5, scan time LE16 at 51, contact count at 53. Returns ok=false for
// other report IDs or short reads.
func parseTouchReport(t int64, b []byte) (Frame, bool) {
	if len(b) < touchReportLen || b[0] != reportTouch {
		return Frame{}, false
	}
	f := Frame{
		T:        t,
		Scan:     binary.LittleEndian.Uint16(b[51:53]),
		Count:    b[53],
		Contacts: []Contact{},
	}
	n := min(int(f.Count), maxContacts)
	for i := range maxContacts {
		off := 1 + i*5
		flags := b[off]
		tip := flags&0x01 != 0
		// slots past count are stale unless the tip switch says otherwise
		if i >= n && !tip {
			continue
		}
		f.Contacts = append(f.Contacts, Contact{
			ID:  flags >> 4,
			Tip: tip,
			X:   binary.LittleEndian.Uint16(b[off+1 : off+3]),
			Y:   binary.LittleEndian.Uint16(b[off+3 : off+5]),
		})
	}
	return f, true
}

// parseMouseReport decodes the mouse-collection input report 0x07 emitted in
// mouse-emulation mode -- the proven single-touch path. Absolute
// coordinates share the digitizer's logical space (x 0-16383, y 0-9599).
// Single contact, id 0; tip mirrors button 1; no scan time in this report.
func parseMouseReport(t int64, b []byte) (Frame, bool) {
	if len(b) < 7 || b[0] != reportMouse {
		return Frame{}, false
	}
	tip := b[1]&0x01 != 0
	f := Frame{
		T: t,
		Contacts: []Contact{{
			ID:  0,
			Tip: tip,
			X:   binary.LittleEndian.Uint16(b[2:4]),
			Y:   binary.LittleEndian.Uint16(b[4:6]),
		}},
	}
	if tip {
		f.Count = 1
	}
	return f, true
}

// parseReport decodes any known input report into a Frame.
func parseReport(t int64, b []byte) (Frame, bool) {
	if len(b) == 0 {
		return Frame{}, false
	}
	switch b[0] {
	case reportTouch:
		return parseTouchReport(t, b)
	case reportMouse:
		return parseMouseReport(t, b)
	}
	return Frame{}, false
}

func printFrame(f Frame) {
	fmt.Printf("frame scan=%d contacts=%d:", f.Scan, f.Count)
	for _, c := range f.Contacts {
		fmt.Printf(" [id=%d tip=%v x=%d y=%d]", c.ID, c.Tip, c.X, c.Y)
	}
	fmt.Println()
}
