package main

import (
	"bytes"
	"slices"
	"testing"
)

// longFrame pads a partial frame to the 20-byte long-report length.
func longFrame(b ...byte) []byte {
	f := make([]byte, hidppLongLen)
	copy(f, b)
	return f
}

// The 20-byte long-report request layout is the wire contract:
// [0x11][devIdx][featIdx][fnId<<4|swId][params, zero-padded].
func TestHidppRequestFraming(t *testing.T) {
	req := hidppRequest(0xFF, 0x03, 0x2, 0x0B, 0x01, 0x02)
	want := longFrame(0x11, 0xFF, 0x03, 0x2<<4|0x0B, 0x01, 0x02)
	if !bytes.Equal(req, want) {
		t.Fatalf("frame %X, want %X", req, want)
	}
	// every cycled swId must be a nonzero nibble with its MSB set, so replies
	// can never collide with unsolicited events (swId 0)
	r := &probeReader{}
	seen := map[byte]bool{}
	for i := 0; i < 16; i++ {
		sw := r.nextSwID()
		if sw&0x08 == 0 || sw&^0x0F != 0 {
			t.Fatalf("swId 0x%X outside 0x8-0xF", sw)
		}
		seen[sw] = true
	}
	if len(seen) != 8 {
		t.Fatalf("swId cycle covered %d values, want 8 (0x8-0xF)", len(seen))
	}
}

func TestClassifyReply(t *testing.T) {
	const devIdx, featIdx, fnID, swID = 0xFF, 0x00, byte(fnRootPing), byte(0x0B)
	echo := fnID<<4 | swID
	tests := []struct {
		name  string
		frame []byte
		kind  replyKind
		code  byte
	}{
		{"ping reply", longFrame(0x11, devIdx, featIdx, echo, 4, 5, 0x5A), replyOK, 0},
		{"short-report reply", []byte{0x10, devIdx, featIdx, echo, 4, 5, 0x5A}, replyOK, 0},
		{"foreign swId", longFrame(0x11, devIdx, featIdx, fnID<<4|0x01, 4, 5, 0x5A), replyForeign, 0},
		{"stale reply, our other swId", longFrame(0x11, devIdx, featIdx, fnID<<4|0x0C, 4, 5, 0x5A), replyForeign, 0},
		{"unsolicited event (swId 0)", longFrame(0x11, devIdx, featIdx, fnID<<4), replyForeign, 0},
		{"wrong devIdx", longFrame(0x11, 0x00, featIdx, echo, 4, 5, 0x5A), replyForeign, 0},
		{"wrong featIdx", longFrame(0x11, devIdx, 0x06, echo, 4, 5, 0x5A), replyForeign, 0},
		{"hid++2.0 error", longFrame(0x11, devIdx, hidppErrLong, featIdx, echo, 0x06), replyErr, 0x06},
		{"hid++1.0 error", []byte{0x10, devIdx, hidppErrShort, featIdx, echo, 0x01, 0x00}, replyErr, 0x01},
		{"error for a foreign request", longFrame(0x11, devIdx, hidppErrLong, featIdx, fnID<<4|0x01, 0x06), replyForeign, 0},
		{"mouse report", []byte{0x02, 0x00, 0x10, 0x00, 0x20, 0x00, 0x00}, replyForeign, 0},
		{"short garbage", []byte{0x11, devIdx}, replyForeign, 0},
	}
	for _, tt := range tests {
		kind, params, code := classifyReply(tt.frame, devIdx, featIdx, fnID, swID)
		if kind != tt.kind || code != tt.code {
			t.Fatalf("%s: kind=%v code=0x%02X, want kind=%v code=0x%02X", tt.name, kind, code, tt.kind, tt.code)
		}
		if kind == replyOK {
			major, minor, marker, ok := decodePing(params)
			if !ok || major != 4 || minor != 5 || marker != 0x5A {
				t.Fatalf("%s: ping decode %d.%d marker=0x%02X ok=%v", tt.name, major, minor, marker, ok)
			}
		}
	}
}

