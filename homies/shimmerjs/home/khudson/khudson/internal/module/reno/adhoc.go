// Ad hoc recording controls: the pending RecordConfig (audio device,
// screens + regions, webcam preset), the probed OBS inventory feeding
// the recorder view's menus, and scriptless speaker notes. Every
// mutation lands on the same core the script headers drive -- arm copies
// one config shape into the journal regardless of who produced it.
package reno

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// Recorder-view config acts (in-process, like the goto menu): each tap
// mutates the pending config; the change applies at the next arm.
const (
	actCfgAudio  = "reno:cfg-audio"  // + device value | none | default
	actCfgScreen = "reno:cfg-screen" // + display value (toggles membership)
	actCfgWebcam = "reno:cfg-webcam" // + preset | default
	actProbe     = "reno:probe"
	actArm       = "reno:arm"
	actWindow    = "reno:obs-window"
	actVCam      = "reno:obs-vcam"
	actOpenClip  = "reno:open-clip"
	actFXMove    = "reno:fx-move" // + placement preset
	actFXHide    = "reno:fx-hide"
	actFXLevel   = "reno:fx-level" // + level fx kind + up|down
)

// cfgDefault is the menu value for "leave OBS alone" (clears the knob).
const cfgDefault = "default"

// The two directions a level tile walks its effect in.
const (
	levelUp   = "up"
	levelDown = "down"
)

// applyWedge bounds an in-flight live apply. The latch clears on the
// verb's note, but a ctl that never lands (bus restart mid-apply, a
// killed child) would otherwise hold it set forever and silently stop
// applying every later tap. Past this the next change fires regardless.
const applyWedge = 30 * time.Second

// armWedge bounds an in-flight standby. Far longer than applyWedge
// because standby may be COLD-LAUNCHING OBS, and the launch path budgets
// two minutes plus a watchdog margin -- releasing the latch sooner would
// fire a second launch at an app that is still starting.
const armWedge = 3 * time.Minute

// probeWedge bounds an in-flight one-shot that only reads and writes state
// somewhere already running -- the window toggle (an AX write plus a
// readback), the virtual-camera sweep (one socket round trip). Anything past
// a few seconds is a lost child, not slowness.
const probeWedge = 10 * time.Second

// maxNotesBytes bounds an ad hoc notes file read: notes are prose for a
// talk, not a data file; a fat misload must not balloon the bus.
const maxNotesBytes = 1 << 20

// ApplyRequest is the apply-begin ctl payload the live-apply verb
// consumes. A nil Config means nothing is configured yet and the
// verb has nothing to drive.
type ApplyRequest struct {
	Config *renocore.RecordConfig `json:"config,omitempty"`
}

// setRecordConfigLocked replaces the pending config from the ctl seam
// (`khudson reno config` marshals its flags). --clear resets. Caller
// holds mu.
func (m *Mod) setRecordConfigLocked(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "--clear":
		m.desired = renocore.RecordConfig{}
		return nil
	case len(args) == 1:
		var cfg renocore.RecordConfig
		if err := json.Unmarshal([]byte(args[0]), &cfg); err != nil {
			return fmt.Errorf("reno record-config: %w", err)
		}
		if cfg.Webcam != "" {
			if err := renocore.CheckWebcam(cfg.Webcam); err != nil {
				return fmt.Errorf("reno record-config: %w", err)
			}
		}
		m.desired = cfg
		return nil
	}
	return errors.New("reno record-config: need one JSON payload or --clear")
}

