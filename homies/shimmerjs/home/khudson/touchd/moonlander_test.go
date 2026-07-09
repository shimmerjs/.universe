package main

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// moonReport pads an event to the fixed 32-byte QMK raw-HID report, the way
// the firmware frames it on the wire.
func moonReport(b ...byte) []byte {
	r := make([]byte, moonReportLen)
	copy(r, b)
	return r
}

// The decoder handles the full protocol per the decode doc: [0x06,col,row]
// keydown, [0x07,col,row] keyup, [0x05,layer] layer change.
func TestParseMoonReport(t *testing.T) {
	for _, tt := range []struct {
		name string
		raw  []byte
		want KeyEvent
		ok   bool
	}{
		{
			name: "keydown col,row order",
			raw:  moonReport(moonKeyDown, 1, 2), // col=1, row=2
			want: KeyEvent{T: 42, Kind: "key", Col: 1, Row: 2, Pressed: true},
			ok:   true,
		},
		{
			name: "keyup",
			raw:  moonReport(moonKeyUp, 6, 11),
			want: KeyEvent{T: 42, Kind: "key", Col: 6, Row: 11, Pressed: false},
			ok:   true,
		},
		{
			name: "layer change",
			raw:  moonReport(moonLayer, 3),
			want: KeyEvent{T: 42, Kind: "layer", Layer: 3},
			ok:   true,
		},
		{
			name: "layer zero",
			raw:  moonReport(moonLayer, 0),
			want: KeyEvent{T: 42, Kind: "layer", Layer: 0},
			ok:   true,
		},
		{name: "unknown event id", raw: moonReport(0x01, 1, 2)},
		{name: "empty read", raw: nil},
		{name: "short keydown", raw: []byte{moonKeyDown, 1}},
		{name: "short layer", raw: []byte{moonLayer}},
		// the Edge's mouse report id collides with moonKeyUp (0x07); the
		// Moonlander reader has its own emit path, never parseReport, so a
		// mouse-shaped buffer here still decodes as the raw-HID keyup it is
		{
			name: "keyup shares the 0x07 id by design",
			raw:  moonReport(0x07, 0x01, 0xF3),
			want: KeyEvent{T: 42, Kind: "key", Col: 0x01, Row: 0xF3, Pressed: false},
			ok:   true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseMoonReport(42, tt.raw)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Fatalf("event = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// The pairing init write is hidapi-framed: leading 0x00 report id (the
// vendor channel is unnumbered) then a full 32-byte report starting 0x01.
func TestMoonInitReport(t *testing.T) {
	b := moonInitReport()
	if len(b) != moonReportLen+1 {
		t.Fatalf("len = %d, want %d", len(b), moonReportLen+1)
	}
	if b[0] != 0x00 || b[1] != moonPairInit {
		t.Fatalf("head = %X, want 00 01", b[:2])
	}
	for i, v := range b[2:] {
		if v != 0 {
			t.Fatalf("byte %d = %#x, want zero padding", i+2, v)
		}
	}
}

// Key events round-trip the keys socket as ndjson lines with the documented
// field names (khudson's proto.KeyEvent mirrors them).
func TestKeysSocketNDJSON(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "keys.sock")
	b, err := newBroadcaster(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForClient(t, b)

	down, _ := parseMoonReport(99, moonReport(moonKeyDown, 4, 7))
	layer, _ := parseMoonReport(100, moonReport(moonLayer, 2))
	b.publishJSON(down)
	b.publishJSON(layer)

	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(conn)

	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	want := `{"t":99,"kind":"key","col":4,"row":7,"pressed":true}`
	var got, wantEv KeyEvent
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("bad ndjson %q: %v", line, err)
	}
	if err := json.Unmarshal([]byte(want), &wantEv); err != nil {
		t.Fatal(err)
	}
	if got != wantEv {
		t.Fatalf("key line = %+v, want %+v", got, wantEv)
	}

	line, err = r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("bad ndjson %q: %v", line, err)
	}
	if got.Kind != "layer" || got.Layer != 2 {
		t.Fatalf("layer line = %+v, want layer 2", got)
	}
}
