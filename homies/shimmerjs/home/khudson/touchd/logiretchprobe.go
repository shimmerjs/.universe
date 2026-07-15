// logiretchprobe.go: the logiretch-0 spike prober (magicbus-design.md logi section,
// logi-replacement-design.md spike list). One-shot and strictly read-only:
// it enumerates the MX Master 4's HID++ vendor collection over direct
// Bluetooth, opens it non-exclusive, and probes framing, the feature table,
// battery, and 1B04 divert state. The only functions sent are ping /
// getFeature / getCount / getFeatureID / getStatus / getCidInfo /
// getCidReporting -- nothing on the device is mutated, and the usage-page-1
// pointer collections are never opened. The reader/demux transport lives in
// hidppConn (hidppconn.go), shared with the persistent logiretch module.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/sstallion/go-hid"
)

const (
	probeListenWindow = 10 * time.Second
	probeSpoolPrint   = 100 // cap on individually printed frames in step 9
)

// captured is one unrouted inbound report with its arrival time.
type captured struct {
	t     time.Time
	frame []byte
}

// runLogiretchProbe runs the one-shot logiretch-0 feasibility probe against the first
// 0x046D HID++ vendor collection found, printing a self-contained report
// with a GO/NO-GO verdict. Read-only throughout (see the file comment).
func runLogiretchProbe(ctx context.Context, w io.Writer) error {
	p := &logiretchProbe{w: w}
	return p.run(ctx)
}

type logiretchProbe struct {
	w        io.Writer
	r        *hidppConn
	devIdx   byte
	features map[uint16]featureEntry // discovered via the step-4 walk, id-keyed

	accessOK   bool
	batteryOK  bool
	writeOK    bool
	absencesOK bool
}

func (p *logiretchProbe) printf(format string, a ...any) { fmt.Fprintf(p.w, format, a...) }

func (p *logiretchProbe) run(ctx context.Context) error {
	p.printf("khudson-touchd logiretch-probe -- MX Master 4 HID++ feasibility (logiretch-0 spike)\n")
	p.printf("read-only: ping/getFeature/getCount/getFeatureID/getStatus/getCidInfo/getCidReporting only\n\n")

	vendor, err := p.step1Enumerate()
	if err != nil {
		return fmt.Errorf("enumerate: %w", err)
	}
	if vendor == nil {
		p.printf("\nno HID++ vendor collection (usage_page=0x%04X usage=0x%04X); probe cannot continue\n\n", uint16(logiUsagePage), uint16(logiUsage))
		p.verdict()
		return nil
	}
	dev, ok := p.step2Open(vendor.Path)
	if !ok {
		p.printf("\n")
		p.verdict()
		return nil
	}
	p.r = newHidppConn(dev)
	defer func() {
		p.r.close() // join the reader before dev.Close and run's deferred hid.Exit
		dev.Close()
	}()

	p.reportDescriptor(dev)

	if p.step3Ping(ctx) {
		p.step4FeatureTable(ctx)
		p.step5Assertions()
		p.step6Battery(ctx)
		p.step7NumberedWrite(ctx)
		p.step8Controls(ctx)
	} else {
		p.printf("\nsteps 4-8 skipped: no working device index\n")
	}
	p.step9Listen(ctx)
	p.step10Coexistence()
	p.verdict()
	return nil
}

