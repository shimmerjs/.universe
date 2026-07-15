package envelope

import (
	"strings"
	"testing"

	"github.com/shimmerjs/krib/chord"
)

func bindingsEnv(entries ...Entry) *Envelope {
	return &Envelope{SchemaVersion: SchemaVersion, Kind: KindBindings, Entries: entries}
}

func key(mods []string, k string) []chord.Chord {
	return []chord.Chord{{Mods: mods, Key: k}}
}

func TestEntryID(t *testing.T) {
	b := Entry{Mode: "default", Keys: key([]string{"kitty_mod"}, "t"), Cmd: "new_tab"}
	if got := b.ID(KindBindings); got != "default/kitty_mod+t" {
		t.Fatalf("bindings id = %q", got)
	}
	// empty mode reads as default; alias spellings canonicalize identically
	b2 := Entry{Keys: key([]string{"super"}, "T")}
	b3 := Entry{Keys: key([]string{"cmd"}, "t")}
	if b2.ID(KindBindings) != b3.ID(KindBindings) {
		t.Fatalf("alias ids differ: %q vs %q", b2.ID(KindBindings), b3.ID(KindBindings))
	}
	c := Entry{Group: "aw-review", Term: "--votes"}
	if got := c.ID(KindCards); got != "aw-review/--votes" {
		t.Fatalf("cards id = %q", got)
	}
}

func TestVetDuplicateIDs(t *testing.T) {
	env := bindingsEnv(
		Entry{Mode: "default", Keys: key([]string{"cmd"}, "t"), Cmd: "a"},
		Entry{Mode: "default", Keys: key([]string{"super"}, "t"), Cmd: "b"},
	)
	if _, err := env.Vet(); err == nil || !strings.Contains(err.Error(), "duplicate entry id") {
		t.Fatalf("want duplicate id error, got %v", err)
	}
}

func TestIDNoSlashCollision(t *testing.T) {
	// group "g/a" + term "b" and group "g" + term "a/b" both derive id
	// "g/a/b"; Vet must reject the slashed names, not report a duplicate.
	env := &Envelope{
		SchemaVersion: SchemaVersion,
		Kind:          KindCards,
		Entries: []Entry{
			{Group: "g/a", Term: "b"},
			{Group: "g", Term: "a/b"},
		},
	}
	_, err := env.Vet()
	if err == nil {
		t.Fatal("want vet error for slashed names")
	}
	if strings.Contains(err.Error(), "duplicate entry id") {
		t.Fatalf("collision surfaced as duplicate id: %v", err)
	}
	if !strings.Contains(err.Error(), `group "g/a"`) {
		t.Fatalf("error does not name the slashed group: %v", err)
	}

	env = &Envelope{
		SchemaVersion: SchemaVersion,
		Kind:          KindCards,
		Entries:       []Entry{{Group: "g", Term: "a/b"}},
	}
	if _, err := env.Vet(); err == nil || !strings.Contains(err.Error(), `term "a/b"`) {
		t.Fatalf("want error naming the slashed term, got %v", err)
	}

	env = bindingsEnv(Entry{Mode: "a/b", Keys: key(nil, "x")})
	if _, err := env.Vet(); err == nil || !strings.Contains(err.Error(), `mode "a/b"`) {
		t.Fatalf("want error naming the slashed mode, got %v", err)
	}

	env = &Envelope{
		SchemaVersion: SchemaVersion,
		Kind:          KindCards,
		Groups:        []Group{{Name: "g/a"}},
	}
	if _, err := env.Vet(); err == nil || !strings.Contains(err.Error(), `group "g/a"`) {
		t.Fatalf("want error for slashed declared group name, got %v", err)
	}

	// keys are exempt: a binding on the literal "/" key is legitimate.
	env = bindingsEnv(Entry{Keys: key(nil, "/"), Cmd: "search"})
	if _, err := env.Vet(); err != nil {
		t.Fatalf("binding on literal / key should vet clean: %v", err)
	}
}

func TestVetVersionPolicy(t *testing.T) {
	env := bindingsEnv(Entry{Keys: key(nil, "a"), Cmd: "x"})

	if _, err := env.Vet(); err != nil {
		t.Fatalf("current version: %v", err)
	}
	env.SchemaVersion = SchemaVersion + 1
	if _, err := env.Vet(); err == nil {
		t.Fatal("want error for future version")
	}
	env.SchemaVersion = 0
	if _, err := env.Vet(); err == nil {
		t.Fatal("want error for version 0")
	}
	// N-1 is accepted loudly once N >= 2; at N == 1 there is no valid N-1.
	if SchemaVersion >= 2 {
		env.SchemaVersion = SchemaVersion - 1
		warnings, err := env.Vet()
		if err != nil || len(warnings) == 0 {
			t.Fatalf("want loud acceptance of N-1, got warnings=%v err=%v", warnings, err)
		}
	}
}

func TestVetKindShape(t *testing.T) {
	cases := []*Envelope{
		{SchemaVersion: SchemaVersion, Kind: "layout", Entries: []Entry{{Term: "x", Group: "g"}}},
		bindingsEnv(Entry{Cmd: "no keys"}),
		bindingsEnv(Entry{Keys: key(nil, "a"), Term: "cards field"}),
		{SchemaVersion: SchemaVersion, Kind: KindCards, Entries: []Entry{{Group: "g"}}},                                            // no term
		{SchemaVersion: SchemaVersion, Kind: KindCards, Entries: []Entry{{Term: "t"}}},                                             // no group
		{SchemaVersion: SchemaVersion, Kind: KindCards, Entries: []Entry{{Term: "t", Group: "g", Mode: "m"}}},                      // bindings field
		bindingsEnv(Entry{Keys: key([]string{"bogus"}, "a")}),                                                                      // bad mod
		{SchemaVersion: SchemaVersion, Kind: KindCards, Groups: []Group{{Name: "g"}, {Name: "g"}}},                                 // dup group
		{SchemaVersion: SchemaVersion, Kind: KindCards, Groups: []Group{{Name: "g"}}, Entries: []Entry{{Term: "t", Group: "zzz"}}}, // undeclared group
	}
	for i, env := range cases {
		if _, err := env.Vet(); err == nil {
			t.Errorf("case %d: want vet error", i)
		}
	}
}

