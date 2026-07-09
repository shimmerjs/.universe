// Package kittysessions lists the windows of the user's main kitty
// instance over its RC socket (kitten @ ls). Pure data mapper: one row per
// kitty window carrying the full RC address (os window / tab / window ids)
// so the control-panel milestone can act on rows later; focus/resume verbs
// are NOT this module's. The socket defaults to the fixed main-kitty.sock
// under the khudson state root; auth is the M9 remote_control_password,
// read from the same 0600 conf include the main kitty loads and passed via
// the KITTY_RC_PASSWORD env var -- never argv (ps-visible), never logged.
package kittysessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

// Mod implements module.Module.
type Mod struct{}

func (Mod) Name() string { return "kitty-sessions" }

func (Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	socket, _ := params["socket"].(string)
	if socket == "" {
		p, err := paths.Resolve()
		if err != nil {
			return module.Data{}, err
		}
		socket = p.MainKittySocket()
	}
	pwFile, _ := params["passwordFile"].(string)
	if pwFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return module.Data{}, fmt.Errorf("resolve home: %w", err)
		}
		pwFile = filepath.Join(home, ".config", "kitty", "rc-password.conf")
	}
	if _, err := exec.LookPath("kitten"); err != nil {
		return module.Data{Title: "kitty", Rows: []module.Row{
			{Kind: module.RowText, Text: "kitten not on PATH", Style: module.StyleDim},
		}}, nil
	}
	password, err := readPassword(pwFile)
	if err != nil {
		return module.Data{}, err
	}
	out, err := runLS(ctx, socket, password)
	if err != nil {
		return module.Data{}, err
	}
	wins, err := parseLS(out)
	if err != nil {
		return module.Data{}, err
	}
	return renderWins(wins), nil
}

// readPassword reads the kitty rc-password conf include. A missing file
// means "" -- the main kitty includes the same file, so mirroring its
// absence keeps client and server auth posture in sync (sending a password
// to a passwordless kitty hangs on a confirmation prompt).
func readPassword(file string) (string, error) {
	a, err := ReadRCAuth(file)
	return a.Password, err
}

// RCAuth is the parsed rc-password.conf posture: the M9 password plus the
// verb allowlist that follows it on the remote_control_password line.
// kitty semantics: an empty action list allows EVERY action, so nil Verbs
// (including a missing file, which also means a passwordless socket) is
// unrestricted.
type RCAuth struct {
	Password string
	Verbs    []string
}

// Allows reports whether the allowlist permits verb (kitty matches actions
// with fnmatch; "*" is the only pattern the hand-written file plausibly
// uses, so exact match + "*" covers it).
func (a RCAuth) Allows(verb string) bool {
	if len(a.Verbs) == 0 {
		return true
	}
	for _, v := range a.Verbs {
		if v == verb || v == "*" {
			return true
		}
	}
	return false
}

// ReadRCAuth reads and parses the rc-password conf include; missing file
// semantics match readPassword.
func ReadRCAuth(file string) (RCAuth, error) {
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return RCAuth{}, nil
	}
	if err != nil {
		return RCAuth{}, fmt.Errorf("read rc password: %w", err)
	}
	return parseRCAuth(b), nil
}

// parseRCAuth parses the first remote_control_password line: the password
// -- single- or double-quoted (may contain spaces) or a bare token -- then
// the whitespace-separated action allowlist. Comments and unrelated lines
// are skipped; no backslash-escape handling (the hand-created file quotes
// plainly).
func parseRCAuth(conf []byte) RCAuth {
	const opt = "remote_control_password"
	for line := range strings.SplitSeq(string(conf), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), opt)
		if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			continue
		}
		var password, tail string
		switch q := rest[0]; q {
		case '\'', '"':
			if i := strings.IndexByte(rest[1:], q); i >= 0 {
				password, tail = rest[1:1+i], rest[2+i:]
			} else {
				password = rest[1:]
			}
		default:
			if i := strings.IndexAny(rest, " \t"); i >= 0 {
				password, tail = rest[:i], rest[i:]
			} else {
				password = rest
			}
		}
		verbs := strings.Fields(tail)
		if len(verbs) == 0 {
			verbs = nil // nil = unrestricted, the documented semantics
		}
		return RCAuth{Password: password, Verbs: verbs}
	}
	return RCAuth{}
}

