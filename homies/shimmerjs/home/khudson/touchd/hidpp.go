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
	// prints only; live requests cycle via probeReader.nextSwID.
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
