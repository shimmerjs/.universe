// logiretch.go: the persistent MX Master 4 module (magicbus-design.md logi
// section; logi-replacement-design.md phase 5/5b). It surfaces battery over
// the proven 0x1004 getStatus poll, applies desired device settings from
// config on every (re)connect, and resets Options+ 1B04 divert residue on
// takeover. It NEVER seizes (shared open only, Options+/Keymapp-style
// coexistence) and owns no backoff of its own -- the shared scanner does.
//
// ON-DEVICE EFFECT IS UNVERIFIED. The setters (DPI, SmartShift, hi-res wheel,
// thumbwheel, haptic, button remaps) build frames from Solaar/logiops-sourced
// function ids (cited in hidpp.go), but nothing here has been confirmed to
// change the physical device: with Options+ installed both HID++ masters share
// the vendor node and Options+ will re-divert, so takeoverReset is only
// meaningful AFTER Options+ is uninstalled (logi-replacement-design.md:20,94).
// The module logs every setter it sends plus the read-back echo so the effect
// can be confirmed from touchd.log post-Options+-removal.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	// logiPID is the MX Master 4 product id over direct Bluetooth, confirmed
	// by the logiretch-0 spike (logi-replacement-design.md:10). The arrival
	// scanner matches the full VID/PID/usage tuple, so the module pins the
	// spike-observed PID; a Mac-variant PID (unobserved, low risk per the
	// spike list) would need this updated. Matching is otherwise by VID+usage,
	// never hid.Open(vid,pid).
	logiPID = 0xB042

	logiBatteryDefaultSec = 120
	logiBatteryMinSec     = 60
	logiBatteryMaxSec     = 300

	// logiApplyRetries bounds the connect-time apply retry leg: steps that
	// failed on a transport error are re-attempted on at most this many
	// link-live battery ticks, then dropped loudly.
	logiApplyRetries = 3

	// 0x1004 charging-state enum values that count as "charging" (per
	// batteryStatus.chargingString: 1 = charging, 2 = charging slow).
	logiStateCharging     = 1
	logiStateChargingSlow = 2
)

// logiMatch is the vendor HID++ collection over direct BT (never usage-page-1
// pointer collections, never OpenExclusive).
var logiMatch = Match{VID: logiVID, PID: logiPID, UsagePage: logiUsagePage, Usage: logiUsage}

// LogiState is one line on logiretch.sock; the bus battery-icon consumer
// (out of this cut) reads this shape. Wire shape is PINNED:
//
//	{"t": <unix ns>, "kind": "battery", "soc": n, "charging": bool, "state": n}
//
// soc is 0-100; charging is derived from the raw charging-state enum; state is
// that raw enum. Published on connect and on each poll interval; the
// broadcaster is fire-and-forget with no retained replay.
type LogiState struct {
	T        int64  `json:"t"`
	Kind     string `json:"kind"`
	SoC      int    `json:"soc"`
	Charging bool   `json:"charging"`
	State    int    `json:"state"`
}

// logiretchModule is one HID source: config is baked at construction (daemon.go
// registration), mirroring edgeModule.
type logiretchModule struct {
	cfg logiConfig
}

func (m *logiretchModule) Name() string { return "logiretch" }

// Run awaits the vendor collection, opens it shared, and runs a session per
// open, reopening via the shared scanner on device loss. Absence is quiet (a
// napping/unpaired mouse is normal): only the first open failure of an episode
// and each (re)connect are logged.
func (m *logiretchModule) Run(ctx context.Context, env Env) error {
	loggedFail := false
	for {
		path, err := env.AwaitDevice(ctx, logiMatch)
		if err != nil {
			return nil
		}
		dev, err := env.OpenShared(path)
		if err != nil {
			if !loggedFail {
				fmt.Fprintf(os.Stderr, "logiretch open (Input Monitoring granted?): %v (quiet retry, backoff caps at %s)\n", err, reconnectCap)
				loggedFail = true
			}
			continue
		}
		loggedFail = false
		fmt.Println("logiretch open (shared)")
		conn := newHidppConn(dev)
		m.session(ctx, env, conn)
		conn.close()
		dev.Close()
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintln(os.Stderr, "logiretch: device lost, reopening")
	}
}

