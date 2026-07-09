// Identity palette for khudson-owned surfaces (rail buttons, claude session
// names). DOCTRINE: identity colors are DATA-NOT-STYLE -- the same
// carve-out as gauge heat: which-app / which-session is information, so
// identity may exceed plain chrome styling. The DEFAULT assignment stays
// theme-constrained by drawing only from the ANSI-16 palette (the kitty
// theme still owns how those hues look); explicit config overrides may
// use hex.
package dock

import (
	"hash/fnv"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// identityHues are the usable ANSI-16 hues: red, green, yellow, blue,
// magenta, cyan and their brights. Black, white, and bright-black are
// excluded so identities never vanish into fg/bg.
var identityHues = []color.Color{
	lipgloss.Red, lipgloss.Green, lipgloss.Yellow,
	lipgloss.Blue, lipgloss.Magenta, lipgloss.Cyan,
	lipgloss.BrightRed, lipgloss.BrightGreen, lipgloss.BrightYellow,
	lipgloss.BrightBlue, lipgloss.BrightMagenta, lipgloss.BrightCyan,
}

// hueNames maps ANSI color names to palette entries for config overrides;
// lipgloss.Color itself parses only hex and numeric strings.
var hueNames = map[string]color.Color{
	"red": lipgloss.Red, "green": lipgloss.Green, "yellow": lipgloss.Yellow,
	"blue": lipgloss.Blue, "magenta": lipgloss.Magenta, "cyan": lipgloss.Cyan,
	"bright-red": lipgloss.BrightRed, "bright-green": lipgloss.BrightGreen,
	"bright-yellow": lipgloss.BrightYellow, "bright-blue": lipgloss.BrightBlue,
	"bright-magenta": lipgloss.BrightMagenta, "bright-cyan": lipgloss.BrightCyan,
}

// identityHue hashes key to one hue, stably: same key = same hue every
// frame and every restart, never a per-poll flicker.
func identityHue(key string) color.Color {
	h := fnv.New32a()
	h.Write([]byte(key))
	return identityHues[h.Sum32()%uint32(len(identityHues))]
}

// parseIdentColor turns a config override string into a color: ANSI names
// from the palette vocabulary, else hex ("#fb4934") or ANSI numbers via
// lipgloss.Color.
func parseIdentColor(s string) color.Color {
	if c, ok := hueNames[strings.ToLower(s)]; ok {
		return c
	}
	return lipgloss.Color(s)
}

// identityColor resolves key: an explicit override beats the stable hash.
func identityColor(key string, overrides map[string]string) color.Color {
	if c, ok := overrides[key]; ok && c != "" {
		return parseIdentColor(c)
	}
	return identityHue(key)
}
