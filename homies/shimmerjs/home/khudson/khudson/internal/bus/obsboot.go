// OBS bootstrap: khudson manages the obs-websocket server itself, so
// recording never depends on a hand-enabled socket. Ground truth from
// the installed OBS 32.2 binaries (not docs): obs-websocket persists
// {server_enabled, server_port, server_password, auth_required,
// first_load} in plugin_config/obs-websocket/config.json, and the CLI
// flags (--websocket_port/--websocket_password/--websocket_debug) are
// overrides only -- there is no enable flag, so seeding config.json IS
// the enable mechanism, and it binds at launch. user.ini [General]
// FirstRun gates the first-run wizard.
package bus

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
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
	running func() bool
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
			return exec.Command("open", "-g", "-a", app).Run()
		},
		running: func() bool {
			return exec.Command("/usr/bin/pgrep", "-xq", "OBS").Run() == nil
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
	if t, err := b.dial(b.Host, b.password()); err == nil {
		return t, nil
	}
	if !b.localhost() {
		return nil, fmt.Errorf("obs at %s is down and not local -- nothing to launch", b.Host)
	}
	if b.AppPath == "" {
		return nil, fmt.Errorf("OBS.app not found under %s (cask not landed?)", strings.Join(obsAppCandidates(), " or "))
	}
	if err := b.Seed(); err != nil {
		return nil, err
	}
	if b.running() {
		// the seeded config binds at launch; a live OBS with the socket
		// down keeps its dead posture until relaunched
		return nil, fmt.Errorf("OBS is running but its websocket at %s is down; the seeded config binds at launch -- quit OBS and re-run", b.Host)
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
	userIni := filepath.Join(b.ConfigDir, "user.ini")
	if _, err := os.Stat(userIni); os.IsNotExist(err) {
		// virgin install: mark first-run done so the auto-config wizard
		// never blocks an unattended launch. An existing user.ini is the
		// user's; never touched.
		if err := writeFileAtomic(userIni, []byte("[General]\nFirstRun=true\n"), 0o600); err != nil {
			return err
		}
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
