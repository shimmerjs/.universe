package main

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/sstallion/go-hid"
)

// shortHidppTimeouts shrinks the conn timeouts so tests never wait seconds on
// a missing reply or a slow reader stop.
func shortHidppTimeouts(t *testing.T) {
	t.Helper()
	rep, rd := hidppReplyTimeout, hidppReadTimeout
	hidppReplyTimeout, hidppReadTimeout = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { hidppReplyTimeout, hidppReadTimeout = rep, rd })
}

// fakeDev is a byte-fixture hidDevice: Write records the request and hands it
// to a responder that may enqueue a reply frame; ReadWithTimeout pops queued
// frames. inject feeds unsolicited frames (no matching Write).
type fakeDev struct {
	mu      sync.Mutex
	writes  [][]byte
	replies chan []byte
	respond func(req []byte) ([]byte, bool)
}

func newFakeDev(respond func([]byte) ([]byte, bool)) *fakeDev {
	return &fakeDev{replies: make(chan []byte, 256), respond: respond}
}

func (f *fakeDev) Write(p []byte) (int, error) {
	cp := append([]byte(nil), p...)
	f.mu.Lock()
	f.writes = append(f.writes, cp)
	f.mu.Unlock()
	if f.respond != nil {
		if r, ok := f.respond(cp); ok {
			f.replies <- r
		}
	}
	return len(p), nil
}

func (f *fakeDev) ReadWithTimeout(p []byte, d time.Duration) (int, error) {
	select {
	case r := <-f.replies:
		return copy(p, r), nil
	case <-time.After(d):
		return 0, hid.ErrTimeout
	}
}

func (f *fakeDev) inject(frame []byte) { f.replies <- frame }

func (f *fakeDev) written() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.writes...)
}

// hidppReply builds a normal reply echoing the request's devIdx/featIdx/fnId+swId.
func hidppReply(req []byte, params ...byte) []byte {
	f := make([]byte, hidppLongLen)
	f[0] = hidppReportLong
	f[1] = req[1]
	f[2] = req[2]
	f[3] = req[3]
	copy(f[4:], params)
	return f
}

// hidppErrReply builds a HID++ 2.0 error frame answering req with code.
func hidppErrReply(req []byte, code byte) []byte {
	f := make([]byte, hidppLongLen)
	f[0] = hidppReportLong
	f[1] = req[1]
	f[2] = hidppErrLong
	f[3] = req[2]
	f[4] = req[3]
	f[5] = code
	return f
}

// fakeControl is one 1B04 control's mutable divert state.
type fakeControl struct {
	cid   uint16
	flags byte
	remap uint16
}

// fakeMouse is a scripted MX-Master model: it answers ping on devIdx 0xFF,
// serves an IFeatureSet walk, battery, the 1B04 control table, and the setter
// getters/setters. errOn forces a HID++ error reply for a (featIdx, fnId).
type fakeMouse struct {
	mu       sync.Mutex
	ifsIdx   byte
	features []featureEntry
	byIndex  map[byte]uint16
	idx      map[uint16]byte
	dpiList  []byte // getSensorDpiList body after the leading sensor byte
	dpiCur   uint16
	controls []fakeControl
	errOn    map[[2]byte]byte
}

func newFakeMouse() *fakeMouse {
	m := &fakeMouse{
		ifsIdx:  1,
		byIndex: map[byte]uint16{},
		idx:     map[uint16]byte{},
		dpiCur:  1000,
		// explicit DPI list 800 / 1600 / 3200, 0x0000-terminated
		dpiList: []byte{0x03, 0x20, 0x06, 0x40, 0x0C, 0x80, 0x00, 0x00},
		errOn:   map[[2]byte]byte{},
		controls: []fakeControl{
			{cid: 0x0050, flags: 0x00},        // not diverted
			{cid: 0x00C3, flags: mapDiverted}, // diverted (gesture)
			{cid: 0x01A0, flags: mapDiverted | mapRawXY},
			{cid: 0x0053, flags: 0x00}, // not diverted (back)
		},
	}
	ids := []uint16{featFeatureSet, featUnifiedBattery, featReprogControls,
		featAdjustableDPI, featSmartShiftEnh, featHiresWheel, featThumbWheel,
		featHaptic, featWirelessStatus}
	for i, id := range ids {
		index := byte(i + 1)
		m.features = append(m.features, featureEntry{Index: index, ID: id, Version: 1})
		m.byIndex[index] = id
		m.idx[id] = index
	}
	return m
}

func (m *fakeMouse) control(cid uint16) *fakeControl {
	for i := range m.controls {
		if m.controls[i].cid == cid {
			return &m.controls[i]
		}
	}
	return nil
}

