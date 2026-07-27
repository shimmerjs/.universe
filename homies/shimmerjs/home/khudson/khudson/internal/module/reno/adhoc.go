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
	"strconv"
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

// maxNotesBytes bounds an ad hoc notes file read: notes are prose for a
// talk, not a data file; a fat misload must not balloon the bus.
const maxNotesBytes = 1 << 20

// setRecordConfigLocked replaces the pending config from the ctl seam
// (`khudson reno config` marshals its flags). --clear resets. Caller
// holds mu.
func (m *Mod) setRecordConfigLocked(args []string) error {
	switch {
	case len(args) == 1 && args[0] == "--clear":
		m.pending = renocore.RecordConfig{}
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
		m.pending = cfg
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

// recoverInventory reloads the cached probe. A cache older than invStale
// still loads: the pad renders it and offers a re-probe (the At stamp
// carries the age), which beats booting cold with dead menus.
func (m *Mod) recoverInventory() {
	if m.stateDir == "" {
		return
	}
	body, err := os.ReadFile(filepath.Join(m.stateDir, inventoryFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("khudson reno: obs inventory cache: %v", err)
		}
		return
	}
	var inv renocore.ObsInventory
	if err := json.Unmarshal(body, &inv); err != nil {
		log.Printf("khudson reno: obs inventory cache: %v", err)
		return
	}
	m.inventory = &inv
	log.Printf("khudson reno: obs inventory cache: %d displays, %d mics, %d cams (%s old)",
		len(inv.Screens), len(inv.Mics), len(inv.Cams), m.now().Sub(inv.At).Round(time.Second))
}

// saveInventoryLocked caches the probe for the next bus. Best effort: a
// failed write costs a re-probe, never the running surface. Caller holds
// mu.
func (m *Mod) saveInventoryLocked() {
	if m.stateDir == "" || m.inventory == nil {
		return
	}
	body, err := json.Marshal(m.inventory)
	if err != nil {
		return
	}
	if err := os.MkdirAll(m.stateDir, 0o700); err != nil {
		log.Printf("khudson reno: obs inventory cache: %v", err)
		return
	}
	// write-rename: a torn cache would fail the next unmarshal and boot cold
	tmp := filepath.Join(m.stateDir, inventoryFile+".next")
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		log.Printf("khudson reno: obs inventory cache: %v", err)
		return
	}
	if err := os.Rename(tmp, filepath.Join(m.stateDir, inventoryFile)); err != nil {
		log.Printf("khudson reno: obs inventory cache: %v", err)
	}
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

// handleCfgActLocked is the recorder view's config-menu dispatch.
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
			m.pending.Audio = ""
		} else {
			m.pending.Audio = argv[1]
		}
		return true, nil
	case actCfgWebcam:
		if argv[1] == cfgDefault {
			m.pending.Webcam = ""
		} else if err := renocore.CheckWebcam(argv[1]); err == nil {
			m.pending.Webcam = argv[1]
		}
		return true, nil
	case actCfgScreen:
		m.toggleScreenLocked(argv[1])
		return true, nil
	}
	return false, nil
}

// toggleScreenLocked flips one display's membership in the pending
// screen list (regions are a CLI/script refinement; taps select whole
// displays). Caller holds mu.
func (m *Mod) toggleScreenLocked(display string) {
	for i, s := range m.pending.Screens {
		if s.Display == display {
			m.pending.Screens = append(m.pending.Screens[:i], m.pending.Screens[i+1:]...)
			return
		}
	}
	m.pending.Screens = append(m.pending.Screens, renocore.ScreenSel{Display: display})
}

// armConfigLocked picks the config the arm journals: the script's for a
// scripted recording run, the pending one for ad hoc. The returned value
// is a DEEP snapshot -- a shallow copy would alias the Screens backing
// array, so a later screen toggle (in-place delete) would corrupt the
// journaled config and race the post-unlock ArmResult marshal (arc-2
// review #3). Caller holds mu.
func (m *Mod) armConfigLocked() *renocore.RecordConfig {
	src := m.pending
	if m.staged != nil && m.run != nil && m.run.Demo == m.staged.Name {
		src = m.staged.RecordCfg
	}
	if src.Empty() {
		return nil
	}
	cfg := src
	cfg.Screens = make([]renocore.ScreenSel, len(src.Screens))
	for i, s := range src.Screens {
		cfg.Screens[i] = s
		if s.Region != nil {
			r := *s.Region
			cfg.Screens[i].Region = &r
		}
	}
	return &cfg
}
