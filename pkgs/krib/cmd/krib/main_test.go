package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/krib/classify"
	"github.com/shimmerjs/krib/envelope"
	"github.com/shimmerjs/krib/sheets"
	"github.com/shimmerjs/krib/state"
)

const cardsInput = `{
  "schemaVersion": 1,
  "kind": "cards",
  "groups": [{"name": "aw-review", "meta": {"description": "adversarial review", "whenToUse": "diffs", "phases": ["map", "verify"]}}],
  "entries": [
    {"group": "aw-review", "term": "votes", "body": "int default=[3]"},
    {"group": "aw-review", "term": "e.g. aw-review votes=5"}
  ]
}`

const multilineCardsInput = `{
  "schemaVersion": 1,
  "kind": "cards",
  "entries": [
    {"group": "aw-review", "term": "votes", "body": "line1\nline2\nline3"},
    {"group": "aw-review", "term": "run", "body": "ignored\nline2", "cmd": "aw-review votes=5"}
  ]
}`

const kittenInput = `{"kitty_mod": "ctrl+opt+shift"}
{"mode": "default", "keys": "cmd+t", "action": "new_tab_with_cwd"}
{"mode": "default", "keys": "cmd+w", "action": "close_window"}
`

func kittySheet(t *testing.T) classify.Sheet {
	t.Helper()
	sheet, err := sheets.Load("")
	if err != nil {
		t.Fatal(err)
	}
	return sheet
}

func listFor(t *testing.T, env *envelope.Envelope, st *state.File, opts listOpts) []listEntry {
	t.Helper()
	if st == nil {
		st = state.New()
	}
	entries, err := buildList(env, kittySheet(t), st, opts)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

func TestDecodeSniffing(t *testing.T) {
	env, err := decode("auto", strings.NewReader(cardsInput))
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != envelope.KindCards {
		t.Fatalf("kind = %q", env.Kind)
	}

	env, err = decode("auto", strings.NewReader(kittenInput))
	if err != nil {
		t.Fatal(err)
	}
	if env.Kind != envelope.KindBindings || len(env.Entries) != 2 {
		t.Fatalf("kitten sniff: %+v", env)
	}

	if _, err := decode("envelope", strings.NewReader(kittenInput)); err == nil {
		t.Fatal("forcing envelope on JSONL should fail")
	}
	if _, err := decode("nope", strings.NewReader(cardsInput)); err == nil {
		t.Fatal("unknown --from should fail")
	}
}

func TestListOneLinePerEntry(t *testing.T) {
	env, err := decode("auto", strings.NewReader(multilineCardsInput))
	if err != nil {
		t.Fatal(err)
	}
	entries := listFor(t, env, nil, listOpts{})
	var buf bytes.Buffer
	if err := writeList(&buf, entries, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("output does not end in newline: %q", out)
	}
	if got, want := strings.Count(out, "\n"), len(env.Entries); got != want {
		t.Fatalf("output has %d lines, want %d:\n%q", got, want, out)
	}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if got := strings.Count(line, "\t"); got != 3 {
			t.Errorf("line %q has %d tabs, want 3", line, got)
		}
		if strings.Contains(line, "line2") {
			t.Errorf("line %q carries a collapsed remainder", line)
		}
	}
}

func TestRenderPagerClean(t *testing.T) {
	for _, in := range []string{cardsInput, kittenInput} {
		env, err := decode("auto", strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}
		groups, err := groupsFor(env, kittySheet(t))
		if err != nil {
			t.Fatal(err)
		}
		out := render(env, groups, 80, nil)
		// static ANSI only: SGR styling is fine, screen/cursor control is not
		for _, banned := range []string{"\x1b[?1049", "\x1b[2J", "\x1b[H", "\x1b[?25"} {
			if strings.Contains(out, banned) {
				t.Fatalf("output contains screen-control sequence %q", banned)
			}
		}
		if !strings.HasSuffix(out, "\n") {
			t.Fatal("output does not end in newline")
		}
	}
}