// setNotesLocked loads (or clears) the scriptless speaker notes. The
// path must be absolute -- it crosses from the CLI process to the bus.
// Caller holds mu.
func (m *Mod) setNotesLocked(args []string) error {
	if len(args) != 1 {
		return errors.New("reno notes: need a markdown path or --clear")
	}
	if args[0] == "--clear" {
		m.notes, m.notesPath = "", ""
		return nil
	}
	path := args[0]
	if !filepath.IsAbs(path) {
		return fmt.Errorf("reno notes: %q must be absolute (the bus reads it, not your shell)", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("reno notes: %w", err)
	}
	if !info.Mode().IsRegular() {
		// the read runs in the bus under the module lock: a FIFO here
		// would wedge every ctl and act (arc-2 review #18)
		return fmt.Errorf("reno notes: %s is not a regular file", path)
	}
	if info.Size() > maxNotesBytes {
		return fmt.Errorf("reno notes: %s is %d bytes (cap %d) -- that is not a notes file", path, info.Size(), maxNotesBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reno notes: %w", err)
	}
	m.notes, m.notesPath = string(body), path
	return nil
}

// inventoryFile is the probed OBS enumeration, cached on disk. Every
// switch restarts the bus, and an in-memory-only inventory meant the
// recorder pad came back COLD after every system update -- the device
// menus are the ad hoc controls, so losing them loses the surface.
// Re-probing costs an OBS launch, so it must stay a deliberate tap.
const inventoryFile = "inventory.json"

// configFile is the ad hoc recording posture, cached on disk beside the
// inventory. Desired state that evaporates on a bus restart is just a
// bug: every switch restarts the bus, and losing the screen/mic/cam
// selection mid-setup reads as the pad forgetting what you told it.
const configFile = "config.json"

// levelsFile is where the level effects' magnitudes are cached. The
// filters they drive live in OBS's own scene collection and survive a bus
// restart; the level does not read back out of them, so khudson has to
// remember it or the tile comes back saying off over a camera that is
// still purple.
const levelsFile = "fxlevels.json"

// readState loads a JSON cache from the state dir into v, reporting
// whether it landed. A missing file is the ordinary cold case and stays
// quiet; anything else is logged and treated as absent -- a corrupt
// cache must degrade to empty, never wedge the boot.
func (m *Mod) readState(name string, v any) bool {
	if m.stateDir == "" {
		return false
	}
	body, err := os.ReadFile(filepath.Join(m.stateDir, name))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("khudson reno: %s: %v", name, err)
		}
		return false
	}
	if err := json.Unmarshal(body, v); err != nil {
		log.Printf("khudson reno: %s: %v", name, err)
		return false
	}
	return true
}

// writeState caches v for the next bus, tmp+rename so a torn file can
// never fail the next unmarshal. Best effort: a failed write costs the
// cache, never the running surface.
func (m *Mod) writeState(name string, v any) {
	if m.stateDir == "" {
		return
	}
	body, err := json.Marshal(v)
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.stateDir, 0o700); err != nil {
		log.Printf("khudson reno: %s: %v", name, err)
		return
	}
	tmp := filepath.Join(m.stateDir, name+".next")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		log.Printf("khudson reno: %s: %v", name, err)
		return
	}
	if err := os.Rename(tmp, filepath.Join(m.stateDir, name)); err != nil {
		log.Printf("khudson reno: %s: %v", name, err)
	}
}

// recoverInventory reloads the cached probe. There is no age at which it
// stops loading: the pad renders it and offers a re-probe (the At stamp
// carries the age), which beats booting cold with dead menus. What escalates
// is identity, not age -- see displaysMoved.
func (m *Mod) recoverInventory() {
	var inv renocore.ObsInventory
	if !m.readState(inventoryFile, &inv) {
		return
	}
	m.inventory = &inv
	log.Printf("khudson reno: obs inventory cache: %d displays, %d mics, %d cams (%s old)",
		len(inv.Screens), len(inv.Mics), len(inv.Cams), m.now().Sub(inv.At).Round(time.Second))
}

// saveInventoryLocked caches the probe for the next bus. Caller holds mu.
func (m *Mod) saveInventoryLocked() {
	if m.inventory == nil {
		return
	}
	m.writeState(inventoryFile, m.inventory)
}

// recoverConfig reloads the ad hoc posture. It does NOT re-apply it to
// OBS: a bus start must not reach out and mutate a scene nobody asked it
// to touch, and OBS may not even be running. The next change applies the
// whole config, and a record arms with it regardless.
func (m *Mod) recoverConfig() {
	var cfg renocore.RecordConfig
	if !m.readState(configFile, &cfg) || cfg.Empty() {
		return
	}
	m.desired = cfg
	log.Printf("khudson reno: recovered record config: %s", cfg.Summary())
}

