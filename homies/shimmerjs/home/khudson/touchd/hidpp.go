// hidpp.go: pure HID++ 2.0 functional core for the logi probe -- frame
// builder, reply classification, and decoders, all []byte in/out with no
// hidapi dependency so the byte layout is testable without hardware
// (logi-replacement-design.md transport rules; magicbus-design.md logi
// section).
package main

import (
	"fmt"
	"strings"
)

const (
	logiVID = 0x046D
	// MX Master 4 over direct Bluetooth: the HID++ vendor collection tuple
	// (the receiver path enumerates 0xFF00/0x0001 instead)
	logiUsagePage = 0xFF43
	logiUsage     = 0x0202

	hidppReportShort = 0x10 // 7-byte frame
	hidppReportLong  = 0x11 // 20-byte frame
	hidppLongLen     = 20

	// direct-BLE device index, with 0x00 as the probe fallback
	hidppDevIdxBLE      = 0xFF
	hidppDevIdxFallback = 0x00

	// software id nibble: cycled per request across 0x8..0xF (MSB set, always
	// nonzero) so a reply to us never collides with an unsolicited event
	// (swId 0), and a late reply to a timed-out request cannot be aliased onto
	// the next request -- which reuses the same feature/function and would
	// otherwise misread it (and could dispatch a follow-up write to the wrong
	// feature index). hidppSwID is the representative value for illustrative
	// prints only; live requests cycle via hidppConn.nextSwID.
	hidppSwID    = 0x08
	hidppSwIDLo  = 0x08
	hidppSwIDLen = 0x08 // 0x08..0x0F

	// error markers in the feature-index slot of a reply
	hidppErrLong  = 0xFF // HID++ 2.0 error frame
	hidppErrShort = 0x8F // HID++ 1.0 error frame (a 1.0 answer to a 2.0 ping)
)

// features and function ids the probe touches -- all read-only
const (
	featRoot           = 0x0000
	featFeatureSet     = 0x0001
	featUnifiedBattery = 0x1004
	featReprogControls = 0x1B04
	featWirelessStatus = 0x1D4B

	rootFeatIdx = 0x00 // the root feature is always at index 0

	fnRootGetFeature      = 0x0
	fnRootPing            = 0x1
	fnSetGetCount         = 0x0
	fnSetGetFeatureID     = 0x1
	fnBatteryGetStatus    = 0x1
	fnCtrlGetCount        = 0x0
	fnCtrlGetCidInfo      = 0x1
	fnCtrlGetCidReporting = 0x2
)

// features the persistent logiretch module drives (getters above are shared
// with the probe; the setters below are the module's only writes). The module
// resolves each feature INDEX fresh per connect -- these are the id constants,
// never a hardcoded index.
const (
	featAdjustableDPI = 0x2201
	featSmartShiftEnh = 0x2111 // SMART_SHIFT_ENHANCED; 0x2110 is absent on the MX4
	featHiresWheel    = 0x2121
	featThumbWheel    = 0x2150
	featHaptic        = 0x19B0
)

// setter function ids sourced from Solaar (settings_templates.py write_fnid /
// hidpp20.py, master). Solaar's FeatureRW encodes write_fnid as fnId<<4, so
// the fnId here is write_fnid>>4. Haptic has no Solaar settings class; its
// set-level fnId comes from logi-replacement-design.md. Every setter is
// on-device-UNVERIFIED (user-gated, see logiretch.go).
const (
	fnDpiGetList          = 0x1 // 0x2201 getSensorDpiList (Solaar function 0x10)
	fnDpiGet              = 0x2 // 0x2201 getSensorDpi   (read_fnid 0x20)
	fnDpiSet              = 0x3 // 0x2201 setSensorDpi   (write_fnid 0x30)
	fnSmartShiftGet       = 0x1 // 0x2111 read_fnid 0x10
	fnSmartShiftSet       = 0x2 // 0x2111 write_fnid 0x20
	fnHiresGet            = 0x1 // 0x2121 read_fnid 0x10
	fnHiresSet            = 0x2 // 0x2121 write_fnid 0x20
	fnThumbGet            = 0x1 // 0x2150 read_fnid 0x10
	fnThumbSet            = 0x2 // 0x2150 write_fnid 0x20
	fnHapticSet           = 0x2 // 0x19B0 set level (logi-replacement-design.md)
	fnCtrlSetCidReporting = 0x3 // 0x1B04 setCidReporting (write_fnid 0x30)
)

