package chord

import "strings"

// glyphs maps canonical part names to display glyphs. Go escapes keep the
// source ASCII. Parts without an entry display as their name.
var glyphs = map[string]string{
	"cmd":       "\u2318",
	"opt":       "\u2325",
	"ctrl":      "\u2303",
	"shift":     "\u21e7",
	"enter":     "\u23ce",
	"escape":    "\u238b",
	"backspace": "\u232b",
	"tab":       "\u21e5",
	"up":        "\u2191",
	"down":      "\u2193",
	"left":      "\u2190",
	"right":     "\u2192",
}

// Glyph returns the display form of a single mod or key name.
func Glyph(part string) string {
	if g, ok := glyphs[strings.ToLower(part)]; ok {
		return g
	}
	return part
}

// Format renders one chord for display: glyphed parts joined by " + ".
func Format(c Chord) string {
	parts := make([]string, 0, len(c.Mods)+1)
	for _, m := range c.Mods {
		parts = append(parts, Glyph(m))
	}
	parts = append(parts, Glyph(c.Key))
	return strings.Join(parts, " + ")
}

// FormatSeq renders a chord sequence for display, chords joined by " > ".
func FormatSeq(cs []Chord) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = Format(c)
	}
	return strings.Join(parts, " > ")
}

// FormatMods renders a bare modifier list (e.g. a kitty_mod definition).
func FormatMods(mods []string) string {
	parts := make([]string, len(mods))
	for i, m := range mods {
		parts[i] = Glyph(m)
	}
	return strings.Join(parts, " + ")
}
