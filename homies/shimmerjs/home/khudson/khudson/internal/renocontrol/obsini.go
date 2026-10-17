// OBS ini editing: the settings obs-websocket CANNOT reach at runtime.
//
// Verified on OBS 32.2 (2026-08-21): SetProfileParameter writes basic.ini
// immediately, but the RUNNING instance keeps the output it already
// built -- flipping RecEncoder live left StartRecord reporting success
// while no output started, no file appeared, and OBS raised a dialog.
// Canvas and fps are the exception: SetVideoSettings resets the video
// pipeline live (the log says "video settings reset"). So encoder,
// container, and quality have to be on disk BEFORE launch, and the
// global window settings in user.ini are unreachable over the socket
// entirely -- obs-websocket has no global-config request at all.
//
// Seeding therefore only ever runs against a stopped OBS. A running
// instance holds the whole config in memory and writes its own version
// out on exit, so an edit behind its back is not merely ignored, it is
// reverted.
package renocontrol

import (
	"fmt"
	"os"
	"strings"
)

// iniSetting is one section/key/value assertion.
type iniSetting struct{ Section, Key, Value string }

// obsWindowSettings is the user.ini posture: keep OBS out of the Dock and
// out of the way. It lives in the GLOBAL config, which no obs-websocket
// request can touch, so pre-boot seeding is the only mechanism.
var obsWindowSettings = []iniSetting{
	{"BasicWindow", "SysTrayEnabled", "true"},
	{"BasicWindow", "SysTrayWhenStarted", "true"},
	{"BasicWindow", "SysTrayMinimizeToTray", "true"},
	// an unattended capture engine must never block on a confirm dialog
	{"BasicWindow", "WarnBeforeStartingStream", "false"},
	{"BasicWindow", "WarnBeforeStoppingStream", "false"},
	{"BasicWindow", "WarnBeforeStoppingRecord", "false"},
	{"General", "ConfirmOnExit", "false"},
}

// obsProfileSettings is the basic.ini recording posture.
//
// RecQuality is the load-bearing one: at the default "Stream" a recording
// inherits the STREAM bitrate (6000 kbps stock), which is starvation for
// a 4K screen capture. "HQ" decouples the two and targets quality rather
// than a wire budget.
//
// RecEncoder is deliberately absent. This machine has hardware
// VideoToolbox H264/HEVC encoders and x264 software encoding is the wrong
// default for 4K, but switching it is unverified from a cold boot -- the
// live switch is what broke the output above. Add the encoder here only
// after a launch-and-record proves it.
var obsProfileSettings = []iniSetting{
	{"SimpleOutput", "RecQuality", "HQ"},
}

// applyIniSettings reads path, asserts every setting, and writes it back
// only when something actually changed. Unknown sections, unknown keys,
// comments, and ordering all survive: this file is OBS's, and khudson
// owns exactly the keys it names.
//
// A missing file is created with just the asserted keys -- enough for OBS
// to fill in the rest at first launch.
func applyIniSettings(path string, settings []iniSetting) (bool, error) {
	var lines []string
	if raw, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	changed := false
	for _, s := range settings {
		var did bool
		lines, did = setIniKey(lines, s)
		changed = changed || did
	}
	if !changed {
		return false, nil
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}

// findIniKey locates a key inside its section: at is the key's line index
// (-1 when the key is absent) and sectionEnd the last non-blank line of the
// section (-1 when the section itself is absent, which is where a new one
// gets appended).
func findIniKey(lines []string, section, key string) (at, sectionEnd int) {
	at, sectionEnd = -1, -1
	inSection := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			if inSection {
				return at, sectionEnd // left our section without finding the key
			}
			inSection = trimmed == "["+section+"]"
			if inSection {
				sectionEnd = i
			}
			continue
		}
		if !inSection {
			continue
		}
		if trimmed != "" {
			sectionEnd = i // last non-blank line of the section so far
		}
		if k, _, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == key {
			return i, sectionEnd
		}
	}
	return at, sectionEnd
}

// setIniKey sets one key, reporting whether the file changed. The key is
// replaced in place when present (preserving position), appended to the
// end of its section when the section exists, and a new section is
// appended when it does not.
func setIniKey(lines []string, s iniSetting) ([]string, bool) {
	want := s.Key + "=" + s.Value
	at, sectionEnd := findIniKey(lines, s.Section, s.Key)
	if at >= 0 {
		if lines[at] == want {
			return lines, false
		}
		lines[at] = want
		return lines, true
	}
	if sectionEnd < 0 {
		// no such section: append it, with a blank separator when the file
		// already has content
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		return append(lines, "["+s.Section+"]", want), true
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, lines[:sectionEnd+1]...)
	out = append(out, want)
	return append(out, lines[sectionEnd+1:]...), true
}

// checkIniSettings reports the asserted keys path does not currently match,
// as "Section/Key=have" (have is "absent" when the key is missing). It never
// writes: the caller runs it against a LIVE OBS, which holds the config in
// memory and writes its own copy on exit, so a repair here would be reverted
// rather than ignored. Reporting the drift and leaving the file alone is the
// honest move -- the fix is quitting OBS, which re-runs the seed.
func checkIniSettings(path string, settings []iniSetting) []string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil // no file yet: the seed owns that case, not this one
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var drift []string
	for _, s := range settings {
		at, _ := findIniKey(lines, s.Section, s.Key)
		have := "absent"
		if at >= 0 {
			_, v, _ := strings.Cut(strings.TrimSpace(lines[at]), "=")
			if strings.TrimSpace(v) == s.Value {
				continue
			}
			have = strings.TrimSpace(v)
		}
		drift = append(drift, s.Section+"/"+s.Key+"="+have)
	}
	return drift
}

// profileDir reads the active profile's directory name from user.ini
// ([Basic] ProfileDir). Empty means OBS has never written one, in which
// case there is no profile ini to seed yet.
func profileDir(userIniPath string) string {
	raw, err := os.ReadFile(userIniPath)
	if err != nil {
		return ""
	}
	inBasic := false
	for line := range strings.SplitSeq(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inBasic = trimmed == "[Basic]"
			continue
		}
		if !inBasic {
			continue
		}
		if k, v, ok := strings.Cut(trimmed, "="); ok && strings.TrimSpace(k) == "ProfileDir" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
