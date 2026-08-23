package reno

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// Lemmings is an html overlay composited over the webcam: a browser source
// carrying a transparent page that draws itself.
//
// It is the one effect here that is not OBS doing something to the camera,
// and it exists because obs-browser makes an arbitrary renderer available
// with no plugin -- browser_source is bundled with OBS and confirmed
// present on this one (GetInputKindList, 2026-08-23). The exposure it
// carries is the same one the design already accepts for OBS itself: the
// cask is unpinned and nix owns none of it, so the kind is capability-
// probed like every filter and refuses by name when it is absent.
//
// The PAGE, by contrast, nix does own -- it is embedded in this binary from
// the source tree nix builds, so it can never be a version behind the code
// that places it. It is written out to the state dir and loaded by local
// file path, and it must never reach the network: an overlay that fetched
// anything would render differently, or not at all, depending on what the
// wifi is doing mid-demo.

//go:embed lemmings.html
var lemmingsPage string

const (
	// LemmingsInput is the browser source's name in the reno scene.
	LemmingsInput = "reno-lemmings"
	// LemmingsKind is the stock OBS browser source. There is no fallback:
	// a build of OBS without obs-browser has nothing that renders html, so
	// the effect refuses rather than substituting something else.
	LemmingsKind = "browser_source"
	// LemmingsFile is the page's name under the reno state dir.
	LemmingsFile = "lemmings.html"
	// LemmingsW/LemmingsH are the page's own canvas. It draws to whatever
	// size it is given, so this only sets the resolution the overlay is
	// rendered at before OBS scales it into the camera's box -- 720p is
	// past what a corner PiP can show and cheap for a page drawing a dozen
	// rectangles a frame.
	LemmingsW = 1280
	LemmingsH = 720
)

// LemmingsSettings is the browser source's configuration. Local file, never
// a url: the page is on disk beside the run journals, and the distinction
// is the whole no-network guarantee.
//
// shutdown_source_when_not_visible is on because the page animates
// continuously -- an overlay toggled off should stop costing a render
// thread, and it has no state worth preserving across a toggle.
func LemmingsSettings(path string) map[string]any {
	return map[string]any{
		"is_local_file":       true,
		"local_file":          path,
		"width":               LemmingsW,
		"height":              LemmingsH,
		"reroute_audio":       false,
		"shutdown":            true,
		"restart_when_active": true,
		// obs-browser's default css is what makes a page transparent; set
		// it explicitly rather than inherit a default, so the overlay is
		// not a grey rectangle over the camera on some future OBS
		"css": "body { background-color: rgba(0, 0, 0, 0); margin: 0px auto; overflow: hidden; }",
	}
}

// WriteLemmingsPage puts the embedded page in dir and returns its path,
// writing only when the file on disk is not already this exact page.
//
// Skipping the identical write is not an optimisation. The browser source
// points at this path, and rewriting the file under a live source is how
// you get the overlay blinking out mid-demo on an apply that changed
// nothing about it.
func WriteLemmingsPage(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("lemmings: no state dir to write %s into", LemmingsFile)
	}
	path := filepath.Join(dir, LemmingsFile)
	if have, err := os.ReadFile(path); err == nil && pageSum(string(have)) == pageSum(lemmingsPage) {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("lemmings: %w", err)
	}
	// tmp+rename, so a source reading the file mid-write can never see a
	// half-written page
	tmp := path + ".next"
	if err := os.WriteFile(tmp, []byte(lemmingsPage), 0o600); err != nil {
		return "", fmt.Errorf("lemmings: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fmt.Errorf("lemmings: %w", err)
	}
	return path, nil
}

func pageSum(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
