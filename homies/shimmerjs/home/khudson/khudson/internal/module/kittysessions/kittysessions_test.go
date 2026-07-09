package kittysessions

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// Hand-written multi-window multi-tab fixture in the `kitten @ ls` shape:
// two os windows, three tabs, four windows. Window 7 carries a nested
// foreground group (shell + nvim) to pin the last-entry choice.
const lsFixture = `[
  {
    "id": 1,
    "is_focused": true,
    "tabs": [
      {
        "id": 4,
        "title": "khudson",
        "is_focused": true,
        "windows": [
          {
            "id": 7,
            "title": "nvim",
            "is_focused": true,
            "cwd": "/Users/shimmerjs/src/khudson",
            "foreground_processes": [
              {"cmdline": ["-zsh"], "cwd": "/Users/shimmerjs/src/khudson", "pid": 4241},
              {"cmdline": ["nvim", "main.go"], "cwd": "/Users/shimmerjs/src/khudson", "pid": 4242}
            ]
          },
          {
            "id": 8,
            "title": "zsh",
            "is_focused": false,
            "cwd": "/Users/shimmerjs",
            "foreground_processes": [
              {"cmdline": ["-zsh"], "cwd": "/Users/shimmerjs", "pid": 4243}
            ]
          }
        ]
      },
      {
        "id": 5,
        "title": "logs",
        "is_focused": false,
        "windows": [
          {
            "id": 9,
            "title": "tail",
            "is_focused": true,
            "cwd": "/var/log",
            "foreground_processes": [
              {"cmdline": ["tail", "-f", "system.log"], "cwd": "/var/log", "pid": 4244}
            ]
          }
        ]
      }
    ]
  },
  {
    "id": 2,
    "is_focused": false,
    "tabs": [
      {
        "id": 6,
        "title": "notes",
        "is_focused": true,
        "windows": [
          {
            "id": 10,
            "title": "",
            "is_focused": true,
            "cwd": "/Users/shimmerjs/notes",
            "foreground_processes": []
          }
        ]
      }
    ]
  }
]`