// saveConfigLocked persists the posture. Caller holds mu.
func (m *Mod) saveConfigLocked() { m.writeState(configFile, m.desired) }

// recoverFXLevels reloads the level effects' magnitudes. Like the config
// this does NOT re-assert them into OBS: the filters are still whatever
// the scene collection holds, and a bus start must not reach out and
// change a camera nobody asked it to touch. Unknown kinds are dropped, so
// a level for an effect that no longer exists cannot resurrect a tile.
func (m *Mod) recoverFXLevels() {
	var levels map[string]int
	if !m.readState(levelsFile, &levels) {
		return
	}
	for kind, level := range levels {
		if !renocore.IsLevelFX(kind) || level <= 0 || level > renocore.FXMaxLevel {
			continue
		}
		if m.fxLevels == nil {
			m.fxLevels = map[string]int{}
		}
		m.fxLevels[kind] = level
	}
}

// bumpFXLevelLocked walks one level effect a step and returns the effect
// to fire. At either end of the range it returns false: the tile is dim
// there and has nothing to send. Caller holds mu.
func (m *Mod) bumpFXLevelLocked(kind string, up bool) (renocore.CamFX, bool) {
	if !renocore.IsLevelFX(kind) {
		return renocore.CamFX{}, false
	}
	next := m.fxLevels[kind] - 1
	if up {
		next = m.fxLevels[kind] + 1
	}
	if next < 0 || next > renocore.FXMaxLevel {
		return renocore.CamFX{}, false
	}
	if m.fxLevels == nil {
		m.fxLevels = map[string]int{}
	}
	m.fxLevels[kind] = next
	m.writeState(levelsFile, m.fxLevels)
	return renocore.CamFX{Kind: kind, Level: next}, true
}

// localDisplays is the CG enumeration seam (tests fake it), mirroring the
// bus's. Enumeration is local, unrestricted, and cheap -- no OBS, no TCC.
var localDisplays = cgdisplay.Active

// displaysMoved reports that the machine's display set no longer matches
// the one the inventory was probed against. Screens are the one part of
// an OBS enumeration that changes without warning: undock, and a cached
// inventory still looks current -- its clock has not run out -- while its
// selectors point at displays that are gone. A time-based staleness check
// cannot see that, so the pad checks identity, not age.
//
// The probe records display selectors straight from CoreGraphics (UUID
// for SCK, CGDirectDisplayID for the legacy source, chosen by the OBS
// source kind), so both forms are accepted here rather than plumbing
// which one was used.
func displaysMoved(inv *renocore.ObsInventory, local []cgdisplay.Display) bool {
	if inv == nil || len(local) == 0 {
		return false // nothing probed, or no enumeration to judge against
	}
	if len(local) != len(inv.Screens) {
		return true
	}
	have := make(map[string]bool, len(local)*2)
	for _, d := range local {
		have[d.UUID] = true
		have[strconv.FormatUint(uint64(d.ID), 10)] = true
	}
	for _, s := range inv.Screens {
		if !have[s.Value] {
			return true
		}
	}
	return false
}

