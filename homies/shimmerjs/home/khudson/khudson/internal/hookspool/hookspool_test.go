package hookspool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Unix(1_700_000_000, 0)

func noEnv(string) string { return "" }

func run(t *testing.T, dir, event, payload string, env func(string) string) {
	t.Helper()
	if err := Run(event, dir, strings.NewReader(payload), env, t0); err != nil {
		t.Fatalf("Run(%s): %v", event, err)
	}
}

func spool(t *testing.T, dir, sid string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, sid+".json"))
	if err != nil {
		t.Fatalf("spool read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("spool parse: %v", err)
	}
	return m
}

// prompt writes the turn fields, clears attention, and mirrors cwd into
// workspace.current_dir (the shape parseSpool reads).
func TestPromptMergesAndClearsAttention(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, EventNotify, `{"session_id":"s1","message":"waiting"}`, noEnv)
	run(t, dir, EventPrompt, `{"session_id":"s1","prompt":"do the thing","cwd":"/x/repo",
		"session_title":"tt","transcript_path":"/t/p.jsonl","permission_mode":"auto"}`, noEnv)
	m := spool(t, dir, "s1")
	if m["prompt"] != "do the thing" || m["attention"] != false {
		t.Fatalf("prompt merge = %+v", m)
	}
	if m["ts"] != float64(t0.Unix()) {
		t.Errorf("ts = %v", m["ts"])
	}
	ws, _ := m["workspace"].(map[string]any)
	if m["cwd"] != "/x/repo" || ws["current_dir"] != "/x/repo" {
		t.Errorf("cwd shape = %v / %v", m["cwd"], ws)
	}
	for k, want := range map[string]string{
		"session_title": "tt", "transcript_path": "/t/p.jsonl", "permission_mode": "auto",
	} {
		if m[k] != want {
			t.Errorf("%s = %v, want %s", k, m[k], want)
		}
	}
	// the earlier notification survives the merge (merge-don't-clobber)
	if m["notification"] != "waiting" {
		t.Errorf("notification lost in merge: %+v", m)
	}
}

// A missing or empty prompt still writes prompt:"" (the field is the
// panel's staleness anchor); absent optional fields stay absent.
func TestPromptOptionalFieldsAbsent(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, EventPrompt, `{"session_id":"s1"}`, noEnv)
	m := spool(t, dir, "s1")
	if m["prompt"] != "" {
		t.Errorf("prompt = %v", m["prompt"])
	}
	for _, k := range []string{"cwd", "workspace", "session_title", "transcript_path", "permission_mode"} {
		if _, ok := m[k]; ok {
			t.Errorf("%s present, want absent", k)
		}
	}
}

// start records birth fields, resolves the model object display_name > id,
// and plants KITTY_WINDOW_ID from env.
func TestStartFields(t *testing.T) {
	dir := t.TempDir()
	env := func(k string) string {
		if k == "KITTY_WINDOW_ID" {
			return "42"
		}
		return ""
	}
	run(t, dir, EventStart, `{"session_id":"s1","source":"resume",
		"model":{"display_name":"fable","id":"claude-fable-5"},"cwd":"/x"}`, env)
	m := spool(t, dir, "s1")
	if m["source"] != "resume" || m["model"] != "fable" || m["kitty_window_id"] != "42" {
		t.Fatalf("start merge = %+v", m)
	}
	if m["started_ts"] != float64(t0.Unix()) {
		t.Errorf("started_ts = %v", m["started_ts"])
	}
	// scalar model + defaulted source
	dir2 := t.TempDir()
	run(t, dir2, EventStart, `{"session_id":"s2","model":"claude-x"}`, noEnv)
	m2 := spool(t, dir2, "s2")
	if m2["source"] != "startup" || m2["model"] != "claude-x" {
		t.Fatalf("scalar start = %+v", m2)
	}
	if _, ok := m2["kitty_window_id"]; ok {
		t.Error("kitty_window_id present without env")
	}
}