// runLS execs `kitten @ ls` against the socket. The password rides the
// KITTY_RC_PASSWORD env var only; it never appears in argv or in errors.
func runLS(ctx context.Context, socket, password string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "kitten", "@", "--to", "unix:"+socket, "ls")
	if password != "" {
		cmd.Env = append(os.Environ(), "KITTY_RC_PASSWORD="+password)
	}
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n")
			return nil, fmt.Errorf("kitten @ ls: %s", msg)
		}
		return nil, fmt.Errorf("main kitty RC socket not reachable at %s (listen_on not configured?)", socket)
	}
	return out, nil
}

// `kitten @ ls` JSON shape: os_windows -> tabs -> windows.
type lsOSWindow struct {
	ID        int     `json:"id"`
	IsFocused bool    `json:"is_focused"`
	Tabs      []lsTab `json:"tabs"`
}

type lsTab struct {
	ID        int        `json:"id"`
	Title     string     `json:"title"`
	IsFocused bool       `json:"is_focused"`
	Windows   []lsWindow `json:"windows"`
}

type lsWindow struct {
	ID                  int      `json:"id"`
	Title               string   `json:"title"`
	IsFocused           bool     `json:"is_focused"`
	Cwd                 string   `json:"cwd"`
	ForegroundProcesses []lsProc `json:"foreground_processes"`
}

type lsProc struct {
	Cmdline []string `json:"cmdline"`
}

// win is one kitty window: the full RC address plus what a row shows.
type win struct {
	OSWindowID int
	TabID      int
	WindowID   int
	TabTitle   string
	Title      string
	Focused    bool // focused window of the focused tab of the focused os window
	Cwd        string
	FgCmdline  []string
}

// parseLS flattens `kitten @ ls` JSON into one entry per window. The
// foreground process kept is the LAST of the window's foreground group:
// in captured output (testdata/ls.json) the job leader the user launched
// follows its spawned descendants, so the last entry is the informative one.
func parseLS(out []byte) ([]win, error) {
	var osws []lsOSWindow
	if err := json.Unmarshal(out, &osws); err != nil {
		return nil, fmt.Errorf("kitten ls: %w", err)
	}
	var wins []win
	for _, osw := range osws {
		for _, t := range osw.Tabs {
			for _, w := range t.Windows {
				entry := win{
					OSWindowID: osw.ID,
					TabID:      t.ID,
					WindowID:   w.ID,
					TabTitle:   t.Title,
					Title:      w.Title,
					Focused:    osw.IsFocused && t.IsFocused && w.IsFocused,
					Cwd:        w.Cwd,
				}
				if n := len(w.ForegroundProcesses); n > 0 {
					entry.FgCmdline = w.ForegroundProcesses[n-1].Cmdline
				}
				wins = append(wins, entry)
			}
		}
	}
	return wins, nil
}

// procName is the display name of a foreground cmdline: basename of argv[0]
// with the login-shell dash trimmed ("-zsh" -> "zsh").
func procName(cmdline []string) string {
	if len(cmdline) == 0 {
		return ""
	}
	return strings.TrimPrefix(path.Base(cmdline[0]), "-")
}

const maxRows = 10

// renderWins maps windows to spans rows: a dim "osw:tab:win" id triplet
// (the panel's future RC address), the window title (accent when focused),
// then dim cwd basename and foreground process name; capped at maxRows
// with a "+N more" tail.
func renderWins(wins []win) module.Data {
	if len(wins) == 0 {
		return module.Data{Title: "kitty", Rows: []module.Row{
			{Kind: module.RowText, Text: "no windows", Style: module.StyleDim},
		}}
	}
	shown := len(wins)
	if shown > maxRows {
		shown = maxRows - 1
	}
	rows := make([]module.Row, 0, maxRows)
	for _, w := range wins[:shown] {
		style := module.StyleDim
		if w.Focused {
			style = module.StyleAccent
		}
		title := w.Title
		if title == "" {
			title = w.TabTitle
		}
		spans := []module.Span{
			{Text: fmt.Sprintf("%d:%d:%d", w.OSWindowID, w.TabID, w.WindowID), Style: module.StyleDim},
			{Text: " " + title, Style: style},
		}
		if w.Cwd != "" {
			spans = append(spans, module.Span{Text: " " + path.Base(w.Cwd), Style: module.StyleDim})
		}
		if p := procName(w.FgCmdline); p != "" {
			spans = append(spans, module.Span{Text: " " + p, Style: module.StyleDim})
		}
		rows = append(rows, module.Row{Kind: module.RowSpans, Spans: spans})
	}
	if rest := len(wins) - shown; rest > 0 {
		rows = append(rows, module.Row{Kind: module.RowText,
			Text: fmt.Sprintf("+%d more", rest), Style: module.StyleDim})
	}
	return module.Data{Title: "kitty", Rows: rows}
}
