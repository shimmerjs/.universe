package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shimmerjs/krib/classify"
	"github.com/shimmerjs/krib/envelope"
	"github.com/shimmerjs/krib/state"
)

// stubExec intercepts the spawn/prompt seams for one test and returns the
// recorded calls.
type stubExec struct {
	argv      [][]string
	confirms  int
	confirmOK bool
}

func stubSeams(t *testing.T) *stubExec {
	t.Helper()
	s := &stubExec{}
	oldRun, oldConfirm := runArgv, confirmEntry
	runArgv = func(argv []string) error {
		s.argv = append(s.argv, argv)
		return nil
	}
	confirmEntry = func(label string) (bool, error) {
		s.confirms++
		return s.confirmOK, nil
	}
	t.Cleanup(func() { runArgv, confirmEntry = oldRun, oldConfirm })
	return s
}

func bindingsEnv(t *testing.T) *envelope.Envelope {
	t.Helper()
	env, err := decode("auto", strings.NewReader(kittenInput))
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestExecArgv(t *testing.T) {
	tmpl := []string{"kitten", "@", "action", "--match=id:{window}", "{cmd}"}

	got, err := execArgv(tmpl, "clear_terminal clear active", "7")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"kitten", "@", "action", "--match=id:7", "clear_terminal clear active"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}

	// no target window: the {window} element drops entirely
	got, err = execArgv(tmpl, "next_window", "")
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"kitten", "@", "action", "next_window"}
	if !slices.Equal(got, want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}

	if _, err := execArgv(tmpl, "   ", "7"); err == nil {
		t.Fatal("empty cmd should fail")
	}
	if _, err := execArgv([]string{"{window}"}, "x", ""); err == nil {
		t.Fatal("template rendering empty should fail")
	}
}

// Parent targeting: an accept with --window renders the kitty sheet's
// descriptor against the PARENT window id, so window-class actions do not
// hit the palette overlay.
func TestExecuteEntryTargetsParent(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := stubSeams(t)
	env := bindingsEnv(t)
	en, ok := entryByID(env, "default/cmd+w")
	if !ok {
		t.Fatal("entry missing")
	}
	if err := executeEntry(env, kittySheet(t), en, "42", true); err != nil {
		t.Fatal(err)
	}
	want := []string{"kitten", "@", "action", "--match=id:42", "close_window"}
	if len(s.argv) != 1 || !slices.Equal(s.argv[0], want) {
		t.Fatalf("spawned %v, want [%v]", s.argv, want)
	}
}

// The confirm gate never fires a flagged entry on first accept: it runs only
// after an explicit confirmation (or --yes), and a decline runs nothing.
func TestExecuteEntryConfirmGate(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	env := bindingsEnv(t)
	sheet := kittySheet(t)
	flagged, _ := entryByID(env, "default/cmd+w") // close_window: confirm rule
	plain, _ := entryByID(env, "default/cmd+t")   // new_tab_with_cwd: unflagged

	// declined: prompted once, nothing spawned
	s := stubSeams(t)
	if err := executeEntry(env, sheet, flagged, "", false); err != nil {
		t.Fatal(err)
	}
	if s.confirms != 1 || len(s.argv) != 0 {
		t.Fatalf("declined accept: confirms=%d spawns=%d, want 1/0", s.confirms, len(s.argv))
	}

	// confirmed: runs
	s = stubSeams(t)
	s.confirmOK = true
	if err := executeEntry(env, sheet, flagged, "", false); err != nil {
		t.Fatal(err)
	}
	if s.confirms != 1 || len(s.argv) != 1 {
		t.Fatalf("confirmed accept: confirms=%d spawns=%d, want 1/1", s.confirms, len(s.argv))
	}

	// --yes skips the prompt
	s = stubSeams(t)
	if err := executeEntry(env, sheet, flagged, "", true); err != nil {
		t.Fatal(err)
	}
	if s.confirms != 0 || len(s.argv) != 1 {
		t.Fatalf("--yes accept: confirms=%d spawns=%d, want 0/1", s.confirms, len(s.argv))
	}

	// unflagged entries never prompt
	s = stubSeams(t)
	if err := executeEntry(env, sheet, plain, "", false); err != nil {
		t.Fatal(err)
	}
	if s.confirms != 0 || len(s.argv) != 1 {
		t.Fatalf("plain accept: confirms=%d spawns=%d, want 0/1", s.confirms, len(s.argv))
	}
}