// 1B04 setCidReporting mapping-flag bits (Solaar MappingFlag): each state bit
// takes effect only when its valid/change companion (state<<1) is also set.
const (
	mapDiverted = 0x01
	mapPersist  = 0x04 // persistently-diverted state (Options+ leaves this too)
	mapRawXY    = 0x10
)

// step-5 expectations for the MX Master 4 feature table, from the three
// public dumps (all Bolt captures -- confirming them over direct BT is the
// point of the probe; report absence as absence).
var (
	logiExpectPresent = []uint16{0x1004, 0x2201, 0x2111, 0x2121, 0x2150, 0x1B04, 0x19B0, 0x19C0, 0x1D4B, 0x1814}
	logiExpectAbsent  = []uint16{0x8100, 0x1C00, 0x2110, 0x6501, 0x1000, 0x2202}
)

// step-8 control ids to confirm in the 1B04 walk: the force/Actions Ring
// trigger and the gesture button.
const (
	cidActionsRing   = 0x01A0
	cidGestureButton = 0x00C3
)

var logiFeatureNames = map[uint16]string{
	0x0000: "ROOT",
	0x0001: "FEATURE_SET",
	0x1000: "BATTERY_STATUS",
	0x1004: "UNIFIED_BATTERY",
	0x1814: "CHANGE_HOST",
	0x19B0: "HAPTIC",
	0x19C0: "FORCE_SENSING_BUTTON",
	0x1B04: "REPROG_CONTROLS_V4",
	0x1C00: "PERSISTENT_REMAPPABLE_ACTION",
	0x1D4B: "WIRELESS_DEVICE_STATUS",
	0x2110: "SMART_SHIFT",
	0x2111: "SMART_SHIFT_ENHANCED",
	0x2121: "HIRES_WHEEL",
	0x2150: "THUMB_WHEEL",
	0x2201: "ADJUSTABLE_DPI",
	0x2202: "EXTENDED_ADJUSTABLE_DPI",
	0x6501: "GESTURE_2",
	0x8100: "ONBOARD_PROFILES",
}

func featureName(id uint16) string {
	if n, ok := logiFeatureNames[id]; ok {
		return n
	}
	return "?"
}

// hidppRequest builds one 20-byte long-report request:
// [0x11][devIdx][featIdx][fnId<<4|swId][params, zero-padded to 16].
func hidppRequest(devIdx, featIdx, fnID, swID byte, params ...byte) []byte {
	b := make([]byte, hidppLongLen)
	b[0] = hidppReportLong
	b[1] = devIdx
	b[2] = featIdx
	b[3] = fnID<<4 | swID&0x0F
	copy(b[4:], params)
	return b
}

// replyKind classifies one inbound frame against a pending request.
type replyKind int

const (
	replyForeign replyKind = iota // not ours: event (swId 0), foreign swId, other feature/device
	replyOK                       // echo matched; params valid
	replyErr                      // error frame answering our request; code valid
)

// classifyReply matches frame against the pending request. A reply echoes
// [devIdx][featIdx][fnId<<4|swId]; an error frame carries the marker in the
// feature-index slot and echoes featIdx + fnId/swId one slot later. The swId
// is the pending request's own (cycled) nibble: a late reply to a prior
// request carries a different nibble and is rejected as foreign, so it can
// never be aliased onto this request.
func classifyReply(frame []byte, devIdx, featIdx, fnID, swID byte) (replyKind, []byte, byte) {
	if len(frame) < 4 || (frame[0] != hidppReportLong && frame[0] != hidppReportShort) {
		return replyForeign, nil, 0
	}
	if frame[1] != devIdx {
		return replyForeign, nil, 0
	}
	echo := fnID<<4 | swID&0x0F
	if frame[2] == hidppErrLong || frame[2] == hidppErrShort {
		if len(frame) >= 6 && frame[3] == featIdx && frame[4] == echo {
			return replyErr, nil, frame[5]
		}
		return replyForeign, nil, 0
	}
	if frame[2] == featIdx && frame[3] == echo {
		return replyOK, frame[4:], 0
	}
	return replyForeign, nil, 0
}

// hidppError is a device-answered HID++ error: the link works, the request
// was refused (contrast a timeout, which proves nothing).
type hidppError byte