// healDisplaysLocked rebuilds the cached screen list from the local CG
// enumeration, reporting whether anything changed.
//
// It exists because the displays half of the inventory never needed OBS in
// the first place: the SELECTORS come from CoreGraphics, and OBS only ever
// supplied the pretty label. So when the displays move there is nothing to
// go and ask -- the answer is already in hand, refreshed every Poll. A
// warning you could clear yourself is just a warning you trained the user
// to ignore.
//
// Labels survive by identity: a display still present keeps the name the
// probe gave it, and only a genuinely new one falls back to CG geometry.
// Mics and cams are untouched -- those DO need OBS, so they keep their age
// readout and their deliberate tap. Caller holds mu.
func (m *Mod) healDisplaysLocked() bool {
	inv := m.inventory
	if inv == nil || len(m.local) == 0 || !displaysMoved(inv, m.local) {
		return false
	}
	// which selector form the probe used is not recorded, so infer it from
	// what is already there: any non-numeric value means UUIDs (SCK), the
	// same tolerance displaysMoved applies when matching
	numeric := len(inv.Screens) > 0
	for _, s := range inv.Screens {
		if _, err := strconv.ParseUint(s.Value, 10, 32); err != nil {
			numeric = false
			break
		}
	}
	named := make(map[string]string, len(inv.Screens))
	for _, s := range inv.Screens {
		named[s.Value] = s.Name
	}
	screens := make([]renocore.PropertyItem, 0, len(m.local))
	for i, d := range m.local {
		val := d.UUID
		if numeric {
			val = strconv.FormatUint(uint64(d.ID), 10)
		}
		name, ok := named[val]
		if !ok {
			main := ""
			if d.Main {
				main = " (main)"
			}
			name = fmt.Sprintf("display %d %dx%d%s", i+1, d.W, d.H, main)
		}
		screens = append(screens, renocore.PropertyItem{Name: name, Value: val})
	}
	inv.Screens = screens
	m.saveInventoryLocked()
	log.Printf("khudson reno: displays moved; screen list rebuilt locally (%d displays, no OBS needed)", len(screens))
	return true
}

// handleCfgActLocked is the recorder view's config-menu dispatch. Every
// knob that moves drives the reno scene right then -- the pad is a mixer
// you tweak against live OBS, not a form you submit at record time.
// Caller holds mu.
func (m *Mod) handleCfgActLocked(argv []string) (bool, []module.Effect) {
	if argv[0] == actProbe {
		if len(argv) != 1 {
			return false, nil
		}
		// unique key per emission: a re-probe after a failure must never
		// be debounce-eaten (the record-stop precedent)
		return true, []module.Effect{{
			Key:  fmt.Sprintf("reno/probe/%d", m.now().UnixNano()),
			Argv: []string{m.exe, "reno", "obs", "probe"},
		}}
	}
	if argv[0] == actArm {
		if len(argv) != 1 {
			return false, nil
		}
		return true, m.armEffectsLocked()
	}
	if argv[0] == actWindow {
		if len(argv) != 1 {
			return false, nil
		}
		return true, m.windowEffectsLocked()
	}
	if argv[0] == actVCam {
		if len(argv) != 1 {
			return false, nil
		}
		return true, m.vcamEffectsLocked()
	}
	if argv[0] == actOpenClip {
		if len(argv) != 2 {
			return false, nil
		}
		// no latch and no wedge: opening a file is idempotent and cheap, and
		// the key is the PATH, so two taps on the same take debounce while a
		// second take's tile is its own button
		return true, []module.Effect{{
			Key:  "reno/clip/" + argv[1],
			Argv: []string{m.exe, "reno", "open-clip", argv[1]},
		}}
	}
	if argv[0] == actFXHide {
		if len(argv) != 1 {
			return false, nil
		}
		return true, m.fxEffectsLocked(renocore.CamFX{Kind: renocore.FXHide})
	}
	if argv[0] == actFXLevel {
		if len(argv) != 3 {
			return false, nil
		}
		fx, ok := m.bumpFXLevelLocked(argv[1], argv[2] == levelUp)
		if !ok {
			return true, nil // end of the range: the tile is dim, nothing fires
		}
		return true, m.fxEffectsLocked(fx)
	}
	if len(argv) != 2 {
		return false, nil
	}
	if argv[0] == actFXMove {
		fx := renocore.CamFX{
			Kind: renocore.FXMove, Preset: argv[1],
			Dur: renocore.FXDefaultDur, Ease: renocore.FXDefaultEase,
		}
		if err := renocore.CheckCamFX(fx); err != nil {
			return true, nil // rejected value: state unchanged, nothing to fire
		}
		return true, m.fxEffectsLocked(fx)
	}
	switch argv[0] {
	case actCfgAudio:
		if argv[1] == cfgDefault {
			m.desired.Audio = ""
		} else {
			m.desired.Audio = argv[1]
		}
	case actCfgWebcam:
		if argv[1] == cfgDefault {
			m.desired.Webcam = ""
		} else if err := renocore.CheckWebcam(argv[1]); err != nil {
			return true, nil // rejected value: state unchanged, nothing to apply
		} else {
			m.desired.Webcam = argv[1]
		}
	case actCfgScreen:
		m.toggleScreenLocked(argv[1])
	default:
		return false, nil
	}
	return true, m.applyEffectsLocked()
}

