package sheets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Loader form (a): empty means the embedded default.
func TestLoadDefault(t *testing.T) {
	sheet, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if sheet.Name != "kitty" {
		t.Fatalf("default sheet = %q, want kitty", sheet.Name)
	}
}

// Loader form (b): a bare name resolves an embedded repo sheet.
func TestLoadNamed(t *testing.T) {
	sheet, err := Load("kitty")
	if err != nil {
		t.Fatal(err)
	}
	if sheet.Name != "kitty" {
		t.Fatalf("named sheet = %q", sheet.Name)
	}
	if _, err := Load("nope"); err == nil || !strings.Contains(err.Error(), "kitty") {
		t.Fatalf("unknown name should fail listing the embedded sheets, got %v", err)
	}
}

// Loader form (c): a JSON file path loads at runtime, no rebuild.
func TestLoadPath(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mine.json")
	if err := os.WriteFile(p, []byte(`{"name": "mine", "groups": [{"name": "all"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sheet, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if sheet.Name != "mine" || len(sheet.Groups) != 1 {
		t.Fatalf("path sheet = %+v", sheet)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing path should fail")
	}
}

// Config errors are LOAD errors, not silent skips or first-use surprises.
func TestDecodeRejects(t *testing.T) {
	cases := map[string]string{
		"unknown sorter":  `{"name": "x", "sort": ["nope"], "groups": [{"name": "all"}]}`,
		"group sorter":    `{"name": "x", "groups": [{"name": "all", "sort": ["nope"]}]}`,
		"unknown field":   `{"name": "x", "grops": []}`,
		"bad regex":       `{"name": "x", "groups": [{"name": "a", "match": {"re": "("}}, {"name": "all"}]}`,
		"bad exec run":    `{"name": "x", "exec": {"run": "shell"}, "groups": [{"name": "all"}]}`,
		"empty exec argv": `{"name": "x", "exec": {"run": "run"}, "groups": [{"name": "all"}]}`,
		"empty rule":      `{"name": "x", "groups": [{"name": "all"}], "entries": [{"match": {}}]}`,
		"two catch-alls":  `{"name": "x", "groups": [{"name": "a"}, {"name": "b"}]}`,
	}
	for name, in := range cases {
		if _, err := Decode(strings.NewReader(in)); err == nil {
			t.Errorf("%s: want load error", name)
		}
	}
}

// The embedded kitty sheet carries the KittySheet() literal content: the
// groups (including the personal alias exact lists), the theme, the exec
// descriptor, and the confirm rules.
func TestKittySheetContent(t *testing.T) {
	sheet, err := Load("kitty")
	if err != nil {
		t.Fatal(err)
	}

	names := make([]string, len(sheet.Groups))
	for i, g := range sheet.Groups {
		names[i] = g.Name
	}
	want := "windows,tabs,layout,scrolling,system,clipboard,other"
	if strings.Join(names, ",") != want {
		t.Fatalf("groups = %v", names)
	}
	if got := sheet.Groups[0].Match.Exact; len(got) != 3 || got[0] != "launch --location=hsplit --cwd=current" {
		t.Fatalf("windows aliases = %v", got)
	}
	if got := sheet.Groups[5].Match.Exact; len(got) != 3 || got[1] != "paste_from_selection" {
		t.Fatalf("clipboard aliases = %v", got)
	}
	if sheet.Groups[6].Match != nil {
		t.Fatal("other is not the catch-all")
	}
	if got := strings.Join(sheet.Groups[0].Sort, ","); got != "leading-key,longest-last,group-next-prev,int-keys-last" {
		t.Fatalf("windows sort = %q", got)
	}

	if sheet.Theme == nil || sheet.Theme.LeftWidth != 26 || sheet.Theme.Keys != "#d3c6aa" {
		t.Fatalf("theme = %+v", sheet.Theme)
	}
	if sheet.Exec == nil || sheet.Exec.Run != "run" {
		t.Fatalf("exec = %+v", sheet.Exec)
	}
	argv := strings.Join(sheet.Exec.Argv, " ")
	if !strings.Contains(argv, "--match=id:{window}") || !strings.Contains(argv, "{cmd}") {
		t.Fatalf("exec argv = %q", argv)
	}

	for _, cmd := range []string{"quit", "close_window", "close_tab"} {
		r := sheet.Rule(cmd)
		if r == nil || !r.Confirm {
			t.Errorf("%s: want a confirm rule", cmd)
		}
	}
	for _, cmd := range []string{"new_tab_with_cwd", "load_config_file", "closet"} {
		if r := sheet.Rule(cmd); r != nil {
			t.Errorf("%s: unexpectedly matched rule %+v", cmd, r)
		}
	}
}