func (e hidppError) Error() string {
	return fmt.Sprintf("hid++ error 0x%02X (%s)", byte(e), hidppErrString(byte(e)))
}

func hidppErrString(code byte) string {
	switch code {
	case 0x01:
		return "UNKNOWN"
	case 0x02:
		return "INVALID_ARGUMENT"
	case 0x03:
		return "OUT_OF_RANGE"
	case 0x04:
		return "HW_ERROR"
	case 0x05:
		return "LOGITECH_INTERNAL"
	case 0x06:
		return "INVALID_FEATURE_INDEX"
	case 0x07:
		return "INVALID_FUNCTION_ID"
	case 0x08:
		return "BUSY"
	case 0x09:
		return "UNSUPPORTED"
	}
	return "?"
}

// decodePing decodes a root ping reply: protocol major.minor plus the echoed
// marker byte the request carried in params[2].
func decodePing(params []byte) (major, minor, marker byte, ok bool) {
	if len(params) < 3 {
		return 0, 0, 0, false
	}
	return params[0], params[1], params[2], true
}

// decodeGetFeature decodes a root getFeature reply; index 0 = feature absent.
func decodeGetFeature(params []byte) (index, flags, version byte, ok bool) {
	if len(params) < 3 {
		return 0, 0, 0, false
	}
	return params[0], params[1], params[2], true
}

// decodeCount decodes the one-byte count replies (IFeatureSet getCount, 1B04
// getControlCount).
func decodeCount(params []byte) (byte, bool) {
	if len(params) < 1 {
		return 0, false
	}
	return params[0], true
}

// featureEntry is one decoded IFeatureSet table row.
type featureEntry struct {
	Index   byte
	ID      uint16
	Flags   byte
	Version byte
}

// decodeFeatureEntry decodes one getFeatureID reply:
// [featID be16][type flags][version].
func decodeFeatureEntry(index byte, params []byte) (featureEntry, bool) {
	if len(params) < 4 {
		return featureEntry{}, false
	}
	return featureEntry{Index: index, ID: be16(params), Flags: params[2], Version: params[3]}, true
}

// featureFlagsString renders the IFeatureSet type bits; empty for a plain
// feature.
func featureFlagsString(flags byte) string {
	var parts []string
	if flags&0x80 != 0 {
		parts = append(parts, "obsolete")
	}
	if flags&0x40 != 0 {
		parts = append(parts, "hidden")
	}
	if flags&0x20 != 0 {
		parts = append(parts, "engineering")
	}
	return strings.Join(parts, ",")
}

// featureAssertions checks a discovered feature table against the step-5
// expectations, returning the missing expect-present ids and the wrongly
// present expect-absent ids, in table order.
func featureAssertions(features map[uint16]featureEntry) (missing, unexpected []uint16) {
	for _, id := range logiExpectPresent {
		if _, ok := features[id]; !ok {
			missing = append(missing, id)
		}
	}
	for _, id := range logiExpectAbsent {
		if _, ok := features[id]; ok {
			unexpected = append(unexpected, id)
		}
	}
	return missing, unexpected
}

// batteryStatus is a decoded 0x1004 UNIFIED_BATTERY getStatus reply.
type batteryStatus struct {
	SoC      byte // state of charge, percent
	Level    byte // level bitmask
	State    byte // charging-state enum
	ExtPower byte // external power flag
}

// decodeBatteryStatus decodes a getStatus reply:
// [soc%][level bits][charging state][external power].
func decodeBatteryStatus(params []byte) (batteryStatus, bool) {
	if len(params) < 4 {
		return batteryStatus{}, false
	}
	return batteryStatus{SoC: params[0], Level: params[1], State: params[2], ExtPower: params[3]}, true
}

func (b batteryStatus) chargingString() string {
	switch b.State {
	case 0:
		return "discharging"
	case 1:
		return "charging"
	case 2:
		return "charging (slow)"
	case 3:
		return "charge complete"
	case 4:
		return "charge error"
	}
	return fmt.Sprintf("charging state 0x%02X", b.State)
}

// cidInfo is a decoded 1B04 getCidInfo reply (logiops field layout).
type cidInfo struct {
	CID, Task             uint16
	Flags                 byte
	Pos, Group, GroupMask byte
	AddFlags              byte
}

