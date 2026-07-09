package keyboard

// Moonlander geometry: the fixed 72-slot template that Keymapp's positional
// keys[] array indexes into. There are no per-key x/y on the wire -- slot N
// is the Nth key of the LAYOUT macro -- so the physical arrangement is
// embedded here, derived from the QMK zsa/moonlander keyboard.json shape and
// verified against a real Keymapp export.
//
// Keymapp orders the array LEFT half first (slots 0-35) then RIGHT half
// (36-71). Within a half it walks the 5 main rows top-to-bottom, each row
// left-to-right including its trailing outer/pinky key, then the thumb
// cluster (a 2u key followed by a 3-key arc). Per-half counts: 7,7,7,6,5 main
// + 4 thumb = 36.

// Half names which side a slot sits on.
type Half int

const (
	Left Half = iota
	Right
)

// Slot is one physical key position in the render grid. Row/Col place a main
// key (Col 0 = innermost alpha column .. up to the outer/pinky column); Thumb
// marks a thumb-cluster key, and ThumbIdx orders it (0 = the wide 2u key,
// 1..3 = the arc).
type Slot struct {
	Half     Half
	Row      int // 0..4 for main keys; -1 for thumb keys
	Col      int // 0-based column within the row for main keys; -1 for thumb
	Thumb    bool
	ThumbIdx int // thumb ordering, 0 = wide key
}

// MoonlanderKeys is the physical key count per layer.
const MoonlanderKeys = 72

// halfSlots is one half's 36-slot template (index 0..35). The right half is
// the same shape mirrored, so the full 72-slot table is this list for the
// left half followed by the same list with Half=Right.
var halfSlots = buildHalfSlots()

func buildHalfSlots() []Slot {
	slots := make([]Slot, 0, 36)
	main := func(row, col int) { slots = append(slots, Slot{Half: Left, Row: row, Col: col}) }
	// rows 0-2: 6 main columns (0-5) then the outer/pinky key (col 6)
	for row := range 3 {
		for col := range 6 {
			main(row, col)
		}
		main(row, 6)
	}
	// row 3: 6 main columns, no outer key
	for col := range 6 {
		main(3, col)
	}
	// row 4: 5 main columns, no outer key
	for col := range 5 {
		main(4, col)
	}
	// thumb cluster: the wide 2u key (idx 0), then the 3-key arc (idx 1-3)
	for t := range 4 {
		slots = append(slots, Slot{Half: Left, Row: -1, Col: -1, Thumb: true, ThumbIdx: t})
	}
	return slots
}

// MoonlanderSlots returns the full 72-slot render template: left half (0-35)
// then right half (36-71), the right side mirrored.
func MoonlanderSlots() []Slot {
	out := make([]Slot, 0, MoonlanderKeys)
	out = append(out, halfSlots...)
	for _, s := range halfSlots {
		s.Half = Right
		out = append(out, s)
	}
	return out
}

// MainCols is the widest main row (rows 0-2 carry 7 columns including the
// outer key); the render grid reserves this many main columns per half.
const MainCols = 7
