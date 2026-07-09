// Package media is the now-playing module: Spotify desktop app state via
// AppleScript. Pure data mapper -- osascript is the only shell-out.
package media

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// script returns state/artist/track/album on one line, joined by the ASCII
// unit separator (0x1f, which cannot appear in metadata), or nothing when
// Spotify isn't running.
const script = `tell application "System Events" to set spotifyRunning to exists (processes where name is "Spotify")
if spotifyRunning then tell application "Spotify" to return (player state as text) & (ASCII character 31) & (artist of current track) & (ASCII character 31) & (name of current track) & (ASCII character 31) & (album of current track)`

// Mod implements module.Module. run is the osascript exec seam; the zero
// value (the registry's media.Mod{}) runs the real command.
type Mod struct {
	run func(ctx context.Context) ([]byte, error)
}

func (Mod) Name() string { return "media" }

func (m Mod) Poll(ctx context.Context, _ map[string]any) (module.Data, error) {
	run := m.run
	if run == nil {
		run = func(ctx context.Context) ([]byte, error) {
			return exec.CommandContext(ctx, "osascript", "-e", script).Output()
		}
	}
	out, err := run(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return module.Data{}, fmt.Errorf("osascript: %w", ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) && calmSpotifyErr(ee.Stderr) {
			// no track loaded / Spotify absent: AppleScript raises with
			// empty stdout -- the calm dim row, not a per-poll loud tile
			return renderNowPlaying(nowPlaying{}), nil
		}
		// loud otherwise: a live-ctx exec failure is a TCC/automation
		// denial or a broken osascript -- never masked as not-running
		return module.Data{}, fmt.Errorf("osascript: %w", err)
	}
	np, err := parseNowPlaying(string(out))
	if err != nil {
		return module.Data{}, err
	}
	return renderNowPlaying(np), nil
}

// calmSpotifyErr classifies osascript stderr for the two calm failure
// states: -1728 (can't get current track -- running, nothing loaded) and
// a missing application reference (Spotify not installed). Stderr string
// matching is brittle across macOS versions; anything unrecognized stays
// LOUD, which is the safe direction.
func calmSpotifyErr(stderr []byte) bool {
	s := string(stderr)
	return strings.Contains(s, "-1728") || strings.Contains(s, "Can't get application")
}

// nowPlaying is the parsed script output; zero value means not running.
type nowPlaying struct {
	Running bool
	State   string // "playing" / "paused"
	Artist  string
	Track   string
	Album   string
}

// parseNowPlaying splits the unit-separator-joined state/artist/track/album
// line; empty input means Spotify isn't running.
func parseNowPlaying(out string) (nowPlaying, error) {
	line := strings.TrimSpace(out)
	if line == "" {
		return nowPlaying{}, nil
	}
	parts := strings.Split(line, "\x1f")
	if len(parts) != 4 {
		return nowPlaying{}, fmt.Errorf("osascript: unexpected output %q", line)
	}
	return nowPlaying{
		Running: true,
		State:   strings.TrimSpace(parts[0]),
		Artist:  strings.TrimSpace(parts[1]),
		Track:   strings.TrimSpace(parts[2]),
		Album:   strings.TrimSpace(parts[3]),
	}, nil
}

func renderNowPlaying(np nowPlaying) module.Data {
	if !np.Running {
		return module.Data{Title: "media", Rows: []module.Row{
			{Kind: module.RowText, Text: "spotify not running", Style: module.StyleDim},
		}}
	}
	return module.Data{Title: "media", Rows: []module.Row{
		{Kind: module.RowText, Text: np.Track, Style: module.StyleAccent},
		module.Text(np.Artist),
		{Kind: module.RowText, Text: np.Album, Style: module.StyleDim},
		module.KV("state", np.State),
	}}
}