// step 1: fresh enumerate (never a cached DevSrvsID path), all 0x046D
// tuples; grouping by path shows which usage pairs share one IOHIDDevice --
// the reason collection selection must key on the usage tuple, never the
// path.
func (p *logiretchProbe) step1Enumerate() (*hid.DeviceInfo, error) {
	p.printf("== step 1: enumerate 0x%04X collections ==\n", uint16(logiVID))
	var infos []hid.DeviceInfo
	err := hid.Enumerate(logiVID, hid.ProductIDAny, func(info *hid.DeviceInfo) error {
		infos = append(infos, *info)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		p.printf("  no 0x%04X collections (mouse paired and awake? Input Monitoring granted?)\n", uint16(logiVID))
		return nil, nil
	}
	var vendor *hid.DeviceInfo
	byPath := map[string][]string{}
	var paths []string
	for i := range infos {
		info := &infos[i]
		p.printf("  pid=0x%04X usage_page=0x%04X usage=0x%04X bus=%s product=%q serial=%q\n    path=%s\n",
			info.ProductID, info.UsagePage, info.Usage, info.BusType, info.ProductStr, info.SerialNbr, info.Path)
		if vendor == nil && info.UsagePage == logiUsagePage && info.Usage == logiUsage {
			vendor = info
		}
		if _, ok := byPath[info.Path]; !ok {
			paths = append(paths, info.Path)
		}
		byPath[info.Path] = append(byPath[info.Path], fmt.Sprintf("0x%04X/0x%04X", info.UsagePage, info.Usage))
	}
	for _, path := range paths {
		if tuples := byPath[path]; len(tuples) > 1 {
			p.printf("  usage pairs sharing one path (%s): %v\n", path, tuples)
		}
	}
	if vendor != nil {
		p.printf("  vendor collection (0x%04X/0x%04X) found: pid=0x%04X path=%s\n",
			uint16(logiUsagePage), uint16(logiUsage), vendor.ProductID, vendor.Path)
	}
	p.printf("  report ids/lengths: derived post-open from the descriptor + observed reports below\n  (go-hid's enumerate does not expose kIOHIDMaxInputReportSizeKey)\n\n")
	return vendor, nil
}

// step 2: non-exclusive open through openPath, which serializes under openMu
// and restores the process-default exclusive flag. Never seize: Options+
// coexistence is a design rule, and the pointer collections stay untouched.
func (p *logiretchProbe) step2Open(path string) (*hid.Device, bool) {
	p.printf("== step 2: non-exclusive open ==\n")
	dev, err := openPath(path, false)
	if err != nil {
		p.printf("  open FAILED: %v\n  (Input Monitoring granted? Options+ holding a seize?)\n", err)
		return nil, false
	}
	p.printf("  open OK (shared/kIOHIDOptionsTypeNone)\n\n")
	return dev, true
}

// step 1b (needs the open handle): descriptor-derived report ids -- answers
// 0x10+0x11 vs 0x11-only; lengths come from observed reports in step 9's
// summary, labeled as such.
func (p *logiretchProbe) reportDescriptor(dev *hid.Device) {
	p.printf("== step 1b: report descriptor ==\n")
	buf := make([]byte, 4096)
	n, err := dev.GetReportDescriptor(buf)
	if err != nil {
		p.printf("  GetReportDescriptor failed: %v\n\n", err)
		return
	}
	ids := descriptorReportIDs(buf[:n])
	p.printf("  descriptor: %d bytes, report ids:", n)
	for _, id := range ids {
		p.printf(" 0x%02X", id)
	}
	p.printf("\n")
	hasShort := slices.Contains(ids, byte(hidppReportShort))
	hasLong := slices.Contains(ids, byte(hidppReportLong))
	switch {
	case hasShort && hasLong:
		p.printf("  topology: 0x10+0x11 (short and long HID++ reports)\n\n")
	case hasLong:
		p.printf("  topology: 0x11-only (long HID++ reports)\n\n")
	default:
		p.printf("  topology: no HID++ report ids in the descriptor (unexpected)\n\n")
	}
}

// step 3: root ping (feature index 0, fn 1) on 0x11 frames, devIdx 0xFF then
// the 0x00 fallback; success = marker + swId echo plus a protocol version.
func (p *logiretchProbe) step3Ping(ctx context.Context) bool {
	p.printf("== step 3: HID++ root ping ==\n")
	const marker = 0x5A
	for _, devIdx := range []byte{hidppDevIdxBLE, hidppDevIdxFallback} {
		p.printf("  ping devIdx=0x%02X: ", devIdx)
		res, err := p.r.request(ctx, devIdx, rootFeatIdx, fnRootPing, 0x00, 0x00, marker)
		if err != nil {
			p.printf("%v", err)
			if res.raw != nil {
				p.printf(" (raw %s)", hexFrame(res.raw))
			}
			var he hidppError
			if errors.As(err, &he) {
				p.printf(" -- device answered an error frame: link up, but not a HID++ 2.0 ping reply")
			}
			p.printf("\n")
			continue
		}
		major, minor, echo, ok := decodePing(res.params)
		if !ok || echo != marker {
			p.printf("reply did not echo the marker: raw %s\n", hexFrame(res.raw))
			continue
		}
		p.printf("protocol %d.%d, marker+swId echoed (raw %s)\n", major, minor, hexFrame(res.raw))
		p.printf("  RESULT: HID++ 2.0 access confirmed on devIdx 0x%02X\n\n", devIdx)
		p.devIdx = devIdx
		p.accessOK = true
		return true
	}
	p.printf("  RESULT: no HID++ ping reply on either device index\n")
	return false
}

// step 4: resolve IFeatureSet (0x0001) via root getFeature, then walk
// getCount + getFeatureID and print the table verbatim.
func (p *logiretchProbe) step4FeatureTable(ctx context.Context) {
	p.printf("== step 4: feature table ==\n")
	res, err := p.r.request(ctx, p.devIdx, rootFeatIdx, fnRootGetFeature, byte(featFeatureSet>>8), byte(featFeatureSet))
	if err != nil {
		p.printf("  getFeature(0x0001) failed: %v\n\n", err)
		return
	}
	ifsIdx, _, ifsVer, ok := decodeGetFeature(res.params)
	if !ok || ifsIdx == 0 {
		p.printf("  IFeatureSet not present (raw %s)\n\n", hexFrame(res.raw))
		return
	}
	p.printf("  IFeatureSet at index 0x%02X (v%d)\n", ifsIdx, ifsVer)
	res, err = p.r.request(ctx, p.devIdx, ifsIdx, fnSetGetCount)
	if err != nil {
		p.printf("  getCount failed: %v\n\n", err)
		return
	}
	count, ok := decodeCount(res.params)
	if !ok {
		p.printf("  malformed getCount reply: raw %s\n\n", hexFrame(res.raw))
		return
	}
	p.printf("  %d features (excluding root):\n", count)
	p.features = map[uint16]featureEntry{}
	// int counter: count == 255 would wrap a byte loop and never terminate
	for n := 1; n <= int(count); n++ {
		i := byte(n)
		res, err := p.r.request(ctx, p.devIdx, ifsIdx, fnSetGetFeatureID, i)
		if err != nil {
			p.printf("  [0x%02X] getFeatureID failed: %v\n", i, err)
			continue
		}
		e, ok := decodeFeatureEntry(i, res.params)
		if !ok {
			p.printf("  [0x%02X] malformed reply: raw %s\n", i, hexFrame(res.raw))
			continue
		}
		p.printf("  [0x%02X] 0x%04X v%d %-28s", e.Index, e.ID, e.Version, featureName(e.ID))
		if fl := featureFlagsString(e.Flags); fl != "" {
			p.printf(" (%s)", fl)
		}
		p.printf("  raw %s\n", hexFrame(res.raw))
		p.features[e.ID] = e
	}
	p.printf("\n")
}

// step 5: assert the expected MX4 direct-BT feature set; absence is reported
// as absence, never inferred.
func (p *logiretchProbe) step5Assertions() {
	p.printf("== step 5: feature expectations ==\n")
	if p.features == nil {
		p.printf("  skipped: no feature table\n\n")
		return
	}
	for _, id := range logiExpectPresent {
		if e, ok := p.features[id]; ok {
			p.printf("  0x%04X %-28s PRESENT (idx 0x%02X v%d)\n", id, featureName(id), e.Index, e.Version)
		} else {
			p.printf("  0x%04X %-28s MISSING (expected present)\n", id, featureName(id))
		}
	}
	for _, id := range logiExpectAbsent {
		if e, ok := p.features[id]; ok {
			p.printf("  0x%04X %-28s PRESENT at idx 0x%02X (expected absent!)\n", id, featureName(id), e.Index)
		} else {
			p.printf("  0x%04X %-28s ABSENT (as expected)\n", id, featureName(id))
		}
	}
	missing, unexpected := featureAssertions(p.features)
	p.absencesOK = len(missing) == 0 && len(unexpected) == 0
	p.printf("  RESULT: %d missing, %d unexpectedly present\n\n", len(missing), len(unexpected))
}

// step 6: 0x1004 UNIFIED_BATTERY getStatus -- the phase-1 logi capability.
func (p *logiretchProbe) step6Battery(ctx context.Context) {
	p.printf("== step 6: battery (0x1004 getStatus) ==\n")
	e, ok := p.features[featUnifiedBattery]
	if !ok {
		p.printf("  0x1004 not in the feature table; battery read skipped\n\n")
		return
	}
	res, err := p.r.request(ctx, p.devIdx, e.Index, fnBatteryGetStatus)
	if err != nil {
		p.printf("  getStatus failed: %v\n\n", err)
		return
	}
	st, ok := decodeBatteryStatus(res.params)
	if !ok {
		p.printf("  malformed getStatus reply: raw %s\n\n", hexFrame(res.raw))
		return
	}
	p.printf("  SoC %d%%, %s, level bits 0x%02X, external power %d (raw %s)\n\n",
		st.SoC, st.chargingString(), st.Level, st.ExtPower, hexFrame(res.raw))
	p.batteryOK = true
}

// step 7: numbered-write round-trip proof, read-shaped only. Every request
// above already used a numbered write; this step makes the framing proof
// explicit: the leading 0x11 is the report number, where the moonlander
// vendor write is UNNUMBERED with a leading 0x00 (moonInitReport) and so
// proves the pipe, not this framing.
func (p *logiretchProbe) step7NumberedWrite(ctx context.Context) {
	p.printf("== step 7: numbered-write round trip ==\n")
	const marker = 0xA5
	p.printf("  write %s\n  (leading 0x11 = report number; the moonlander write leads with 0x00; swId cycles per request)\n",
		hexFrame(hidppRequest(p.devIdx, rootFeatIdx, fnRootPing, hidppSwID, 0x00, 0x00, marker)))
	res, err := p.r.request(ctx, p.devIdx, rootFeatIdx, fnRootPing, 0x00, 0x00, marker)
	if err != nil {
		p.printf("  FAILED: %v\n\n", err)
		return
	}
	if _, _, echo, ok := decodePing(res.params); !ok || echo != marker {
		p.printf("  reply did not echo the marker: raw %s\n\n", hexFrame(res.raw))
		return
	}
	p.printf("  reply %s\n  RESULT: numbered Write round trip OK\n\n", hexFrame(res.raw))
	p.writeOK = true
}

// step 8: 1B04 REPROG_CONTROLS_V4 walk -- getCount, getCidInfo per index,
// then read-only getCidReporting per CID for divert state. setCidReporting
// is deliberately absent from this whole file.
func (p *logiretchProbe) step8Controls(ctx context.Context) {
	p.printf("== step 8: 1B04 control walk ==\n")
	e, ok := p.features[featReprogControls]
	if !ok {
		p.printf("  0x1B04 not in the feature table; control walk skipped\n\n")
		return
	}
	res, err := p.r.request(ctx, p.devIdx, e.Index, fnCtrlGetCount)
	if err != nil {
		p.printf("  getControlCount failed: %v\n\n", err)
		return
	}
	count, ok := decodeCount(res.params)
	if !ok {
		p.printf("  malformed getControlCount reply: raw %s\n\n", hexFrame(res.raw))
		return
	}
	p.printf("  %d controls:\n", count)
	var sawRing, sawGesture bool
	for i := byte(0); i < count; i++ {
		res, err := p.r.request(ctx, p.devIdx, e.Index, fnCtrlGetCidInfo, i)
		if err != nil {
			p.printf("  [%d] getCidInfo failed: %v\n", i, err)
			continue
		}
		ci, ok := decodeCidInfo(res.params)
		if !ok {
			p.printf("  [%d] malformed getCidInfo reply: raw %s\n", i, hexFrame(res.raw))
			continue
		}
		sawRing = sawRing || ci.CID == cidActionsRing
		sawGesture = sawGesture || ci.CID == cidGestureButton
		p.printf("  [%d] cid=0x%04X task=0x%04X flags=0x%02X(%s) pos=%d group=%d gmask=0x%02X add=0x%02X(%s)\n",
			i, ci.CID, ci.Task, ci.Flags, cidFlagsString(ci.Flags), ci.Pos, ci.Group, ci.GroupMask, ci.AddFlags, cidAddFlagsString(ci.AddFlags))
		res, err = p.r.request(ctx, p.devIdx, e.Index, fnCtrlGetCidReporting, byte(ci.CID>>8), byte(ci.CID))
		if err != nil {
			p.printf("      getCidReporting failed: %v\n", err)
			continue
		}
		cr, ok := decodeCidReporting(res.params)
		if !ok {
			p.printf("      malformed getCidReporting reply: raw %s\n", hexFrame(res.raw))
			continue
		}
		if cr.CID != ci.CID {
			// the reply echoes the requested CID; a mismatch means a stale
			// reply slipped through -- do not attribute its divert state
			p.printf("      getCidReporting echoed cid 0x%04X, expected 0x%04X (stale reply, skipped)\n", cr.CID, ci.CID)
			continue
		}
		p.printf("      reporting: flags=0x%02X (%s) remap=0x%04X (raw %s)\n", cr.Flags, divertString(cr.Flags), cr.Remap, hexFrame(res.raw))
	}
	p.printf("  RESULT: cid 0x%04X (Actions Ring/force): %v; cid 0x%04X (gesture button): %v\n\n",
		uint16(cidActionsRing), sawRing, uint16(cidGestureButton), sawGesture)
}

// step 9: bounded listen for unsolicited swId-0 traffic (the 0x1004 push
// check and any 0x1D4B status events), printed raw-hex; mouse reports on the
// shared handle are tallied, not printed. The probe wires its spool through
// the conn's onEvent hook.
func (p *logiretchProbe) step9Listen(ctx context.Context) {
	p.printf("== step 9: %s unsolicited-notification listen ==\n", probeListenWindow)
	p.printf("  (wiggle/click/scroll the mouse; plug or unplug charging if handy)\n")
	spool := make(chan captured, 64)
	p.r.setOnEvent(func(frame []byte) {
		select {
		case spool <- captured{t: time.Now(), frame: frame}:
		default: // spool full: keep draining, drop for the printer
		}
	})
	defer p.r.setOnEvent(nil)
	start := time.Now()
	timer := time.NewTimer(probeListenWindow)
	defer timer.Stop()
	printed, other := 0, 0
loop:
	for {
		select {
		case <-ctx.Done():
			p.printf("  (interrupted: %v)\n", ctx.Err())
			break loop
		case c := <-spool:
			if c.frame[0] != hidppReportLong && c.frame[0] != hidppReportShort {
				other++
				continue
			}
			printed++
			if printed <= probeSpoolPrint {
				p.printf("  +%5.2fs %s%s\n", c.t.Sub(start).Seconds(), hexFrame(c.frame), p.eventLabel(c.frame))
			}
		case <-timer.C:
			break loop
		}
	}
	if printed > probeSpoolPrint {
		p.printf("  (%d further HID++ frames suppressed)\n", printed-probeSpoolPrint)
	}
	p.printf("  window: %d HID++ frames, %d other reports\n", printed, other)

	st := p.r.snapshot()
	p.printf("  totals since open (%d frames; lengths observed, not from enumerate):\n", st.total)
	for _, id := range slices.Sorted(maps.Keys(st.byID)) {
		p.printf("    report 0x%02X: %d frames, lengths %v\n", id, st.byID[id], st.lens[id])
	}
	p.printf("  unsolicited HID++ events (swId 0): %d; foreign-swId HID++ frames: %d\n", st.events, st.foreign)
	if st.readErr != nil {
		p.printf("  reader stopped early: %v\n", st.readErr)
	}
	p.printf("\n")
}

// eventLabel decorates the notifications the design cares about: a 0x1004
// battery push (event addr 0x00) and 0x1D4B wireless-device-status.
func (p *logiretchProbe) eventLabel(frame []byte) string {
	if len(frame) < 4 || frame[1] != p.devIdx {
		return ""
	}
	if e, ok := p.features[featUnifiedBattery]; ok && frame[2] == e.Index && frame[3] == 0x00 {
		if st, ok := decodeBatteryStatus(frame[4:]); ok {
			return fmt.Sprintf("  <- 0x1004 battery push: SoC %d%%, %s", st.SoC, st.chargingString())
		}
		return "  <- 0x1004 battery push"
	}
	if e, ok := p.features[featWirelessStatus]; ok && frame[2] == e.Index {
		return "  <- 0x1D4B wireless-device-status event"
	}
	return ""
}

// step 10: Options+ coexistence, inferred from shared-handle traffic: a
// reply carrying a foreign nonzero swId means another HID++ master was
// talking on this collection during the run.
func (p *logiretchProbe) step10Coexistence() {
	p.printf("== step 10: Options+ coexistence ==\n")
	if st := p.r.snapshot(); st.foreign > 0 {
		p.printf("  %d foreign-swId HID++ frames observed: another HID++ master (Options+?) was active\n  on this collection and our swId-matched requests still completed -- coexistence held\n\n", st.foreign)
	} else {
		p.printf("  no foreign-swId HID++ traffic observed (Options+ idle or not running);\n  rerun with Options+ running to document seize-fail vs coexistence (spike bullet)\n\n")
	}
}

func (p *logiretchProbe) verdict() {
	mark := func(b bool) string {
		if b {
			return "[x]"
		}
		return "[ ]"
	}
	p.printf("== SPIKE VERDICT ==\n")
	p.printf("  %s access: non-exclusive open + HID++ 2.0 ping\n", mark(p.accessOK))
	p.printf("  %s battery: 0x1004 getStatus decoded\n", mark(p.batteryOK))
	p.printf("  %s numbered write: leading-0x11 Write round trip\n", mark(p.writeOK))
	p.printf("  %s feature table: PRESENT/ABSENT expectations hold\n", mark(p.absencesOK))
	if p.accessOK && p.batteryOK && p.writeOK && p.absencesOK {
		p.printf("  GO: the logi module's transport assumptions hold on this device\n")
	} else {
		p.printf("  NO-GO (or partial): the logi module design gates on the unchecked items above\n")
	}
}
