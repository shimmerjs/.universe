package keydict

import "testing"

// arrowLeft is the Unicode legend the arrow_left glyph maps to
// (rune-constructed so the source stays ASCII).
var arrowLeft = string(rune(0x2190))

func TestEmbeddedLoads(t *testing.T) {
	d := Embedded()
	if len(d) < 2000 {
		t.Fatalf("embedded dict has %d entries, want the full vendored snapshot", len(d))
	}
	if e := d["RESET"]; e.Label != "Reset" {
		t.Errorf(`RESET label = %q, want "Reset"`, e.Label)
	}
}

// Resolution order: glyph -> label -> alias -> humanized, with the
// transparent convention short-circuiting the dictionary entirely.
func TestLegend(t *testing.T) {
	d := Embedded()
	for _, tc := range []struct{ code, want string }{
		{"KC_LEFT", arrowLeft}, // glyph mapped to Unicode
		{"KC_A", "A"},          // plain label
		{"RESET", "Reset"},     // the flash trigger key
		{"KC_TRANSPARENT", transparentGlyph},
		{"KC_TRNS", transparentGlyph},
		{"KC_ZZZ_NOT_A_CODE", "zzz not a code"}, // humanized fallback
		{"", ""},
	} {
		if got := d.Legend(tc.code); got != tc.want {
			t.Errorf("Legend(%q) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

// A nil Dict must stay usable: aliases and humanizing work without the
// vendored data.
func TestLegendNilDict(t *testing.T) {
	var d Dict
	if got := d.Legend("QK_BOOT"); got != "boot" {
		t.Errorf(`nil Legend(QK_BOOT) = %q, want "boot"`, got)
	}
	if got := d.Legend("KC_BSPC"); got != "bksp" {
		t.Errorf(`nil Legend(KC_BSPC) = %q, want "bksp"`, got)
	}
}