// applyEffectsLocked emits the live scene apply for a config change.
// At most ONE apply is in flight: a change that lands during one sets the
// dirty latch and the completing apply re-emits, so hammering the pad
// costs two applies rather than one per tap (each apply is a full OBS
// probe -- a tap-per-apply fan-out would wedge the socket).
//
// An empty config emits nothing: an untouched pad must not seize OBS,
// and BuildScenePlan refuses an empty config by contract, so there is no
// teardown to express anyway. OBS keeps whatever the last apply left and
// the readout says unconfigured rather than implying a clean slate.
// Caller holds mu.
func (m *Mod) applyEffectsLocked() []module.Effect {
	// every mutation path funnels here, so this is the one place the
	// posture has to be persisted
	m.saveConfigLocked()
	if m.desired.Empty() {
		m.applyNote, m.applyFail = "unconfigured (obs untouched)", false
		return nil
	}
	if m.applyLive && m.now().Sub(m.applyAt) < applyWedge {
		m.applyDirty = true
		return nil
	}
	m.applyLive, m.applyAt = true, m.now()
	m.applyGen++
	return []module.Effect{{
		Key:  fmt.Sprintf("reno/apply/%d", m.applyGen),
		Argv: []string{m.exe, "reno", "obs", "apply"},
	}}
}

// armEffectsLocked emits phase one: boot OBS and match the scene. One
// at a time, and NO dirty re-fire -- unlike an apply this carries no
// pending edit that could be missed, so a second tap during a cold start
// is impatience, not a change to converge on. Caller holds mu.
func (m *Mod) armEffectsLocked() []module.Effect {
	if m.armLive && m.now().Sub(m.armAt) < armWedge {
		return nil
	}
	m.armLive, m.armAt = true, m.now()
	m.armNote, m.armFail = "", false
	m.armGen++
	return []module.Effect{{
		Key:  fmt.Sprintf("reno/arm/%d", m.armGen),
		Argv: []string{m.exe, "reno", "obs", "arm"},
	}}
}

// noteArmLocked closes a standby with its outcome. Caller holds mu.
func (m *Mod) noteArmLocked(args []string) error {
	if len(args) == 0 {
		return errors.New("reno standby-note: need ok|error [detail]")
	}
	detail := strings.Join(args[1:], " ")
	switch args[0] {
	case "ok":
		m.armNote, m.armFail = detail, false
	case "released":
		// the rig is down: unarmed, and the transport falls back to ARM
		m.armNote, m.armFail = "", false
	case "error":
		m.armNote, m.armFail = detail, true
	default:
		return fmt.Errorf("reno standby-note: unknown outcome %q", args[0])
	}
	m.armLive = false
	return nil
}

// windowEffectsLocked toggles OBS's own window. Caller holds mu.
func (m *Mod) windowEffectsLocked() []module.Effect {
	if m.winLive && m.now().Sub(m.winAt) < probeWedge {
		return nil
	}
	m.winLive, m.winAt = true, m.now()
	m.winGen++
	return []module.Effect{{
		Key:  fmt.Sprintf("reno/window/%d", m.winGen),
		Argv: []string{m.exe, "reno", "obs", "window", "toggle"},
	}}
}

