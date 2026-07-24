package bus

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBoot(t *testing.T) *ObsBoot {
	t.Helper()
	dir := t.TempDir()
	return &ObsBoot{
		Host:      "localhost:4455",
		AppPath:   "/fake/OBS.app",
		ConfigDir: filepath.Join(dir, "obs-studio"),
		StateDir:  filepath.Join(dir, "state", "reno"),
		dial: func(string, string) (ObsTransport, error) {
			return nil, errors.New("down")
		},
		launch:  func(string) error { return nil },
		running: func() bool { return false },
		sleep:   func(time.Duration) {},
		now:     time.Now,
	}
}

func readSeededConfig(t *testing.T, b *ObsBoot) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(b.ConfigDir, "plugin_config", "obs-websocket", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Seed enables the server, generates a password shared between
// config.json and the state file ObsPassword reads, and marks first-run
// done on a virgin install.
func TestSeedVirginInstall(t *testing.T) {
	b := testBoot(t)
	if err := b.Seed(); err != nil {
		t.Fatal(err)
	}
	cfg := readSeededConfig(t, b)
	if cfg["server_enabled"] != true || cfg["auth_required"] != true || cfg["first_load"] != false {
		t.Errorf("cfg = %v", cfg)
	}
	if port, ok := cfg["server_port"].(float64); !ok || int(port) != 4455 {
		t.Errorf("port = %v", cfg["server_port"])
	}
	pw := b.password()
	if pw == "" || cfg["server_password"] != pw {
		t.Errorf("password mismatch: file=%q cfg=%v", pw, cfg["server_password"])
	}
	info, err := os.Stat(filepath.Join(b.StateDir, "obs-password"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("password file: %v %v", info, err)
	}
	ini, err := os.ReadFile(filepath.Join(b.ConfigDir, "user.ini"))
	if err != nil || !strings.Contains(string(ini), "FirstRun=true") {
		t.Errorf("user.ini = %q, %v", ini, err)
	}

	// idempotent: a second seed keeps the same password
	if err := b.Seed(); err != nil {
		t.Fatal(err)
	}
	if b.password() != pw {
		t.Error("re-seed rotated the password")
	}
}

// An existing config keeps its unknown keys; an existing user.ini is the
// user's and is never touched.
func TestSeedPreservesExistingConfig(t *testing.T) {
	b := testBoot(t)
	cfgPath := filepath.Join(b.ConfigDir, "plugin_config", "obs-websocket", "config.json")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(`{"alerts_enabled": true, "server_enabled": false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	userIni := filepath.Join(b.ConfigDir, "user.ini")
	if err := os.WriteFile(userIni, []byte("[General]\nTheme=dark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.Seed(); err != nil {
		t.Fatal(err)
	}
	cfg := readSeededConfig(t, b)
	if cfg["alerts_enabled"] != true {
		t.Error("unknown key dropped")
	}
	if cfg["server_enabled"] != true {
		t.Error("server not enabled")
	}
	ini, _ := os.ReadFile(userIni)
	if string(ini) != "[General]\nTheme=dark\n" {
		t.Errorf("user.ini touched: %q", ini)
	}
}

// A short-circuit dial never seeds or launches.
func TestEnsureShortCircuit(t *testing.T) {
	b := testBoot(t)
	launched := false
	b.launch = func(string) error { launched = true; return nil }
	b.dial = func(string, string) (ObsTransport, error) { return &fakeObs{version: "32.2.0"}, nil }
	obs, err := b.Ensure(time.Second)
	if err != nil || obs.Version() != "32.2.0" {
		t.Fatalf("ensure: %v", err)
	}
	if launched {
		t.Error("must not launch when the socket answers")
	}
	if _, err := os.Stat(filepath.Join(b.StateDir, "obs-password")); !os.IsNotExist(err) {
		t.Error("must not seed when the socket answers")
	}
}

// OBS running with the websocket down refuses loudly: the seeded config
// binds at launch.
func TestEnsureRefusesRunningDeadSocket(t *testing.T) {
	b := testBoot(t)
	b.running = func() bool { return true }
	_, err := b.Ensure(time.Second)
	if err == nil || !strings.Contains(err.Error(), "quit OBS") {
		t.Fatalf("err = %v", err)
	}
}

// The launch path seeds, launches once, and polls until the dial lands.
func TestEnsureLaunchAndPoll(t *testing.T) {
	b := testBoot(t)
	launches := 0
	b.launch = func(app string) error {
		if app != "/fake/OBS.app" {
			t.Errorf("launch %q", app)
		}
		launches++
		return nil
	}
	dials := 0
	b.dial = func(_, pw string) (ObsTransport, error) {
		dials++
		if dials < 3 {
			return nil, errors.New("still starting")
		}
		if pw == "" {
			t.Error("post-seed dial must carry the generated password")
		}
		return &fakeObs{version: "32.2.0"}, nil
	}
	obs, err := b.Ensure(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Close()
	if launches != 1 || dials != 3 {
		t.Errorf("launches=%d dials=%d", launches, dials)
	}
	if _, err := os.Stat(filepath.Join(b.ConfigDir, "plugin_config", "obs-websocket", "config.json")); err != nil {
		t.Error("seed must land before launch")
	}
}

// A dead socket past the deadline errors with the last dial failure.
func TestEnsureDeadline(t *testing.T) {
	b := testBoot(t)
	clock := time.Now()
	b.now = func() time.Time { return clock }
	b.sleep = func(d time.Duration) { clock = clock.Add(d) }
	_, err := b.Ensure(3 * time.Second)
	if err == nil || !strings.Contains(err.Error(), "never came up") {
		t.Fatalf("err = %v", err)
	}
}

// Remote hosts are dial-only: nothing to seed or launch.
func TestEnsureRemoteHostNeverLaunches(t *testing.T) {
	b := testBoot(t)
	b.Host = "10.0.0.9:4455"
	_, err := b.Ensure(time.Second)
	if err == nil || !strings.Contains(err.Error(), "not local") {
		t.Fatalf("err = %v", err)
	}
}
