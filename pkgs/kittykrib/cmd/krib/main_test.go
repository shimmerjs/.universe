package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/shimmerjs/kittykrib/classify"
	"github.com/shimmerjs/kittykrib/envelope"
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
	var buf bytes.Buffer
	if err := writeList(&buf, env, false); err != nil {
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
		if got := strings.Count(line, "\t"); got != 2 {
			t.Errorf("line %q has %d tabs, want 2", line, got)
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
		groups, err := groupsFor(env)
		if err != nil {
			t.Fatal(err)
		}
		out := render(env, groups, 80)
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
	out := render(env, groupsFor2(t, env), 80)
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
	out = render(env, groups, 80)
	if !strings.Contains(out, "kitty_mod = ") {
		t.Error("kitty_mod line missing")
	}
}

func groupsFor2(t *testing.T, env *envelope.Envelope) []classify.Grouped {
	t.Helper()
	groups, err := groupsFor(env)
	if err != nil {
		t.Fatal(err)
	}
	return groups
}

// The column contract holds for EVERY column, defensively: even a field
// that slipped past vet (or a multiline Cmd, which vet does not constrain)
// renders as one line with exactly two tabs (wave-5 review).
func TestListColumnsSanitized(t *testing.T) {
	env := &envelope.Envelope{SchemaVersion: 1, Kind: envelope.KindCards,
		Entries: []envelope.Entry{{Group: "g", Term: "t", Cmd: "run\tthing\nsecond"}}}
	var buf bytes.Buffer
	if err := writeList(&buf, env, false); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("output = %q, want one line", out)
	}
	if strings.Count(strings.TrimSuffix(out, "\n"), "\t") != 2 {
		t.Fatalf("output = %q, want exactly two tabs", out)
	}
	if strings.Contains(out, "second") {
		t.Fatalf("output = %q, want the multiline Cmd collapsed", out)
	}
}

// A whitespace-only Cmd is as empty as empty: the Body fallback fires.
func TestListWhitespaceCmdFallsBack(t *testing.T) {
	env := &envelope.Envelope{SchemaVersion: 1, Kind: envelope.KindCards,
		Entries: []envelope.Entry{{Group: "g", Term: "t", Cmd: "   ", Body: "real detail"}}}
	var buf bytes.Buffer
	if err := writeList(&buf, env, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "real detail") {
		t.Fatalf("output = %q, want the Body fallback", buf.String())
	}
}