func decodeCidInfo(params []byte) (cidInfo, bool) {
	if len(params) < 9 {
		return cidInfo{}, false
	}
	return cidInfo{
		CID:       be16(params[0:2]),
		Task:      be16(params[2:4]),
		Flags:     params[4],
		Pos:       params[5],
		Group:     params[6],
		GroupMask: params[7],
		AddFlags:  params[8],
	}, true
}

// cidFlagsString names the getCidInfo flag bits (logiops convention).
func cidFlagsString(flags byte) string {
	names := []string{"mouse-button", "fkey", "hotkey", "fn-toggle", "reprog", "divertable", "persist-divertable", "virtual"}
	return bitNames(flags, names)
}

// cidAddFlagsString names the additional-flags bits.
func cidAddFlagsString(flags byte) string {
	return bitNames(flags, []string{"rawXY", "force-rawXY", "analytics"})
}

func bitNames(flags byte, names []string) string {
	var parts []string
	for i, n := range names {
		if flags&(1<<i) != 0 {
			parts = append(parts, n)
		}
	}
	return strings.Join(parts, ",")
}

// cidReporting is a decoded 1B04 getCidReporting reply: the echoed CID, the
// divert-state flags, and the remap target CID.
type cidReporting struct {
	CID   uint16
	Flags byte
	Remap uint16
}

func decodeCidReporting(params []byte) (cidReporting, bool) {
	if len(params) < 5 {
		return cidReporting{}, false
	}
	return cidReporting{CID: be16(params[0:2]), Flags: params[2], Remap: be16(params[3:5])}, true
}

// divertString names the getCidReporting divert-state bits.
func divertString(flags byte) string {
	var parts []string
	if flags&0x01 != 0 {
		parts = append(parts, "diverted")
	}
	if flags&0x04 != 0 {
		parts = append(parts, "persistently-diverted")
	}
	if flags&0x10 != 0 {
		parts = append(parts, "rawXY-diverted")
	}
	if len(parts) == 0 {
		return "not diverted"
	}
	return strings.Join(parts, ",")
}

// --- logiretch setter bodies (pure frame builders, frame-asserted by tests;
// on-device effect user-gated). Sources cited at each fn const above. ---

// dpiSetParams builds a 0x2201 setSensorDpi body: sensor index 0 then the DPI
// as 2 big-endian bytes (Solaar write_prefix_bytes b"\x00" + 2-byte value).
func dpiSetParams(dpi uint16) []byte {
	return []byte{0x00, byte(dpi >> 8), byte(dpi)}
}

// parseDpiList decodes a 0x2201 getSensorDpiList reply body (Solaar
// produce_dpi_list): 2-byte big-endian values terminated by 0x0000, with a
// range encoded when the top three bits are 0b111 (step in the low 13 bits,
// followed by the inclusive max). raw is the reply params AFTER the leading
// sensor-index byte.
func parseDpiList(raw []byte) []uint16 {
	var list []uint16
	for i := 0; i+1 < len(raw); {
		val := be16(raw[i : i+2])
		if val == 0 {
			break
		}
		if val>>13 == 0b111 {
			step := int(val & 0x1FFF)
			if step == 0 || len(list) == 0 || i+3 >= len(raw) {
				break
			}
			last := int(be16(raw[i+2 : i+4]))
			for v := int(list[len(list)-1]) + step; v <= last; v += step {
				list = append(list, uint16(v))
			}
			i += 4
		} else {
			list = append(list, val)
			i += 2
		}
	}
	return list
}

// snapDPI returns the entry of list closest to want; want unchanged if list is
// empty (the device then rejects an unlisted value, logged as a failed echo).
func snapDPI(list []uint16, want uint16) uint16 {
	if len(list) == 0 {
		return want
	}
	best, bestDiff := list[0], diffU16(list[0], want)
	for _, v := range list[1:] {
		if d := diffU16(v, want); d < bestDiff {
			best, bestDiff = v, d
		}
	}
	return best
}

func diffU16(a, b uint16) uint16 {
	if a > b {
		return a - b
	}
	return b - a
}

// smartShiftSetParams builds a 0x2111 setStatus body: wheel mode, auto-
// disengage threshold, tunable torque. Solaar writes [mode, threshold] (mode 0
// leaves the wheel mode, threshold 255 selects the max); torque is the
// enhanced (0x2111) field per the design doc, 0 = leave. When torque is 0 this
// zero-pads identically to Solaar's confirmed 2-byte write.
func smartShiftSetParams(mode, threshold, torque byte) []byte {
	return []byte{mode, threshold, torque}
}

