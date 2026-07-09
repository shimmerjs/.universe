package chord

import (
	"reflect"
	"testing"
)

func TestParseSpec(t *testing.T) {
	cases := []struct {
		spec string
		want []Chord
	}{
		{"kitty_mod+t", []Chord{{Mods: []string{"kitty_mod"}, Key: "t"}}},
		{"f1", []Chord{{Key: "f1"}}},
		{"ctrl+cmd+,", []Chord{{Mods: []string{"ctrl", "cmd"}, Key: ","}}},
		// literal plus, both kitten and authoring spellings
		{"cmd++", []Chord{{Mods: []string{"cmd"}, Key: "+"}}},
		{"cmd+plus", []Chord{{Mods: []string{"cmd"}, Key: "+"}}},
		{"+", []Chord{{Key: "+"}}},
		// sequences: kitten emits " > ", authoring uses ">"
		{"kitty_mod+p > f", []Chord{{Mods: []string{"kitty_mod"}, Key: "p"}, {Key: "f"}}},
		{"kitty_mod+p > shift+f", []Chord{{Mods: []string{"kitty_mod"}, Key: "p"}, {Mods: []string{"shift"}, Key: "f"}}},
		{"ctrl+a>ctrl+b>c", []Chord{{Mods: []string{"ctrl"}, Key: "a"}, {Mods: []string{"ctrl"}, Key: "b"}, {Key: "c"}}},
		// literal '>' key via '+>' adjacency
		{"ctrl+>", []Chord{{Mods: []string{"ctrl"}, Key: ">"}}},
		// a sequence whose first chord ends in the plus key
		{"cmd++ > x", []Chord{{Mods: []string{"cmd"}, Key: "+"}, {Key: "x"}}},
		// alias normalization and canonical mod order
		{"super+alt+control+a", []Chord{{Mods: []string{"ctrl", "opt", "cmd"}, Key: "a"}}},
		{"shift+ctrl+A", []Chord{{Mods: []string{"ctrl", "shift"}, Key: "a"}}},
		{"fn+hyper+meta+space", []Chord{{Mods: []string{"hyper", "meta", "fn"}, Key: "space"}}},
	}
	for _, c := range cases {
		got, err := ParseSpec(c.spec)
		if err != nil {
			t.Errorf("ParseSpec(%q): %v", c.spec, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseSpec(%q) = %#v, want %#v", c.spec, got, c.want)
		}
	}
}

func TestParseSpecErrors(t *testing.T) {
	for _, spec := range []string{"", "bogus_mod+x", "ctrl+"} {
		if _, err := ParseSpec(spec); err == nil {
			t.Errorf("ParseSpec(%q): want error", spec)
		}
	}
}

// The old splitKeys mangled sequences: "ctrl+a > ctrl+b" became
// ["ctrl","a > ctrl","b"]. Pin the fix.
func TestSequenceBugFixed(t *testing.T) {
	got, err := ParseSpec("ctrl+a > ctrl+b")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "a" || got[1].Key != "b" {
		t.Fatalf("sequence parsed wrong: %#v", got)
	}
}

func TestParseMods(t *testing.T) {
	got, err := ParseMods("ctrl+opt+shift")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"ctrl", "opt", "shift"}) {
		t.Fatalf("ParseMods = %v", got)
	}
	if _, err := ParseMods("ctrl+nope"); err == nil {
		t.Fatal("want error for unknown mod")
	}
}

func TestCanonicalSeq(t *testing.T) {
	cs, err := ParseSpec("kitty_mod+p > shift+f")
	if err != nil {
		t.Fatal(err)
	}
	if got := CanonicalSeq(cs); got != "kitty_mod+p>shift+f" {
		t.Fatalf("CanonicalSeq = %q", got)
	}
	// canonicalization is spelling-insensitive
	a, _ := ParseSpec("super+alt+x")
	b, _ := ParseSpec("opt+cmd+x")
	if CanonicalSeq(a) != CanonicalSeq(b) {
		t.Fatalf("canonical mismatch: %q vs %q", CanonicalSeq(a), CanonicalSeq(b))
	}
}

func TestExpand(t *testing.T) {
	cs, _ := ParseSpec("kitty_mod+t")
	got := Expand(cs, map[string][]string{"kitty_mod": {"ctrl", "opt", "shift"}})
	want := []Chord{{Mods: []string{"ctrl", "shift", "opt"}, Key: "t"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Expand = %#v, want %#v", got, want)
	}
}

func TestFormat(t *testing.T) {
	cs, _ := ParseSpec("cmd+shift+k")
	// canonical order puts shift before cmd
	if got := Format(cs[0]); got != "\u21e7 + \u2318 + k" {
		t.Fatalf("Format = %q", got)
	}
	seq, _ := ParseSpec("kitty_mod+p > f")
	if got := FormatSeq(seq); got != "kitty_mod + p > f" {
		t.Fatalf("FormatSeq = %q", got)
	}
}