// stop stamps the turn end, counts fleet arrays, trims last_assistant to
// its first line capped at 200 runes, lifts effort.level, and cures a
// prior StopFailure error.
func TestStopCuresErrorAndTrims(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, EventStopFail, `{"session_id":"s1","error":"rate_limit"}`, noEnv)
	if m := spool(t, dir, "s1"); m["error"] != "rate_limit" {
		t.Fatalf("stopfail = %+v", m)
	}
	long := strings.Repeat("x", 300) + "\nsecond line"
	run(t, dir, EventStop, `{"session_id":"s1",
		"background_tasks":[1,2],"session_crons":[],
		"last_assistant_message":`+string(mustJSON(long))+`,
		"effort":{"level":"xhigh"}}`, noEnv)
	m := spool(t, dir, "s1")
	if _, ok := m["error"]; ok {
		t.Error("clean Stop did not cure the StopFailure error")
	}
	if m["attention"] != false || m["stopped_ts"] != float64(t0.Unix()) {
		t.Errorf("stop stamp = %+v", m)
	}
	if m["bg_tasks"] != float64(2) || m["crons"] != float64(0) {
		t.Errorf("fleet counts = %v / %v", m["bg_tasks"], m["crons"])
	}
	la, _ := m["last_assistant"].(string)
	if len(la) != 200 || strings.Contains(la, "second") {
		t.Errorf("last_assistant = %d bytes %q...", len(la), la[:10])
	}
	if m["effort"] != "xhigh" {
		t.Errorf("effort = %v", m["effort"])
	}
}

// stopfail defaults a missing error to "error" -- the panel's outcome row
// needs a non-empty reason.
func TestStopFailDefaultsReason(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, EventStopFail, `{"session_id":"s1"}`, noEnv)
	if m := spool(t, dir, "s1"); m["error"] != "error" {
		t.Fatalf("stopfail = %+v", m)
	}
}

// notify sets attention + the typed fields; cwd backfills ONLY when the
// spool has none (a real prompt cwd must win).
func TestNotifyBackfillsCwdOnce(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, EventNotify, `{"session_id":"s1","message":"m","notification_type":"idle_prompt",
		"title":"T","cwd":"/from-notify"}`, noEnv)
	m := spool(t, dir, "s1")
	if m["attention"] != true || m["notification"] != "m" ||
		m["notification_type"] != "idle_prompt" || m["notification_title"] != "T" {
		t.Fatalf("notify merge = %+v", m)
	}
	ws, _ := m["workspace"].(map[string]any)
	if ws["current_dir"] != "/from-notify" {
		t.Fatalf("backfill missing: %+v", m)
	}
	run(t, dir, EventPrompt, `{"session_id":"s1","prompt":"p","cwd":"/from-prompt"}`, noEnv)
	run(t, dir, EventNotify, `{"session_id":"s1","message":"again","cwd":"/from-notify-2"}`, noEnv)
	m = spool(t, dir, "s1")
	ws, _ = m["workspace"].(map[string]any)
	if ws["current_dir"] != "/from-prompt" {
		t.Errorf("notify clobbered the prompt cwd: %+v", ws)
	}
}

