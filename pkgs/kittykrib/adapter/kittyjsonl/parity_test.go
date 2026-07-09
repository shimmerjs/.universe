package kittyjsonl

import (
	"regexp"
	"slices"
	"sort"
	"testing"

	"github.com/shimmerjs/kittykrib/classify"
)

// oldCategories is the hardcoded table from the old cheatsheet main.go,
// verbatim. oldClassify reimplements its matching: regex OR exact list over
// the raw action string, membership in every matching category, unmatched
// falls through to other. classify.KittySheet must reproduce it exactly.
var oldCategories = []struct {
	name    string
	re      string
	actions []string
}{
	{"windows", "window|layout_action", []string{
		"launch --location=hsplit --cwd=current",
		"launch --location=vsplit --cwd=current",
		"launch --location=split --cwd=current",
	}},
	{"tabs", "tab", nil},
	{"layout", "layout", nil},
	{"scrolling", "^scroll_|show_scrollback|show_.*_command_output|clear_terminal", nil},
	{"system", "config|macos|show_kitty_doc", []string{"quit"}},
	{"clipboard", "clipboard|copy", []string{
		"copy_or_noop",
		"paste_from_selection",
		"pass_selection_from_program",
	}},
}

func oldClassify(action string) []string {
	var got []string
	for _, c := range oldCategories {
		if regexp.MustCompile(c.re).MatchString(action) || slices.Contains(c.actions, action) {
			got = append(got, c.name)
		}
	}
	if len(got) == 0 {
		got = []string{"other"}
	}
	return got
}

func TestClassifyParityWithOldTable(t *testing.T) {
	env := decodeFixture(t)

	groups, err := classify.Classify(classify.KittySheet(), env)
	if err != nil {
		t.Fatal(err)
	}

	membership := make(map[string][]string) // entry id -> group names
	for _, g := range groups {
		for _, e := range g.Entries {
			id := e.ID(env.Kind)
			membership[id] = append(membership[id], g.Name)
		}
	}

	for _, e := range env.Entries {
		id := e.ID(env.Kind)
		want := oldClassify(e.Cmd)
		got := membership[id]
		sort.Strings(want)
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s (%q): new %v, old %v", id, e.Cmd, got, want)
		}
	}

	// spot checks the table is meant to guarantee
	spot := map[string][]string{
		"default/ctrl+shift+r":    {"layout", "windows"}, // layout_action rotate: multi-membership
		"default/kitty_mod+enter": {"windows"},           // exact-list launch
		"default/cmd+q":           {"system"},            // exact-list quit
		"default/cmd++":           {"other"},             // catch-all
		"default/kitty_mod+f3":    {"other"},             // catch-all
		"mw/left":                 {"windows"},           // mode entries classify like any other
	}
	for id, want := range spot {
		got := append([]string{}, membership[id]...)
		sort.Strings(got)
		if !slices.Equal(got, want) {
			t.Errorf("spot %s: got %v, want %v", id, got, want)
		}
	}
}