// session runs one open: the once-per-open connect sequence, then a single
// steady-state timer for the battery poll, the bounded pending-apply retry,
// plus a 0x1D4B re-apply trigger. It returns on ctx cancel or reader death
// (device loss -> reopen). A request timeout on a live reader (sleeping mouse)
// is non-fatal: log, keep the handle, retry next interval.
func (m *logiretchModule) session(ctx context.Context, env Env, conn *hidppConn) {
	s := &logiSession{m: m, env: env, conn: conn}
	s.connect(ctx)
	if conn.readError() != nil {
		return
	}

	reconf := make(chan struct{}, 1)
	s.watchReconf(reconf)
	defer conn.setOnEvent(nil)

	interval := m.batteryInterval()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-conn.done:
			// reader died (device loss): return so Run reopens promptly
			// instead of waiting up to a poll interval to notice
			return
		case <-timer.C:
			if s.feats == nil {
				// connect never completed (mouse asleep at open); retry the
				// whole sequence -- the mouse may have woken
				s.connect(ctx)
				s.watchReconf(reconf)
			} else if s.pollBattery(ctx) {
				// battery answered, so the link is live: spend a pending-apply
				// retry against an awake mouse, never a sleeping one
				s.retryApply(ctx)
			}
			if conn.readError() != nil {
				return
			}
			timer.Reset(interval)
		case <-reconf:
			fmt.Println("logiretch: 0x1D4B reconnection -- re-applying takeover + config")
			s.reapply(ctx)
			if conn.readError() != nil {
				return
			}
		}
	}
}

// reapply re-runs the divert takeover and the config setters on a live handle
// (0x1D4B reconnection). The feature table is still valid, so it skips the
// ping/resolve; a full device-loss reopen re-runs connect() from scratch.
func (s *logiSession) reapply(ctx context.Context) {
	if s.feats == nil {
		return
	}
	s.startApply(ctx)
}

// startApply runs the full takeover+config apply and arms the bounded retry
// leg for any steps that failed on a transport error (mouse asleep at
// connect). A device-refused step is dropped, never retried.
func (s *logiSession) startApply(ctx context.Context) {
	s.pending = s.applySteps(ctx, stepAll)
	s.retries = logiApplyRetries
	if s.pending != 0 {
		fmt.Fprintf(os.Stderr, "logiretch: apply incomplete (%s failed on transport); retrying on up to %d battery ticks\n", s.pending, s.retries)
	}
}

// retryApply re-attempts the pending steps on a link-live battery tick. A step
// that succeeds is cleared and never re-fires; after logiApplyRetries failing
// ticks the remainder is dropped loudly (the next reconnect or 0x1D4B re-apply
// covers it).
func (s *logiSession) retryApply(ctx context.Context) {
	if s.pending == 0 {
		return
	}
	attempt := s.pending
	s.pending = s.applySteps(ctx, attempt)
	if done := attempt &^ s.pending; done != 0 {
		fmt.Printf("logiretch: apply retry succeeded: %s\n", done)
	}
	if s.pending == 0 {
		return
	}
	s.retries--
	if s.retries <= 0 {
		fmt.Fprintf(os.Stderr, "logiretch: apply retries EXHAUSTED, %s NOT applied; next reconnect re-applies\n", s.pending)
		s.pending = 0
	}
}