// noteWindowLocked records where OBS's window ended up. The verb reports
// the RESOLVED state rather than the requested one, so a toggle that lost
// a race with the user's own cmd-H still leaves the tile honest. Caller
// holds mu.
func (m *Mod) noteWindowLocked(args []string) error {
	if len(args) == 0 {
		return errors.New("reno window-note: need shown|hidden|error [detail]")
	}
	switch args[0] {
	case "shown", "hidden":
		m.winState, m.winFail = args[0], false
	case "error":
		// state unknown now, not stale-but-plausible: OBS may not even be
		// running, and a tile claiming "shown" would be inventing it
		m.winState, m.winFail = "", true
	default:
		return fmt.Errorf("reno window-note: unknown state %q", args[0])
	}
	m.winLive = false
	return nil
}

// vcamEffectsLocked fires the virtual-camera sweep: read the output back and
// take it down if it is up. Only ever "stop" -- there is no case where this
// rig wants OBS's virtual camera running, so the tile is one safe tap whether
// the readout says on, off, or nothing yet. Caller holds mu.
func (m *Mod) vcamEffectsLocked() []module.Effect {
	if m.vcamLive && m.now().Sub(m.vcamAt) < probeWedge {
		return nil
	}
	m.vcamLive, m.vcamAt = true, m.now()
	m.vcamGen++
	return []module.Effect{{
		Key:  fmt.Sprintf("reno/vcam/%d", m.vcamGen),
		Argv: []string{m.exe, "reno", "obs", "vcam", "stop"},
	}}
}

// noteVCamLocked records what the virtual camera turned out to be doing.
// Arming and releasing note here too, so the readout is fresh at the two
// moments it matters without the tile having to be tapped. Caller holds mu.
func (m *Mod) noteVCamLocked(args []string) error {
	if len(args) == 0 {
		return errors.New("reno vcam-note: need on|off|error [detail]")
	}
	switch args[0] {
	case "on", "off":
		m.vcamState, m.vcamFail = args[0], false
	case "error":
		// unknown, not stale-but-plausible: the one failure this readout
		// exists to catch is a camera held by an output nothing reported
		m.vcamState, m.vcamFail = "", true
	default:
		return fmt.Errorf("reno vcam-note: unknown state %q", args[0])
	}
	// the detail (args[1:]) stays in the verb's events journal and off the
	// pad: a tile is ~12 interior cells and "socket gone" would crop
	m.vcamLive = false
	return nil
}

// fxEffectsLocked emits one webcam FX one-shot. An FX carries no desired
// state of its own -- but a move IS a placement, so the pending webcam
// preset follows it, or the cam band would stay lit on a corner the
// camera has left. Two cases divert:
//
//   - never configured (Webcam == ""): there is no cam item to animate
//     yet, and the FX verb refuses to conjure one, so the first tap goes
//     through the ordinary full apply and lands the device.
//   - hide with nothing staged: a no-op, not an error -- the camera is
//     already as absent as it can get.
//
// Caller holds mu.
func (m *Mod) fxEffectsLocked(fx renocore.CamFX) []module.Effect {
	switch fx.Kind {
	case renocore.FXMove, renocore.FXJump:
		unconfigured := m.desired.Webcam == ""
		m.desired.Webcam = fx.Preset
		if unconfigured {
			return m.applyEffectsLocked()
		}
	case renocore.FXHide:
		if m.desired.Webcam == "" {
			return nil
		}
		m.desired.Webcam = "off"
	}
	m.saveConfigLocked()
	m.fxGen++
	return []module.Effect{{
		// unique key per emission: repeated taps of the same effect are
		// each a deliberate one-shot, never a debounce candidate
		Key:  fmt.Sprintf("reno/fx/%d", m.fxGen),
		Argv: append([]string{m.exe, "reno", "obs", "cam"}, fx.Args()...),
	}}
}

// noteFXLocked records the last FX one-shot's outcome for the pad
// readout. Deliberately NOT apply-note: the apply latch belongs to the
// config reconciler, and an FX that cleared it would strand a queued
// config change. Caller holds mu.
func (m *Mod) noteFXLocked(args []string) error {
	if len(args) == 0 {
		return errors.New("reno fx-note: need ok|error [detail]")
	}
	detail := strings.Join(args[1:], " ")
	switch args[0] {
	case "ok":
		m.fxNote, m.fxFail = detail, false
		m.adoptFXLevelLocked(detail)
	case "error":
		m.fxNote, m.fxFail = detail, true
	default:
		return fmt.Errorf("reno fx-note: unknown outcome %q", args[0])
	}
	return nil
}

