package kittyjsonl

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/shimmerjs/kittykrib/chord"
	"github.com/shimmerjs/kittykrib/envelope"
)

func decodeFixture(t *testing.T) *envelope.Envelope {
	t.Helper()
	f, err := os.Open("testdata/kitty-bindings.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	env, err := Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestDecodeFixture(t *testing.T) {
	env := decodeFixture(t)

	if env.SchemaVersion != envelope.SchemaVersion || env.Kind != envelope.KindBindings {
		t.Fatalf("envelope header wrong: %+v", env)
	}
	// pseudo-record becomes meta, not an entry
	if got := env.Meta.ModAliases["kitty_mod"]; !reflect.DeepEqual(got, []string{"ctrl", "opt", "shift"}) {
		t.Fatalf("kitty_mod = %v", got)
	}
	// fixture has 56 lines: 1 pseudo-record + 55 bindings
	if len(env.Entries) != 55 {
		t.Fatalf("entries = %d", len(env.Entries))
	}

	byID := make(map[string]envelope.Entry, len(env.Entries))
	for _, e := range env.Entries {
		byID[e.ID(env.Kind)] = e
	}

	// sequence keys parse into two chords (the old splitKeys mangled these)
	seq, ok := byID["default/kitty_mod+p>shift+f"]
	if !ok {
		t.Fatalf("sequence entry missing; ids: %v", keysOf(byID))
	}
	want := []chord.Chord{
		{Mods: []string{"kitty_mod"}, Key: "p"},
		{Mods: []string{"shift"}, Key: "f"},
	}
	if !reflect.DeepEqual(seq.Keys, want) {
		t.Fatalf("sequence keys = %#v", seq.Keys)
	}

	// literal plus
	plus, ok := byID["default/cmd++"]
	if !ok || plus.Cmd != "change_font_size all + 2.0" {
		t.Fatalf("cmd++ entry: %+v", plus)
	}

	// non-default mode flows into the id
	if _, ok := byID["mw/left"]; !ok {
		t.Fatal("mw mode entry missing")
	}
}

func keysOf(m map[string]envelope.Entry) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestDecodeErrors(t *testing.T) {
	cases := []string{
		"",
		"{\"kitty_mod\": \"ctrl+shift\"}\n", // no bindings
		"not json\n",                        // parse error
		"{\"mode\": \"default\", \"keys\": \"\"}\n",                                                      // empty key spec
		"{\"kitty_mod\": \"ctrl+bogus\"}\n{\"mode\": \"default\", \"keys\": \"a\", \"action\": \"x\"}\n", // bad kitty_mod
	}
	for i, in := range cases {
		if _, err := Decode(strings.NewReader(in)); err == nil {
			t.Errorf("case %d: want error", i)
		}
	}
}