// batteryInterval is the poll cadence: default 120s, clamped 60-300s. NEVER
// per-tick (khudson-constant-cost-invariant): one timer, O(1) per fire.
func (m *logiretchModule) batteryInterval() time.Duration {
	sec := logiBatteryDefaultSec
	if m.cfg.BatteryPollSec != nil {
		sec = *m.cfg.BatteryPollSec
	}
	sec = min(max(sec, logiBatteryMinSec), logiBatteryMaxSec)
	return time.Duration(sec) * time.Second
}

func (m *logiretchModule) takeoverResetEnabled() bool {
	return m.cfg.TakeoverReset == nil || *m.cfg.TakeoverReset
}

// buttonRemaps indexes the configured CID->target remaps for the 1B04 walk.
func (m *logiretchModule) buttonRemaps() map[uint16]uint16 {
	if len(m.cfg.Buttons) == 0 {
		return nil
	}
	out := make(map[uint16]uint16, len(m.cfg.Buttons))
	for _, b := range m.cfg.Buttons {
		out[uint16(b.CID)] = uint16(b.Remap)
	}
	return out
}

// applyStep is a bitmask over the connect-time apply steps; a step that fails
// on a transport-class error stays pending and retries on the battery tick.
type applyStep uint8

const (
	stepTakeover applyStep = 1 << iota
	stepDPI
	stepSmartShift
	stepHires
	stepThumb
	stepHaptic

	stepAll = stepTakeover | stepDPI | stepSmartShift | stepHires | stepThumb | stepHaptic
)

var applyStepNames = []struct {
	bit  applyStep
	name string
}{
	{stepTakeover, "takeoverReset"},
	{stepDPI, "dpi"},
	{stepSmartShift, "smartShift"},
	{stepHires, "hiresWheel"},
	{stepThumb, "thumbwheel"},
	{stepHaptic, "haptic"},
}

