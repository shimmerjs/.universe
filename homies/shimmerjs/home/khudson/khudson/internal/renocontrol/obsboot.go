// OBS bootstrap: khudson manages the obs-websocket server itself, so
// recording never depends on a hand-enabled socket. Ground truth from
// the installed OBS 32.2 binaries (not docs): obs-websocket persists
// {server_enabled, server_port, server_password, auth_required,
// first_load} in plugin_config/obs-websocket/config.json, and the CLI
// flags (--websocket_port/--websocket_password/--websocket_debug) are
// overrides only -- there is no enable flag, so seeding config.json IS
// the enable mechanism, and it binds at launch. user.ini [General]
// FirstRun gates the first-run wizard.
package renocontrol

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/subprocess"
)

// obsAppCandidates is where the homebrew cask lands, user appdir first.
func obsAppCandidates() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, "Applications", "OBS.app"),
		"/Applications/OBS.app",
	}
}

// ObsBoot ensures a reachable obs-websocket: seed config, launch OBS,
// wait for the socket. Every side effect rides a seam so tests never
// touch the real app.
type ObsBoot struct {
	Host      string // host:port the transport dials
	AppPath   string // resolved OBS.app bundle ("" = not installed)
	ConfigDir string // ~/Library/Application Support/obs-studio
	StateDir  string // <state>/reno

	dial    func(host, password string) (ObsTransport, error)
	launch  func(app string) error
	running func() (bool, error)
	sleep   func(time.Duration)
	now     func() time.Time
}

// NewObsBoot wires the real seams for host (only localhost is ever
// launched or seeded; a remote host just dials).
func NewObsBoot(host string) (*ObsBoot, error) {
	p, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	b := &ObsBoot{
		Host:      host,
		ConfigDir: filepath.Join(home, "Library", "Application Support", "obs-studio"),
		StateDir:  filepath.Join(p.Dir, "reno"),
		dial:      DialOBS,
		launch: func(app string) error {
			// -g: never steal focus mid-demo; detached, OBS outlives the verb
			_, err := subprocess.Output(context.Background(), "open", "-g", "-a", app)
			return err
		},
		running: func() (bool, error) {
			_, err := subprocess.Output(context.Background(), "/usr/bin/pgrep", "-xq", "OBS")
			if err == nil {
				return true, nil
			}
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return false, nil
			}
			return false, err
		},
		sleep: time.Sleep,
		now:   time.Now,
	}
	for _, cand := range obsAppCandidates() {
		if _, err := os.Stat(cand); err == nil {
			b.AppPath = cand
			break
		}
	}
	return b, nil
}

