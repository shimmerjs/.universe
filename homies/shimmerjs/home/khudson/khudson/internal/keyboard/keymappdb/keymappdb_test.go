package keymappdb

import (
	"errors"
	"os"
	"testing"
)

const (
	fixtureDB = "testdata/fixture.sqlite3"
	emptyDB   = "testdata/empty.sqlite3"
)

// arrowLeft is the Unicode legend the arrow_left glyph maps to (escaped so
// the source stays ASCII).
const arrowLeft = "\u2190"

// needSqlite3 skips when the exec'd reader's sqlite3 CLI is missing (the
// nix checkPhase sandbox has no host binaries).
func needSqlite3(t *testing.T) {
	t.Helper()
	if _, err := Sqlite3Bin(); err != nil {
		t.Skipf("sqlite3: %v", err)
	}
}

// Active decodes the fixture revision blob (plain JSON) into the Oryx layout
// shape: the aw4 layout, 4 layers, 72 positional keys each.
func TestActiveDecodesFixture(t *testing.T) {
	needSqlite3(t)
	rev, err := Active(fixtureDB)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if rev.ID == "" {
		t.Fatal("revision id empty")
	}
	if rev.Layout == nil {
		t.Fatal("nil layout")
	}
	if rev.Layout.Title != "aw4" {
		t.Errorf("title = %q, want aw4", rev.Layout.Title)
	}
	if len(rev.Layout.Layers) != 4 {
		t.Fatalf("layers = %d, want 4", len(rev.Layout.Layers))
	}
	for i, l := range rev.Layout.Layers {
		if len(l.Keys) != 72 {
			t.Errorf("layer %d keys = %d, want 72", i, len(l.Keys))
		}
	}
}

// The key dictionary resolves codes to legends: a plain letter, a glyph key,
// and an aliased bare code.
func TestActiveDictLegends(t *testing.T) {
	needSqlite3(t)
	rev, err := Active(fixtureDB)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if rev.Dict == nil {
		t.Fatal("nil dict")
	}
	cases := []struct{ code, want string }{
		{"KC_1", "1"},
		{"KC_LEFT", arrowLeft}, // arrow_left glyph mapped to Unicode
		{"KC_BSPC", "bksp"},    // dictionary omits it: codeAlias fallback
		{"", ""},
	}
	for _, c := range cases {
		if got := rev.Dict.Legend(c.code); got != c.want {
			t.Errorf("Legend(%q) = %q, want %q", c.code, got, c.want)
		}
	}
}

// A dictionary miss falls through to the humanized code, never empty.
func TestLegendHumanizesUnknownCode(t *testing.T) {
	var d Dict // nil dict: every code humanizes
	cases := []struct{ code, want string }{
		{"KC_BSPC", "bksp"}, // aliased
		{"MY_CUSTOM", "my custom"},
		{"KC_F13", "f13"},
	}
	for _, c := range cases {
		if got := d.Legend(c.code); got != c.want {
			t.Errorf("Legend(%q) = %q, want %q", c.code, got, c.want)
		}
	}
}

// An empty store (Keymapp never synced) reports ErrNoRevision, never panics.
func TestActiveEmptyDB(t *testing.T) {
	needSqlite3(t)
	_, err := Active(emptyDB)
	if !errors.Is(err, ErrNoRevision) {
		t.Fatalf("err = %v, want ErrNoRevision", err)
	}
}

// A missing file is a plain error, never a panic.
func TestActiveMissingFile(t *testing.T) {
	needSqlite3(t)
	_, err := Active("testdata/does-not-exist.sqlite3")
	if err == nil {
		t.Fatal("missing db must error")
	}
	if errors.Is(err, ErrNoRevision) {
		t.Fatal("missing file is not the empty-store case")
	}
}

// Live-gated: read the user's REAL Keymapp store. Set KHUDSON_KEYMAPP_DB=1 to
// run. Proves the newest-revision heuristic and dict resolution against real
// data.
func TestActiveRealDB(t *testing.T) {
	needSqlite3(t)
	if os.Getenv("KHUDSON_KEYMAPP_DB") == "" {
		t.Skip("set KHUDSON_KEYMAPP_DB=1 to read the real Keymapp store")
	}
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	rev, err := Active(path)
	if err != nil {
		t.Fatalf("Active(real): %v", err)
	}
	t.Logf("real revision %s: %q, %d layers", rev.ID, rev.Layout.Title, len(rev.Layout.Layers))
	if len(rev.Layout.Layers) == 0 {
		t.Fatal("real layout has no layers")
	}
}