func TestRenderContent(t *testing.T) {
	env, _ := decode("auto", strings.NewReader(cardsInput))
	out := render(env, groupsFor2(t, env), 80, nil)
	for _, want := range []string{"aw-review", "adversarial review", "when: diffs", "map -> verify", "votes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}

	env, _ = decode("auto", strings.NewReader(kittenInput))
	groups := groupsFor2(t, env)
	if got := filterGroups(groups, "TAB"); len(got) != 1 || got[0].Name != "tabs" {
		t.Fatalf("filter = %+v", got)
	}
	out = render(env, groups, 80, nil)
	if !strings.Contains(out, "kitty_mod = ") {
		t.Error("kitty_mod line missing")
	}
}

func groupsFor2(t *testing.T, env *envelope.Envelope) []classify.Grouped {
	t.Helper()
	groups, err := groupsFor(env, kittySheet(t))
	if err != nil {
		t.Fatal(err)
	}
	return groups
}

// The column contract holds for EVERY column, defensively: even a field
// that slipped past vet (or a multiline Cmd, which vet does not constrain)
// renders as one line with exactly three tabs (wave-5 review).
func TestListColumnsSanitized(t *testing.T) {
	env := &envelope.Envelope{SchemaVersion: 1, Kind: envelope.KindCards,
		Entries: []envelope.Entry{{Group: "g", Term: "t", Cmd: "run\tthing\nsecond"}}}
	entries := listFor(t, env, nil, listOpts{})
	var buf bytes.Buffer
	if err := writeList(&buf, entries, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("output = %q, want one line", out)
	}
	if strings.Count(strings.TrimSuffix(out, "\n"), "\t") != 3 {
		t.Fatalf("output = %q, want exactly three tabs", out)
	}
	if strings.Contains(out, "second") {
		t.Fatalf("output = %q, want the multiline Cmd collapsed", out)
	}
}

// A whitespace-only Cmd is as empty as empty: the Body fallback fires.
func TestListWhitespaceCmdFallsBack(t *testing.T) {
	env := &envelope.Envelope{SchemaVersion: 1, Kind: envelope.KindCards,
		Entries: []envelope.Entry{{Group: "g", Term: "t", Cmd: "   ", Body: "real detail"}}}
	entries := listFor(t, env, nil, listOpts{})
	var buf bytes.Buffer
	if err := writeList(&buf, entries, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "real detail") {
		t.Fatalf("output = %q, want the Body fallback", buf.String())
	}
}

// The show-all toggle contract: entries only the catch-all matched are
// hidden by default and included with all; multi-membership and curated
// entries always show.
func TestListCatchAllToggle(t *testing.T) {
	input := kittenInput + `{"mode": "default", "keys": "kitty_mod+f3", "action": "command_palette"}
`
	env, err := decode("auto", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	curated := listFor(t, env, nil, listOpts{})
	all := listFor(t, env, nil, listOpts{all: true})
	if len(all) != len(env.Entries) {
		t.Fatalf("all = %d entries, want %d", len(all), len(env.Entries))
	}
	if len(curated) != len(all)-1 {
		t.Fatalf("curated = %d entries, want %d", len(curated), len(all)-1)
	}
	for _, le := range curated {
		if le.cmd == "command_palette" {
			t.Fatal("catch-all-only entry leaked into the curated list")
		}
	}
}

// Recently-changed markers key off an OBSERVED change: a fresh statefile
// (bootstrap) marks nothing, a moved since does, and the recent filter
// orders most-recent-first.
func TestListRecentMarkers(t *testing.T) {
	env, err := decode("auto", strings.NewReader(kittenInput))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	st := state.New()
	st.Observe(env, now.Add(-30*24*time.Hour))

	// bootstrap only: nothing is marked
	for _, le := range listFor(t, env, st, listOpts{recentWindow: 14 * 24 * time.Hour, now: now}) {
		if le.changed {
			t.Fatalf("bootstrap-only entry %q marked changed", le.id)
		}
	}

	// one binding changes value
	env.Entries[1].Cmd = "close_tab"
	st.Observe(env, now.Add(-2*24*time.Hour))
	entries := listFor(t, env, st, listOpts{recentWindow: 14 * 24 * time.Hour, now: now})
	var marked []string
	for _, le := range entries {
		if le.changed {
			marked = append(marked, le.id)
		}
	}
	if len(marked) != 1 || marked[0] != "default/cmd+w" {
		t.Fatalf("marked = %v, want [default/cmd+w]", marked)
	}

	var buf bytes.Buffer
	if err := writeList(&buf, entries, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "* ") {
		t.Fatal("changed marker missing from list output")
	}

	// recent filters to the changed entry only
	recent := listFor(t, env, st, listOpts{recent: true, recentWindow: 14 * 24 * time.Hour, now: now})
	if len(recent) != 1 || recent[0].id != "default/cmd+w" {
		t.Fatalf("recent = %+v, want just default/cmd+w", recent)
	}

	// outside the window: no marker
	old := listFor(t, env, st, listOpts{recentWindow: 24 * time.Hour, now: now})
	for _, le := range old {
		if le.changed {
			t.Fatalf("entry %q marked outside the window", le.id)
		}
	}
}

// observeState persists observations through the locked cycle and sweeps
// state for ids gone from the envelope.
func TestObserveStatePersistsAndSweeps(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	env, err := decode("auto", strings.NewReader(kittenInput))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	observeState(env, now)
	path := filepath.Join(dir, "krib", "kitty.json")
	if got := len(state.Load(path).Entries); got != 2 {
		t.Fatalf("persisted %d entries, want 2", got)
	}

	// one binding disappears: its state entry sweeps on the next observe
	env.Entries = env.Entries[:1]
	st := observeState(env, now.Add(time.Hour))
	if _, ok := st.Entries["default/cmd+w"]; ok {
		t.Fatal("absent id retained in the returned state")
	}
	if got := len(state.Load(path).Entries); got != 1 {
		t.Fatalf("swept statefile holds %d entries, want 1", got)
	}
}

// A mode-qualified binding and a multi-chord sequence survive the loaded
// (CUE->JSON->decode) sheet end to end: classification, list display, and
// render.
func TestModeSequenceRoundTrip(t *testing.T) {
	f, err := os.Open("../../adapter/kittyjsonl/testdata/kitty-bindings.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	env, err := decode("kitty-jsonl", f)
	if err != nil {
		t.Fatal(err)
	}

	entries := listFor(t, env, nil, listOpts{all: true})
	byID := make(map[string]listEntry, len(entries))
	for _, le := range entries {
		byID[le.id] = le
	}
	mw, ok := byID["mw/left"]
	if !ok {
		t.Fatal("mode entry mw/left missing from the list")
	}
	if !strings.HasPrefix(mw.display, "[mw] ") {
		t.Fatalf("mode display = %q, want the [mw] prefix", mw.display)
	}
	if strings.Join(mw.groups, ",") != "windows" {
		t.Fatalf("mw/left groups = %v, want [windows]", mw.groups)
	}
	seq, ok := byID["default/kitty_mod+p>f"]
	if !ok {
		t.Fatal("sequence entry kitty_mod+p>f missing from the list")
	}
	if !strings.Contains(seq.display, " > ") {
		t.Fatalf("sequence display = %q, want a chord separator", seq.display)
	}

	groups, err := groupsFor(env, kittySheet(t))
	if err != nil {
		t.Fatal(err)
	}
	out := render(env, groups, 80, kittySheet(t).Theme)
	if !strings.Contains(out, "move_window left") {
		t.Fatal("mode-qualified binding missing from render")
	}
}

// Golden pin: the kitty sheet's theme block supplying today's values renders
// byte-identical to the hardcoded defaults.
func TestRenderThemeParity(t *testing.T) {
	sheet := kittySheet(t)
	if sheet.Theme == nil {
		t.Fatal("kitty sheet has no theme block")
	}
	th := *sheet.Theme
	if th.Keys == "" || th.Cmd == "" || th.Header == "" || th.RowSep == "" || th.Dim == "" || th.LeftWidth == 0 {
		t.Fatalf("kitty sheet theme is not fully specified: %+v", th)
	}

	f, err := os.Open("../../adapter/kittyjsonl/testdata/kitty-bindings.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	env, err := decode("kitty-jsonl", f)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := groupsFor(env, sheet)
	if err != nil {
		t.Fatal(err)
	}
	plain := render(env, groups, 80, nil)
	themed := render(env, groups, 80, sheet.Theme)
	if plain != themed {
		t.Fatal("kitty sheet theme does not render byte-identical to the defaults")
	}
}

// Pinned groups lead the print order; unpinned order is preserved.
func TestPinFirst(t *testing.T) {
	groups := []classify.Grouped{
		{Name: "a"}, {Name: "b", Pin: true}, {Name: "c"}, {Name: "d", Pin: true},
	}
	got := pinFirst(groups)
	want := []string{"b", "d", "a", "c"}
	for i, g := range got {
		if g.Name != want[i] {
			t.Fatalf("pinFirst order = %v, want %v", got, want)
		}
	}
}
