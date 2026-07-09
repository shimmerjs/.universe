// Package dockmirror lists the running Dock apps: running state from
// lsappinfo, ordering from the dock plist's pinned section, minimized
// windows read directly from the Dock's own AX tree (internal/ax; ONE
// TCC grant, Accessibility on the fixed-path khudson binary) --
// CGWindowList cannot distinguish minimized windows from other-Space
// windows on a machine with separate Spaces per display, so AX is the
// truthful source. Minimized rows act per window through `khudson ax
// unminimize`; the Acts are NEVER downgraded to app-activate on an
// ungranted machine -- the degrade path is the verb itself failing loud
// with the grant hint.
package dockmirror

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/ax"
	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// minimizedEvery is the default floor between AX sweeps; params
// "minimizedEvery" (a duration string) overrides it.
const minimizedEvery = 30 * time.Second

// selfBundleID / selfName identify khudson's own HUD bundle (the rebranded
// kitty: CFBundleIdentifier org.khudson.hud, display name "khudson"); the
// mirror must never list itself. The bundle id is the match; the name is
// the fallback when the lsappinfo entry carries no bundle id (the AX
// minimized tier carries only names).
const (
	selfBundleID = "org.khudson.hud"
	selfName     = "khudson"
)

// Mod implements module.Module. The singleton caches the minimized-window
// sweep between its cadence ticks; now and sample are test seams.
type Mod struct {
	mu         sync.Mutex
	now        func() time.Time
	sample     func(context.Context) ([]minWin, error)
	last       time.Time
	mins       []minWin
	minErr     error
	promptOnce sync.Once
	exe        string
}

// New returns the registry's instance. exe feeds the minimized rows' Act
// argv (`<exe> ax unminimize <title>`): the bus -- the process that polls
// (publishing the acts handleRowAct allows) AND the process that execs the
// argv -- runs the fixed-path install, so os.Executable() is the stable
// TCC-granted argv[0] with no config templating (bare-name PATH fallback
// if it cannot resolve).
func New() *Mod {
	exe, err := os.Executable()
	if err != nil {
		exe = "khudson"
	}
	return &Mod{exe: exe}
}

func (*Mod) Name() string { return "dock-mirror" }

func (m *Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	plist, err := run(ctx, "defaults", "export", "com.apple.dock", "-")
	if err != nil {
		return module.Data{}, err
	}
	apps, err := run(ctx, "lsappinfo", "list")
	if err != nil {
		return module.Data{}, err
	}
	mins, minErr := m.minimizedCached(ctx, params)
	return render(m.exe, parsePinnedApps(plist), parseRunning(apps), mins, minErr), nil
}

