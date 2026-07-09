package keyboard

import (
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/keymappdb"
)

const fixtureDB = "keymappdb/testdata/fixture.sqlite3"

// needSqlite3 skips when the exec'd keymappdb reader's sqlite3 CLI is
// missing (skip-on-missing: the nix checkPhase has no host binaries).
func needSqlite3(t *testing.T) {
	t.Helper()
	if _, err := keymappdb.Sqlite3Bin(); err != nil {
		t.Skipf("sqlite3: %v", err)
	}
}

// The geometry template covers all 72 slots: 36 per half, 32 main + 4 thumb
// each, and the halves partition Left/Right.
func TestMoonlanderSlotsShape(t *testing.T) {
	slots := MoonlanderSlots()
	if len(slots) != MoonlanderKeys {
		t.Fatalf("slots = %d, want %d", len(slots), MoonlanderKeys)
	}
	var left, right, thumbs int
	rowCount := map[int]int{}
	for i, s := range slots {
		switch s.Half {
		case Left:
			left++
			if i >= 36 {
				t.Errorf("slot %d is Left but past the left range", i)
			}
		case Right:
			right++
			if i < 36 {
				t.Errorf("slot %d is Right but in the left range", i)
			}
		}
		if s.Thumb {
			thumbs++
		} else if s.Half == Left {
			rowCount[s.Row]++
		}
	}
	if left != 36 || right != 36 {
		t.Errorf("halves = %d/%d, want 36/36", left, right)
	}
	if thumbs != 8 {
		t.Errorf("thumbs = %d, want 8 (4 per half)", thumbs)
	}
	// left main row widths: rows 0-2 have 7 (incl outer), row3 6, row4 5
	for row, want := range map[int]int{0: 7, 1: 7, 2: 7, 3: 6, 4: 5} {
		if rowCount[row] != want {
			t.Errorf("left row %d = %d keys, want %d", row, rowCount[row], want)
		}
	}
}

// FromRevision parses the fixture into a board: 4 layers named home/syms/
// osm-nav/sys, each with 72 placed keys, and known keys resolve to legends.
func TestFromRevisionFixture(t *testing.T) {
	needSqlite3(t)
	rev, err := keymappdb.Active(fixtureDB)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	b := FromRevision(rev)
	if b.Title != "aw4" {
		t.Errorf("title = %q, want aw4", b.Title)
	}
	if b.LayoutID != "0Nw4x" {
		t.Errorf("layout id = %q, want 0Nw4x", b.LayoutID)
	}
	if b.Geometry != "moonlander" {
		t.Errorf("geometry = %q, want moonlander", b.Geometry)
	}
	if len(b.Layers) != 4 {
		t.Fatalf("layers = %d, want 4", len(b.Layers))
	}
	if b.Layers[0].Title != "home" {
		t.Errorf("layer 0 title = %q, want home", b.Layers[0].Title)
	}
	for i, l := range b.Layers {
		if len(l.Keys) != MoonlanderKeys {
			t.Errorf("layer %d keys = %d, want %d", i, len(l.Keys), MoonlanderKeys)
		}
	}

	// slot 0 is the top-left key (KC_GRAVE -> "`"); slot 8 is Q on layer 0
	home := b.Layers[0]
	if home.Keys[0].Slot.Half != Left || home.Keys[0].Slot.Row != 0 || home.Keys[0].Slot.Col != 0 {
		t.Errorf("slot 0 placement = %+v, want Left row0 col0", home.Keys[0].Slot)
	}
	if home.Keys[0].Tap != "`" {
		t.Errorf("slot 0 tap = %q, want backtick", home.Keys[0].Tap)
	}

	// a layer-switch key (OSL/MO/LT) carries its target layer index for tint
	sawLayerSwitch := false
	for _, k := range home.Keys {
		if k.TapLayer >= 0 || k.HoldLayer >= 0 {
			sawLayerSwitch = true
			break
		}
	}
	if !sawLayerSwitch {
		t.Error("no layer-switch key found on the home layer")
	}
}

// A customLabel wins over the dictionary legend (the user's own key text).
func TestPlaceKeyCustomLabel(t *testing.T) {
	needSqlite3(t)
	rev, err := keymappdb.Active(fixtureDB)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	b := FromRevision(rev)
	found := ""
	oslLayer := -2
	for _, l := range b.Layers {
		for _, k := range l.Keys {
			if k.Tap == "1 osm/nav" || k.Tap == "1 up" {
				found = k.Tap
			}
			if k.Tap == "1 osm/nav" && oslLayer == -2 {
				oslLayer = k.TapLayer
			}
		}
	}
	if found == "" {
		t.Error("no customLabel legend surfaced (fixture has custom labels)")
	}
	// a custom-labeled layer-switch key keeps its TapLayer for the tint: the
	// fixture's "1 osm/nav" is {"customLabel":...,"tap":{"code":"OSL","layer":2}}
	if oslLayer == -2 {
		t.Fatal("fixture lost its custom-labeled OSL key")
	}
	if oslLayer != 2 {
		t.Errorf("custom-labeled OSL key TapLayer = %d, want 2", oslLayer)
	}
}
