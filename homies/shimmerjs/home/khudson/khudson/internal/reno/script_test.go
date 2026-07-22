package reno

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goodScript = `demo widget-tour
budget 20m

step intro
note intro.md
budget 90s
launch-session work --cwd /tmp --keep-focus -- claude

step code
note code.md
budget 3m
focus-session work

step slides
budget 2m
open-url https://slides.example.com/deck
focus-app com.google.Chrome

-- intro.md --
# welcome
say the thing.

-- code.md --
walk the diff bottom-up.
`

func TestParseGoodScript(t *testing.T) {
	s, err := Parse("", []byte(goodScript))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Name != "widget-tour" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Budget != 20*time.Minute {
		t.Errorf("budget = %v", s.Budget)
	}
	if len(s.Steps) != 3 {
		t.Fatalf("steps = %d", len(s.Steps))
	}
	if s.Steps[0].Budget != 90*time.Second {
		t.Errorf("step budget = %v", s.Steps[0].Budget)
	}
	if !strings.Contains(s.Steps[0].Note, "say the thing") {
		t.Errorf("intro note not resolved: %q", s.Steps[0].Note)
	}
	if s.Steps[2].Note != "" || s.Steps[2].NoteRef != "" {
		t.Errorf("slides step should have no note")
	}
	got := s.Steps[2].Commands
	if len(got) != 2 || got[0].Name != "open-url" || got[1].Name != "focus-app" {
		t.Errorf("slides commands = %+v", got)
	}
	if s.Hash == "" {
		t.Error("hash empty")
	}
	if s.StepIndex("code") != 1 || s.StepIndex("nope") != -1 {
		t.Error("StepIndex misresolves")
	}
}

func TestParseCollectsAllErrors(t *testing.T) {
	src := `demo Bad_Name
step intro
zap-session work
step intro
budget -3s
`
	_, err := Parse("", []byte(src))
	if err == nil {
		t.Fatal("want errors")
	}
	msg := err.Error()
	for _, want := range []string{
		"line 1", "must match",
		"line 3", "unknown command \"zap-session\"",
		"line 4", "already used",
		"line 5", "not a positive duration",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q in:\n%s", want, msg)
		}
	}
}

func TestParseVetRules(t *testing.T) {
	cases := []struct {
		name, src, want string
	}{
		{"missing demo", "step a\nfocus-session w\n", "missing demo <name> header"},
		{"no steps", "demo d\n", "no steps"},
		{"adhoc reserved", "demo adhoc\nstep a\n", "reserved for scriptless"},
		{"command in header", "demo d\nfocus-session w\nstep a\n", "before the first step"},
		{"reserved wait", "demo d\nstep a\nwait ok\n", "reserved wait-guard"},
		{"record header args", "demo d\nrecord display 1\nstep a\n", "takes no arguments"},
		{"record op bad", "demo d\nrecord\nstep a\nrecord go\n", "pause or resume"},
		{"scene without record", "demo d\nscene main\nstep a\n", "without a record header"},
		{"scene cmd without record", "demo d\nstep a\nscene main\n", "never records"},
		{"dup record", "demo d\nrecord\nrecord\nstep a\n", "duplicate record header"},
		{"bad arity", "demo d\nstep a\nfocus-session\n", "wrong arity"},
		{"bad url", "demo d\nstep a\nopen-url ftp://x\n", "http(s) URL"},
		{"bad handle", "demo d\nstep a\nfocus-session Work!\n", "must match"},
		{"launch flag", "demo d\nstep a\nlaunch-session w --wat\n", "unknown launch-session flag"},
		{"launch cwd value", "demo d\nstep a\nlaunch-session w --cwd\n", "--cwd needs a directory"},
		{"launch dashdash empty", "demo d\nstep a\nlaunch-session w --\n", "-- needs a command"},
		{"dup note", "demo d\nstep a\nnote x.md\nnote y.md\n-- x.md --\nhi\n-- y.md --\nyo\n", "already has a note"},
		{"dup demo budget", "demo d\nbudget 1m\nbudget 2m\nstep a\n", "duplicate demo budget"},
		{"dup step budget", "demo d\nstep a\nbudget 1m\nbudget 2m\n", "already has a budget"},
		{"note outside step", "demo d\nnote x.md\nstep a\n", "note outside a step"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("", []byte(tc.src))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q in:\n%s", tc.want, err)
			}
		})
	}
}

func TestNoteContainment(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared.md")
	if err := os.WriteFile(shared, []byte("shared body"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "demo.txtar")

	src := "demo d\nstep a\nnote shared.md\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("Load with external note: %v", err)
	}
	if s.Steps[0].Note != "shared body" {
		t.Errorf("note = %q", s.Steps[0].Note)
	}

	for _, tc := range []struct{ name, ref, want string }{
		{"absolute", shared, "absolute note paths"},
		{"traversal", "../shared.md", "escapes the script directory"},
		{"missing", "nope.md", "no such file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(path, []byte("demo d\nstep a\nnote "+tc.ref+"\n"))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got %v", tc.want, err)
			}
		})
	}

	t.Run("no script path", func(t *testing.T) {
		_, err := Parse("", []byte("demo d\nstep a\nnote ext.md\n"))
		if err == nil || !strings.Contains(err.Error(), "no script path") {
			t.Errorf("got %v", err)
		}
	})
}

func TestExampleScriptVets(t *testing.T) {
	s, err := Load(filepath.Join("testdata", "example.txtar"))
	if err != nil {
		t.Fatalf("testdata example must vet clean: %v", err)
	}
	if len(s.Steps) == 0 {
		t.Fatal("example has no steps")
	}
}

func TestRecordingScript(t *testing.T) {
	src := `demo rec-tour
record
scene main

step intro
scene closeup
record pause

step wrap
record resume
`
	s, err := Parse("", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if !s.Record || s.Scene != "main" {
		t.Errorf("header: record=%v scene=%q", s.Record, s.Scene)
	}
	got := s.Steps[0].Commands
	if len(got) != 2 || got[0].Name != "scene" || got[1].Name != "record" || got[1].Args[0] != "pause" {
		t.Errorf("intro commands = %+v", got)
	}
}