// minimizedCached reruns the AX sweep at most once per cadence tick and
// reuses the cached result (windows OR error) between ticks -- running-app
// sampling stays at the widget poll, the sweep does not.
func (m *Mod) minimizedCached(ctx context.Context, params map[string]any) ([]minWin, error) {
	every := minimizedEvery
	if s, ok := params["minimizedEvery"].(string); ok {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			every = d
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now, sample := time.Now, m.minimizedSample
	if m.now != nil {
		now = m.now
	}
	if m.sample != nil {
		sample = m.sample
	}
	if !m.last.IsZero() && now().Sub(m.last) < every {
		return m.mins, m.minErr
	}
	mins, err := sample(ctx)
	if err != nil && ctx.Err() != nil {
		return mins, err // timeout: leave m.last unset so the next poll retries
	}
	m.mins, m.minErr = mins, err
	m.last = now()
	return m.mins, m.minErr
}

// minWin is one minimized window from the AX sampler.
type minWin struct{ title, app string }

// minimizedSample reads the Dock's minimized items over the direct AX
// API (internal/ax): the Dock indexes every minimized window as a dock
// item with subrole AXMinimizedWindowDockItem, one single-process walk.
// The FIRST sweep in the process asks for the Accessibility grant with
// the system prompt (ax.EnsureTrusted(true) -- fired once, from the bus,
// the fixed-path TCC client; the process must restart after granting);
// later sweeps just check. Untrusted is the typed ax.ErrUntrusted --
// minimizedCached caches errors between ticks, so there is no prompt or
// exec storm while the grant note shows.
func (m *Mod) minimizedSample(ctx context.Context) ([]minWin, error) {
	trusted := false
	m.promptOnce.Do(func() { trusted = ax.EnsureTrusted(true) })
	if !trusted && !ax.Trusted() {
		return nil, fmt.Errorf("dock AX sweep: %w", ax.ErrUntrusted)
	}
	items, err := ax.DockMinimizedItems(ctx)
	if err != nil {
		return nil, fmt.Errorf("dock AX sweep: %w", err)
	}
	var wins []minWin
	for _, it := range items {
		wins = append(wins, minWin{title: it.Title, app: titleApp(it.Title)})
	}
	return wins, nil
}

// emDashSep is the U+2014 variant of the " - " title separator.
var emDashSep = " " + string(rune(0x2014)) + " "

// titleApp extracts the owning app from a window title's trailing
// " - App" (or em-dash) segment, falling back to the whole title.
func titleApp(title string) string {
	for _, sep := range []string{" - ", emDashSep} {
		if i := strings.LastIndex(title, sep); i > 0 {
			if app := strings.TrimSpace(title[i+len(sep):]); app != "" {
				return app
			}
		}
	}
	return title
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	return string(out), nil
}

// render emits one button row per RUNNING app: pinned-and-running first in
// Dock order, then the rest alphabetically, then the minimized-window
// section. Pinned-but-not-running apps do not appear. No cap: the rail
// truncates loudly for itself, and a cap here would skew its overflow
// count.
func render(exe string, pinned []string, running map[string]bool, mins []minWin, minErr error) module.Data {
	appRow := func(app string) module.Row {
		return module.Row{Kind: module.RowText, Text: app, Act: []string{"open", "-a", app}}
	}

	seen := map[string]bool{}
	var rows []module.Row
	for _, app := range pinned {
		if running[app] && !seen[app] {
			seen[app] = true
			rows = append(rows, appRow(app))
		}
	}

	var rest []string
	for app := range running {
		if !seen[app] {
			rest = append(rest, app)
		}
	}
	sort.Strings(rest)
	for _, app := range rest {
		rows = append(rows, appRow(app))
	}

	// minimized section: dim rows after the running buttons, Text = window
	// title (Key keeps the owning app for the rail's fallback). The Act is
	// the per-window unminimize verb -- `<exe> ax unminimize <title>`,
	// plus `--app <app>` when titleApp found a real split (the verb's
	// in-app fallback needs an owning app). Rows keep this argv
	// UNCONDITIONALLY: on an ungranted or stale-permission machine the
	// degrade path is the verb itself failing loud with the grant hint,
	// never a downgrade to app-activate. A failed sweep (untrusted
	// included) is one dim note row, never a hard poll failure: it is the
	// expected state until Accessibility is granted to khudson.
	if minErr != nil {
		note := "minimized: sweep failed: " + minErr.Error()
		if errors.Is(minErr, ax.ErrUntrusted) {
			note = "minimized: grant accessibility to khudson"
		}
		rows = append(rows, module.Row{Kind: module.RowText, Text: note, Style: module.StyleDim})
	}
	for _, w := range mins {
		if w.app == selfName {
			// the mirror must not list khudson itself (the AX tier carries
			// only names; the bundle-id match lives in parseRunning)
			continue
		}
		act := []string{exe, "ax", "unminimize", w.title}
		if w.app != w.title {
			act = append(act, "--app", w.app)
		}
		title := w.title
		if title == "" {
			title = w.app
		}
		rows = append(rows, module.Row{Kind: module.RowText,
			Text: title, Key: w.app, Style: module.StyleDim, Act: act})
	}
	return module.Data{Title: "dock", Rows: rows}
}

// parsePinnedApps extracts tile-data file-labels from the persistent-apps
// array of a `defaults export com.apple.dock -` plist. Hand-parsed: the
// file-label key/string pairing is stable for this plist.
func parsePinnedApps(plistXML string) []string {
	section := plistXML
	if i := strings.Index(plistXML, "<key>persistent-apps</key>"); i >= 0 {
		section = arrayBody(plistXML[i:])
	}
	var apps []string
	for {
		i := strings.Index(section, "<key>file-label</key>")
		if i < 0 {
			break
		}
		section = section[i+len("<key>file-label</key>"):]
		v, rest, ok := nextString(section)
		if !ok {
			break
		}
		apps = append(apps, v)
		section = rest
	}
	return apps
}

// nextString returns the contents of the next <string> element.
func nextString(s string) (val, rest string, ok bool) {
	i := strings.Index(s, "<string>")
	if i < 0 {
		return "", "", false
	}
	s = s[i+len("<string>"):]
	j := strings.Index(s, "</string>")
	if j < 0 {
		return "", "", false
	}
	return s[:j], s[j+len("</string>"):], true
}

// arrayBody returns the contents of the first <array> element in s,
// balancing nested arrays. Empty for <array/>.
func arrayBody(s string) string {
	open := strings.Index(s, "<array>")
	if self := strings.Index(s, "<array/>"); self >= 0 && (open < 0 || self < open) {
		return ""
	}
	if open < 0 {
		return ""
	}
	body := s[open+len("<array>"):]
	depth, pos := 1, 0
	for {
		o := strings.Index(body[pos:], "<array>")
		c := strings.Index(body[pos:], "</array>")
		if c < 0 {
			return body
		}
		if o >= 0 && o < c {
			depth++
			pos += o + len("<array>")
			continue
		}
		depth--
		if depth == 0 {
			return body[:pos+c]
		}
		pos += c + len("</array>")
	}
}

// parseRunning extracts Dock-visible apps from `lsappinfo list`. Entries
// are multi-line: a numbered header carries the quoted app name
// (` 1) "loginwindow" ASN:0x0-0x2002:`), a `bundleID="..."` detail line
// carries the bundle id, and a later detail line carries
// `type="Foreground"`. Only Foreground apps appear in the real Dock;
// UIElement/BackgroundOnly processes (agents, helpers) do not. khudson's
// own HUD bundle is dropped -- by bundle id, by name only when the entry
// carries no bundle id -- so the mirror never lists itself.
func parseRunning(lsappinfoOut string) map[string]bool {
	running := map[string]bool{}
	current, bundle, fg := "", "", false
	flush := func() {
		if current == "" || !fg {
			return
		}
		if bundle == selfBundleID || (bundle == "" && current == selfName) {
			return
		}
		running[current] = true
	}
	for line := range strings.SplitSeq(lsappinfoOut, "\n") {
		t := strings.TrimSpace(line)
		i := 0
		for i < len(t) && t[i] >= '0' && t[i] <= '9' {
			i++
		}
		if i > 0 && i < len(t) && t[i] == ')' {
			flush()
			current, bundle, fg = "", "", false
			if q := strings.Index(t, `"`); q >= 0 {
				if end := strings.Index(t[q+1:], `"`); end >= 0 {
					current = t[q+1 : q+1+end]
				}
			}
			continue
		}
		if current == "" {
			continue
		}
		if v, ok := strings.CutPrefix(t, `bundleID="`); ok {
			if end := strings.Index(v, `"`); end >= 0 {
				bundle = v[:end]
			}
		}
		if strings.Contains(t, `type="Foreground"`) {
			fg = true
		}
	}
	flush()
	return running
}
