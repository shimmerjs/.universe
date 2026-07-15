// Package chord models kitty key chords and parses kitty key-spec strings.
// The string parser exists for authoring shorthand and for adapting kitten
// output ONLY; the normalized interchange form is []Chord.
package chord

import (
	"fmt"
	"strings"
)

// Chord is one key press: zero or more modifiers plus a key.
type Chord struct {
	Mods []string `json:"mods,omitempty"`
	Key  string   `json:"key"`
}

// canonical modifier order, matching kitty's human_repr emission order
// (kitty_mod first, then modmap order on macOS), with fn appended.
var modOrder = []string{
	"kitty_mod", "ctrl", "shift", "opt", "cmd", "hyper", "meta", "fn", "caps_lock", "num_lock",
}

// modAlias maps every accepted modifier spelling to its canonical name.
var modAlias = map[string]string{
	"kitty_mod": "kitty_mod",
	"ctrl":      "ctrl",
	"control":   "ctrl",
	"shift":     "shift",
	"opt":       "opt",
	"option":    "opt",
	"alt":       "opt",
	"cmd":       "cmd",
	"command":   "cmd",
	"super":     "cmd",
	"hyper":     "hyper",
	"meta":      "meta",
	"fn":        "fn",
	"caps_lock": "caps_lock",
	"num_lock":  "num_lock",
}

var modRank = func() map[string]int {
	m := make(map[string]int, len(modOrder))
	for i, n := range modOrder {
		m[n] = i
	}
	return m
}()

// keyAlias maps authoring key names to the canonical (kitten-emitted) form.
var keyAlias = map[string]string{
	"plus":  "+",
	"space": "space",
}

// NormalizeMod resolves a modifier spelling to its canonical name.
func NormalizeMod(m string) (string, error) {
	c, ok := modAlias[strings.ToLower(m)]
	if !ok {
		return "", fmt.Errorf("unknown modifier %q", m)
	}
	return c, nil
}

// Normalize returns c with aliased mods resolved, mods deduped and put in
// canonical order, and the key lowercased and de-aliased.
func Normalize(c Chord) (Chord, error) {
	if c.Key == "" {
		return Chord{}, fmt.Errorf("chord has no key")
	}
	seen := make(map[string]bool, len(c.Mods))
	mods := make([]string, 0, len(c.Mods))
	for _, m := range c.Mods {
		cm, err := NormalizeMod(m)
		if err != nil {
			return Chord{}, err
		}
		if !seen[cm] {
			seen[cm] = true
			mods = append(mods, cm)
		}
	}
	for i := 1; i < len(mods); i++ {
		for j := i; j > 0 && modRank[mods[j-1]] > modRank[mods[j]]; j-- {
			mods[j-1], mods[j] = mods[j], mods[j-1]
		}
	}
	key := strings.ToLower(c.Key)
	if a, ok := keyAlias[key]; ok {
		key = a
	}
	if len(mods) == 0 {
		mods = nil
	}
	return Chord{Mods: mods, Key: key}, nil
}

// Canonical renders the normalized chord as a key-spec string. Chords that do
// not normalize are rendered as-is (id stability over strictness; Vet is the
// place that rejects them).
func Canonical(c Chord) string {
	n, err := Normalize(c)
	if err != nil {
		n = c
	}
	return strings.Join(append(append([]string{}, n.Mods...), n.Key), "+")
}

// CanonicalSeq renders a chord sequence as a canonical key-spec string.
func CanonicalSeq(cs []Chord) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = Canonical(c)
	}
	return strings.Join(parts, ">")
}

// ParseSpec parses a kitty key-spec string into a chord sequence. Grammar is
// kitty's: mods and key joined by '+', a trailing '+' means the literal '+'
// key (cmd++), '>' separates sequence chords (spaces around it tolerated, the
// kitten emits " > "), and '+>' is the literal '>' key.
func ParseSpec(spec string) ([]Chord, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("empty key spec")
	}
	var out []Chord
	for _, tok := range splitSeq(spec) {
		c, err := parseChord(tok)
		if err != nil {
			return nil, fmt.Errorf("key spec %q: %w", spec, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// splitSeq splits a spec on sequence separators. A '>' separates chords
// unless it starts a chord (the key is '>') or is directly adjacent to a
// preceding '+' (the literal '>' key; kitten output always spaces its
// separators as " > ", so adjacency is unambiguous).
func splitSeq(s string) []string {
	var parts []string
	var cur strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '>' && cur.Len() > 0 && !strings.HasSuffix(cur.String(), "+") {
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(ch)
	}
	if t := strings.TrimSpace(cur.String()); t != "" {
		parts = append(parts, t)
	}
	return parts
}

func parseChord(tok string) (Chord, error) {
	if tok == "" {
		return Chord{}, fmt.Errorf("empty chord")
	}
	if tok == "+" {
		return Chord{Key: "+"}, nil
	}
	// kitty: a trailing '++' is the literal plus key (cmd++ == cmd+plus).
	// A lone trailing '+' (ctrl+) stays a parse error rather than kitty's
	// accidental "ctrlplus" key.
	if strings.HasSuffix(tok, "++") {
		tok = tok[:len(tok)-1] + "plus"
	}
	parts := strings.Split(tok, "+")
	c := Chord{Key: parts[len(parts)-1], Mods: parts[:len(parts)-1]}
	if c.Key == "" {
		return Chord{}, fmt.Errorf("chord %q has no key", tok)
	}
	return Normalize(c)
}

// ParseMods parses a '+'-joined modifier list (the kitty_mod value shape:
// every part is a modifier, there is no key).
func ParseMods(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty modifier list")
	}
	parts := strings.Split(s, "+")
	mods := make([]string, 0, len(parts))
	for _, p := range parts {
		m, err := NormalizeMod(p)
		if err != nil {
			return nil, err
		}
		mods = append(mods, m)
	}
	return mods, nil
}

// Expand replaces aliased modifiers (kitty_mod and friends) with their
// definitions from meta.modAliases, re-normalizing the result.
func Expand(cs []Chord, aliases map[string][]string) []Chord {
	if len(aliases) == 0 {
		return cs
	}
	out := make([]Chord, len(cs))
	for i, c := range cs {
		mods := make([]string, 0, len(c.Mods))
		for _, m := range c.Mods {
			if exp, ok := aliases[m]; ok {
				mods = append(mods, exp...)
			} else {
				mods = append(mods, m)
			}
		}
		n, err := Normalize(Chord{Mods: mods, Key: c.Key})
		if err != nil {
			n = Chord{Mods: mods, Key: c.Key}
		}
		out[i] = n
	}
	return out
}
