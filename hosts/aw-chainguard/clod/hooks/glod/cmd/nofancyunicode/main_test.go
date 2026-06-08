package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"glod/internal/txtar"
)

// txtar fixtures live in testdata/*.txtar, populated from real Claude PreToolUse
// payloads (each fixture's header comment records provenance) plus adversarial
// cases. Each archive holds:
//
//	-- input.json --     the raw hook stdin payload ({tool_name, tool_input})
//	-- want --           expected process exit code (0 allow, 2 deny)
//	-- want_stderr --    optional substring the deny message must contain
//
// We exercise decide() directly rather than the built binary so the suite runs
// inside the nix buildGoModule check phase, before the binary is installed.

func TestTxtarFixtures(t *testing.T) {
	paths, err := filepath.Glob("testdata/*.txtar")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no testdata/*.txtar fixtures found")
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".txtar")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			files := txtar.Parse(raw)

			input, ok := files["input.json"]
			if !ok {
				t.Fatalf("%s: missing -- input.json --", p)
			}
			wantStr, ok := files["want"]
			if !ok {
				t.Fatalf("%s: missing -- want --", p)
			}
			want, err := strconv.Atoi(strings.TrimSpace(wantStr))
			if err != nil {
				t.Fatalf("%s: unparseable want %q: %v", p, wantStr, err)
			}

			code, stderr := decide([]byte(input))
			if code != want {
				t.Errorf("exit = %d, want %d (stderr: %q)", code, want, stderr)
			}
			if sub := strings.TrimSpace(files["want_stderr"]); sub != "" && !strings.Contains(stderr, sub) {
				t.Errorf("stderr %q does not contain %q", stderr, sub)
			}
			// A deny must explain itself; an allow must stay silent.
			if code == 2 && stderr == "" {
				t.Errorf("exit 2 with empty stderr")
			}
			if code == 0 && stderr != "" {
				t.Errorf("exit 0 but non-empty stderr %q", stderr)
			}
		})
	}
}