// localhost reports whether the boot may seed and launch: managing OBS
// only ever means the local one.
func (b *ObsBoot) localhost() bool {
	host, _, err := net.SplitHostPort(b.Host)
	if err != nil {
		return false
	}
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (b *ObsBoot) port() int {
	_, ps, err := net.SplitHostPort(b.Host)
	if err != nil {
		return 4455
	}
	n, err := strconv.Atoi(ps)
	if err != nil {
		return 4455
	}
	return n
}

// Ensure returns a connected transport, seeding + launching OBS when
// the socket is down. The deadline bounds the whole attempt; launch
// waits are the slow path (cold start plus one-time Gatekeeper /
// wizard dialogs), so callers pass minutes, not the 30s verb budget.
func (b *ObsBoot) Ensure(deadline time.Duration) (ObsTransport, error) {
	t, dialErr := b.dial(b.Host, b.password())
	if dialErr == nil {
		// already up, so the seed below never runs: say so when the config
		// has drifted out from under us rather than letting a stale posture
		// ride until some later cold launch silently rewrites it
		if drift := b.IniDrift(); len(drift) > 0 {
			fmt.Fprintf(os.Stderr, "khudson obs: config drifted from the asserted posture (%s); OBS is live so it was NOT rewritten -- quit OBS and re-run to re-assert\n",
				strings.Join(drift, ", "))
		}
		return t, nil
	}
	// dialErr rides every branch below: "auth failed" and "connection
	// refused" demand different operator moves, and collapsing them to
	// "down" misdiagnoses a password mismatch
	if !b.localhost() {
		return nil, fmt.Errorf("obs at %s unreachable and not local -- nothing to launch: %w", b.Host, dialErr)
	}
	if b.AppPath == "" {
		return nil, fmt.Errorf("OBS.app not found under %s (cask not landed?); dial: %w", strings.Join(obsAppCandidates(), " or "), dialErr)
	}
	if err := b.Seed(); err != nil {
		return nil, err
	}
	running, err := b.running()
	if err != nil {
		return nil, fmt.Errorf("probe OBS process: %w", err)
	}
	if running {
		if b.safeModeWaiting() {
			return nil, fmt.Errorf("OBS is running but its websocket at %s did not answer (%v); its log says it detected an unclean shutdown and never got past the safe-mode dialog -- answer the dialog, or quit OBS and re-run (the launch path clears the crash sentinel)", b.Host, dialErr)
		}
		// the seeded config binds at launch; a live OBS with the socket
		// down keeps its dead posture until relaunched
		return nil, fmt.Errorf("OBS is running but its websocket at %s did not answer (%v); the seeded config binds at launch -- quit OBS and re-run", b.Host, dialErr)
	}
	if n := b.clearCrashSentinel(); n > 0 {
		fmt.Fprintf(os.Stderr, "khudson obs: OBS's last run ended uncleanly (%d stale sentinel(s) cleared) -- safe mode DECLINED on your behalf so the websocket binds; if a plugin is what killed it, that choice is where to look\n", n)
	}
	if err := b.launch(b.AppPath); err != nil {
		return nil, fmt.Errorf("launch %s: %w", b.AppPath, err)
	}
	start := b.now()
	var last error
	for b.now().Sub(start) < deadline {
		b.sleep(500 * time.Millisecond)
		t, err := b.dial(b.Host, b.password())
		if err == nil {
			return t, nil
		}
		last = err
	}
	return nil, fmt.Errorf("obs launched but its websocket never came up within %s (a first-launch Gatekeeper or permission dialog may be waiting): %w", deadline, last)
}

// clearCrashSentinel removes OBS's leftover run markers and reports how
// many went. OBS drops a `.sentinel/run_<uuid>` file for every live
// instance and deletes it on a clean exit, so a marker with no OBS running
// is a dead instance's -- and the next launch answers it with the
// safe-mode dialog instead of the main window. The dialog never binds the
// websocket, so Ensure would spend its whole deadline dialling a modal.
//
// This DECLINES OBS's safe-mode offer on khudson's behalf, which is the
// trade an unattended rig wants: every reno verb rides that socket, and
// the plugin set here is OBS's own. Only the launch path calls it, and
// only after running() has said no instance is up -- a hand-started OBS
// still gets the offer.
func (b *ObsBoot) clearCrashSentinel() int {
	dir := filepath.Join(b.ConfigDir, ".sentinel")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "run_") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			n++
		}
	}
	return n
}

// safeModeWaiting reports whether OBS's newest log says it detected an
// unclean shutdown and has not yet logged which safe-mode choice was made
// -- i.e. the dialog is still up, holding the socket down. Both lines land
// within the first few lines of a launch, so only the head is read.
//
// Verified against this machine's logs (OBS 32.2): "Crash or unclean
// shutdown detected" on the launch after a crash, then "[Safe Mode] Normal
// launch selected, ..." the moment the dialog is answered.
func (b *ObsBoot) safeModeWaiting() bool {
	dir := filepath.Join(b.ConfigDir, "logs")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	newest := ""
	for _, e := range ents {
		// names are "YYYY-MM-DD HH-MM-SS.txt", so lexical is chronological
		if !e.IsDir() && e.Name() > newest {
			newest = e.Name()
		}
	}
	if newest == "" {
		return false
	}
	f, err := os.Open(filepath.Join(dir, newest))
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 64<<10)
	n, _ := io.ReadFull(f, head)
	text := string(head[:n])
	return strings.Contains(text, "Crash or unclean shutdown detected") &&
		!strings.Contains(text, "[Safe Mode]")
}

