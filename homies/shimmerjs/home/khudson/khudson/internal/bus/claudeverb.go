// Claude control verbs: the `khudson claude focus|resume <sid>` wrapper that
// panel row Acts exec. handleRowAct only vets the argv against the published
// acts and surfaces a nonzero exit, so all smarts live here -- a FRESH
// `kitten @ ls` before acting (never trust poll-time window ids), the
// resolution chain, and miss logging to <state root>/log/claude-verbs.log
// (the log is the observable). RC auth is the socket itself: the daily
// kitty runs allow_remote_control=socket-only, which trusts peers on the
// user-write-only socket file -- no password (kitty never consults
// remote_control_password for socket peers; the retired password arc sent
// one anyway and hard-failed on the missing KITTY_PUBLIC_KEY from launchd,
// so no verb ever worked until it was dropped).
//
// Resolution chain, in order:
//  1. user var claude_session=<sid> (panel-launched windows plant it via
//     launch --var).
//  2. the spool's kitty_window_id (SessionStart hook env plant), accepted
//     only if that window's foreground pids still include the registry pid
//     for the sid -- ids decay between poll and tap.
//  3. registry-pid join over ALL foreground_processes[].pid (fg[0] is
//     caffeinate/-zsh in captured data; claude sits later in the group).
//
// Resume launches for real (tab in the daily kitty, `claude --resume`).
// It is reachable only by hand-running the CLI -- no panel row publishes
// it -- so running the command carries the consent; the old rc-password verb
// allowlist gate retired with the password machinery.
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
)

// claudeSessionVar is the kitty user var carrying session identity on
// panel-launched windows.
const claudeSessionVar = "claude_session"

// kittenRunner execs one `kitten @` command against a socket. Seam for
// tests.
type kittenRunner func(ctx context.Context, socket string, args ...string) ([]byte, error)

// ClaudeVerbs is the wrapper's wiring: sockets, source dirs, the miss log,
// and the exec seam.
type ClaudeVerbs struct {
	Socket      string // main kitty RC socket
	SpoolDir    string // hook-written session spool
	SessionsDir string // claude session registry (<pid>.json)
	LogPath     string // append-only verb log; "" = stderr only
	Run         kittenRunner
}

// NewClaudeVerbs wires the defaults: state-root sockets/spool/log,
// ~/.claude/sessions.
func NewClaudeVerbs() (*ClaudeVerbs, error) {
	p, err := paths.Resolve()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home: %w", err)
	}
	return &ClaudeVerbs{
		Socket:      p.MainKittySocket(),
		SpoolDir:    p.ClaudeSpool(),
		SessionsDir: filepath.Join(home, ".claude", "sessions"),
		LogPath:     filepath.Join(p.Dir, "log", "claude-verbs.log"),
		Run:         runKitten,
	}, nil
}

// logf records one verb outcome to stderr and the append-only log. The
// caller never puts secrets in the line; misses MUST land here because the
// bus discards the wrapper's exit status.
func (v *ClaudeVerbs) logf(format string, args ...any) {
	line := time.Now().Format(time.RFC3339) + " khudson claude " + fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, line)
	if v.LogPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(v.LogPath), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(v.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, line)
}

// Focus resolves the kitty window hosting session sid via a fresh ls and
// focuses it. Nonzero return on miss, after logging.
func (v *ClaudeVerbs) Focus(ctx context.Context, sid string) error {
	tree, err := v.freshLS(ctx)
	if err != nil {
		v.logf("focus %s: ls: %v", sid, err)
		return err
	}
	win, how := v.resolveWindow(tree, sid)
	if win == 0 {
		err := fmt.Errorf("no window for session %s (user var, spool window, registry pid all missed)", sid)
		v.logf("focus %s: MISS: %v", sid, err)
		return err
	}
	if _, err := v.Run(ctx, v.Socket, "focus-window", "--match", "id:"+strconv.Itoa(win)); err != nil {
		v.logf("focus %s: focus-window id:%d (%s): %v", sid, win, how, err)
		return err
	}
	v.logf("focus %s: ok -> window %d via %s", sid, win, how)
	return nil
}

// Resume relaunches a spool-backed session (`claude --resume` reuses the
// original sid, so the planted user var stays correct). Reachable only by
// hand-running the CLI -- no panel row publishes it -- so running the
// command carries the consent. A still-running session is focused, never
// duplicated (revalidate-at-exec).
func (v *ClaudeVerbs) Resume(ctx context.Context, sid, cwd string) error {
	if cwd == "" {
		cwd = v.spoolCwd(sid)
	}
	if cwd == "" {
		err := fmt.Errorf("session %s has no spool cwd; resume serves spool-backed sessions only", sid)
		v.logf("resume %s: %v", sid, err)
		return err
	}
	tree, err := v.freshLS(ctx)
	if err != nil {
		v.logf("resume %s: ls: %v", sid, err)
		return err
	}
	if win, how := v.resolveWindow(tree, sid); win != 0 {
		// already running: focus instead of forking a duplicate session
		if _, err := v.Run(ctx, v.Socket, "focus-window", "--match", "id:"+strconv.Itoa(win)); err != nil {
			v.logf("resume %s: still running (window %d via %s) but focus failed: %v", sid, win, how, err)
			return err
		}
		v.logf("resume %s: still running -> focused window %d via %s", sid, win, how)
		return nil
	}
	if _, err := v.Run(ctx, v.Socket,
		"launch", "--type", "tab", "--cwd", cwd, "--var", claudeSessionVar+"="+sid,
		"claude", "--resume", sid); err != nil {
		v.logf("resume %s: launch: %v", sid, err)
		return err
	}
	v.logf("resume %s: launched in %s", sid, cwd)
	return nil
}