// end: clear/logout drop the spool + agent sidecar; a normal quit keeps
// both; the 7d reaper sweeps dead spools and orphaned sidecars.
func TestEndRetentionAndReaper(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, EventPrompt, `{"session_id":"keep","prompt":"p"}`, noEnv)
	run(t, dir, EventPrompt, `{"session_id":"gone","prompt":"p"}`, noEnv)
	if err := os.MkdirAll(filepath.Join(dir, "gone.agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "orphan.agents"), 0o700); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(dir, "dead.json")
	if err := os.WriteFile(dead, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := t0.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(dead, old, old); err != nil {
		t.Fatal(err)
	}

	run(t, dir, EventEnd, `{"session_id":"keep","reason":"prompt_input_exit"}`, noEnv)
	if _, err := os.Stat(filepath.Join(dir, "keep.json")); err != nil {
		t.Fatal("normal quit deleted the spool (kills tap-to-resume)")
	}
	run(t, dir, EventEnd, `{"session_id":"gone","reason":"clear"}`, noEnv)
	if _, err := os.Stat(filepath.Join(dir, "gone.json")); err == nil {
		t.Error("clear kept the spool")
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.agents")); err == nil {
		t.Error("clear kept the agent sidecar")
	}
	if _, err := os.Stat(dead); err == nil {
		t.Error("reaper kept an 8d-dead spool")
	}
	if _, err := os.Stat(filepath.Join(dir, "orphan.agents")); err == nil {
		t.Error("reaper kept an orphaned sidecar")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep.json")); err != nil {
		t.Error("reaper ate a fresh spool")
	}
}

// Every write stamps the current shape version; a legacy stampless file
// gains it on the next merge without losing its fields.
func TestVersionStamp(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, EventPrompt, `{"session_id":"s1","prompt":"p"}`, noEnv)
	if m := spool(t, dir, "s1"); m["spool_version"] != float64(Version) {
		t.Fatalf("spool_version = %v, want %d", m["spool_version"], Version)
	}
	legacy := filepath.Join(dir, "s2.json")
	if err := os.WriteFile(legacy, []byte(`{"session_id":"s2","prompt":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, EventStop, `{"session_id":"s2"}`, noEnv)
	m := spool(t, dir, "s2")
	if m["spool_version"] != float64(Version) {
		t.Errorf("legacy file not stamped on merge: %+v", m)
	}
	if m["prompt"] != "old" {
		t.Errorf("merge clobbered legacy fields: %+v", m)
	}
}

// Sweep prunes by age and by foreign version stamp: fresh current-version
// and fresh legacy (unstamped) spools survive; aged spools of any version
// and fresh foreign-stamp spools go, orphaned sidecars with them.
func TestSweep(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, EventPrompt, `{"session_id":"live","prompt":"p"}`, noEnv)
	write := func(name, body string, mtime time.Time) {
		t.Helper()
		f := filepath.Join(dir, name)
		if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	old := t0.Add(-8 * 24 * time.Hour)
	write("legacy-fresh.json", `{"session_id":"legacy-fresh"}`, t0.Add(-time.Hour))
	write("legacy-old.json", `{"session_id":"legacy-old"}`, old)
	write("foreign.json", fmt.Sprintf(`{"session_id":"foreign","spool_version":%d}`, Version+1), t0.Add(-time.Minute))
	write("current-old.json", fmt.Sprintf(`{"session_id":"current-old","spool_version":%d}`, Version), old)
	for _, d := range []string{"live.agents", "foreign.agents"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	Sweep(dir, t0)

	for _, keep := range []string{"live.json", "legacy-fresh.json", "live.agents"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Errorf("Sweep removed %s", keep)
		}
	}
	for _, gone := range []string{"legacy-old.json", "foreign.json", "current-old.json", "foreign.agents"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); err == nil {
			t.Errorf("Sweep kept %s", gone)
		}
	}
}

// Corrupt spool files start from {}; sid-less or malformed payloads are
// silent no-ops; unknown events fail loudly; a path-traversal sid is
// refused.
func TestGuards(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "s1.json")
	if err := os.WriteFile(f, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, dir, EventPrompt, `{"session_id":"s1","prompt":"p"}`, noEnv)
	if m := spool(t, dir, "s1"); m["prompt"] != "p" {
		t.Fatalf("corrupt base not reset: %+v", m)
	}

	run(t, dir, EventPrompt, `{"prompt":"no sid"}`, noEnv)
	run(t, dir, EventPrompt, `not json at all`, noEnv)
	run(t, dir, EventPrompt, `{"session_id":"../evil","prompt":"p"}`, noEnv)
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "evil.json")); err == nil {
		t.Fatal("path-traversal sid escaped the spool dir")
	}
	if err := Run("bogus", dir, strings.NewReader(`{"session_id":"s1"}`), noEnv, t0); err == nil {
		t.Error("unknown event did not fail loudly")
	}
}

func mustJSON(s string) []byte {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return b
}