func TestConfirmReader(t *testing.T) {
	cases := map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "YES\n": true,
		"n\n": false, "\n": false, "": false, "quit\n": false,
	}
	for in, want := range cases {
		var out bytes.Buffer
		if got := confirm(strings.NewReader(in), &out, "quit"); got != want {
			t.Errorf("confirm(%q) = %v, want %v", in, got, want)
		}
		if !strings.Contains(out.String(), "quit") {
			t.Errorf("prompt %q does not name the entry", out.String())
		}
	}
}

// Accepts record usage in the statefile: count and last-used.
func TestExecuteEntryRecordsUsage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	s := stubSeams(t)
	env := bindingsEnv(t)
	en, _ := entryByID(env, "default/cmd+t")

	for i := 0; i < 2; i++ {
		if err := executeEntry(env, kittySheet(t), en, "", false); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.argv) != 2 {
		t.Fatalf("spawns = %d, want 2", len(s.argv))
	}
	st := state.Load(filepath.Join(dir, "krib", "kitty.json"))
	se, ok := st.Entries["default/cmd+t"]
	if !ok {
		t.Fatal("no usage recorded")
	}
	if se.Accepts != 2 || se.LastUsed.IsZero() {
		t.Fatalf("usage = %+v, want accepts=2 with lastUsed", se)
	}
	// exec observes before recording: a hashless entry here would make the
	// next list/print misread the binding as recently changed
	if se.Hash == "" {
		t.Fatalf("usage entry has no value-hash: %+v", se)
	}
}

// exec resolves the accepted id from the session cache file; nothing
// re-runs the scrape.
func TestExecIDResolvesFromCache(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := stubSeams(t)
	cache := filepath.Join(t.TempDir(), "cache.jsonl")
	if err := os.WriteFile(cache, []byte(kittenInput), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := execID("default/cmd+t", cache, "auto", "", "9", true); err != nil {
		t.Fatal(err)
	}
	want := []string{"kitten", "@", "action", "--match=id:9", "new_tab_with_cwd"}
	if len(s.argv) != 1 || !slices.Equal(s.argv[0], want) {
		t.Fatalf("spawned %v, want [%v]", s.argv, want)
	}

	if err := execID("default/nope", cache, "auto", "", "", true); err == nil {
		t.Fatal("unknown id should fail")
	}
}

// Non-kitty sheets get NO arbitrary Cmd passthrough: no exec descriptor
// means no run, and run=none refuses.
func TestExecuteEntryRefusals(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := stubSeams(t)
	env := bindingsEnv(t)
	en, _ := entryByID(env, "default/cmd+t")

	if err := executeEntry(env, classify.Sheet{Name: "bare"}, en, "", true); err == nil {
		t.Fatal("sheet without exec should refuse")
	}
	none := classify.Sheet{Name: "n", Exec: &classify.ExecSpec{Run: "none"}}
	if err := executeEntry(env, none, en, "", true); err == nil {
		t.Fatal("exec none should refuse")
	}
	if len(s.argv) != 0 {
		t.Fatalf("refusals spawned %v", s.argv)
	}
}

// A per-entry exec override (copy) wins over the sheet descriptor.
func TestExecuteEntryCopyOverride(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	s := stubSeams(t)
	var clip bytes.Buffer
	old := clipboardW
	clipboardW = &clip
	t.Cleanup(func() { clipboardW = old })

	env := bindingsEnv(t)
	en, _ := entryByID(env, "default/cmd+t")
	sheet := classify.Sheet{
		Name: "c",
		Exec: &classify.ExecSpec{Run: "run", Argv: []string{"x", "{cmd}"}},
		Entries: []classify.EntryRule{
			{Match: classify.Match{Exact: []string{"new_tab_with_cwd"}}, Exec: &classify.ExecSpec{Run: "copy"}},
		},
	}
	if err := executeEntry(env, sheet, en, "", false); err != nil {
		t.Fatal(err)
	}
	if len(s.argv) != 0 {
		t.Fatalf("copy override spawned %v", s.argv)
	}
	want := base64.StdEncoding.EncodeToString([]byte("new_tab_with_cwd"))
	if !strings.Contains(clip.String(), want) {
		t.Fatalf("clipboard = %q, want OSC52 payload %q", clip.String(), want)
	}
}