func TestParseLS(t *testing.T) {
	wins, err := parseLS([]byte(lsFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := []win{
		{OSWindowID: 1, TabID: 4, WindowID: 7, TabTitle: "khudson", Title: "nvim",
			Focused: true, Cwd: "/Users/shimmerjs/src/khudson",
			FgCmdline: []string{"nvim", "main.go"}},
		{OSWindowID: 1, TabID: 4, WindowID: 8, TabTitle: "khudson", Title: "zsh",
			Cwd: "/Users/shimmerjs", FgCmdline: []string{"-zsh"}},
		// window focused within an unfocused tab: not the focused row
		{OSWindowID: 1, TabID: 5, WindowID: 9, TabTitle: "logs", Title: "tail",
			Cwd: "/var/log", FgCmdline: []string{"tail", "-f", "system.log"}},
		// focused chain broken at the os window: not the focused row
		{OSWindowID: 2, TabID: 6, WindowID: 10, TabTitle: "notes", Title: "",
			Cwd: "/Users/shimmerjs/notes"},
	}
	if !reflect.DeepEqual(wins, want) {
		t.Errorf("parseLS = %+v, want %+v", wins, want)
	}
}

// TestParseLSReal parses a fixture captured from a real `kitten @ ls`
// against the main kitty (kitty 0.42.1): 3 os windows, 4 tabs,
// 8 windows. Titles/paths sanitized in place; structure and key set verbatim.
func TestParseLSReal(t *testing.T) {
	out, err := os.ReadFile(filepath.Join("testdata", "ls.json"))
	if err != nil {
		t.Fatal(err)
	}
	wins, err := parseLS(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(wins) != 8 {
		t.Fatalf("len(wins) = %d, want 8", len(wins))
	}
	wantIDs := [][3]int{
		{1, 1, 1}, {1, 1, 2}, {2, 2, 4}, {2, 2, 5},
		{2, 2, 6}, {2, 3, 8}, {2, 3, 9}, {3, 4, 10},
	}
	for i, w := range wins {
		got := [3]int{w.OSWindowID, w.TabID, w.WindowID}
		if got != wantIDs[i] {
			t.Errorf("wins[%d] ids = %v, want %v", i, got, wantIDs[i])
		}
	}
	first := win{OSWindowID: 1, TabID: 1, WindowID: 1, TabTitle: "tab 1",
		Title: "window 1", Focused: true, Cwd: "/Users/user/dev",
		FgCmdline: []string{
			"/etc/profiles/per-user/user/bin/claude",
			"--plugin-dir", "/nix/store/cxrkf1d06qcmhggl81qjfnkvzadpyn72-claude-code-hm-plugin",
			"--plugin-dir", "/nix/store/m0fnx8kxvhpxy2vx57q30z8r2yraci5m-source/plugins/worktrunk",
		}}
	if !reflect.DeepEqual(wins[0], first) {
		t.Errorf("wins[0] = %+v, want %+v", wins[0], first)
	}
	last := win{OSWindowID: 3, TabID: 4, WindowID: 10, TabTitle: "tab 4",
		Title: "window 10", Cwd: "/Users/user/dev/proj",
		FgCmdline: []string{"caffeinate", "-d"}}
	if !reflect.DeepEqual(wins[7], last) {
		t.Errorf("wins[7] = %+v, want %+v", wins[7], last)
	}
	focused := 0
	for _, w := range wins {
		if w.Focused {
			focused++
		}
	}
	if focused != 1 {
		t.Errorf("focused windows = %d, want 1", focused)
	}
}

func TestParseLSBadJSON(t *testing.T) {
	if _, err := parseLS([]byte("kitten: connection refused")); err == nil {
		t.Fatal("parseLS on non-JSON: want error, got nil")
	}
}

func TestParsePassword(t *testing.T) {
	for _, tt := range []struct {
		name, conf, want string
	}{
		{"single quoted", "remote_control_password 'hunter two' ls focus-window\n", "hunter two"},
		{"double quoted", `remote_control_password "pa ss" ls focus-window focus-tab send-text` + "\n", "pa ss"},
		{"bare token", "remote_control_password hunter2 ls\n", "hunter2"},
		{"bare token no verbs", "remote_control_password hunter2", "hunter2"},
		{"comments and blanks", "# main kitty RC password\n\nremote_control_password 'pw' ls\n", "pw"},
		{"commented option skipped", "# remote_control_password 'nope' ls\nremote_control_password 'yep' ls\n", "yep"},
		{"first line wins", "remote_control_password 'one' ls\nremote_control_password 'two' ls\n", "one"},
		{"leading whitespace", "  \tremote_control_password 'pw' ls\n", "pw"},
		{"prefix without boundary", "remote_control_passwords 'nope' ls\n", ""},
		{"option without value", "remote_control_password\nremote_control_password   \n", ""},
		{"unterminated quote", "remote_control_password 'pw ls\n", "pw ls"},
		{"unrelated lines only", "allow_remote_control socket-only\n", ""},
		{"empty", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseRCAuth([]byte(tt.conf)).Password; got != tt.want {
				t.Errorf("parseRCAuth(%q).Password = %q, want %q", tt.conf, got, tt.want)
			}
		})
	}
}

func TestReadPassword(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rc-password.conf")
	if err := os.WriteFile(file, []byte("remote_control_password 'pw' ls\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPassword(file)
	if err != nil || got != "pw" {
		t.Errorf("readPassword = %q, %v, want %q, nil", got, err, "pw")
	}
	got, err = readPassword(filepath.Join(dir, "missing.conf"))
	if err != nil || got != "" {
		t.Errorf("readPassword(missing) = %q, %v, want empty, nil (mirror the absent include)", got, err)
	}
}

// The verb allowlist follows the password on the remote_control_password
// line; kitty semantics make an empty list unrestricted.
func TestParseRCAuthVerbs(t *testing.T) {
	for _, tt := range []struct {
		name, conf string
		verbs      []string
	}{
		{"M9 set", `remote_control_password "pw" ls focus-window focus-tab send-text` + "\n",
			[]string{"ls", "focus-window", "focus-tab", "send-text"}},
		{"no verbs", `remote_control_password "pw"` + "\n", nil},
		{"tab separated", "remote_control_password 'pw'\tls\tlaunch\n", []string{"ls", "launch"}},
		{"empty conf", "", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRCAuth([]byte(tt.conf))
			if !reflect.DeepEqual(got.Verbs, tt.verbs) {
				t.Errorf("verbs = %v, want %v", got.Verbs, tt.verbs)
			}
		})
	}
}

func TestRCAuthAllows(t *testing.T) {
	m9 := RCAuth{Password: "pw", Verbs: []string{"ls", "focus-window", "focus-tab", "send-text"}}
	if m9.Allows("launch") {
		t.Error("M9 allowlist must NOT allow launch (the resume user gate)")
	}
	if !m9.Allows("focus-window") {
		t.Error("M9 allowlist must allow focus-window")
	}
	if !(RCAuth{}).Allows("launch") {
		t.Error("empty allowlist (or missing file) is unrestricted per kitty semantics")
	}
	if !(RCAuth{Verbs: []string{"*"}}).Allows("launch") {
		t.Error("* wildcard must allow everything")
	}
}

func TestReadRCAuth(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "rc-password.conf")
	conf := `remote_control_password "pw" ls focus-window` + "\n"
	if err := os.WriteFile(file, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := ReadRCAuth(file)
	if err != nil || a.Password != "pw" || len(a.Verbs) != 2 {
		t.Errorf("ReadRCAuth = %+v, %v", a, err)
	}
	a, err = ReadRCAuth(filepath.Join(dir, "missing.conf"))
	if err != nil || a.Password != "" || a.Verbs != nil {
		t.Errorf("ReadRCAuth(missing) = %+v, %v, want zero (mirror the absent include)", a, err)
	}
}

func winRow(ids, title, titleStyle string, rest ...string) module.Row {
	spans := []module.Span{
		{Text: ids, Style: module.StyleDim},
		{Text: " " + title, Style: titleStyle},
	}
	for _, r := range rest {
		spans = append(spans, module.Span{Text: " " + r, Style: module.StyleDim})
	}
	return module.Row{Kind: module.RowSpans, Spans: spans}
}

func TestRenderWins(t *testing.T) {
	wins, err := parseLS([]byte(lsFixture))
	if err != nil {
		t.Fatal(err)
	}
	d := renderWins(wins)
	if d.Title != "kitty" {
		t.Errorf("Title = %q, want %q", d.Title, "kitty")
	}
	want := []module.Row{
		winRow("1:4:7", "nvim", module.StyleAccent, "khudson", "nvim"),
		winRow("1:4:8", "zsh", module.StyleDim, "shimmerjs", "zsh"),
		winRow("1:5:9", "tail", module.StyleDim, "log", "tail"),
		// empty window title falls back to the tab title; no fg process span
		winRow("2:6:10", "notes", module.StyleDim, "notes"),
	}
	if !reflect.DeepEqual(d.Rows, want) {
		t.Errorf("Rows = %+v, want %+v", d.Rows, want)
	}
}

func TestRenderWinsCap(t *testing.T) {
	wins := make([]win, 14)
	for i := range wins {
		wins[i] = win{OSWindowID: 1, TabID: 2, WindowID: i + 1,
			Title: fmt.Sprintf("w%d", i), Cwd: "/tmp"}
	}
	d := renderWins(wins)
	if len(d.Rows) != maxRows {
		t.Fatalf("len(Rows) = %d, want %d", len(d.Rows), maxRows)
	}
	last := d.Rows[maxRows-1]
	if last.Kind != module.RowText || last.Text != "+5 more" || last.Style != module.StyleDim {
		t.Errorf("last row = %+v, want dim text %q", last, "+5 more")
	}
}

func TestRenderWinsEmpty(t *testing.T) {
	d := renderWins(nil)
	if len(d.Rows) != 1 || d.Rows[0].Text != "no windows" || d.Rows[0].Style != module.StyleDim {
		t.Errorf("Rows = %+v, want single dim %q row", d.Rows, "no windows")
	}
}

func TestPollKittenMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // LookPath must miss regardless of host
	d, err := Mod{}.Poll(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Poll without kitten: err = %v, want friendly row", err)
	}
	if len(d.Rows) != 1 || d.Rows[0].Text != "kitten not on PATH" || d.Rows[0].Style != module.StyleDim {
		t.Errorf("Rows = %+v, want single dim %q row", d.Rows, "kitten not on PATH")
	}
}

func TestPollSocketUnreachable(t *testing.T) {
	if _, err := exec.LookPath("kitten"); err != nil {
		t.Skip("kitten not installed")
	}
	dir := t.TempDir()
	pwFile := filepath.Join(dir, "rc-password.conf")
	if err := os.WriteFile(pwFile, []byte("remote_control_password 'pw' ls\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := Mod{}.Poll(ctx, map[string]any{
		"socket":       filepath.Join(dir, "absent.sock"),
		"passwordFile": pwFile,
	})
	if err == nil {
		t.Fatal("Poll against absent socket: want error, got nil")
	}
	if strings.Contains(err.Error(), "pw") {
		t.Errorf("error leaks password: %v", err)
	}
}
