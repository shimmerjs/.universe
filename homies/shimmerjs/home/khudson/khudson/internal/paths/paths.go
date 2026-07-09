// Package paths resolves khudson's runtime state directory. Never /tmp:
// macOS reaps /private/tmp entries idle >3 days.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths is the runtime state root plus the well-known files under it.
type Paths struct {
	Dir string
}

// Resolve returns the state root without creating it.
func Resolve() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home: %w", err)
	}
	return Paths{Dir: filepath.Join(home, "Library", "Application Support", "khudson")}, nil
}

// Ensure resolves the state root and creates it 0700.
func Ensure() (Paths, error) {
	p, err := Resolve()
	if err != nil {
		return Paths{}, err
	}
	if err := os.MkdirAll(p.Dir, 0o700); err != nil {
		return Paths{}, fmt.Errorf("create state dir: %w", err)
	}
	if err := os.Chmod(p.Dir, 0o700); err != nil {
		return Paths{}, fmt.Errorf("tighten state dir: %w", err)
	}
	return p, nil
}

// BusSocket is where khudson bus listens for dock and ctl connections.
func (p Paths) BusSocket() string { return filepath.Join(p.Dir, "khudson.sock") }

// TouchSocket is where khudson-touchd serves contact frames.
func (p Paths) TouchSocket() string { return filepath.Join(p.Dir, "touch.sock") }

// KeysSocket is where khudson-touchd serves Moonlander key events.
func (p Paths) KeysSocket() string { return filepath.Join(p.Dir, "keys.sock") }

// KittySocket is the scrape-substrate kitty's RC socket: the windowless
// instance hosting scraped TUIs. The HUD window lives in its own instance
// (HudKittySocket); under the three-process topology no code may treat one
// kitty as both.
func (p Paths) KittySocket() string { return filepath.Join(p.Dir, "kitty.sock") }

// HudKittySocket is the HUD kitty's RC socket: the fullscreen window on the
// Edge running the dock. Input injection targets this instance.
func (p Paths) HudKittySocket() string { return filepath.Join(p.Dir, "kitty-panel.sock") }

// MainKittySocket is the daily (Launch Services) kitty's RC socket, bound
// CLI-verbatim via darwinLaunchOptions. The bus never injects through it;
// it only health-probes the fixed path so a SIGKILL corpse can be unlinked
// (bus/mainkitty.go).
func (p Paths) MainKittySocket() string { return filepath.Join(p.Dir, "main-kitty.sock") }

// ClaudeSpool is where the claude statusline tee drops session JSON.
func (p Paths) ClaudeSpool() string { return filepath.Join(p.Dir, "claude") }

// HistSnap is the module-history snapshot: ring buffers survive bus
// restarts through it (internal/module/histsnap).
func (p Paths) HistSnap() string { return filepath.Join(p.Dir, "hist.snap") }

// OryxCache is where fetched Oryx keyboard layouts are cached, one JSON per
// layout hash, so keyboard renders keep working offline.
func (p Paths) OryxCache() string { return filepath.Join(p.Dir, "oryx") }
