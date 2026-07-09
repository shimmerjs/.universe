package keymappdb

import (
	"strings"
)

// dictEntry is one metadata.keys row: a QMK code and how Keymapp labels it.
type dictEntry struct {
	Code    string `json:"code"`
	Label   string `json:"label"`
	Glyph   string `json:"glyph"`
	Deleted bool   `json:"deleted"`
}

// Dict maps a QMK code to its metadata entry.
type Dict map[string]dictEntry

// glyphRunes maps Keymapp's symbolic glyph names to a compact Unicode legend
// (escaped so the source stays ASCII). Only names on physical keys are
// mapped; anything else falls through to the entry's label.
var glyphRunes = map[string]string{
	"arrow_left":  "\u2190", // left arrow
	"arrow_up":    "\u2191",
	"arrow_right": "\u2192",
	"arrow_down":  "\u2193",
	"enter":       "\u21b5", // return
	"space":       "spc",
	"backspace":   "\u232b", // erase to the left
	"tab":         "\u21b9", // tab
	"escape":      "esc",
	"delete":      "del",
	"home":        "home",
	"end":         "end",
	"page_up":     "pgup",
	"page_down":   "pgdn",
}

// Legend resolves a code to a short display string via the dictionary. Order:
// a glyph name mapped to Unicode, then the human label, then a humanized
// code. A nil Dict degrades straight to the humanized code.
func (d Dict) Legend(code string) string {
	if code == "" {
		return ""
	}
	if e, ok := d[code]; ok {
		if e.Glyph != "" {
			if g, ok := glyphRunes[e.Glyph]; ok {
				return g
			}
		}
		if e.Label != "" {
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
// read cleanly instead of as stripped QMK spelling.
var codeAlias = map[string]string{
	"KC_BSPC":          "bksp",
	"KC_LBRC":          "[",
	"KC_RBRC":          "]",
	"KC_BSLS":          "\\",
	"KC_SCLN":          ";",
	"KC_QUOTE":         "'",
	"KC_COMMA":         ",",
	"KC_DOT":           ".",
	"KC_SLASH":         "/",
	"KC_GRAVE":         "`",
	"KC_SPACE":         glyphRunes["space"],
	"KC_TAB":           glyphRunes["tab"],
	"KC_ENTER":         glyphRunes["enter"],
	"KC_ESCAPE":        glyphRunes["escape"],
	"KC_RIGHT_CTRL":    "rctl",
	"KC_PAGE_UP":       glyphRunes["page_up"],
	"KC_PGDN":          glyphRunes["page_down"],
	"SC_LCPO":          "(",
	"SC_RCPC":          ")",
	"CW_TOGG":          "caps",
	"QK_BOOT":          "boot",
	"RGB_MODE_FORWARD": "rgb",
}