func (m applyStep) String() string {
	var parts []string
	for _, e := range applyStepNames {
		if m&e.bit != 0 {
			parts = append(parts, e.name)
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

// transportErr reports whether err is transport-class (write failure, timeout,
// dead reader) rather than a device-answered HID++ refusal. Only transport
// failures earn a retry: a refusal would just refuse again.
func transportErr(err error) bool {
	var he hidppError
	return err != nil && !errors.As(err, &he)
}

// logiSession is the per-open state: the conn, the resolved device index, and
// the feature table (resolved FRESH every connect, never a hardcoded index).
type logiSession struct {
	m        *logiretchModule
	env      Env
	conn     *hidppConn
	devIdx   byte
	feats    map[uint16]featureEntry
	watching bool
	pending  applyStep // apply steps awaiting a transport retry
	retries  int       // failing link-live ticks left before pending drops
}

// connect runs the once-per-open sequence in order; each step is logged and a
// failing step does NOT abort the others (a napping subsystem should not block
// the battery read or vice versa). Device loss surfaces via conn.readError,
// checked by the caller.
func (s *logiSession) connect(ctx context.Context) {
	if err := s.resolveFeatures(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "logiretch: feature resolution failed: %v\n", err)
		return
	}
	fmt.Printf("logiretch: connected devIdx=0x%02X, %d features resolved\n", s.devIdx, len(s.feats))
	s.startApply(ctx)
	s.pollBattery(ctx)
}

// resolveFeatures pings to establish the working device index (BLE 0xFF, then
// the 0x00 fallback) and walks IFeatureSet, id-keyed. A device-loss error
// propagates; a transient per-feature failure is skipped (the probe saw one
// under Options+ contention).
func (s *logiSession) resolveFeatures(ctx context.Context) error {
	idx, err := s.ping(ctx)
	if err != nil {
		return err
	}
	res, err := s.conn.request(ctx, idx, rootFeatIdx, fnRootGetFeature, byte(featFeatureSet>>8), byte(featFeatureSet))
	if err != nil {
		return fmt.Errorf("getFeature(FEATURE_SET): %w", err)
	}
	ifsIdx, _, _, ok := decodeGetFeature(res.params)
	if !ok || ifsIdx == 0 {
		return errors.New("IFeatureSet absent")
	}
	res, err = s.conn.request(ctx, idx, ifsIdx, fnSetGetCount)
	if err != nil {
		return fmt.Errorf("getCount: %w", err)
	}
	count, ok := decodeCount(res.params)
	if !ok {
		return errors.New("malformed getCount reply")
	}
	feats := map[uint16]featureEntry{}
	dropped := 0
	for n := 1; n <= int(count); n++ {
		i := byte(n)
		res, err := s.conn.request(ctx, idx, ifsIdx, fnSetGetFeatureID, i)
		if err != nil {
			if s.conn.readError() != nil {
				return err
			}
			dropped++ // transient (probe saw one under Options+ contention)
			continue
		}
		if e, ok := decodeFeatureEntry(i, res.params); ok {
			feats[e.ID] = e
		}
	}
	// don't commit a partial walk that dropped the battery feature: the
	// session only re-resolves while feats==nil, so a committed battery-less
	// map would surface no battery for the whole handle life. A genuinely
	// absent battery (no drops) is committed as-is and simply skipped.
	if _, ok := feats[featUnifiedBattery]; !ok && dropped > 0 {
		return fmt.Errorf("feature walk incomplete (%d dropped, battery missing); retrying", dropped)
	}
	s.devIdx = idx
	s.feats = feats
	return nil
}

func (s *logiSession) ping(ctx context.Context) (byte, error) {
	const marker = 0x5A
	var lastErr error
	for _, idx := range []byte{hidppDevIdxBLE, hidppDevIdxFallback} {
		res, err := s.conn.request(ctx, idx, rootFeatIdx, fnRootPing, 0x00, 0x00, marker)
		if err != nil {
			lastErr = err
			if s.conn.readError() != nil {
				return 0, err // device loss: stop trying indices
			}
			continue
		}
		if _, _, echo, ok := decodePing(res.params); ok && echo == marker {
			return idx, nil
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no HID++ ping reply on either device index")
	}
	return 0, lastErr
}

// watchReconf wires the conn's onEvent to a 0x1D4B reconfNeeded watcher once
// the feature table is known. Identical repeats are cookie-deduped and the
// buffered-1 channel coalesces a reconnect burst so a re-apply never thrashes.
func (s *logiSession) watchReconf(reconf chan struct{}) {
	if s.watching || s.feats == nil {
		return
	}
	ws, ok := s.feats[featWirelessStatus]
	if !ok {
		return
	}
	s.watching = true
	devIdx := s.devIdx
	s.conn.setOnEvent(func(frame []byte) {
		if !isWirelessReconf(frame, devIdx, ws.Index) {
			return
		}
		// the buffered-1 channel coalesces a reconnection burst to one pending
		// signal; a later reconnection re-signals once this one is consumed
		select {
		case reconf <- struct{}{}:
		default:
		}
	})
}

// pollBattery reads 0x1004 getStatus and publishes a LogiState line; it
// reports whether the device answered at all (the link-live gate for the
// apply retry leg). A live reader timing out (mouse asleep) is non-fatal:
// log, keep the handle.
func (s *logiSession) pollBattery(ctx context.Context) bool {
	e, ok := s.feats[featUnifiedBattery]
	if !ok {
		s.skip("battery", featUnifiedBattery)
		return true // no probe available; let the retry leg probe for itself
	}
	res, err := s.conn.request(ctx, s.devIdx, e.Index, fnBatteryGetStatus)
	if err != nil {
		if s.conn.readError() != nil {
			return false // device loss -> caller reopens
		}
		fmt.Fprintf(os.Stderr, "logiretch: battery getStatus timed out (mouse asleep?), keeping handle: %v\n", err)
		return !transportErr(err)
	}
	st, ok := decodeBatteryStatus(res.params)
	if !ok {
		fmt.Fprintf(os.Stderr, "logiretch: malformed battery reply: %s\n", hexFrame(res.raw))
		return true
	}
	state := LogiState{
		T:        time.Now().UnixNano(),
		Kind:     "battery",
		SoC:      int(st.SoC),
		Charging: st.State == logiStateCharging || st.State == logiStateChargingSlow,
		State:    int(st.State),
	}
	s.env.Publish(state)
	fmt.Printf("logiretch: battery %d%% state=%d charging=%v\n", state.SoC, state.State, state.Charging)
	return true
}

// takeoverReset walks 1B04 and, for each control, either applies a configured
// remap (a CID in cfg.buttons) or clears the divert+rawXY residue Options+
// leaves (only for controls that show diverted). This is the ONLY
// setCidReporting call site; it never creates a divert. Guarded by
// cfg.takeoverReset (default true) -- since the remap path shares this single
// call site, disabling the reset also disables configured remaps (logged).
// Returns true when any part of the walk failed on a transport error and the
// whole step should retry (the retry re-reads divert state, so a control
// already cleared is not re-written).
func (s *logiSession) takeoverReset(ctx context.Context) bool {
	remaps := s.m.buttonRemaps()
	if !s.m.takeoverResetEnabled() {
		if len(remaps) > 0 {
			fmt.Fprintf(os.Stderr, "logiretch: takeoverReset disabled; %d configured button remap(s) not applied (they share the takeoverReset call site)\n", len(remaps))
		} else {
			fmt.Println("logiretch: takeoverReset disabled by config")
		}
		return false
	}
	e, ok := s.feats[featReprogControls]
	if !ok {
		s.skip("takeoverReset", featReprogControls)
		return false
	}
	res, err := s.conn.request(ctx, s.devIdx, e.Index, fnCtrlGetCount)
	if err != nil {
		s.stepErr("takeoverReset getControlCount", err)
		return transportErr(err)
	}
	count, ok := decodeCount(res.params)
	if !ok {
		fmt.Fprintf(os.Stderr, "logiretch: malformed getControlCount reply: %s\n", hexFrame(res.raw))
		return false
	}
	retry := false
	for i := byte(0); i < count; i++ {
		res, err := s.conn.request(ctx, s.devIdx, e.Index, fnCtrlGetCidInfo, i)
		if err != nil {
			s.stepErr(fmt.Sprintf("takeoverReset getCidInfo[%d]", i), err)
			if s.conn.readError() != nil {
				return true
			}
			retry = retry || transportErr(err)
			continue
		}
		ci, ok := decodeCidInfo(res.params)
		if !ok {
			continue
		}
		res, err = s.conn.request(ctx, s.devIdx, e.Index, fnCtrlGetCidReporting, byte(ci.CID>>8), byte(ci.CID))
		if err != nil {
			s.stepErr(fmt.Sprintf("takeoverReset getCidReporting cid 0x%04X", ci.CID), err)
			if s.conn.readError() != nil {
				return true
			}
			retry = retry || transportErr(err)
			continue
		}
		cr, ok := decodeCidReporting(res.params)
		if !ok || cr.CID != ci.CID {
			continue // stale/malformed: do not attribute divert state
		}
		if target, want := remaps[ci.CID]; want {
			retry = s.setCidReporting(ctx, e.Index, ci.CID, cidRemapParams(ci.CID, target), "remap") || retry
			continue
		}
		if cr.Flags&(mapDiverted|mapRawXY) == 0 {
			continue // not diverted: leave the control untouched
		}
		retry = s.setCidReporting(ctx, e.Index, ci.CID, cidClearDivertParams(ci.CID), "clear-divert") || retry
	}
	return retry
}

// setCidReporting issues one 1B04 write, then re-reads to confirm the CID echo
// and the resulting flags (the on-device confirmation the log carries).
// Returns true when the write itself failed on a transport error (a failed
// readback still means the write was sent, so it never retries).
func (s *logiSession) setCidReporting(ctx context.Context, idx byte, cid uint16, params []byte, kind string) bool {
	if _, err := s.conn.request(ctx, s.devIdx, idx, fnCtrlSetCidReporting, params...); err != nil {
		fmt.Fprintf(os.Stderr, "logiretch: setCidReporting %s cid 0x%04X FAILED (params %s): %v\n", kind, cid, hexFrame(params), err)
		return transportErr(err)
	}
	rb, err := s.conn.request(ctx, s.devIdx, idx, fnCtrlGetCidReporting, byte(cid>>8), byte(cid))
	if err != nil {
		fmt.Printf("logiretch: setCidReporting %s cid 0x%04X sent (params %s); readback failed: %v\n", kind, cid, hexFrame(params), err)
		return false
	}
	cr, ok := decodeCidReporting(rb.params)
	fmt.Printf("logiretch: setCidReporting %s cid 0x%04X sent (params %s); readback flags=0x%02X(%s) remap=0x%04X echo-ok=%v\n",
		kind, cid, hexFrame(params), cr.Flags, divertString(cr.Flags), cr.Remap, ok && cr.CID == cid)
	return false
}

// applySteps runs the steps in mask: the divert takeover plus each PRESENT
// config setter, reading back and logging every write's echo. A mismatch or
// HID++ error is logged loudly and skipped (never aborts, never retries in a
// storm); the returned mask holds the steps that failed on a transport error
// and should retry. Config setters do NOT touch 1B04 -- button remaps ride
// the takeoverReset call site.
func (s *logiSession) applySteps(ctx context.Context, mask applyStep) applyStep {
	if s.feats == nil {
		return 0
	}
	c := s.m.cfg
	var failed applyStep
	if mask&stepTakeover != 0 && s.takeoverReset(ctx) {
		failed |= stepTakeover
	}
	if mask&stepDPI != 0 && c.DPI != nil && s.applyDPI(ctx, *c.DPI) {
		failed |= stepDPI
	}
	if mask&stepSmartShift != 0 && c.SmartShift != nil && s.applySmartShift(ctx, c.SmartShift) {
		failed |= stepSmartShift
	}
	if mask&stepHires != 0 && c.HiresWheel != nil && s.applyHires(ctx, *c.HiresWheel) {
		failed |= stepHires
	}
	if mask&stepThumb != 0 && c.Thumbwheel != nil && s.applyThumb(ctx, *c.Thumbwheel) {
		failed |= stepThumb
	}
	if mask&stepHaptic != 0 && c.Haptic != nil && s.applyHaptic(ctx, *c.Haptic) {
		failed |= stepHaptic
	}
	return failed
}

func (s *logiSession) applyDPI(ctx context.Context, want int) bool {
	e, ok := s.feats[featAdjustableDPI]
	if !ok {
		s.skip("dpi", featAdjustableDPI)
		return false
	}
	var list []uint16
	if res, err := s.conn.request(ctx, s.devIdx, e.Index, fnDpiGetList, 0x00, 0x00, 0x00); err == nil && len(res.params) >= 2 {
		list = parseDpiList(res.params[1:])
	}
	target := snapDPI(list, uint16(want))
	params := dpiSetParams(target)
	res, err := s.conn.request(ctx, s.devIdx, e.Index, fnDpiSet, params...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logiretch: dpi set FAILED (params %s, requested %d, snapped %d): %v\n", hexFrame(params), want, target, err)
		return transportErr(err)
	}
	got := "?"
	if rb, err := s.conn.request(ctx, s.devIdx, e.Index, fnDpiGet, 0x00); err == nil && len(rb.params) >= 3 {
		got = fmt.Sprintf("%d", be16(rb.params[1:3]))
	}
	fmt.Printf("logiretch: dpi set=%d (requested %d) params %s; readback=%s reply %s\n", target, want, hexFrame(params), got, hexFrame(res.raw))
	return false
}

func (s *logiSession) applySmartShift(ctx context.Context, c *smartShiftConfig) bool {
	e, ok := s.feats[featSmartShiftEnh]
	if !ok {
		s.skip("smartShift", featSmartShiftEnh)
		return false
	}
	mode := ptrByte(c.Mode)
	torque := ptrByte(c.Torque)
	var threshold byte
	if c.Threshold != nil {
		t := *c.Threshold
		switch {
		case t < 0:
			t = 0
		case t >= 50: // Solaar: threshold >= MAX_VALUE selects the max (255)
			t = 255
		}
		threshold = byte(t)
	}
	params := smartShiftSetParams(mode, threshold, torque)
	res, err := s.conn.request(ctx, s.devIdx, e.Index, fnSmartShiftSet, params...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logiretch: smartShift set FAILED (params %s): %v\n", hexFrame(params), err)
		return transportErr(err)
	}
	fmt.Printf("logiretch: smartShift set mode=%d threshold=%d torque=%d params %s; reply %s\n", mode, threshold, torque, hexFrame(params), hexFrame(res.raw))
	return false
}

func (s *logiSession) applyHires(ctx context.Context, hires bool) bool {
	e, ok := s.feats[featHiresWheel]
	if !ok {
		s.skip("hiresWheel", featHiresWheel)
		return false
	}
	// MX4 quirk: logitune ships smooth scroll DISABLED because the HID++
	// config misbehaves on this line; only touched when explicitly configured.
	fmt.Fprintf(os.Stderr, "logiretch: hiresWheel is EXPERIMENTAL on the MX4 (logitune ships it disabled); applying %v anyway\n", hires)
	params := hiresSetParams(hires)
	res, err := s.conn.request(ctx, s.devIdx, e.Index, fnHiresSet, params...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logiretch: hiresWheel set FAILED (params %s): %v\n", hexFrame(params), err)
		return transportErr(err)
	}
	fmt.Printf("logiretch: hiresWheel set=%v params %s; reply %s\n", hires, hexFrame(params), hexFrame(res.raw))
	return false
}

func (s *logiSession) applyThumb(ctx context.Context, invert bool) bool {
	e, ok := s.feats[featThumbWheel]
	if !ok {
		s.skip("thumbwheel", featThumbWheel)
		return false
	}
	params := thumbSetParams(invert)
	res, err := s.conn.request(ctx, s.devIdx, e.Index, fnThumbSet, params...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logiretch: thumbwheel set FAILED (params %s): %v\n", hexFrame(params), err)
		return transportErr(err)
	}
	fmt.Printf("logiretch: thumbwheel invert=%v params %s; reply %s\n", invert, hexFrame(params), hexFrame(res.raw))
	return false
}

func (s *logiSession) applyHaptic(ctx context.Context, level int) bool {
	e, ok := s.feats[featHaptic]
	if !ok {
		s.skip("haptic", featHaptic)
		return false
	}
	level = min(max(level, 0), 100)
	params := hapticSetParams(byte(level))
	res, err := s.conn.request(ctx, s.devIdx, e.Index, fnHapticSet, params...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "logiretch: haptic set FAILED (params %s): %v\n", hexFrame(params), err)
		return transportErr(err)
	}
	fmt.Printf("logiretch: haptic level=%d params %s; reply %s\n", level, hexFrame(params), hexFrame(res.raw))
	return false
}

func (s *logiSession) skip(name string, feat uint16) {
	fmt.Fprintf(os.Stderr, "logiretch: feature 0x%04X absent, %s skipped\n", feat, name)
}

func (s *logiSession) stepErr(what string, err error) {
	fmt.Fprintf(os.Stderr, "logiretch: %s: %v\n", what, err)
}

// ptrByte is *int with nil = 0 (the HID++ "leave unchanged" convention).
func ptrByte(p *int) byte {
	if p == nil {
		return 0
	}
	return byte(*p)
}
