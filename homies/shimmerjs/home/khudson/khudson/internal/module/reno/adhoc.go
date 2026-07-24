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
	"os"
	"path/filepath"
	"time"

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
// scripted recording run, the pending one for ad hoc. Caller holds mu.
func (m *Mod) armConfigLocked() *renocore.RecordConfig {
	if m.staged != nil && m.run != nil && m.run.Demo == m.staged.Name {
		if m.staged.RecordCfg.Empty() {
			return nil
		}
		cfg := m.staged.RecordCfg
		return &cfg
	}
	if m.pending.Empty() {
		return nil
	}
	cfg := m.pending
	return &cfg
}
