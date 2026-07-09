package keyboard

// QMK matrix bridge. The Moonlander firmware's raw-HID Oryx protocol
// streams key events as MATRIX coordinates ([0x06,col,row] etc, see
// touchd), while the render side addresses keys by Keymapp slot index
// (geometry.go). The matrix shape comes verbatim from QMK's
// zsa/moonlander keyboard.json: 12 rows x 7 cols, left half rows 0-5,
// right half rows 6-11; within a half, main-row cols ascend with the
// LAYOUT walk (left: outer->inner, right: inner->outer -- both visually
// left-to-right), short rows anchor at the outer edge (right row 3 starts
// at col 1, right row 4 at col 2), and the thumb cluster is col 3 for the
// wide key with the left arc at cols 0-2 and the right arc at cols 4-6 of
// the half's row 5. Verified against the fixture export: QWERTY rows and
// the thumb legends land on their physical keys under this bridge.

// Matrix dimensions per QMK zsa/moonlander.
const (
	MatrixRows = 12
	MatrixCols = 7
)

// matrixOf places one geometry slot on the QMK matrix.
func matrixOf(s Slot) (row, col int) {
	base := 0
	if s.Half == Right {
		base = 6
	}
	if s.Thumb {
		// wide key = col 3; the arc fills the remaining in-walk cols
		// (left [5,0..2], right [11,4..6])
		if s.ThumbIdx == 0 {
			return base + 5, 3
		}
		if s.Half == Left {
			return base + 5, s.ThumbIdx - 1
		}
		return base + 5, s.ThumbIdx + 3
	}
	col = s.Col
	if s.Half == Right {
		// right short rows lose their INNER keys, which lead the walk:
		// row 3 spans matrix cols 1-6, row 4 spans 2-6
		switch s.Row {
		case 3:
			col++
		case 4:
			col += 2
		}
	}
	return base + s.Row, col
}

// slotAt is the inverse table, built from the slot template so the bridge
// can never drift from the geometry.
var slotAt = buildSlotAt()

func buildSlotAt() [MatrixRows][MatrixCols]int {
	var t [MatrixRows][MatrixCols]int
	for r := range t {
		for c := range t[r] {
			t[r][c] = -1
		}
	}
	for i, s := range MoonlanderSlots() {
		r, c := matrixOf(s)
		t[r][c] = i
	}
	return t
}

// SlotAt resolves a firmware matrix coordinate to the Keymapp slot index
// (the position in Layer.Keys), or -1 for coordinates with no physical key.
func SlotAt(row, col int) int {
	if row < 0 || row >= MatrixRows || col < 0 || col >= MatrixCols {
		return -1
	}
	return slotAt[row][col]
}
