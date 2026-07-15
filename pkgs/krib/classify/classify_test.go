package classify

import (
	"strings"
	"testing"

	"github.com/shimmerjs/krib/chord"
	"github.com/shimmerjs/krib/envelope"
)

func bind(spec, cmd string) envelope.Entry {
	keys, err := chord.ParseSpec(spec)
	if err != nil {
		panic(err)
	}
	return envelope.Entry{Mode: "default", Keys: keys, Cmd: cmd}
}

func env(entries ...envelope.Entry) *envelope.Envelope {
	return &envelope.Envelope{
		SchemaVersion: envelope.SchemaVersion,
		Kind:          envelope.KindBindings,
		Entries:       entries,
	}
}

func names(gs []Grouped) map[string][]string {
	out := make(map[string][]string)
	for _, g := range gs {
		for _, e := range g.Entries {
			out[g.Name] = append(out[g.Name], e.Cmd)
		}
	}
	return out
}

func TestClassifyMembership(t *testing.T) {
	sheet := Sheet{
		Name: "t",
		Groups: []GroupSpec{
			{Name: "windows", Match: &Match{Re: "window|layout_action", Exact: []string{"launch --location=hsplit"}}},
			{Name: "layout", Match: &Match{Re: "layout"}},
			{Name: "other"},
		},
	}
	got, err := Classify(sheet, env(
		bind("cmd+w", "close_window"),
		bind("ctrl+shift+r", "layout_action rotate"), // windows AND layout
		bind("kitty_mod+enter", "launch --location=hsplit"),
		bind("cmd+p", "command_palette"), // catch-all
	))
	if err != nil {
		t.Fatal(err)
	}
	m := names(got)
	want := map[string][]string{
		"windows": {"close_window", "layout_action rotate", "launch --location=hsplit"},
		"layout":  {"layout_action rotate"},
		"other":   {"command_palette"},
	}
	for g, cmds := range want {
		if strings.Join(m[g], ",") != strings.Join(cmds, ",") {
			t.Errorf("group %s = %v, want %v", g, m[g], cmds)
		}
	}
}

func TestClassifyErrors(t *testing.T) {
	one := env(bind("cmd+a", "x"))

	// two catch-alls
	_, err := Classify(Sheet{Groups: []GroupSpec{{Name: "a"}, {Name: "b"}}}, one)
	if err == nil || !strings.Contains(err.Error(), "catch-all") {
		t.Errorf("want catch-all error, got %v", err)
	}
	// unmatched entry without a catch-all
	_, err = Classify(Sheet{Groups: []GroupSpec{{Name: "a", Match: &Match{Re: "^never$"}}}}, one)
	if err == nil || !strings.Contains(err.Error(), "no catch-all") {
		t.Errorf("want unmatched error, got %v", err)
	}
	// invalid RE2
	_, err = Classify(Sheet{Groups: []GroupSpec{{Name: "a", Match: &Match{Re: "("}}}}, one)
	if err == nil {
		t.Error("want regexp error")
	}
	// unknown sorter
	_, err = Classify(Sheet{Groups: []GroupSpec{{Name: "a", Sort: []string{"nope"}}}}, one)
	if err == nil || !strings.Contains(err.Error(), "unknown sorter") {
		t.Errorf("want sorter error, got %v", err)
	}
	// duplicate group names
	_, err = Classify(Sheet{Groups: []GroupSpec{
		{Name: "a", Match: &Match{Re: "x"}},
		{Name: "a", Match: &Match{Re: "y"}},
	}}, one)
	if err == nil || !strings.Contains(err.Error(), "duplicate group") {
		t.Errorf("want duplicate group error, got %v", err)
	}
}

func TestSorters(t *testing.T) {
	// int-keys-last: numbered bindings sort ascending after unnumbered
	ee := []envelope.Entry{
		bind("cmd+3", "third_window"),
		bind("cmd+1", "first_window"),
		bind("cmd+w", "close_window"),
		bind("cmd+2", "second_window"),
	}
	sortIntKeysLast(ee)
	got := []string{ee[0].Cmd, ee[1].Cmd, ee[2].Cmd, ee[3].Cmd}
	want := []string{"close_window", "first_window", "second_window", "third_window"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("int-keys-last order = %v", got)
		}
	}

	// group-next-prev: next_/previous_ pairs group by base name, ahead of the rest
	ee = []envelope.Entry{
		bind("cmd+t", "new_tab"),
		bind("cmd+]", "next_window"),
		bind("cmd+shift+]", "next_tab"),
		bind("cmd+[", "previous_window"),
	}
	sortGroupNextPrev(ee)
	if ee[0].Cmd != "next_tab" || ee[1].Cmd != "next_window" || ee[2].Cmd != "previous_window" || ee[3].Cmd != "new_tab" {
		t.Fatalf("group-next-prev order = %v", []string{ee[0].Cmd, ee[1].Cmd, ee[2].Cmd, ee[3].Cmd})
	}

	// leading-key: stable sort on the first displayed token
	ee = []envelope.Entry{
		bind("kitty_mod+c", "c1"),
		bind("cmd+a", "a1"),
		bind("f1", "f1cmd"),
	}
	sortByLeadingKey(ee)
	if ee[0].Cmd != "a1" || ee[1].Cmd != "f1cmd" || ee[2].Cmd != "c1" {
		t.Fatalf("leading-key order = %v", []string{ee[0].Cmd, ee[1].Cmd, ee[2].Cmd})
	}

	// longest-last on cmd length
	ee = []envelope.Entry{bind("cmd+a", "longest_command_here"), bind("cmd+b", "short")}
	sortLongestLast(ee)
	if ee[0].Cmd != "short" {
		t.Fatalf("longest-last order = %v", []string{ee[0].Cmd, ee[1].Cmd})
	}
}

