// Package keydict resolves QMK key codes to short display legends. The
// dictionary is a vendored snapshot (dict.json, 2026-07-21) of the key
// metadata Oryx ships and keymapp used to sync locally -- no public Oryx
// query serves it, so it is embedded: trimmed to code/label/glyph, deleted
// entries dropped, first entry per code kept. A stale snapshot degrades to
// humanized codes for new keys, never fails.
package keydict

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"
)

//go:embed dict.json
var dictJSON []byte

// Entry is one dictionary row: how Oryx labels a QMK code.
type Entry struct {
	Code  string `json:"code"`
	Label string `json:"label"`
	Glyph string `json:"glyph,omitempty"`
}

// Dict maps a QMK code to its entry. The zero value is usable: a nil Dict
// degrades every lookup to the humanized code.
type Dict map[string]Entry

var (
	embedOnce sync.Once
	embedded  Dict
)

// Embedded returns the vendored dictionary, parsed once. The embed is
// build-time data; a decode failure is a broken build, so it panics rather
// than smuggling an error into every render path.
func Embedded() Dict {
	embedOnce.Do(func() {
		var rows []Entry
		if err := json.Unmarshal(dictJSON, &rows); err != nil {
			panic("keydict: corrupt embedded dict.json: " + err.Error())
		}
		embedded = make(Dict, len(rows))
		for _, e := range rows {
			embedded[e.Code] = e
		}
	})
	return embedded
}

// glyphRunes maps Oryx's symbolic glyph names to a compact Unicode legend
// (rune-constructed so the source stays ASCII). Only names the vendored
// dict actually carries (or codeAlias references) are mapped; anything
// else falls through to the entry's label.
var glyphRunes = map[string]string{
	"arrow_left":  string(rune(0x2190)), // left arrow
	"arrow_up":    string(rune(0x2191)),
	"arrow_right": string(rune(0x2192)),
	"arrow_down":  string(rune(0x2193)),
	"enter":       string(rune(0x21b5)), // return
	"space":       "spc",
	"backspace":   string(rune(0x232b)), // erase to the left
	"page_up":     "pgup",
	"page_down":   "pgdn",
}

// transparentGlyph is the QMK convention for KC_TRANSPARENT (the key falls
// through to the layer below); the dictionary's own label ("transparent")
// crops to noise at narrow key widths.
const transparentGlyph = string(rune(0x25bd)) // white down-pointing triangle

// Legend resolves a code to a short display string via the dictionary. Order:
// the transparent convention glyph, a glyph name mapped to Unicode, then the
// human label, then a humanized code. A nil Dict degrades straight to the
// humanized code.
func (d Dict) Legend(code string) string {
	if code == "" {
		return ""
	}
	if code == "KC_TRANSPARENT" || code == "KC_TRNS" {
		return transparentGlyph
	}
	if e, ok := d[code]; ok {
		if e.Glyph != "" {
			if g, ok := glyphRunes[e.Glyph]; ok {
				return g
			}
		}
		if e.Label != "" {
			if strings.EqualFold(e.Label, "transparent") {
				return transparentGlyph
			}
			return e.Label
		}
	}
	return humanizeCode(code)
}

// humanizeCode turns a raw QMK code into a readable token when the dictionary
// has no entry (e.g. KC_BSPC, SC_LCPO, QK_BOOT). Strips the family prefix and
// lowercases; a handful of well-known bare codes get friendlier names.
func humanizeCode(code string) string {
	if s, ok := codeAlias[code]; ok {
		return s
	}
	c := code
	for _, p := range []string{"KC_", "QK_", "SC_", "CW_", "RGB_", "TG_", "TO_", "MO_", "OSL_", "OSM_"} {
		c = strings.TrimPrefix(c, p)
	}
	return strings.ToLower(strings.ReplaceAll(c, "_", " "))
}

// codeAlias names bare codes the dictionary omits, so common thumb/mod keys
// read cleanly instead of as stripped QMK spelling. Codes the dictionary
// resolves (dict label/glyph wins in Legend) do not belong here -- an entry
// shadowed by the dict is dead.
var codeAlias = map[string]string{
	"KC_BSPC":          "bksp",
	"KC_LBRC":          "[",
	"KC_RBRC":          "]",
	"KC_BSLS":          "\\",
	"KC_SCLN":          ";",
	"KC_RIGHT_CTRL":    "rctl",
	"KC_PAGE_UP":       glyphRunes["page_up"],
	"KC_PGDN":          glyphRunes["page_down"],
	"SC_LCPO":          "(",
	"SC_RCPC":          ")",
	"CW_TOGG":          "caps",
	"QK_BOOT":          "boot",
	"RGB_MODE_FORWARD": "rgb",
}