func TestVetCardsImplicitGroups(t *testing.T) {
	// with no declared groups, data-borne groups are allowed
	env := &Envelope{
		SchemaVersion: SchemaVersion,
		Kind:          KindCards,
		Entries:       []Entry{{Group: "g", Term: "t", Body: "b"}},
	}
	if _, err := env.Vet(); err != nil {
		t.Fatal(err)
	}
}

func TestDecode(t *testing.T) {
	in := `{
	  "schemaVersion": 1,
	  "kind": "cards",
	  "meta": {"sheet": "clod"},
	  "groups": [{"name": "aw-review", "meta": {"description": "adversarial review", "phases": ["map", "review"]}}],
	  "entries": [{"group": "aw-review", "term": "votes", "body": "int default 3", "cmd": ""}]
	}`
	env, warnings, err := Decode(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if env.Groups[0].Meta.Description != "adversarial review" {
		t.Fatalf("group meta lost: %+v", env.Groups[0])
	}
	if env.Entries[0].ID(env.Kind) != "aw-review/votes" {
		t.Fatalf("id = %q", env.Entries[0].ID(env.Kind))
	}
}

// Name fields also reject control whitespace: a tab or newline in a
// mode/group/term shifts columns or spans lines in the one-line list
// contract downstream (wave-5 review).
func TestNamesRejectControlWhitespace(t *testing.T) {
	for _, tt := range []struct {
		name string
		env  Envelope
		want string
	}{
		{"tab in term", Envelope{SchemaVersion: 1, Kind: KindCards,
			Entries: []Entry{{Group: "g", Term: "a\tb", Body: "x"}}}, `term "a\tb"`},
		{"newline in group", Envelope{SchemaVersion: 1, Kind: KindCards,
			Entries: []Entry{{Group: "g\nh", Term: "t", Body: "x"}}}, `group "g\nh"`},
		{"cr in mode", Envelope{SchemaVersion: 1, Kind: KindBindings,
			Entries: []Entry{{Mode: "m\rn", Keys: key(nil, "a"), Cmd: "x"}}}, `mode "m\rn"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.env.Vet()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Vet = %v, want error naming %s", err, tt.want)
			}
		})
	}
}

// The additive extension: examples, structured flag columns, and the card
// discriminator decode; existing envelopes (no new fields) are untouched.
func TestDecodeAdditiveExtension(t *testing.T) {
	in := `{
	  "schemaVersion": 1,
	  "kind": "cards",
	  "groups": [{"name": "aw-review", "meta": {"description": "d", "examples": ["aw-review votes=5", "aw-review passes=3"]}}],
	  "entries": [
	    {"group": "aw-review", "term": "votes", "flag": {"short": "v", "type": "int", "default": "3", "range": "1-9", "help": "verifier quorum"}},
	    {"group": "aw-review", "term": "example", "card": "1", "cmd": "aw-review votes=5"},
	    {"group": "aw-review", "term": "example", "card": "2", "cmd": "aw-review passes=3"}
	  ]
	}`
	env, warnings, err := Decode(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if got := env.Groups[0].Meta.Examples; len(got) != 2 || got[0] != "aw-review votes=5" {
		t.Fatalf("examples = %v", got)
	}
	f := env.Entries[0].Flag
	if f == nil || f.Short != "v" || f.Type != "int" || f.Default != "3" || f.Range != "1-9" || f.Help != "verifier quorum" {
		t.Fatalf("flag = %+v", f)
	}
	if got := env.Entries[1].ID(env.Kind); got != "aw-review/example/1" {
		t.Fatalf("card id = %q", got)
	}
}

// Card disambiguation: same group+term with distinct cards vets clean, a
// repeated card collides, a slashed card is rejected by name, and bindings
// entries may not carry the new cards fields.
func TestVetCardDisambiguation(t *testing.T) {
	env := &Envelope{
		SchemaVersion: SchemaVersion,
		Kind:          KindCards,
		Entries: []Entry{
			{Group: "g", Term: "example", Card: "1"},
			{Group: "g", Term: "example", Card: "2"},
		},
	}
	if _, err := env.Vet(); err != nil {
		t.Fatalf("distinct cards should vet clean: %v", err)
	}

	env.Entries[1].Card = "1"
	if _, err := env.Vet(); err == nil || !strings.Contains(err.Error(), "duplicate entry id") {
		t.Fatalf("want duplicate id error, got %v", err)
	}

	env.Entries[1].Card = "a/b"
	if _, err := env.Vet(); err == nil || !strings.Contains(err.Error(), `card "a/b"`) {
		t.Fatalf("want error naming the slashed card, got %v", err)
	}

	b := bindingsEnv(Entry{Keys: key(nil, "a"), Cmd: "x", Card: "1"})
	if _, err := b.Vet(); err == nil {
		t.Fatal("bindings entry with a card should fail vet")
	}
	b = bindingsEnv(Entry{Keys: key(nil, "a"), Cmd: "x", Flag: &FlagCols{Short: "s"}})
	if _, err := b.Vet(); err == nil {
		t.Fatal("bindings entry with flag columns should fail vet")
	}
}