func TestByGroup(t *testing.T) {
	e := &envelope.Envelope{
		SchemaVersion: envelope.SchemaVersion,
		Kind:          envelope.KindCards,
		Groups: []envelope.Group{
			{Name: "aw-review", Meta: envelope.GroupMeta{Description: "review"}},
			{Name: "aw-audit"},
		},
	}
	e.Entries = []envelope.Entry{
		{Group: "aw-audit", Term: "lenses", Body: "bug,test-gap"},
		{Group: "aw-review", Term: "votes", Body: "3"},
		{Group: "aw-implant", Term: "stray", Body: "undeclared group"},
	}
	gs := ByGroup(e)
	if len(gs) != 3 {
		t.Fatalf("groups = %d", len(gs))
	}
	if gs[0].Name != "aw-review" || gs[0].Meta.Description != "review" || len(gs[0].Entries) != 1 {
		t.Fatalf("declared order/meta lost: %+v", gs[0])
	}
	if gs[2].Name != "aw-implant" {
		t.Fatalf("undeclared group should trail: %+v", gs[2])
	}
}

func TestRuleFirstMatchWins(t *testing.T) {
	s := Sheet{Entries: []EntryRule{
		{Match: Match{Re: "^close_"}, Confirm: true},
		{Match: Match{Exact: []string{"close_window"}}, Exec: &ExecSpec{Run: "copy"}},
	}}
	r := s.Rule("close_window")
	if r == nil || !r.Confirm || r.Exec != nil {
		t.Fatalf("rule = %+v, want the first (confirm) rule", r)
	}
	if s.Rule("new_tab") != nil {
		t.Fatal("unmatched cmd returned a rule")
	}
}

func TestVetSheet(t *testing.T) {
	good := Sheet{Name: "ok", Groups: []GroupSpec{{Name: "all"}},
		Exec:    &ExecSpec{Run: "run", Argv: []string{"x", "{cmd}"}},
		Entries: []EntryRule{{Match: Match{Exact: []string{"quit"}}, Confirm: true}}}
	if err := VetSheet(good); err != nil {
		t.Fatal(err)
	}
	bad := []Sheet{
		{Name: "sort", Sort: []string{"nope"}, Groups: []GroupSpec{{Name: "all"}}},
		{Name: "rule-re", Groups: []GroupSpec{{Name: "all"}}, Entries: []EntryRule{{Match: Match{Re: "("}}}},
		{Name: "rule-empty", Groups: []GroupSpec{{Name: "all"}}, Entries: []EntryRule{{}}},
		{Name: "exec-vocab", Groups: []GroupSpec{{Name: "all"}}, Exec: &ExecSpec{Run: "shell"}},
		{Name: "exec-empty", Groups: []GroupSpec{{Name: "all"}}, Exec: &ExecSpec{Run: "run"}},
	}
	for _, s := range bad {
		if err := VetSheet(s); err == nil {
			t.Errorf("%s: want error", s.Name)
		}
	}
}

// Group names feed the palette's groups column, the fzf group-filter binds,
// and the case-insensitive --filter; keys become alt-<key> binds. Hazardous
// names and keys are load errors naming the sheet and group.
func TestVetSheetGroupHardening(t *testing.T) {
	sheet := func(gs ...GroupSpec) Sheet { return Sheet{Name: "h", Groups: gs} }
	re := &Match{Re: "x"}

	good := sheet(
		GroupSpec{Name: "aw-review", Key: "r", Match: re},
		GroupSpec{Name: "misc_2.0", Key: "M"},
	)
	if err := VetSheet(good); err != nil {
		t.Fatal(err)
	}

	bad := map[string]Sheet{
		"empty name":      sheet(GroupSpec{Name: ""}),
		"space in name":   sheet(GroupSpec{Name: "two words"}),
		"tab in name":     sheet(GroupSpec{Name: "a\tb"}),
		"comma in name":   sheet(GroupSpec{Name: "a,b"}),
		"paren in name":   sheet(GroupSpec{Name: "win)"}),
		"quote in name":   sheet(GroupSpec{Name: "don't"}),
		"fzf operator":    sheet(GroupSpec{Name: "a|b"}),
		"case-folded dup": sheet(GroupSpec{Name: "Windows", Match: re}, GroupSpec{Name: "windows"}),
		"multi-char key":  sheet(GroupSpec{Name: "a", Key: "ab"}),
		"non-alnum key":   sheet(GroupSpec{Name: "a", Key: ","}),
		"duplicate key":   sheet(GroupSpec{Name: "a", Key: "w", Match: re}, GroupSpec{Name: "b", Key: "w"}),
	}
	for name, s := range bad {
		if err := VetSheet(s); err == nil {
			t.Errorf("%s: want error", name)
		}
	}

	// loud: the error names the sheet and the offending group
	err := VetSheet(sheet(GroupSpec{Name: "a,b"}))
	if err == nil || !strings.Contains(err.Error(), "sheet h") || !strings.Contains(err.Error(), `"a,b"`) {
		t.Fatalf("error does not name sheet and group: %v", err)
	}
}
