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
)

// cfgDefault is the menu value for "leave OBS alone" (clears the knob).
const cfgDefault = "default"

// invStale is how old a probed inventory may get before the recorder
// view offers a re-probe instead of trusting the menus (devices and
// displays move: docks get unplugged).
const invStale = time.Hour

// applyWedge bounds an in-flight live apply. The latch clears on the
// verb's note, but a ctl that never lands (bus restart mid-apply, a
// killed child) would otherwise hold it set forever and silently stop
// applying every later tap. Past this the next change fires regardless.
const applyWedge = 30 * time.Second

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

// recoverInventory reloads the cached probe. A cache older than invStale
// still loads: the pad renders it and offers a re-probe (the At stamp
// carries the age), which beats booting cold with dead menus.
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
	if len(argv) != 2 {
		return false, nil
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

// armConfigLocked picks the config the arm journals: the script's for a
// scripted recording run, the pending one for ad hoc. The returned value
// is a DEEP snapshot -- a shallow copy would alias the Screens backing
// array, so a later screen toggle (in-place delete) would corrupt the
// journaled config and race the post-unlock ArmResult marshal (arc-2
// review #3). Caller holds mu.
func (m *Mod) armConfigLocked() *renocore.RecordConfig {
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