// adoptFXLevelLocked takes the level out of a landed effect's summary. A
// tap already set it before emitting, so this is for the CLI seam: a
// hand-run `khudson reno obs cam purple 4` moved the camera, and a pad
// still reading off over it would be the lying tile the level was supposed
// to avoid. Caller holds mu.
func (m *Mod) adoptFXLevelLocked(summary string) {
	// the plan appends its own parentheticals ("(no filter to disable)")
	if i := strings.Index(summary, " ("); i > 0 {
		summary = summary[:i]
	}
	fx, err := renocore.ParseCamFX(strings.Fields(summary))
	if err != nil || !renocore.IsLevelFX(fx.Kind) {
		return
	}
	if m.fxLevels == nil {
		m.fxLevels = map[string]int{}
	}
	if m.fxLevels[fx.Kind] == fx.Level {
		return
	}
	m.fxLevels[fx.Kind] = fx.Level
	m.writeState(levelsFile, m.fxLevels)
}

// beginApplyLocked hands the apply verb the config to drive OBS to. The
// dirty latch clears here, not at emission: the verb reads the CURRENT
// pending, so every tap that landed before this moment is already
// covered and re-applying for them would be pure waste. Caller holds mu.
func (m *Mod) beginApplyLocked() *renocore.RecordConfig {
	m.applyDirty = false
	return cloneConfig(m.desired)
}

// noteApplyLocked closes an apply and re-fires when the config moved
// under it. Caller holds mu.
func (m *Mod) noteApplyLocked(args []string) ([]module.Effect, error) {
	if len(args) == 0 {
		return nil, errors.New("reno apply-note: need ok|error [detail]")
	}
	detail := strings.Join(args[1:], " ")
	switch args[0] {
	case "ok":
		m.applyNote, m.applyFail = detail, false
	case "error":
		m.applyNote, m.applyFail = detail, true
	default:
		return nil, fmt.Errorf("reno apply-note: unknown outcome %q", args[0])
	}
	m.applyLive = false
	if !m.applyDirty {
		return nil, nil
	}
	return m.applyEffectsLocked(), nil
}

// toggleScreenLocked flips one display's membership in the pending
// screen list (regions are a CLI/script refinement; taps select whole
// displays). Caller holds mu.
func (m *Mod) toggleScreenLocked(display string) {
	for i, s := range m.desired.Screens {
		if s.Display == display {
			m.desired.Screens = append(m.desired.Screens[:i], m.desired.Screens[i+1:]...)
			return
		}
	}
	m.desired.Screens = append(m.desired.Screens, renocore.ScreenSel{Display: display})
}

// openConfigLocked picks the config the arm journals: the script's for a
// scripted recording run, the pending one for ad hoc. The returned value
// is a DEEP snapshot -- a shallow copy would alias the Screens backing
// array, so a later screen toggle (in-place delete) would corrupt the
// journaled config and race the post-unlock RecordOpen marshal (arc-2
// review #3). Caller holds mu.
func (m *Mod) openConfigLocked() *renocore.RecordConfig {
	src := m.desired
	if m.staged != nil && m.run != nil && m.run.Demo == m.staged.Name {
		src = m.staged.RecordCfg
	}
	return cloneConfig(src)
}

// cloneConfig deep-copies a config for a caller that outlives the lock,
// nil for an empty one. Screens carry a slice and per-entry Region
// pointers; both alias without this.
func cloneConfig(src renocore.RecordConfig) *renocore.RecordConfig {
	if src.Empty() {
		return nil
	}
	cfg := src
	cfg.Screens = slices.Clone(src.Screens)
	for i, s := range cfg.Screens {
		if s.Region != nil {
			r := *s.Region
			cfg.Screens[i].Region = &r
		}
	}
	return &cfg
}
