package keyboard

import "testing"

// Every slot maps to a unique in-range matrix coordinate and SlotAt inverts
// the bridge exactly; unmapped coordinates (KC_NO holes) resolve to -1.
func TestMatrixBridgeRoundTrip(t *testing.T) {
	seen := map[[2]int]int{}
	for i, s := range MoonlanderSlots() {
		r, c := matrixOf(s)
		if r < 0 || r >= MatrixRows || c < 0 || c >= MatrixCols {
			t.Fatalf("slot %d: matrix (%d,%d) out of range", i, r, c)
		}
		if prev, dup := seen[[2]int{r, c}]; dup {
			t.Fatalf("slots %d and %d collide at matrix (%d,%d)", prev, i, r, c)
		}
		seen[[2]int{r, c}] = i
		if got := SlotAt(r, c); got != i {
			t.Fatalf("SlotAt(%d,%d) = %d, want %d", r, c, got, i)
		}
	}
	if len(seen) != MoonlanderKeys {
		t.Fatalf("mapped %d coordinates, want %d", len(seen), MoonlanderKeys)
	}
	holes := 0
	for r := range MatrixRows {
		for c := range MatrixCols {
			if SlotAt(r, c) == -1 {
				holes++
			}
		}
	}
	if holes != MatrixRows*MatrixCols-MoonlanderKeys {
		t.Fatalf("holes = %d, want %d", holes, MatrixRows*MatrixCols-MoonlanderKeys)
	}
}

// Spot checks pinned to QMK zsa/moonlander keyboard.json (LAYOUT macro
// matrix pairs) and the fixture export's QWERTY orientation.
func TestMatrixBridgeSpots(t *testing.T) {
	for _, tt := range []struct {
		name     string
		row, col int
		slot     int
	}{
		{"left top-left (grave)", 0, 0, 0},
		{"left top inner", 0, 6, 6},
		{"Q (left row1 col1)", 1, 1, 8},
		{"left row3 first (lcpo)", 3, 0, 21},
		{"left row4 last (cmd)", 4, 4, 31},
		{"left thumb wide", 5, 3, 32},
		{"left arc first (spc)", 5, 0, 33},
		{"left arc last (tab)", 5, 2, 35},
		{"right top inner", 6, 0, 36},
		{"right top outer (osl)", 6, 6, 42},
		{"Y (right row1 col1)", 7, 1, 44},
		{"P (right row1 col5)", 7, 5, 48},
		{"N (right row3, matrix col1)", 9, 1, 57},
		{"right row3 outer (rcpc)", 9, 6, 62},
		{"right row4 first (matrix col2)", 10, 2, 63},
		{"right row4 outer (rctl)", 10, 6, 67},
		{"right thumb wide", 11, 3, 68},
		{"right arc first (esc)", 11, 4, 69},
		{"right arc last (enter)", 11, 6, 71},
	} {
		if got := SlotAt(tt.row, tt.col); got != tt.slot {
			t.Errorf("%s: SlotAt(%d,%d) = %d, want %d", tt.name, tt.row, tt.col, got, tt.slot)
		}
	}
	// KC_NO holes per the LAYOUT macro
	for _, hole := range [][2]int{{3, 6}, {4, 5}, {4, 6}, {5, 4}, {5, 5}, {5, 6}, {9, 0}, {10, 0}, {10, 1}, {11, 0}, {11, 1}, {11, 2}} {
		if got := SlotAt(hole[0], hole[1]); got != -1 {
			t.Errorf("SlotAt(%d,%d) = %d, want -1 (KC_NO)", hole[0], hole[1], got)
		}
	}
	// out of range never panics
	for _, bad := range [][2]int{{-1, 0}, {0, -1}, {12, 0}, {0, 7}} {
		if got := SlotAt(bad[0], bad[1]); got != -1 {
			t.Errorf("SlotAt(%d,%d) = %d, want -1", bad[0], bad[1], got)
		}
	}
}