// Seed writes the obs-websocket config (enabled, port, generated
// password) and, on a virgin install only, a user.ini that skips the
// first-run wizard. Idempotent; unknown config.json keys survive.
func (b *ObsBoot) Seed() error {
	pw, err := b.ensurePassword()
	if err != nil {
		return err
	}
	cfgPath := filepath.Join(b.ConfigDir, "plugin_config", "obs-websocket", "config.json")
	cfg := map[string]any{}
	if raw, err := os.ReadFile(cfgPath); err == nil {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("existing %s is not JSON: %w", cfgPath, err)
		}
	}
	cfg["server_enabled"] = true
	cfg["server_port"] = b.port()
	cfg["auth_required"] = true
	cfg["server_password"] = pw
	cfg["first_load"] = false
	if err := writeFileAtomic(cfgPath, mustJSON(cfg), 0o600); err != nil {
		return err
	}
	// obs-websocket 5.x has no loopback-only bind: enabling the server
	// listens on EVERY interface, auth'd but cleartext ws://, and the
	// posture persists across khudson. Surfaced here
	// so the exposure is a stated cost, never a silent one.
	fmt.Fprintf(os.Stderr, "khudson obs seed: websocket server enabled on port %d -- obs-websocket binds ALL interfaces (auth required, cleartext); firewall the port if the LAN is hostile\n", b.port())
	userIni := filepath.Join(b.ConfigDir, "user.ini")
	if _, err := os.Stat(userIni); os.IsNotExist(err) {
		// virgin install: mark first-run done so the auto-config wizard
		// never blocks an unattended launch.
		if err := writeFileAtomic(userIni, []byte("[General]\nFirstRun=true\n"), 0o600); err != nil {
			return err
		}
	}
	return b.seedIni(userIni)
}

// IniDrift reports the asserted ini keys the on-disk config no longer
// matches, "Section/Key=have" per entry. Read-only by construction: the
// caller reaches for it when OBS is ALREADY UP, where the seed cannot run,
// so a config edited in OBS's own settings UI would otherwise go unnoticed
// until the next cold launch happened to rewrite it.
func (b *ObsBoot) IniDrift() []string {
	userIni := filepath.Join(b.ConfigDir, "user.ini")
	drift := checkIniSettings(userIni, obsWindowSettings)
	if dir := profileDir(userIni); dir != "" {
		drift = append(drift,
			checkIniSettings(filepath.Join(b.ConfigDir, "basic", "profiles", dir, "basic.ini"), obsProfileSettings)...)
	}
	return drift
}

// seedIni asserts the settings obs-websocket cannot reach at runtime.
//
// It refuses to run against a LIVE OBS. A running instance holds the
// config in memory and writes its own copy out on exit, so an edit
// behind its back is reverted rather than ignored -- worse than not
// writing at all, because the pad would report a posture that silently
// evaporates on quit.
func (b *ObsBoot) seedIni(userIni string) error {
	running, err := b.running()
	if err != nil {
		return fmt.Errorf("probe OBS before config write: %w", err)
	}
	if running {
		return nil
	}
	changed, err := applyIniSettings(userIni, obsWindowSettings)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintln(os.Stderr, "khudson obs seed: user.ini window posture asserted (tray, no confirm dialogs)")
	}
	dir := profileDir(userIni)
	if dir == "" {
		// OBS has not written a profile yet; it will on first launch, and
		// the next seed picks it up
		return nil
	}
	profileIni := filepath.Join(b.ConfigDir, "basic", "profiles", dir, "basic.ini")
	if _, err := os.Stat(profileIni); err != nil {
		return nil // same: nothing to assert against yet
	}
	changed, err = applyIniSettings(profileIni, obsProfileSettings)
	if err != nil {
		return err
	}
	if changed {
		fmt.Fprintf(os.Stderr, "khudson obs seed: profile %q recording posture asserted\n", dir)
	}
	return nil
}

// password reads the state-root password file (ObsPassword's source of
// truth); "" when absent.
func (b *ObsBoot) password() string {
	raw, err := os.ReadFile(filepath.Join(b.StateDir, "obs-password"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// ensurePassword generates the websocket password once and keeps it in
// the state root (0600); the seeded config.json carries the same value,
// so dials auth without any hand-managed secret.
func (b *ObsBoot) ensurePassword() (string, error) {
	if pw := b.password(); pw != "" {
		return pw, nil
	}
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	pw := hex.EncodeToString(raw[:])
	path := filepath.Join(b.StateDir, "obs-password")
	if err := os.MkdirAll(b.StateDir, 0o700); err != nil {
		return "", err
	}
	if err := writeFileAtomic(path, []byte(pw+"\n"), 0o600); err != nil {
		return "", err
	}
	return pw, nil
}

func mustJSON(v any) []byte {
	raw, err := json.MarshalIndent(v, "", "    ")
	if err != nil {
		panic(err) // map[string]any of scalars cannot fail to marshal
	}
	return raw
}

// writeFileAtomic is tmp+rename with parent creation (run-journal
// discipline: OBS must never read a torn config).
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