// getFeature reply: params[0] is the feature index, 0 meaning absent.
func TestDecodeGetFeature(t *testing.T) {
	index, flags, version, ok := decodeGetFeature([]byte{0x08, 0x40, 0x02, 0, 0, 0})
	if !ok || index != 0x08 || flags != 0x40 || version != 2 {
		t.Fatalf("got idx=0x%02X flags=0x%02X v%d ok=%v", index, flags, version, ok)
	}
	if index, _, _, ok := decodeGetFeature([]byte{0x00, 0x00, 0x00}); !ok || index != 0 {
		t.Fatalf("absent feature: idx=0x%02X ok=%v", index, ok)
	}
	if _, _, _, ok := decodeGetFeature([]byte{0x08}); ok {
		t.Fatal("short params decoded")
	}
}

// A canned IFeatureSet walk must decode into a table that satisfies the
// step-5 PRESENT/ABSENT expectations, and mutations must be caught.
func TestFeatureTableWalk(t *testing.T) {
	e, ok := decodeFeatureEntry(3, []byte{0x1B, 0x04, 0x40, 0x06})
	if !ok || e != (featureEntry{Index: 3, ID: 0x1B04, Flags: 0x40, Version: 6}) {
		t.Fatalf("entry decode: %+v ok=%v", e, ok)
	}
	if _, ok := decodeFeatureEntry(1, []byte{0x1B, 0x04}); ok {
		t.Fatal("short params decoded")
	}

	// canned getFeatureID reply params, one per index, covering every
	// expect-present id plus IFeatureSet itself
	walk := [][]byte{{0x00, 0x01, 0x00, 0x02}}
	for _, id := range logiExpectPresent {
		walk = append(walk, []byte{byte(id >> 8), byte(id), 0x00, 0x01})
	}
	features := map[uint16]featureEntry{}
	for i, params := range walk {
		e, ok := decodeFeatureEntry(byte(i+1), params)
		if !ok {
			t.Fatalf("walk index %d did not decode", i+1)
		}
		features[e.ID] = e
	}
	missing, unexpected := featureAssertions(features)
	if len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("full table: missing %04X unexpected %04X", missing, unexpected)
	}

	delete(features, 0x1004)
	features[0x2110] = featureEntry{Index: 0x20, ID: 0x2110}
	missing, unexpected = featureAssertions(features)
	if !slices.Equal(missing, []uint16{0x1004}) || !slices.Equal(unexpected, []uint16{0x2110}) {
		t.Fatalf("mutated table: missing %04X unexpected %04X", missing, unexpected)
	}
}

// 0x1004 getStatus reply: [soc%][level bits][charging state][ext power].
func TestDecodeBatteryStatus(t *testing.T) {
	st, ok := decodeBatteryStatus([]byte{0x55, 0x02, 0x01, 0x01, 0, 0})
	if !ok || st.SoC != 85 || st.Level != 0x02 || st.State != 1 || st.ExtPower != 1 {
		t.Fatalf("decode: %+v ok=%v", st, ok)
	}
	if got := st.chargingString(); got != "charging" {
		t.Fatalf("charging string %q", got)
	}
	st.State = 0
	if got := st.chargingString(); got != "discharging" {
		t.Fatalf("discharging string %q", got)
	}
	if _, ok := decodeBatteryStatus([]byte{0x55, 0x02}); ok {
		t.Fatal("short params decoded")
	}
}

// Report IDs come from descriptor 0x85 items; long items are skipped whole
// so payload bytes cannot masquerade as items.
func TestDescriptorReportIDs(t *testing.T) {
	desc := []byte{
		0x05, 0x01, // Usage Page
		0x85, 0x02, // Report ID 0x02
		0x75, 0x08, // Report Size
		0xFE, 0x02, 0x00, 0x85, 0x10, // long item; payload holds a fake 0x85 0x10
		0x85, 0x11, // Report ID 0x11
		0x85, 0x02, // duplicate
	}
	if ids := descriptorReportIDs(desc); !slices.Equal(ids, []byte{0x02, 0x11}) {
		t.Fatalf("ids %X", ids)
	}
}