func (m *fakeMouse) respond(req []byte) ([]byte, bool) {
	if len(req) < 4 {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	devIdx, featIdx, fn := req[1], req[2], req[3]>>4
	if code, ok := m.errOn[[2]byte{featIdx, fn}]; ok {
		return hidppErrReply(req, code), true
	}
	switch featIdx {
	case rootFeatIdx:
		switch fn {
		case fnRootPing:
			if devIdx != hidppDevIdxBLE {
				return nil, false // only BLE index answers; other index times out
			}
			return hidppReply(req, 4, 5, req[6]), true // protocol 4.5, echo marker
		case fnRootGetFeature:
			id := be16(req[4:6])
			if index, ok := m.idx[id]; ok {
				return hidppReply(req, index, 0x00, 0x01), true
			}
			return hidppReply(req, 0x00, 0x00, 0x00), true // absent
		}
	case m.ifsIdx:
		switch fn {
		case fnSetGetCount:
			return hidppReply(req, byte(len(m.features))), true
		case fnSetGetFeatureID:
			i := int(req[4])
			if i >= 1 && i <= len(m.features) {
				e := m.features[i-1]
				return hidppReply(req, byte(e.ID>>8), byte(e.ID), e.Flags, e.Version), true
			}
			return hidppErrReply(req, 0x03), true
		}
	}
	switch m.byIndex[featIdx] {
	case featUnifiedBattery:
		if fn == fnBatteryGetStatus {
			return hidppReply(req, 75, 0x02, 1, 0), true // 75%, charging
		}
	case featReprogControls:
		switch fn {
		case fnCtrlGetCount:
			return hidppReply(req, byte(len(m.controls))), true
		case fnCtrlGetCidInfo:
			i := int(req[4])
			if i >= 0 && i < len(m.controls) {
				c := m.controls[i]
				return hidppReply(req, byte(c.cid>>8), byte(c.cid), 0, 0, 0, 0, 0, 0, 0), true
			}
			return hidppErrReply(req, 0x03), true
		case fnCtrlGetCidReporting:
			cid := be16(req[4:6])
			if c := m.control(cid); c != nil {
				return hidppReply(req, byte(cid>>8), byte(cid), c.flags, byte(c.remap>>8), byte(c.remap)), true
			}
			return hidppReply(req, req[4], req[5], 0, 0, 0), true
		case fnCtrlSetCidReporting:
			cid := be16(req[4:6])
			flags, remap := req[6], be16(req[7:9])
			if c := m.control(cid); c != nil {
				if flags&(mapDiverted<<1) != 0 {
					c.flags &^= mapDiverted
				}
				if flags&(mapRawXY<<1) != 0 {
					c.flags &^= mapRawXY
				}
				if flags == 0 {
					c.remap = remap
				}
			}
			return hidppReply(req, req[4], req[5], req[6], req[7], req[8]), true
		}
	case featAdjustableDPI:
		switch fn {
		case fnDpiGetList:
			return hidppReply(req, append([]byte{0x00}, m.dpiList...)...), true
		case fnDpiGet:
			return hidppReply(req, 0x00, byte(m.dpiCur>>8), byte(m.dpiCur)), true
		case fnDpiSet:
			m.dpiCur = be16(req[5:7])
			return hidppReply(req, req[4], req[5], req[6]), true
		}
	case featSmartShiftEnh:
		switch fn {
		case fnSmartShiftGet:
			return hidppReply(req, 0, 30, 0), true
		case fnSmartShiftSet:
			return hidppReply(req, req[4], req[5], req[6]), true
		}
	case featHiresWheel:
		switch fn {
		case fnHiresGet:
			return hidppReply(req, 0x00), true
		case fnHiresSet:
			return hidppReply(req, req[4]), true
		}
	case featThumbWheel:
		switch fn {
		case fnThumbGet:
			return hidppReply(req, 0x00, 0x00), true
		case fnThumbSet:
			return hidppReply(req, req[4], req[5]), true
		}
	case featHaptic:
		if fn == fnHapticSet {
			return hidppReply(req, req[4]), true
		}
	}
	return nil, false
}

// newSession wires a logiSession over a conn on the fake, with a no-op
// publisher. Callers resolve features then drive the step under test.
func newSession(t *testing.T, cfg logiConfig, dev hidDevice) (*logiSession, *hidppConn) {
	t.Helper()
	conn := newHidppConn(dev)
	t.Cleanup(conn.close)
	published := func(any) {}
	s := &logiSession{m: &logiretchModule{cfg: cfg}, conn: conn, env: Env{publish: published}}
	return s, conn
}

// findWrite returns the first recorded frame with the given feature index and
// function id (byte[3]>>4), or nil.
func findWrite(writes [][]byte, featIdx, fn byte) []byte {
	for _, w := range writes {
		if len(w) >= 4 && w[2] == featIdx && w[3]>>4 == fn {
			return w
		}
	}
	return nil
}

// wantBody asserts a recorded frame's body (byte[4:] up to len(params))
// equals params, ignoring the transport swId nibble.
func wantBody(t *testing.T, frame []byte, name string, params ...byte) {
	t.Helper()
	if frame == nil {
		t.Fatalf("%s: no matching write", name)
	}
	if len(frame) < 4+len(params) {
		t.Fatalf("%s: frame too short: %s", name, hexFrame(frame))
	}
	if !bytes.Equal(frame[4:4+len(params)], params) {
		t.Fatalf("%s: body %s, want %X", name, hexFrame(frame[4:4+len(params)]), params)
	}
}

// The conn round-trips a request to its swId-matched reply, cycles the swId
// per request (nonzero nibble), and hands unrouted frames (a synthetic 0x1D4B
// event) to onEvent -- all off the byte-fixture fake, no hardware.
func TestHidppConnRequestReplyAndEvent(t *testing.T) {
	shortHidppTimeouts(t)
	m := newFakeMouse()
	dev := newFakeDev(m.respond)
	conn := newHidppConn(dev)
	defer conn.close()
	ctx := context.Background()

	res, err := conn.request(ctx, hidppDevIdxBLE, rootFeatIdx, fnRootPing, 0x00, 0x00, 0x5A)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	if major, minor, marker, ok := decodePing(res.params); !ok || major != 4 || minor != 5 || marker != 0x5A {
		t.Fatalf("ping decode %d.%d marker=0x%02X ok=%v", major, minor, marker, ok)
	}
	if _, err := conn.request(ctx, hidppDevIdxBLE, rootFeatIdx, fnRootPing, 0x00, 0x00, 0x5A); err != nil {
		t.Fatalf("second request: %v", err)
	}
	w := dev.written()
	if len(w) < 2 {
		t.Fatalf("expected two writes, got %d", len(w))
	}
	sw1, sw2 := w[0][3]&0x0F, w[1][3]&0x0F
	if sw1&0x08 == 0 || sw2&0x08 == 0 || sw1 == sw2 {
		t.Fatalf("swId cycle: 0x%X then 0x%X (want distinct nonzero-MSB nibbles)", sw1, sw2)
	}

	// onEvent routing: a synthetic 0x1D4B event (swId 0) is unrouted and
	// classified by isWirelessReconf on the resolved index.
	const wsIdx = 9
	got := make(chan []byte, 1)
	conn.setOnEvent(func(frame []byte) { got <- frame })
	evt := longFrame(hidppReportLong, hidppDevIdxBLE, wsIdx, 0x00, 0x01)
	dev.inject(evt)
	select {
	case frame := <-got:
		if !isWirelessReconf(frame, hidppDevIdxBLE, wsIdx) {
			t.Fatalf("0x1D4B frame not classified as reconf: %s", hexFrame(frame))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("onEvent never fired for the injected 0x1D4B frame")
	}
}

// A device-loss read error is distinguishable from a request timeout: the
// reader dies, readError reports it, and a subsequent request wraps it.
func TestHidppConnReaderDeath(t *testing.T) {
	shortHidppTimeouts(t)
	conn := newHidppConn(&deadDev{})
	defer conn.close()
	// give the reader a moment to hit the error
	for i := 0; i < 200 && conn.readError() == nil; i++ {
		time.Sleep(2 * time.Millisecond)
	}
	if conn.readError() == nil {
		t.Fatal("readError not set after a reader read error")
	}
	if _, err := conn.request(context.Background(), hidppDevIdxBLE, rootFeatIdx, fnRootPing); err == nil {
		t.Fatal("request on a dead reader returned nil error")
	}
}

// deadDev returns a non-timeout read error, modeling device loss.
type deadDev struct{}

func (deadDev) Write(p []byte) (int, error) { return len(p), nil }
func (deadDev) ReadWithTimeout(p []byte, d time.Duration) (int, error) {
	return 0, errDeviceGone
}

var errDeviceGone = &deviceGoneError{}

type deviceGoneError struct{}

func (*deviceGoneError) Error() string { return "device gone" }

// resolveFeatures pings for the device index and walks IFeatureSet fresh,
// yielding the id-keyed index table -- never a hardcoded index.
func TestLogiResolveFeatures(t *testing.T) {
	shortHidppTimeouts(t)
	m := newFakeMouse()
	s, _ := newSession(t, logiConfig{}, newFakeDev(m.respond))
	if err := s.resolveFeatures(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.devIdx != hidppDevIdxBLE {
		t.Fatalf("devIdx = 0x%02X, want 0x%02X", s.devIdx, hidppDevIdxBLE)
	}
	for id, wantIdx := range map[uint16]byte{
		featUnifiedBattery: 2, featReprogControls: 3, featAdjustableDPI: 4,
		featSmartShiftEnh: 5, featHiresWheel: 6, featThumbWheel: 7,
		featHaptic: 8, featWirelessStatus: 9,
	} {
		e, ok := s.feats[id]
		if !ok || e.Index != wantIdx {
			t.Fatalf("feature 0x%04X resolved to %+v ok=%v, want index %d", id, e, ok, wantIdx)
		}
	}
}

// takeoverReset issues setCidReporting ONLY for diverted controls, clearing
// divert+rawXY bits, and remaps (never clears) a control named in cfg.buttons.
func TestLogiTakeoverReset(t *testing.T) {
	shortHidppTimeouts(t)
	m := newFakeMouse()
	dev := newFakeDev(m.respond)
	// 0x01A0 is diverted AND configured for a remap: it must be remapped, not
	// cleared. 0x00C3 is diverted with no config: cleared. 0x0050/0x0053 are
	// not diverted: untouched.
	cfg := logiConfig{Buttons: []buttonRemap{{CID: 0x01A0, Remap: 0x0050}}}
	s, _ := newSession(t, cfg, dev)
	if err := s.resolveFeatures(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.takeoverReset(context.Background())

	var sets [][]byte
	for _, w := range dev.written() {
		if w[2] == 3 && w[3]>>4 == fnCtrlSetCidReporting {
			sets = append(sets, w)
		}
	}
	if len(sets) != 2 {
		t.Fatalf("setCidReporting issued %d times, want 2 (0x00C3 clear, 0x01A0 remap)", len(sets))
	}
	find := func(cid uint16) []byte {
		for _, w := range sets {
			if be16(w[4:6]) == cid {
				return w
			}
		}
		return nil
	}
	wantBody(t, find(0x00C3), "clear 0x00C3", cidClearDivertParams(0x00C3)...)
	wantBody(t, find(0x01A0), "remap 0x01A0", cidRemapParams(0x01A0, 0x0050)...)
	if find(0x0050) != nil || find(0x0053) != nil {
		t.Fatal("setCidReporting touched a non-diverted, non-configured control")
	}
}

// applyConfig builds the exact setter body per PRESENT capability and issues
// nothing for absent ones (hiresWheel here).
func TestLogiApplyConfigFrames(t *testing.T) {
	shortHidppTimeouts(t)
	m := newFakeMouse()
	dev := newFakeDev(m.respond)
	dpi, hap := 1600, 60
	inv := true
	cfg := logiConfig{
		DPI:        &dpi,
		SmartShift: &smartShiftConfig{Threshold: intp(30)},
		Thumbwheel: &inv,
		Haptic:     &hap,
		// HiresWheel intentionally absent
	}
	s, _ := newSession(t, cfg, dev)
	if err := s.resolveFeatures(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.applyConfig(context.Background())
	w := dev.written()

	wantBody(t, findWrite(w, 4, fnDpiSet), "dpi set", dpiSetParams(1600)...)
	wantBody(t, findWrite(w, 5, fnSmartShiftSet), "smartShift set", smartShiftSetParams(0, 30, 0)...)
	wantBody(t, findWrite(w, 7, fnThumbSet), "thumb set", thumbSetParams(true)...)
	wantBody(t, findWrite(w, 8, fnHapticSet), "haptic set", hapticSetParams(60)...)
	if findWrite(w, 6, fnHiresSet) != nil {
		t.Fatal("hiresWheel absent from config but a setMode was issued")
	}
}

// A setter that answers a HID++ error is logged and skipped; the remaining
// setters still fire (no abort, no retry storm).
func TestLogiApplyConfigErrorContinues(t *testing.T) {
	shortHidppTimeouts(t)
	m := newFakeMouse()
	m.errOn[[2]byte{4, fnDpiSet}] = 0x03 // DPI set -> OUT_OF_RANGE
	dev := newFakeDev(m.respond)
	dpi := 5000
	cfg := logiConfig{DPI: &dpi, SmartShift: &smartShiftConfig{Threshold: intp(20)}}
	s, _ := newSession(t, cfg, dev)
	if err := s.resolveFeatures(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.applyConfig(context.Background())
	w := dev.written()
	if findWrite(w, 4, fnDpiSet) == nil {
		t.Fatal("DPI set was never attempted")
	}
	if findWrite(w, 5, fnSmartShiftSet) == nil {
		t.Fatal("smartShift set did not run after the DPI error (aborted?)")
	}
}

// The LogiState wire shape is pinned: all fields always present, exact keys.
func TestLogiStateJSONShape(t *testing.T) {
	line, err := json.Marshal(LogiState{T: 42, Kind: "battery", SoC: 75, Charging: true, State: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"t":42,"kind":"battery","soc":75,"charging":true,"state":1}`
	if string(line) != want {
		t.Fatalf("LogiState JSON = %s, want %s", line, want)
	}
}

func intp(v int) *int { return &v }
