// Moonlander live-key reader: the second HID source next to the Edge
// digitizer. Speaks the decoded raw-HID Oryx protocol (QMK raw HID vendor
// channel, usage page 0xFF60 usage 0x61): the host writes a 0x01
// PAIRING_INIT report, the firmware then streams [0x06,col,row] keydown /
// [0x07,col,row] keyup / [0x05,layer] events. The channel is opened SHARED
// (hid_darwin_set_open_exclusive(0)) so Keymapp can own it later without an
// unplug cycle; opening it never touches the keyboard collections (no
// seize). Events broadcast as ndjson KeyEvent lines on keys.sock, mirroring
// the touch.sock fanout.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/sstallion/go-hid"
)

const (
	moonVID = 0x3297
	moonPID = 0x1969

	// raw-HID vendor channel (interface 1 on the Moonlander)
	usagePageVendor = 0xFF60
	usageRawHID     = 0x61

	// host -> firmware: start streaming key/layer events
	moonPairInit = 0x01
	// firmware -> host event ids (first byte of each input report)
	moonLayer   = 0x05
	moonKeyDown = 0x06
	moonKeyUp   = 0x07

	// QMK raw HID reports are a fixed RAW_EPSIZE = 32 bytes
	moonReportLen = 32
)

// KeyEvent is one line on keys.sock; khudson mirrors this shape
// (proto.KeyEvent):
//
//	{"t": <unix ns>, "kind": "key", "row": n, "col": n, "pressed": bool}
//	{"t": <unix ns>, "kind": "layer", "layer": n}
//
// row/col are QMK matrix coordinates (rows 0-5 left half, 6-11 right;
// mapping matrix -> physical key is the consumer's job, the render side
// owns the geometry). layer is the 0-based active layer.
type KeyEvent struct {
	T       int64  `json:"t"`
	Kind    string `json:"kind"`
	Row     int    `json:"row,omitempty"`
	Col     int    `json:"col,omitempty"`
	Pressed bool   `json:"pressed,omitempty"`
	Layer   int    `json:"layer,omitempty"`
}

// parseMoonReport decodes one raw-HID input report into a KeyEvent. The
// protocol streams [0x06,col,row] keydown, [0x07,col,row] keyup,
// [0x05,layer]; anything else (or a short read) is not an event.
func parseMoonReport(t int64, b []byte) (KeyEvent, bool) {
	if len(b) == 0 {
		return KeyEvent{}, false
	}
	switch b[0] {
	case moonKeyDown, moonKeyUp:
		if len(b) < 3 {
			return KeyEvent{}, false
		}
		return KeyEvent{
			T:       t,
			Kind:    "key",
			Col:     int(b[1]),
			Row:     int(b[2]),
			Pressed: b[0] == moonKeyDown,
		}, true
	case moonLayer:
		if len(b) < 2 {
			return KeyEvent{}, false
		}
		return KeyEvent{T: t, Kind: "layer", Layer: int(b[1])}, true
	}
	return KeyEvent{}, false
}

// moonInitReport builds the PAIRING_INIT write: hidapi's leading report id
// (0x00 -- the vendor channel is unnumbered) followed by a full 32-byte
// report whose first byte is the 0x01 init command.
func moonInitReport() []byte {
	b := make([]byte, moonReportLen+1)
	b[0] = 0x00
	b[1] = moonPairInit
	return b
}

// openMoonlander finds and opens the vendor channel SHARED and writes the
// pairing init; without the init the firmware never streams, so a failed
// write closes the device and reports an error (the caller retries).
func openMoonlander() (*hid.Device, error) {
	path, err := findCollection(moonVID, moonPID, usagePageVendor, usageRawHID)
	if err != nil {
		return nil, err
	}
	dev, err := openPath(path, false)
	if err != nil {
		return nil, fmt.Errorf("open (Input Monitoring granted?): %w", err)
	}
	if _, err := dev.Write(moonInitReport()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("pairing init: %w", err)
	}
	return dev, nil
}

// moonLoop opens the Moonlander and pumps decoded events into emit,
// reopening with backoff on device loss. Absence is QUIET: the board being
// unplugged is a normal state, so only the first failure of an absence
// episode and each (re)connect are logged -- never a retry-spam crash loop.
func moonLoop(ctx context.Context, emit func(KeyEvent)) {
	backoff := reconnectMin
	loggedAbsent := false
	for {
		if ctx.Err() != nil {
			return
		}
		dev, err := openMoonlander()
		if err != nil {
			if !loggedAbsent {
				fmt.Fprintf(os.Stderr, "moonlander absent: %v (quiet retry)\n", err)
				loggedAbsent = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, reconnectMax)
			continue
		}
		backoff = reconnectMin
		loggedAbsent = false
		fmt.Println("moonlander open (shared), pairing init sent")

		err = readLoop(ctx, dev, func(t int64, raw []byte) {
			if ev, ok := parseMoonReport(t, raw); ok {
				emit(ev)
			}
		})
		dev.Close()
		if ctx.Err() != nil {
			return
		}
		// device loss with keys still down would latch held highlights on
		// every dock: synthesize the clear here -- the bus's clear-on-
		// disconnect only fires when keys.sock itself dies
		emit(KeyEvent{T: time.Now().UnixNano(), Kind: "clear"})
		fmt.Fprintf(os.Stderr, "moonlander gone, reopening: %v\n", err)
	}
}
