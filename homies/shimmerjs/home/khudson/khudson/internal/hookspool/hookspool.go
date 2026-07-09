// Package hookspool is the claude-code hook handler behind `khudson hook
// <event>`: it merges one event payload (stdin JSON) into the session's
// spool file. One static-binary fork replaces the bash+jq hook scripts
// whose 4-9 child forks cost ~65-70ms per fire -- the hook surface fires
// per turn-class event on every session, so the fork tree was the whole
// cost. Semantics are a faithful port of those scripts: merge-don't-
// clobber (a whole-file overwrite would erase earlier events' fields),
// corrupt or missing spool starts from {}, atomic tmp+mv so a concurrent
// panel read never sees a half-written file.
package hookspool

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Events in the vocabulary; Run rejects anything else so a typo'd hook
// wiring fails loudly at first fire, not silently forever.
const (
	EventPrompt   = "prompt"
	EventStart    = "start"
	EventStop     = "stop"
	EventStopFail = "stopfail"
	EventNotify   = "notify"
	EventEnd      = "end"
)

// reapAfter is the spool retention horizon: every hook write refreshes the
// file mtime, so a spool untouched this long is a dead session. The reaper
// rides the end event only (a transcript scan per fire is forbidden by the
// measured hook economics).
const reapAfter = 7 * 24 * time.Hour

// Run handles one hook fire: payload JSON on stdin, spool dir from the
// wiring. A payload without session_id is a silent no-op (exit 0), matching
// claude-code's fire-and-forget hook contract. env resolves process env
// (only KITTY_WINDOW_ID is read); now stamps ts fields.
func Run(event, dir string, stdin io.Reader, env func(string) string, now time.Time) error {
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return fmt.Errorf("hookspool: read payload: %w", err)
	}
	var in map[string]any
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil // malformed payload: nothing to record
	}
	sid, _ := in["session_id"].(string)
	if sid == "" || strings.ContainsAny(sid, "/\x00") {
		return nil
	}

	if event == EventEnd {
		return end(dir, sid, in, now)
	}

	merge := map[string]any{"session_id": sid}
	switch event {
	case EventPrompt:
		merge["prompt"] = str(in["prompt"])
		merge["ts"] = now.Unix()
		merge["attention"] = false
		if cwd := str(in["cwd"]); cwd != "" {
			merge["cwd"] = cwd
			merge["workspace"] = map[string]any{"current_dir": cwd}
		}
		setIf(merge, "session_title", str(in["session_title"]))
		setIf(merge, "transcript_path", str(in["transcript_path"]))
		setIf(merge, "permission_mode", str(in["permission_mode"]))
	case EventStart:
		merge["started_ts"] = now.Unix()
		if s := str(in["source"]); s != "" {
			merge["source"] = s
		} else {
			merge["source"] = "startup"
		}
		setIf(merge, "model", modelName(in["model"]))
		setIf(merge, "session_title", str(in["session_title"]))
		if cwd := str(in["cwd"]); cwd != "" {
			merge["cwd"] = cwd
			merge["workspace"] = map[string]any{"current_dir": cwd}
		}
		setIf(merge, "kitty_window_id", env("KITTY_WINDOW_ID"))
	case EventStop:
		merge["attention"] = false
		merge["stopped_ts"] = now.Unix()
		merge["bg_tasks"] = arrayLen(in["background_tasks"])
		merge["crons"] = arrayLen(in["session_crons"])
		if s, ok := in["last_assistant_message"].(string); ok && s != "" {
			merge["last_assistant"] = firstLine(s, 200)
		}
		if eff, ok := in["effort"].(map[string]any); ok {
			setIf(merge, "effort", str(eff["level"]))
		}
	case EventStopFail:
		merge["attention"] = false
		merge["stopped_ts"] = now.Unix()
		if e := str(in["error"]); e != "" {
			merge["error"] = e
		} else {
			merge["error"] = "error"
		}
	case EventNotify:
		merge["attention"] = true
		merge["notification"] = str(in["message"])
		merge["notification_ts"] = now.Unix()
		setIf(merge, "notification_type", str(in["notification_type"]))
		setIf(merge, "notification_title", str(in["title"]))
	default:
		return fmt.Errorf("hookspool: unknown event %q", event)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("hookspool: %w", err)
	}
	f := filepath.Join(dir, sid+".json")
	base := readBase(f)
	maps.Copy(base, merge)
	// notify backfills cwd only when the spool has none (a notification can
	// precede the first prompt; a real prompt cwd must win)
	if event == EventNotify {
		if cwd := str(in["cwd"]); cwd != "" && curDir(base) == "" {
			base["cwd"] = cwd
			base["workspace"] = map[string]any{"current_dir": cwd}
		}
	}
	// a clean Stop cures any StopFailure error: the err state must not
	// outlive the turn that fixed it
	if event == EventStop {
		delete(base, "error")
	}
	return writeAtomic(f, base)
}

// end is retention, not unconditional delete: clear/logout drop the spool
// (+ agent sidecar); a normal quit (prompt_input_exit) and unknown reasons
// keep it -- it is the likeliest resume target. Then the 7d reaper sweeps
// dead spools and orphaned sidecars.
func end(dir, sid string, in map[string]any, now time.Time) error {
	switch str(in["reason"]) {
	case "clear", "logout":
		os.Remove(filepath.Join(dir, sid+".json"))
		os.RemoveAll(filepath.Join(dir, sid+".agents"))
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil // no spool dir: nothing to reap
	}
	for _, e := range ents {
		name := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(name, ".json") {
			if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > reapAfter {
				os.Remove(filepath.Join(dir, name))
			}
		}
	}
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() && strings.HasSuffix(name, ".agents") {
			owner := strings.TrimSuffix(name, ".agents") + ".json"
			if _, err := os.Stat(filepath.Join(dir, owner)); err != nil {
				os.RemoveAll(filepath.Join(dir, name))
			}
		}
	}
	return nil
}

// readBase parses the existing spool file; anything short of a JSON object
// (missing, unreadable, corrupt, wrong type) starts from {} -- one bad
// write must never wedge the session's spool forever.
func readBase(f string) map[string]any {
	raw, err := os.ReadFile(f)
	if err != nil {
		return map[string]any{}
	}
	var base map[string]any
	if json.Unmarshal(raw, &base) != nil || base == nil {
		return map[string]any{}
	}
	return base
}

func writeAtomic(f string, v map[string]any) error {
	out, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("hookspool: %w", err)
	}
	tmp := fmt.Sprintf("%s.tmp.%d", f, os.Getpid())
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("hookspool: %w", err)
	}
	if err := os.Rename(tmp, f); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("hookspool: %w", err)
	}
	return nil
}

// str mirrors the scripts' `// "" | tostring` guard: null/absent/false
// read as empty, strings pass through, anything else stringifies as JSON.
func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if !t {
			return ""
		}
		return "true"
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", t), ".0")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(b)
	}
}

// modelName mirrors the start script: an object payload reads
// display_name then id; a scalar stringifies.
func modelName(v any) string {
	if m, ok := v.(map[string]any); ok {
		if s := str(m["display_name"]); s != "" {
			return s
		}
		return str(m["id"])
	}
	return str(v)
}

func setIf(m map[string]any, k, v string) {
	if v != "" {
		m[k] = v
	}
}

func arrayLen(v any) int {
	if a, ok := v.([]any); ok {
		return len(a)
	}
	return 0
}

func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func curDir(base map[string]any) string {
	ws, _ := base["workspace"].(map[string]any)
	return str(ws["current_dir"])
}