// freshLS is the revalidation read: a fresh `kitten @ ls` unmarshaled into
// the rc types (UserVars + per-process PID are modeled there; the
// kittysessions widget structs drop both).
func (v *ClaudeVerbs) freshLS(ctx context.Context) ([]rc.OSWindow, error) {
	out, err := v.Run(ctx, v.Socket, "ls")
	if err != nil {
		return nil, err
	}
	var tree []rc.OSWindow
	if err := json.Unmarshal(out, &tree); err != nil {
		return nil, fmt.Errorf("parse ls: %w", err)
	}
	return tree, nil
}

// resolveWindow runs the resolution chain against a fresh tree. Returns the
// kitty window id and which rung matched ("" on miss).
func (v *ClaudeVerbs) resolveWindow(tree []rc.OSWindow, sid string) (int, string) {
	if w, ok := rc.FindWindowByUserVar(tree, claudeSessionVar, sid); ok {
		return w.ID, "user-var"
	}
	pid := registryPID(v.SessionsDir, sid)
	if spoolWin := v.spoolWindowID(sid); spoolWin > 0 && pid > 0 {
		if w, ok := windowByID(tree, spoolWin); ok && hasFGPID(w, pid) {
			return w.ID, "spool-window"
		}
	}
	if pid > 0 {
		for _, osw := range tree {
			for _, tab := range osw.Tabs {
				for _, w := range tab.Windows {
					if hasFGPID(w, pid) {
						return w.ID, "registry-pid"
					}
				}
			}
		}
	}
	return 0, ""
}

func windowByID(tree []rc.OSWindow, id int) (rc.Window, bool) {
	for _, osw := range tree {
		for _, tab := range osw.Tabs {
			for _, w := range tab.Windows {
				if w.ID == id {
					return w, true
				}
			}
		}
	}
	return rc.Window{}, false
}

// hasFGPID scans ALL foreground pids of a window: in captured ls output the
// claude process is not fg[0] (caffeinate/-zsh lead the group).
func hasFGPID(w rc.Window, pid int) bool {
	for _, p := range w.ForegroundProcesses {
		if p.PID == pid {
			return true
		}
	}
	return false
}

// spoolWindowID reads the SessionStart-planted kitty_window_id from the
// session's spool file; string or number tolerated, 0 when absent.
func (v *ClaudeVerbs) spoolWindowID(sid string) int {
	raw := v.readSpool(sid)
	if raw == nil {
		return 0
	}
	var s struct {
		KittyWindowID json.RawMessage `json:"kitty_window_id"`
	}
	if json.Unmarshal(raw, &s) != nil || len(s.KittyWindowID) == 0 {
		return 0
	}
	var n int
	if json.Unmarshal(s.KittyWindowID, &n) == nil {
		return n
	}
	var str string
	if json.Unmarshal(s.KittyWindowID, &str) == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(str)); err == nil {
			return n
		}
	}
	return 0
}

// spoolCwd reads the session's spool cwd (workspace.current_dir over cwd).
func (v *ClaudeVerbs) spoolCwd(sid string) string {
	raw := v.readSpool(sid)
	if raw == nil {
		return ""
	}
	var s struct {
		Cwd       string `json:"cwd"`
		Workspace struct {
			CurrentDir string `json:"current_dir"`
		} `json:"workspace"`
	}
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	if s.Workspace.CurrentDir != "" {
		return s.Workspace.CurrentDir
	}
	return s.Cwd
}

func (v *ClaudeVerbs) readSpool(sid string) []byte {
	if v.SpoolDir == "" {
		return nil
	}
	p := filepath.Join(v.SpoolDir, sid+".json")
	info, err := os.Stat(p)
	// skip non-regular files: reading a FIFO would block the verb forever
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return b
}

// registryPID scans the session registry (<pid>.json) for sid's pid;
// dead-pid files linger, so the newest updatedAt wins. 0 when unknown.
func registryPID(dir, sid string) int {
	if dir == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	pid, newest := 0, int64(-1)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r struct {
			PID       int    `json:"pid"`
			SessionID string `json:"sessionId"`
			UpdatedAt int64  `json:"updatedAt"`
		}
		if json.Unmarshal(b, &r) != nil || r.SessionID != sid {
			continue
		}
		p := r.PID
		if p == 0 {
			// the pid is also the filename stem
			if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json")); err == nil {
				p = n
			}
		}
		if p > 0 && r.UpdatedAt > newest {
			pid, newest = p, r.UpdatedAt
		}
	}
	return pid
}

// runKitten execs `kitten @ --to unix:<socket> <args...>`; errors carry
// kitten's first stderr line.
func runKitten(ctx context.Context, socket string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	argv := append([]string{"@", "--to", "unix:" + socket}, args...)
	cmd := exec.CommandContext(ctx, "kitten", argv...)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			msg, _, _ := strings.Cut(strings.TrimSpace(string(ee.Stderr)), "\n")
			return nil, fmt.Errorf("kitten @ %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("kitten @ %s: main kitty RC socket not reachable at %s: %w", args[0], socket, err)
	}
	return out, nil
}