// hiresSetParams builds a 0x2121 setMode body: the resolution bit (0x02) is
// set for hi-res, cleared for standard (Solaar HiresSmoothResolution mask
// 0x02). The invert (0x04) and target/divert (0x01) bits stay 0 -- the module
// never diverts the wheel (no gesture engine).
func hiresSetParams(hires bool) []byte {
	if hires {
		return []byte{0x02}
	}
	return []byte{0x00}
}

// thumbSetParams builds a 0x2150 setThumbwheel body: [reportMode, invert].
// reportMode 0 keeps the thumbwheel on native HID (no divert); invert sets the
// second byte per Solaar ThumbInvert (true_value b"\x00\x01").
func thumbSetParams(invert bool) []byte {
	var inv byte
	if invert {
		inv = 0x01
	}
	return []byte{0x00, inv}
}

// hapticSetParams builds a 0x19B0 set-level body: a single level byte 0-100.
func hapticSetParams(level byte) []byte {
	return []byte{level}
}

// cidReportingParams packs a 0x1B04 setCidReporting body per Solaar's
// struct.pack("!HBH", cid, flags, remap): CID be16, flags byte, remap be16.
func cidReportingParams(cid uint16, flags byte, remap uint16) []byte {
	return []byte{byte(cid >> 8), byte(cid), flags, byte(remap >> 8), byte(remap)}
}

// cidClearDivertParams clears the divert, persistent-divert, and rawXY
// diversions for cid: each cleared flag sets only its valid/change companion
// bit (state<<1), leaving the state bit 0. remap is left at 0 = keep-current
// (v4 applies a remap only with its own change flag, which this never sets),
// so this un-diverts without disturbing any existing remap.
func cidClearDivertParams(cid uint16) []byte {
	return cidReportingParams(cid, mapDiverted<<1|mapPersist<<1|mapRawXY<<1, 0)
}

// cidRemapParams remaps cid to target without touching its divert state.
func cidRemapParams(cid, target uint16) []byte {
	return cidReportingParams(cid, 0, target)
}

// wirelessReconfReason is the 0x1D4B event's reconnection reason (Solaar:
// byte 0 == 1 means the link came back and settings must be re-applied). On-
// device-unverified like the setters; gating on it stops ordinary status
// events (battery, other reasons) from firing spurious re-applies.
const wirelessReconfReason = 0x01

// isWirelessReconf reports whether frame is an unsolicited 0x1D4B
// WIRELESS_DEVICE_STATUS RECONNECTION event for devIdx on the resolved feature
// index wsIdx -- the belt-and-suspenders reconnect trigger (magicbus-design.md
// phase-5b dual trigger). It gates on the reconfiguration-reason byte so a
// non-reconnection status event does not re-apply config; a genuine reconnect
// that drops the BLE handle is still covered by the reopen path.
func isWirelessReconf(frame []byte, devIdx, wsIdx byte) bool {
	if len(frame) < 5 {
		return false
	}
	if frame[0] != hidppReportLong && frame[0] != hidppReportShort {
		return false
	}
	return frame[1] == devIdx && frame[2] == wsIdx && frame[3]&0x0F == 0 && frame[4] == wirelessReconfReason
}

func be16(b []byte) uint16 { return uint16(b[0])<<8 | uint16(b[1]) }

// hexFrame renders a raw frame as space-separated hex bytes.
func hexFrame(b []byte) string {
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		fmt.Fprintf(&sb, "%02X", c)
	}
	return sb.String()
}

// descriptorReportIDs walks HID report-descriptor short items and collects
// the distinct Report ID (tag 0x85) values in first-appearance order. go-hid
// does not surface kIOHIDMaxInputReportSizeKey, so the probe derives report
// ids from the descriptor and takes lengths from observed reports.
func descriptorReportIDs(desc []byte) []byte {
	var ids []byte
	seen := map[byte]bool{}
	for i := 0; i < len(desc); {
		prefix := desc[i]
		if prefix == 0xFE { // long item: skip its declared payload
			if i+1 >= len(desc) {
				break
			}
			i += 3 + int(desc[i+1])
			continue
		}
		size := int(prefix & 0x03)
		if size == 3 {
			size = 4
		}
		if prefix&0xFC == 0x84 && size >= 1 && i+1 < len(desc) {
			if id := desc[i+1]; !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
		i += 1 + size
	}
	return ids
}
