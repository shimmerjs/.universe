package claudesessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

const fixtureFull = `{
  "session_id": "abc123",
  "model": {"display_name": "Opus 4.8"},
  "workspace": {"current_dir": "/Users/x/dev/foo"},
  "cost": {"total_cost_usd": 1.234, "total_lines_added": 10, "total_lines_removed": 2}
}`

const fixtureNamed = `{
  "session_id": "abc123",
  "session_name": "spoolname",
  "model": {"display_name": "Opus 4.8"},
  "workspace": {"current_dir": "/Users/x/dev/foo"},
  "cost": {"total_cost_usd": 1.234},
  "context_window": {"used_percentage": 43.4}
}`

const fixtureSparse = `{"session_id": "def456"}`

const fixtureMalformed = `{"session_id": "trunc`

// fixtureHooked is a spool file as the full hook set leaves it: prompt + ts
// (UserPromptSubmit), attention + notification fields (Notification), and
// stopped_ts (Stop).
const fixtureHooked = `{
  "session_id": "abc123",
  "prompt": "spool prompt\nsecond line",
  "ts": 1751000000,
  "cwd": "/Users/x/dev/foo",
  "workspace": {"current_dir": "/Users/x/dev/foo"},
  "attention": true,
  "notification": "Claude needs your permission to use Bash",
  "notification_ts": 1751000300,
  "stopped_ts": 1751000200
}`

// fixtureTranscript exercises the shapes lastPrompt must survive: string
// and content-array prompts, tool_result-only user entries, assistant
// entries, and a garbage line.
const fixtureTranscript = `{"type":"user","message":{"content":"older prompt"}}
{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"the answer\nsecond line"}]}}
{"type":"user","message":{"content":"do the thing\nwith details"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"x","name":"Bash","input":{}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"x","content":"out"}]}}
not json at all
`

// regFixtureStart is the pinned process start time fixture records carry
// as startedAt; TestMain pins the procStartTime seam to the same instant,
// so the regAlive identity check passes deterministically (no real sysctl
// in unit tests -- the nix sandbox must not decide liveness).
var regFixtureStart = time.UnixMilli(1_700_000_000_000)

func TestMain(m *testing.M) {
	realProcStart := procStartTime
	procStartTime = func(int) (time.Time, bool) { return regFixtureStart, true }
	// the env-gated live test needs the real seam back (liveSeams)
	sysctlProcStartReal = realProcStart
	os.Exit(m.Run())
}

// sysctlProcStartReal restores the real identity probe for live-gated
// tests that render against the actual host registry.
var sysctlProcStartReal func(int) (time.Time, bool)

func regRecord(id, name, cwd string, updatedAt int64) string {
	return fmt.Sprintf(`{"sessionId":%q,"pid":%d,"name":%q,"cwd":%q,"startedAt":%d,"updatedAt":%d,"status":"busy"}`,
		id, os.Getpid(), name, cwd, regFixtureStart.UnixMilli(), updatedAt)
}

// regStatusRecord is a registry record with an explicit status/waitingFor;
// the pid is the test process and startedAt matches the pinned identity
// seam, so the discover live gate passes.
func regStatusRecord(id, status, waitingFor string, statusUpdatedAt int64) string {
	return fmt.Sprintf(`{"sessionId":%q,"pid":%d,"status":%q,"waitingFor":%q,"startedAt":%d,"updatedAt":1,"statusUpdatedAt":%d}`,
		id, os.Getpid(), status, waitingFor, regFixtureStart.UnixMilli(), statusUpdatedAt)
}

// regLive registers id as a live session with a NEUTRAL status ("idle"):
// it passes the live-registry gate without touching any other signal --
// tone stays honest to file mtimes (busy would force active), needs-user
// stays false, and nothing escapes to the spool heuristic.
func regLive(t *testing.T, sessionsDir, id string) {
	t.Helper()
	touch(t, filepath.Join(sessionsDir, id+".json"), regStatusRecord(id, "idle", "", 0), time.Now())
}

// tsEntry is one transcript head line carrying a start timestamp.
func tsEntry(ts string) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"hi"}}`, ts)
}

// lineText joins a spans row's texts for shape assertions.
func lineText(r module.Row) string {
	var b strings.Builder
	for _, s := range r.Spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

// Static-left span layout: age, state, agents, workflows, the fixed-width
// cwd column, then the variable-length identifier.
const (
	spanAge       = 0
	spanState     = 1
	spanAgents    = 2
	spanWorkflows = 3
	spanCwd       = 4
	spanName      = 5
)

// rowName extracts the identifier from the fixed static-left layout.
func rowName(r module.Row) string { return strings.TrimSpace(r.Spans[spanName].Text) }

// rowIdent is the name span's session id -- the identity discriminator now
// that idless sessions all display "-".
func rowIdent(r module.Row) string { return r.Spans[spanName].Ident }

// staticPrefix joins the fixed-width columns left of the identifier.
func staticPrefix(r module.Row) string {
	var b strings.Builder
	for _, s := range r.Spans[:spanName] {
		b.WriteString(s.Text)
	}
	return b.String()
}

func TestParseSpool(t *testing.T) {
	s, err := parseSpool([]byte(fixtureFull))
	if err != nil {
		t.Fatalf("parseSpool(full): %v", err)
	}
	// model/cost/ctx keys ride real payloads; only dir and name decode
	if s.dir != "/Users/x/dev/foo" || s.name != "" {
		t.Errorf("parseSpool(full) = %+v, want dir only", s)
	}

	s, err = parseSpool([]byte(fixtureNamed))
	if err != nil {
		t.Fatalf("parseSpool(named): %v", err)
	}
	if s.name != "spoolname" || s.dir != "/Users/x/dev/foo" {
		t.Errorf("parseSpool(named) = %+v", s)
	}

	s, err = parseSpool([]byte(fixtureSparse))
	if err != nil {
		t.Fatalf("parseSpool(sparse): %v", err)
	}
	if s.dir != "" || s.name != "" || s.prompt != "" || s.attention ||
		!s.promptTS.IsZero() || !s.stopped.IsZero() || !s.notified.IsZero() {
		t.Errorf("parseSpool(sparse) = %+v, want zero fields", s)
	}

	if _, err = parseSpool([]byte(fixtureMalformed)); err == nil {
		t.Error("parseSpool(malformed): want error, got nil")
	}
}

// fixtureRank12 carries the rank-1/2 hook fields (typed notification,
// Stop/StopFailure detail, SessionStart identity).
const fixtureRank12 = `{
  "session_id": "abc123",
  "session_title": "spike the panel",
  "attention": true,
  "notification": "msg body",
  "notification_ts": 1751000300,
  "notification_type": "permission_prompt",
  "notification_title": "needs Bash",
  "stopped_ts": 1751000200,
  "last_assistant": "all green\nsecond line",
  "effort": "xhigh",
  "error": "rate_limit",
  "bg_tasks": 2,
  "crons": 1,
  "model": "Opus 4.8",
  "source": "resume",
  "started_ts": 1751000000,
  "kitty_window_id": "12"
}`

func TestParseSpoolRank12Fields(t *testing.T) {
	s, err := parseSpool([]byte(fixtureRank12))
	if err != nil {
		t.Fatalf("parseSpool(rank12): %v", err)
	}
	if s.sessionTitle != "spike the panel" {
		t.Errorf("sessionTitle = %q", s.sessionTitle)
	}
	if s.notification != "msg body" || s.notifType != "permission_prompt" || s.notifTitle != "needs Bash" {
		t.Errorf("notification fields = %q %q %q", s.notification, s.notifType, s.notifTitle)
	}
	if s.lastAssistant != "all green" {
		t.Errorf("lastAssistant = %q, want the first line only", s.lastAssistant)
	}
	if s.effort != "xhigh" || s.errMsg != "rate_limit" || s.model != "Opus 4.8" {
		t.Errorf("effort/error/model = %q %q %q", s.effort, s.errMsg, s.model)
	}
	if s.bgTasks != 2 || s.crons != 1 {
		t.Errorf("bgTasks/crons = %d/%d", s.bgTasks, s.crons)
	}
	// statusline-era object model shape still decodes
	s, err = parseSpool([]byte(fixtureNamed))
	if err != nil {
		t.Fatalf("parseSpool(named): %v", err)
	}
	if s.model != "Opus 4.8" {
		t.Errorf("model(object shape) = %q, want display_name", s.model)
	}
}

// session_title keys the row when no registry/spool name exists; explicit
// names still win.
func TestKeySessionTitleFallback(t *testing.T) {
	s := session{id: "aaaaaaaa-1111", sessionTitle: "titled"}
	if got := s.key(); got != "titled" {
		t.Errorf("key = %q, want session_title fallback", got)
	}
	s.name = "named"
	if got := s.key(); got != "named" {
		t.Errorf("key = %q, want the name over session_title", got)
	}
}

func TestParseSpoolHookFields(t *testing.T) {
	s, err := parseSpool([]byte(fixtureHooked))
	if err != nil {
		t.Fatalf("parseSpool(hooked): %v", err)
	}
	if s.prompt != "spool prompt" {
		t.Errorf("prompt = %q, want the first line of the spool prompt", s.prompt)
	}
	if !s.attention {
		t.Error("attention = false, want the spool flag decoded")
	}
	if !s.promptTS.Equal(time.Unix(1751000000, 0)) {
		t.Errorf("promptTS = %v, want ts decoded", s.promptTS)
	}
	if !s.stopped.Equal(time.Unix(1751000200, 0)) {
		t.Errorf("stopped = %v, want stopped_ts decoded", s.stopped)
	}
	if !s.notified.Equal(time.Unix(1751000300, 0)) {
		t.Errorf("notified = %v, want notification_ts decoded", s.notified)
	}
	if s.dir != "/Users/x/dev/foo" {
		t.Errorf("dir = %q", s.dir)
	}
}

// touch writes a file and pins its mtime.
func touch(t *testing.T, path, body string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// touchDir creates a dir and pins its mtime.
func touchDir(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// lastPrompt is lastPromptEntry's text-only form.
func lastPrompt(path string) string {
	text, _, _ := lastPromptEntry(path)
	return text
}

func TestLastPrompt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(p, []byte(fixtureTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	if prompt := lastPrompt(p); prompt != "do the thing" {
		t.Errorf("prompt = %q, want first line of last real user text", prompt)
	}
}

func TestLastPromptArrayPrompt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	body := `{"type":"user","message":{"content":[{"type":"text","text":"array prompt"},{"type":"tool_result","content":"x"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if prompt := lastPrompt(p); prompt != "array prompt" {
		t.Errorf("prompt = %q, want text block of content array", prompt)
	}
}

func TestLastPromptSkipsEnvelopesAndSidechain(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	body := `{"type":"user","message":{"content":"real prompt"}}
{"type":"user","message":{"content":"<command-name>/model</command-name>"}}
{"type":"user","message":{"content":"<local-command-stdout>ok</local-command-stdout>"}}
{"type":"user","message":{"content":"<task-notification>agent done</task-notification>"}}
{"type":"user","isSidechain":true,"message":{"content":"subagent prompt"}}
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if prompt := lastPrompt(p); prompt != "real prompt" {
		t.Errorf("prompt = %q, want envelope and sidechain entries skipped", prompt)
	}
}

func TestLastPromptToolResultOnlyTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	body := `{"type":"user","message":{"content":[{"type":"tool_result","content":"out"}]}}` + "\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if prompt := lastPrompt(p); prompt != "" {
		t.Errorf("prompt = %q, want empty for tool_result-only tail", prompt)
	}
}

func TestLastPromptMissingAndCorrupt(t *testing.T) {
	if prompt := lastPrompt(filepath.Join(t.TempDir(), "nope.jsonl")); prompt != "" {
		t.Errorf("lastPrompt(missing) = %q", prompt)
	}
	if prompt := lastPrompt(""); prompt != "" {
		t.Errorf("lastPrompt(empty path) = %q", prompt)
	}
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if prompt := lastPrompt(empty); prompt != "" {
		t.Errorf("lastPrompt(empty file) = %q", prompt)
	}
	corrupt := filepath.Join(dir, "corrupt.jsonl")
	if err := os.WriteFile(corrupt, []byte("{{{{\nnope\n\x00\x01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if prompt := lastPrompt(corrupt); prompt != "" {
		t.Errorf("lastPrompt(corrupt) = %q", prompt)
	}
}

// lastPromptEntry: text + entry timestamp (zero when absent/unparseable)
// + ok, over the shapes the tail must survive. Steering messages arrive
// wrapped in harness envelopes, so extraction (not rejection) keeps them.
func TestLastPromptEntry(t *testing.T) {
	ts := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		body string
		want string
		ts   time.Time
		ok   bool
	}{
		{"plain string with timestamp",
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"steer the panel"}}` + "\n",
			"steer the panel", ts, true},
		{"array content",
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":[{"type":"text","text":"array prompt"},{"type":"tool_result","content":"x"}]}}` + "\n",
			"array prompt", ts, true},
		{"tool_result-only entry",
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":[{"type":"tool_result","content":"out"}]}}` + "\n",
			"", time.Time{}, false},
		{"caveat-wrapped steering text extracted",
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"<system-reminder>\nmid-turn note\n</system-reminder>\nuse the tail instead"}}` + "\n",
			"use the tail instead", ts, true},
		{"machinery-only entry skipped for the prior prompt",
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"older prompt"}}` + "\n" +
				`{"type":"user","timestamp":"2026-07-07T11:00:00Z","message":{"content":"<task-notification>agent done</task-notification>"}}` + "\n",
			"older prompt", ts, true},
		{"unparseable garbage",
			"{{{{\nnope\n\x00\x01\n",
			"", time.Time{}, false},
		// a foreign or nested close tag inside an envelope body must
		// never tear the span and leak machinery
		{"html close tag inside stdout wrapper does not tear",
			`{"type":"user","timestamp":"2026-07-07T09:00:00Z","message":{"content":"real ask"}}` + "\n" +
				`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"<local-command-stdout><p>rest of page</p></local-command-stdout>"}}` + "\n",
			"real ask", time.Date(2026, 7, 7, 9, 0, 0, 0, time.UTC), true},
		{"quoted close tag inside a reminder does not tear",
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"<system-reminder>never emit </result> blocks</system-reminder>"}}` + "\n",
			"", time.Time{}, false},
		{"nested tags cut at the matching close",
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"<outer-tag><inner>z</inner> </outer-tag>\ntyped after"}}` + "\n",
			"typed after", ts, true},
		{"ansi control bytes scrubbed from the surviving line",
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"steer \u001b[31mred\u001b[m now"}}` + "\n",
			"steer [31mred[m now", ts, true},
		{"missing timestamp yields zero ts",
			`{"type":"user","message":{"content":"untimed prompt"}}` + "\n",
			"untimed prompt", time.Time{}, true},
		{"unparseable timestamp keeps the text",
			`{"type":"user","timestamp":42,"message":{"content":"badly timed"}}` + "\n",
			"badly timed", time.Time{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "t.jsonl")
			if err := os.WriteFile(p, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			text, gotTS, ok := lastPromptEntry(p)
			if text != tt.want || !gotTS.Equal(tt.ts) || ok != tt.ok {
				t.Errorf("lastPromptEntry = (%q, %v, %v), want (%q, %v, %v)",
					text, gotTS, ok, tt.want, tt.ts, tt.ok)
			}
		})
	}
}

func TestLastPromptTailWindow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	// the old prompt sits beyond tailBytes of padding, so only the new
	// one is visible; the torn first line must not confuse the scan.
	pad := `{"type":"padding","message":{"content":"` + strings.Repeat("x", 1024) + `"}}` + "\n"
	var b strings.Builder
	b.WriteString(`{"type":"user","message":{"content":"beyond the tail"}}` + "\n")
	for b.Len() < tailBytes+len(pad) {
		b.WriteString(pad)
	}
	b.WriteString(`{"type":"user","message":{"content":"inside the tail"}}` + "\n")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if prompt := lastPrompt(p); prompt != "inside the tail" {
		t.Errorf("prompt = %q, want only the in-tail prompt", prompt)
	}
}

func TestStartTime(t *testing.T) {
	dir := t.TempDir()
	timed := filepath.Join(dir, "timed.jsonl")
	touch(t, timed, tsEntry("2026-07-03T10:00:00.000Z")+"\n", time.Now())
	want := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	if got := startTime(timed); !got.Equal(want) {
		t.Errorf("startTime(timed) = %v, want %v", got, want)
	}
	// a timestamp-free first line (summary) must not hide a timestamped
	// second entry
	late := filepath.Join(dir, "late.jsonl")
	touch(t, late, `{"type":"summary","summary":"x"}`+"\n"+tsEntry("2026-07-03T11:00:00Z")+"\n", time.Now())
	if got := startTime(late); !got.Equal(want.Add(time.Hour)) {
		t.Errorf("startTime(late) = %v, want %v", got, want.Add(time.Hour))
	}
	bare := filepath.Join(dir, "bare.jsonl")
	touch(t, bare, "{}\n", time.Now())
	if got := startTime(bare); !got.IsZero() {
		t.Errorf("startTime(bare) = %v, want zero", got)
	}
	if got := startTime(filepath.Join(dir, "nope.jsonl")); !got.IsZero() {
		t.Errorf("startTime(missing) = %v, want zero", got)
	}
}

func TestRenderEmpty(t *testing.T) {
	title, rows := render(nil, defaultMax, time.Now())
	if title != "claude" {
		t.Errorf("title = %q, want bare claude", title)
	}
	if len(rows) != 1 || rows[0].Text != "no active sessions" || rows[0].Style != module.StyleDim {
		t.Errorf("render(nil) = %+v", rows)
	}
}

func TestRenderOneLine(t *testing.T) {
	t.Setenv("HOME", "/Users/x")
	now := time.Now()
	stale := now.Add(-2 * time.Hour)
	title, rows := render([]session{
		{id: "bbbbbbbb-live", dir: "/Users/x/dev/foo",
			mtime: now, agents: 1, workflows: 2, prompt: "fix the bug"},
		{id: "aaaaaaaa-old", mtime: stale},
	}, defaultMax, now)
	if title != "claude 1/2" {
		t.Errorf("title = %q, want live/recent tally", title)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want one row per session, no header", rows)
	}
	live := rows[0]
	if live.Kind != module.RowSpans || live.Style != module.StyleAccent {
		t.Fatalf("live row = %+v", live)
	}
	wantSpans := []module.Span{
		{Text: " 0s", Style: module.StyleAccent},
		{Text: "  ", Style: module.StyleDim},
		{Text: " \uf013 1", Style: module.StyleHighlight},
		{Text: " \uf0e8 2", Style: module.StyleHighlight},
		{Text: fmt.Sprintf(" %-*s", cwdWidth, "~/dev/foo"), Style: module.StyleDim},
		{Text: " foo", Style: module.StyleTitle, Ident: "bbbbbbbb-live"},
		{Text: " > fix the bug", Style: module.StyleDim},
	}
	if len(live.Spans) != len(wantSpans) {
		t.Fatalf("live spans = %+v, want %+v", live.Spans, wantSpans)
	}
	for i, want := range wantSpans {
		if live.Spans[i] != want {
			t.Errorf("live span %d = %+v, want %+v", i, live.Spans[i], want)
		}
	}
	staleRow := rows[1]
	if staleRow.Kind != module.RowSpans || staleRow.Style != module.StyleDim {
		t.Fatalf("stale row = %+v", staleRow)
	}
	if len(staleRow.Spans) != spanName+1 {
		t.Fatalf("stale spans = %+v, want the static columns + name only", staleRow.Spans)
	}
	if staleRow.Spans[spanAge] != (module.Span{Text: " 2h", Style: module.StyleDim}) {
		t.Errorf("stale age span = %+v", staleRow.Spans[spanAge])
	}
	if staleRow.Spans[spanAgents].Text != "    " || staleRow.Spans[spanWorkflows].Text != "    " {
		t.Errorf("stale fleet spans = %+v, want blanked fixed-width columns", staleRow.Spans)
	}
	if staleRow.Spans[spanCwd].Text != strings.Repeat(" ", cwdWidth+1) {
		t.Errorf("stale cwd span = %+v, want a blank full-width column", staleRow.Spans[spanCwd])
	}
	if staleRow.Spans[spanName] != (module.Span{Text: " -", Style: module.StyleTitle, Ident: "aaaaaaaa-old"}) {
		t.Errorf("stale name span = %+v", staleRow.Spans[spanName])
	}
}

func TestRenderGlyphPairsOmittedAtZero(t *testing.T) {
	now := time.Now()
	_, rows := render([]session{{id: "aaaaaaaa-x", mtime: now}}, defaultMax, now)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	text := lineText(rows[0])
	if strings.Contains(text, "\uf013") || strings.Contains(text, "\uf0e8") {
		t.Errorf("line = %q, want no fleet glyphs at zero", text)
	}
	for _, s := range rows[0].Spans {
		if s.Style == module.StyleHighlight {
			t.Errorf("highlight span survived an empty fleet: %+v", s)
		}
	}
}

// The static columns (age, state, fleet counts, cwd) are fixed-width and
// sit LEFT of the identifier, so a long name, a missing fleet, a wide age,
// or a short (or absent) cwd cannot shift any other row's identifier
// column.
func TestRenderStaticLeftShape(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("n", 40)
	_, rows := render([]session{
		{id: "aaaaaaaa-x", name: "ab", mtime: now},
		{id: "bbbbbbbb-x", name: long, dir: "/x/foo", mtime: now.Add(-15 * time.Second), agents: 12, workflows: 3},
		{id: "cccccccc-x", mtime: now.Add(-3 * time.Hour), attention: true},
	}, defaultMax, now)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	want := len([]rune(staticPrefix(rows[0])))
	for i, r := range rows {
		if got := len([]rune(staticPrefix(r))); got != want {
			t.Errorf("row %d static prefix = %d runes, want %d (%q)", i, got, want, staticPrefix(r))
		}
	}
	// the identifier is variable-length: long names render whole, never
	// truncated into a fixed column
	if got := rows[1].Spans[spanName].Text; got != " "+long {
		t.Errorf("long name = %q, want untruncated", got)
	}
}

func TestRelTime(t *testing.T) {
	for _, tt := range []struct {
		d    time.Duration
		want string
	}{
		{-5 * time.Second, "0s"},
		{0, "0s"},
		{15 * time.Second, "15s"},
		{59 * time.Second, "59s"},
		{60 * time.Second, "1m"},
		{3 * time.Minute, "3m"},
		{59*time.Minute + 59*time.Second, "59m"},
		{time.Hour, "1h"},
		{2 * time.Hour, "2h"},
		{23 * time.Hour, "23h"},
		{24 * time.Hour, "1d"},
		{72 * time.Hour, "3d"},
	} {
		if got := relTime(tt.d); got != tt.want {
			t.Errorf("relTime(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

// The age column renders the compact relative scheme against an injected
// now, right-aligned to the fixed width.
func TestRenderRelativeAgeColumn(t *testing.T) {
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	_, rows := render([]session{
		{id: "aaaaaaaa-x", mtime: now.Add(-15 * time.Second)},
		{id: "bbbbbbbb-x", mtime: now.Add(-3 * time.Minute)},
		{id: "cccccccc-x", mtime: now.Add(-2 * time.Hour)},
		{id: "dddddddd-x", mtime: now.Add(-3 * 24 * time.Hour)},
	}, defaultMax, now)
	want := []string{"15s", " 3m", " 2h", " 3d"}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for i, w := range want {
		if got := rows[i].Spans[spanAge].Text; got != w {
			t.Errorf("row %d age = %q, want %q", i, got, w)
		}
	}
}

func TestRenderAttentionGlyph(t *testing.T) {
	now := time.Now()
	_, rows := render([]session{
		// attention outranks a recorded turn completion
		{id: "aaaaaaaa-x", mtime: now, attention: true, stopped: now},
		{id: "bbbbbbbb-x", mtime: now},
	}, defaultMax, now)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if got := rows[0].Spans[spanState]; got != (module.Span{Text: " \uf0f3", Style: module.StyleWarn}) {
		t.Errorf("attention state span = %+v, want warn bell", got)
	}
	if got := rows[1].Spans[spanState]; got != (module.Span{Text: "  ", Style: module.StyleDim}) {
		t.Errorf("plain state span = %+v, want blank", got)
	}
}

func TestRenderTurnCompletionGlyph(t *testing.T) {
	now := time.Now()
	_, rows := render([]session{
		// stopped at/after the last prompt: turn complete
		{id: "aaaaaaaa-x", mtime: now, promptTS: now.Add(-time.Minute), stopped: now},
		// prompt newer than the stop: turn in flight
		{id: "bbbbbbbb-x", mtime: now, promptTS: now, stopped: now.Add(-time.Minute)},
		// stop with no prompt on record still reads complete
		{id: "cccccccc-x", mtime: now, stopped: now},
	}, defaultMax, now)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	done := module.Span{Text: " \uf00c", Style: module.StyleDim}
	blank := module.Span{Text: "  ", Style: module.StyleDim}
	if got := rows[0].Spans[spanState]; got != done {
		t.Errorf("completed state span = %+v, want check", got)
	}
	if got := rows[1].Spans[spanState]; got != blank {
		t.Errorf("in-flight state span = %+v, want blank", got)
	}
	if got := rows[2].Spans[spanState]; got != done {
		t.Errorf("promptless-stop state span = %+v, want check", got)
	}
}

func TestCompactPath(t *testing.T) {
	t.Setenv("HOME", "/Users/x")
	for _, tt := range []struct{ in, want string }{
		{"/Users/x/dev/foo", "~/dev/foo"},
		{"/Users/x", "~"},
		{"/Users/xy/dev", "/Users/xy/dev"},
		{"/opt/src", "/opt/src"},
	} {
		if got := compactPath(tt.in); got != tt.want {
			t.Errorf("compactPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestAbbrevPath(t *testing.T) {
	for _, tt := range []struct {
		in     string
		budget int
		want   string
	}{
		// under budget: unchanged
		{"~/dev/foo", 20, "~/dev/foo"},
		// exact budget: passthrough, no abbreviation
		{"~/dev/foo/khudsonxx", 19, "~/dev/foo/khudsonxx"},
		// fish abbreviation: leading segments collapse to one rune
		{"~/code/foo/khudson", 14, "~/c/f/khudson"},
		// absolute paths keep the leading slash
		{"/opt/src/foo/khudson", 15, "/o/s/f/khudson"},
		// still over budget: leading segments drop behind "...", longest
		// fitting tail kept, last segment intact
		{"~/aaaa/bbbb/cccc/khudson", 13, ".../c/khudson"},
		{"~/aaaa/bbbb/cccc/khudson", 12, ".../khudson"},
		// even ".../"+last overflows: hard rune truncate to budget
		{"~/aaaa/" + strings.Repeat("k", 30), 12, ".../kkkkk..."},
	} {
		if got := abbrevPath(tt.in, tt.budget); got != tt.want {
			t.Errorf("abbrevPath(%q, %d) = %q, want %q", tt.in, tt.budget, got, tt.want)
		}
	}
	// composed with compactPath: home substitution feeds the budget
	t.Setenv("HOME", "/Users/x")
	if got := abbrevPath(compactPath("/Users/x/code/foo/khudson"), 14); got != "~/c/f/khudson" {
		t.Errorf("abbrevPath(compactPath) = %q, want %q", got, "~/c/f/khudson")
	}
}

func TestRenderTruncation(t *testing.T) {
	now := time.Now()
	long := strings.Repeat("a", 100)
	_, rows := render([]session{{id: "aaaaaaaa-x", mtime: now, prompt: long}}, defaultMax, now)
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	last := rows[0].Spans[len(rows[0].Spans)-1]
	want := " > " + strings.Repeat("a", lineWidth-3) + "..."
	if last.Text != want || last.Style != module.StyleDim {
		t.Errorf("prompt span = %+v, want %q dim", last, want)
	}
}

func TestRenderCap(t *testing.T) {
	now := time.Now()
	var sessions []session
	for i := range 11 {
		sessions = append(sessions, session{
			id:    fmt.Sprintf("%08d-cap", i),
			mtime: now.Add(-time.Duration(i+2) * time.Minute),
		})
	}
	_, rows := render(sessions, defaultMax, now)
	if len(rows) != defaultMax+1 {
		t.Fatalf("len(rows) = %d, want %d", len(rows), defaultMax+1)
	}
	last := rows[len(rows)-1]
	if want := fmt.Sprintf("+%d more", 11-defaultMax); last.Text != want || last.Style != module.StyleDim {
		t.Errorf("last row = %+v, want %q", last, want)
	}
	// the visible set is the caller's fixed order, not recency
	if got := rowIdent(rows[0]); !strings.HasPrefix(got, "00000000") {
		t.Errorf("first row ident = %q, want the first session in given order", got)
	}
}

func TestKeyResolutionOrder(t *testing.T) {
	for _, tc := range []struct {
		s    session
		want string
	}{
		{session{id: "aaaaaaaa-1111", name: "regname", dir: "/x/foo"}, "regname"},
		// registry kebab names render verbatim
		{session{id: "aaaaaaaa-1111", name: "fix-the-panel", dir: "/x/foo"}, "fix-the-panel"},
		{session{id: "aaaaaaaa-1111", dir: "/x/foo"}, "foo"},
		{session{id: "aaaaaaaa-1111"}, "-"}, // raw ids are noise, not context
	} {
		if got := tc.s.key(); got != tc.want {
			t.Errorf("key(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
}

// The name span's hue keys off the session id, not the displayed key: a
// registry name appearing mid-session cannot flap the color.
func TestRenderHueStableAcrossNaming(t *testing.T) {
	now := time.Now()
	_, unnamed := render([]session{{id: "aaaaaaaa-1111", mtime: now}}, defaultMax, now)
	_, named := render([]session{{id: "aaaaaaaa-1111", name: "fix-the-panel", mtime: now}}, defaultMax, now)
	i1 := unnamed[0].Spans[spanName].Ident
	i2 := named[0].Spans[spanName].Ident
	if i1 != "aaaaaaaa-1111" || i2 != i1 {
		t.Errorf("Idents = %q / %q, want the session id in both", i1, i2)
	}
	if rowName(unnamed[0]) != "-" || rowName(named[0]) != "fix-the-panel" {
		t.Errorf("names = %q / %q, want the placeholder then the kebab name",
			rowName(unnamed[0]), rowName(named[0]))
	}
}

func TestReadNames(t *testing.T) {
	dir := t.TempDir()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	touch(t, filepath.Join(dir, "100.json"), regRecord(id, "stale-name", "/x/old", 1), time.Now())
	touch(t, filepath.Join(dir, "200.json"), regRecord(id, "fresh-name", "/x/new", 2), time.Now())
	touch(t, filepath.Join(dir, "300.json"), `{"pid":300`, time.Now())
	touch(t, filepath.Join(dir, "400.json"), `{"pid":400,"name":"no-session-id"}`, time.Now())
	// pid-less record: the filename stem stands in
	other := "bbbbbbbb-1111-2222-3333-444444444444"
	touch(t, filepath.Join(dir, "500.json"),
		fmt.Sprintf(`{"sessionId":%q,"status":"waiting","waitingFor":"permission prompt","nameSource":"derived","updatedAt":5,"statusUpdatedAt":123}`, other),
		time.Now())

	m, err := readNames(dir)
	if err != nil {
		t.Fatalf("readNames: %v", err)
	}
	if len(m) != 2 || m[id].Name != "fresh-name" || m[id].Cwd != "/x/new" {
		t.Errorf("readNames = %+v, want newest updatedAt to win", m)
	}
	if m[id].PID != os.Getpid() || m[id].Status != "busy" {
		t.Errorf("readNames[%s] = %+v, want the record's pid and status", id, m[id])
	}
	if r := m[other]; r.PID != 500 || r.Status != "waiting" || r.WaitingFor != "permission prompt" ||
		r.NameSource != "derived" || r.StatusUpdatedAt != 123 {
		t.Errorf("readNames[%s] = %+v, want pid from the filename stem and the status fields", other, r)
	}
	// a missing dir is the legit no-registry state, not an error
	if m, err := readNames(filepath.Join(dir, "nope")); err != nil || len(m) != 0 {
		t.Errorf("readNames(missing) = %+v, %v, want empty and no error", m, err)
	}
	if m, err := readNames(""); err != nil || len(m) != 0 {
		t.Errorf("readNames(\"\") = %+v, %v, want empty and no error", m, err)
	}
	// any other ReadDir failure must ERROR: the registry gates visibility,
	// so a faulted read must not render as "no active sessions"
	if _, err := readNames(filepath.Join(dir, "100.json")); err == nil {
		t.Error("readNames(non-dir) = nil error, want the fault surfaced")
	}
}

func TestPollDiscoveryFleetAndOverlay(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	now := time.Now()
	liveID := "aaaaaaaa-1111-2222-3333-444444444444"
	recentID := "bbbbbbbb-1111-2222-3333-444444444444"

	// starts pin group order: the live session's cwd group (spool dir)
	// starts after the dir-less recent one, so it leads
	pa := filepath.Join(projects, "-Users-x-dev-foo")
	touch(t, filepath.Join(pa, liveID+".jsonl"), tsEntry("2026-07-03T11:00:00Z")+"\n", now)
	sess := filepath.Join(pa, liveID)
	touch(t, filepath.Join(sess, "subagents", "agent-live.jsonl"), "{}", now)
	touch(t, filepath.Join(sess, "subagents", "agent-done.jsonl"), "{}", now.Add(-5*time.Minute))
	touch(t, filepath.Join(sess, "subagents", "agent-x.meta.json"), "{}", now)
	touchDir(t, filepath.Join(sess, "subagents", "workflows", "wf_1"), now)
	touchDir(t, filepath.Join(sess, "subagents", "workflows", "wf_2"), now)
	touchDir(t, filepath.Join(sess, "subagents", "workflows", "wf_old"), now.Add(-10*time.Minute))
	touch(t, filepath.Join(spool, liveID+".json"), fixtureFull, now)

	pb := filepath.Join(projects, "-Users-x-dev-bar")
	touch(t, filepath.Join(pb, recentID+".jsonl"), tsEntry("2026-07-03T10:00:00Z")+"\n", now.Add(-5*time.Minute))
	// no registry record: a dead session's transcript never renders
	touch(t, filepath.Join(pb, "dead.jsonl"), "{}", now)
	touch(t, filepath.Join(pb, "notes.txt"), "ignored", now)

	sessionsDir := t.TempDir()
	regLive(t, sessionsDir, liveID)
	regLive(t, sessionsDir, recentID)
	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "claude 1/2" {
		t.Errorf("Title = %q, want the tally on the title", data.Title)
	}
	if len(data.Rows) != 2 {
		t.Fatalf("Rows = %+v, want one row per session", data.Rows)
	}
	live := data.Rows[0]
	if rowName(live) != "foo" || live.Style != module.StyleAccent {
		t.Errorf("live row = %+v", live)
	}
	if text := lineText(live); !strings.Contains(text, " \uf013 1 \uf0e8 2") {
		t.Errorf("live line = %q", text)
	}
	// Poll uses its own now, so the live age is seconds-fresh but not exact
	if age := live.Spans[spanAge]; age.Style != module.StyleAccent ||
		!strings.HasSuffix(strings.TrimSpace(age.Text), "s") {
		t.Errorf("live age span = %+v, want fresh accent seconds", age)
	}
	recent := data.Rows[1]
	if !strings.HasPrefix(rowIdent(recent), "bbbbbbbb") || recent.Style != module.StyleDim {
		t.Errorf("recent row = %+v", recent)
	}
	if recent.Spans[spanAge] != (module.Span{Text: " 5m", Style: module.StyleDim}) {
		t.Errorf("recent age span = %+v", recent.Spans[spanAge])
	}
}

func TestPollPromptAndNames(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	touch(t, filepath.Join(projects, "p", id+".jsonl"), fixtureTranscript, now)
	touch(t, filepath.Join(spool, id+".json"), fixtureNamed, now)
	touch(t, filepath.Join(sessionsDir, "42.json"), regRecord(id, "reg-name", "/x/reg", 9), now)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("Rows = %+v, want the one-liner only", data.Rows)
	}
	row := data.Rows[0]
	// registry kebab name beats spool session_name
	if rowName(row) != "reg-name" || row.Style != module.StyleAccent {
		t.Errorf("row = %+v", row)
	}
	text := lineText(row)
	// fixtureNamed carries no prompt field, so the transcript tail fills in
	if !strings.HasSuffix(text, " > do the thing") {
		t.Errorf("line = %q, want the prompt filling the tail", text)
	}
	// the one-liner spends no width on ctx or replies
	if strings.Contains(text, "ctx") || strings.Contains(text, "< ") {
		t.Errorf("line = %q, want no ctx and no reply", text)
	}
}

// The hook-written spool prompt is primary: when present it wins over the
// transcript tail (which runs empty under notification floods).
func TestPollSpoolPromptPrimary(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	touch(t, filepath.Join(projects, "p", id+".jsonl"), fixtureTranscript, now)
	touch(t, filepath.Join(spool, id+".json"), fixtureHooked, now)
	// live but status-less record: the alive gate passes and the spool
	// heuristic keeps driving the state column
	touch(t, filepath.Join(sessionsDir, id+".json"),
		fmt.Sprintf(`{"sessionId":%q,"pid":%d,"startedAt":%d,"updatedAt":1}`,
			id, os.Getpid(), regFixtureStart.UnixMilli()), now)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("Rows = %+v", data.Rows)
	}
	text := lineText(data.Rows[0])
	if !strings.HasSuffix(text, " > spool prompt") {
		t.Errorf("line = %q, want the spool prompt over the transcript tail", text)
	}
	// fixtureHooked left attention set: the bell rides the state column --
	// dim, since the fixture's fixed-epoch timestamps sit far past the
	// attention horizon
	if got := data.Rows[0].Spans[spanState]; got != (module.Span{Text: " \uf0f3", Style: module.StyleDim}) {
		t.Errorf("state span = %+v, want the dim attention bell", got)
	}
}

// Mid-turn steering never fires UserPromptSubmit: when the transcript
// outdates the spool and its tail carries a strictly newer timestamped
// user entry (envelope-wrapped steering included), the tail text replaces
// the frozen spool prompt.
func TestPollSteeringOverridesStaleSpoolPrompt(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	body := fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"do the thing"}}`+"\n"+
		`{"type":"user","timestamp":%q,"message":{"content":"<system-reminder>\nnudge\n</system-reminder>\nsteer toward the tail"}}`+"\n",
		now.Add(-10*time.Minute).UTC().Format(time.RFC3339),
		now.Add(-time.Minute).UTC().Format(time.RFC3339))
	touch(t, filepath.Join(projects, "p", id+".jsonl"), body, now)
	sp := fmt.Sprintf(`{"session_id":%q,"prompt":"spool prompt","ts":%d}`, id, now.Add(-5*time.Minute).Unix())
	touch(t, filepath.Join(spool, id+".json"), sp, now.Add(-5*time.Minute))
	regLive(t, sessionsDir, id)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("Rows = %+v", data.Rows)
	}
	if text := lineText(data.Rows[0]); !strings.HasSuffix(text, " > steer toward the tail") {
		t.Errorf("line = %q, want the newer tail prompt over the stale spool", text)
	}
}

// Regression: the Stop hook rewrites the spool at turn end
// (bumping its mtime past the transcript) WITHOUT a new prompt -- the
// cached steering tail must keep winning by ts instead of the row
// flapping back to the stale spool text.
func TestPollSteeringSurvivesStopSpoolBump(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	body := fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"steer toward the tail"}}`+"\n",
		now.Add(-time.Minute).UTC().Format(time.RFC3339))
	touch(t, filepath.Join(projects, "p", id+".jsonl"), body, now.Add(-time.Minute))
	sp := fmt.Sprintf(`{"session_id":%q,"prompt":"spool prompt","ts":%d}`, id, now.Add(-5*time.Minute).Unix())
	touch(t, filepath.Join(spool, id+".json"), sp, now.Add(-5*time.Minute))
	regLive(t, sessionsDir, id)

	mod := New()
	params := map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	}
	if _, err := mod.Poll(context.Background(), params); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	// the turn's assistant reply lands in the transcript, bumping its mtime
	// past the cached one -- the cached tail is now mtime-stale
	body += fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":"on it"}}`+"\n",
		now.Add(-30*time.Second).UTC().Format(time.RFC3339))
	touch(t, filepath.Join(projects, "p", id+".jsonl"), body, now.Add(-30*time.Second))
	// the Stop hook: spool rewritten (mtime now NEWER than the
	// transcript) with the same stale prompt fields
	touch(t, filepath.Join(spool, id+".json"), sp, now)
	data, err := mod.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if text := lineText(data.Rows[0]); !strings.HasSuffix(text, " > steer toward the tail") {
		t.Errorf("line = %q, want the cached steering prompt to survive the stop-hook spool bump", text)
	}
}

// The spool prompt stays primary when its ts is at or ahead of the tail's:
// only a STRICTLY newer tail entry replaces it, even when the transcript
// mtime outdates the spool's.
func TestPollSpoolNewerThanTailWins(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	body := fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"older tail prompt"}}`+"\n",
		now.Add(-10*time.Minute).UTC().Format(time.RFC3339))
	touch(t, filepath.Join(projects, "p", id+".jsonl"), body, now)
	sp := fmt.Sprintf(`{"session_id":%q,"prompt":"spool prompt","ts":%d}`, id, now.Add(-time.Minute).Unix())
	touch(t, filepath.Join(spool, id+".json"), sp, now.Add(-2*time.Minute))
	regLive(t, sessionsDir, id)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 {
		t.Fatalf("Rows = %+v", data.Rows)
	}
	if text := lineText(data.Rows[0]); !strings.HasSuffix(text, " > spool prompt") {
		t.Errorf("line = %q, want the newer spool prompt kept", text)
	}
}

// An unchanged transcript is never re-read: the tail result memoizes on
// the transcript mtime, and an mtime bump invalidates exactly once.
func TestPollTailReadCachedByMtime(t *testing.T) {
	projects := t.TempDir()
	now := time.Now()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	p := filepath.Join(projects, "p", id+".jsonl")
	touch(t, p, `{"type":"user","message":{"content":"do the thing"}}`+"\n", now.Add(-time.Minute))
	sessionsDir := t.TempDir()
	regLive(t, sessionsDir, id)
	mod := New()
	params := map[string]any{"projectsDir": projects, "sessionsDir": sessionsDir}
	for i := range 2 {
		if _, err := mod.Poll(context.Background(), params); err != nil {
			t.Fatalf("Poll %d: %v", i, err)
		}
	}
	if mod.tailReads != 1 {
		t.Errorf("tailReads = %d, want 1 (unchanged mtime must hit the cache)", mod.tailReads)
	}
	touch(t, p, `{"type":"user","message":{"content":"do more"}}`+"\n", now)
	if _, err := mod.Poll(context.Background(), params); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if mod.tailReads != 2 {
		t.Errorf("tailReads = %d, want 2 after an mtime bump", mod.tailReads)
	}
}

// benchSessions builds n spool-less sessions whose transcripts exceed the
// tail window, so every cold read costs the full tailBytes scan.
func benchSessions(b *testing.B, n int) ([]session, *Mod) {
	b.Helper()
	dir := b.TempDir()
	pad := `{"type":"padding","message":{"content":"` + strings.Repeat("x", 1024) + `"}}` + "\n"
	now := time.Now()
	sessions := make([]session, 0, n)
	for i := range n {
		var sb strings.Builder
		for sb.Len() < tailBytes+len(pad) {
			sb.WriteString(pad)
		}
		sb.WriteString(`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"steer"}}` + "\n")
		p := filepath.Join(dir, fmt.Sprintf("%02d.jsonl", i))
		if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
			b.Fatal(err)
		}
		sessions = append(sessions, session{
			id: fmt.Sprintf("%02d", i), transcript: p, transcriptMtime: now,
		})
	}
	return sessions, New()
}

// Per-poll cost of the freshness pass at 15 sessions x 64KB tails, cache
// cleared each iteration: every transcript re-read (the worst case, all
// 15 transcripts changed since the last poll).
func BenchmarkFreshenPromptsCold(b *testing.B) {
	sessions, mod := benchSessions(b, 15)
	for b.Loop() {
		mod.tails = map[string]tailEntry{}
		mod.freshenPrompts(sessions)
	}
}

// The steady-state path: unchanged mtimes, all 15 lookups hit the cache.
func BenchmarkFreshenPromptsWarm(b *testing.B) {
	sessions, mod := benchSessions(b, 15)
	mod.freshenPrompts(sessions)
	for b.Loop() {
		mod.freshenPrompts(sessions)
	}
}

// Spool activity (a turn completion) is activity: it drives the age column
// and liveness even when nothing touched the transcript since.
func TestPollTurnCompletionDrivesTime(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "bbbbbbbb-1111-2222-3333-444444444444"
	touch(t, filepath.Join(projects, "p", id+".jsonl"), "{}", now.Add(-5*time.Minute))
	sp := fmt.Sprintf(`{"session_id":%q,"attention":false,"stopped_ts":%d}`, id, now.Unix())
	touch(t, filepath.Join(spool, id+".json"), sp, now)
	regLive(t, sessionsDir, id)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "claude 1/1" || len(data.Rows) != 1 {
		t.Fatalf("Title = %q Rows = %+v, want the stop to keep the session live", data.Title, data.Rows)
	}
	row := data.Rows[0]
	if age := row.Spans[spanAge]; age.Style != module.StyleAccent ||
		!strings.HasSuffix(strings.TrimSpace(age.Text), "s") {
		t.Errorf("age span = %+v, want seconds since the stop, accent", age)
	}
	if got := row.Spans[spanState]; got != (module.Span{Text: " \uf00c", Style: module.StyleDim}) {
		t.Errorf("state span = %+v, want the turn-complete check", got)
	}
}

// A live registry record with no name leaves the spool session_name as
// the display key.
func TestPollSpoolNameWithoutRegistryName(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "bbbbbbbb-1111-2222-3333-444444444444"
	touch(t, filepath.Join(projects, "p", id+".jsonl"), "{}", now)
	touch(t, filepath.Join(spool, id+".json"), fixtureNamed, now)
	regLive(t, sessionsDir, id)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 || rowName(data.Rows[0]) != "spoolname" {
		t.Errorf("Rows = %+v, want spool session_name key", data.Rows)
	}
}

func TestPollRegistryCwdFallback(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "cccccccc-1111-2222-3333-444444444444"
	touch(t, filepath.Join(projects, "p", id+".jsonl"), "{}", now)
	touch(t, filepath.Join(sessionsDir, "42.json"), regRecord(id, "", "/x/regcwd", 9), now)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 || rowName(data.Rows[0]) != "regcwd" {
		t.Errorf("Rows = %+v, want registry cwd basename key", data.Rows)
	}
}

// The window param is vestigial: the live-registry gate replaced age
// pruning, so a bad value still errors but an in-window mtime is no
// longer required to render.
func TestPollWindowVestigial(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	touch(t, filepath.Join(projects, "p", "aaaaaaaa-in.jsonl"), "{}", now.Add(-30*time.Minute))
	touch(t, filepath.Join(projects, "p", "bbbbbbbb-out.jsonl"), "{}", now.Add(-2*time.Hour))
	regLive(t, sessionsDir, "aaaaaaaa-in")
	regLive(t, sessionsDir, "bbbbbbbb-out")

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"window":      "1h",
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "claude 0/2" || len(data.Rows) != 2 {
		t.Fatalf("Title = %q Rows = %+v, want the out-of-window live session kept", data.Title, data.Rows)
	}
	// zero starts order by id: the 30m session leads
	if age := strings.TrimSpace(data.Rows[0].Spans[spanAge].Text); age != "30m" {
		t.Errorf("age = %q, want 30m", age)
	}
	if age := strings.TrimSpace(data.Rows[1].Spans[spanAge].Text); age != "2h" {
		t.Errorf("age = %q, want 2h", age)
	}

	if _, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"window":      "soon",
	}); err == nil {
		t.Error("Poll(bad window): want error, got nil")
	}
}

func TestPollMaxParam(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	for i := range 4 {
		touch(t, filepath.Join(projects, "p", fmt.Sprintf("%08d-max.jsonl", i)), "{}",
			now.Add(-time.Duration(i+2)*time.Minute))
		regLive(t, sessionsDir, fmt.Sprintf("%08d-max", i))
	}
	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"max":         int64(2),
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 3 || data.Rows[2].Text != "+2 more" {
		t.Errorf("Rows = %+v, want 2 sessions + more", data.Rows)
	}
}

func TestPollNegativeMax(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	touch(t, filepath.Join(projects, "p", "aaaaaaaa-neg.jsonl"), "{}", now.Add(-2*time.Minute))
	regLive(t, sessionsDir, "aaaaaaaa-neg")

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"max":         int64(-1),
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 || data.Rows[0].Text != "+1 more" {
		t.Errorf("Rows = %+v, want more row only", data.Rows)
	}

	_, rows := render([]session{{id: "aaaaaaaa-neg", mtime: now}}, -1, now)
	if len(rows) != 1 || rows[0].Text != "+1 more" {
		t.Errorf("render(-1) rows = %+v, want more row only", rows)
	}
}

// Fixed order is the point: activity (mtime churn) between polls must not
// reshuffle rows, whether the start cache is warm or cold.
func TestPollFixedOrderAcrossActivity(t *testing.T) {
	projects := t.TempDir()
	now := time.Now()
	older := "aaaaaaaa-1111-2222-3333-444444444444" // started 10:00
	newer := "bbbbbbbb-1111-2222-3333-444444444444" // started 11:00
	pdir := filepath.Join(projects, "p")
	touch(t, filepath.Join(pdir, older+".jsonl"), tsEntry("2026-07-03T10:00:00Z")+"\n", now.Add(-2*time.Minute))
	touch(t, filepath.Join(pdir, newer+".jsonl"), tsEntry("2026-07-03T11:00:00Z")+"\n", now.Add(-3*time.Minute))
	sessionsDir := t.TempDir()
	regLive(t, sessionsDir, older)
	regLive(t, sessionsDir, newer)

	params := map[string]any{"projectsDir": projects, "sessionsDir": sessionsDir}
	mod := New()
	order := func(data module.Data) []string {
		var got []string
		for _, r := range data.Rows {
			got = append(got, rowIdent(r)[:8])
		}
		return got
	}
	data, err := mod.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	want := []string{"bbbbbbbb", "aaaaaaaa"} // newest start first
	if got := order(data); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("order = %v, want %v", got, want)
	}

	// recency flips (the older-start session becomes the most active)
	if err := os.Chtimes(filepath.Join(pdir, older+".jsonl"), now, now); err != nil {
		t.Fatal(err)
	}
	data, err = mod.Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := order(data); got[0] != want[0] || got[1] != want[1] {
		t.Errorf("warm-cache order = %v, want %v (rows must not swap on activity)", got, want)
	}

	// a cold cache (process restart) re-reads the immutable head: same order
	data, err = New().Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if got := order(data); got[0] != want[0] || got[1] != want[1] {
		t.Errorf("cold-cache order = %v, want %v", got, want)
	}
}

// Rows group by raw cwd: sessions interleaved by start time land adjacent
// per dir, groups order newest-first by their newest member's start, and
// within a group newest start leads. The order is deterministic across
// polls (warm start cache included).
func TestPollGroupedByCwdOrder(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	pdir := filepath.Join(projects, "p")
	mk := func(id, name, dir, start string) {
		touch(t, filepath.Join(pdir, id+".jsonl"), tsEntry(start)+"\n", now)
		sp := fmt.Sprintf(`{"session_id":%q,"session_name":%q,"workspace":{"current_dir":%q}}`, id, name, dir)
		touch(t, filepath.Join(spool, id+".json"), sp, now)
		regLive(t, sessionsDir, id)
	}
	// starts interleave across three cwds: a flat newest-first sort would
	// yield s5 s4 s3 s2 s1
	mk("aaaaaaaa-1111-2222-3333-444444444444", "s1", "/x/one", "2026-07-03T10:00:00Z")
	mk("bbbbbbbb-1111-2222-3333-444444444444", "s2", "/x/two", "2026-07-03T11:00:00Z")
	mk("cccccccc-1111-2222-3333-444444444444", "s3", "/x/three", "2026-07-03T12:00:00Z")
	mk("dddddddd-1111-2222-3333-444444444444", "s4", "/x/one", "2026-07-03T13:00:00Z")
	mk("eeeeeeee-1111-2222-3333-444444444444", "s5", "/x/two", "2026-07-03T14:00:00Z")

	params := map[string]any{"projectsDir": projects, "dir": spool, "sessionsDir": sessionsDir}
	mod := New()
	// group NEWEST member: /x/two 14:00 > /x/one 13:00 > /x/three 12:00;
	// within a group newest start first (a session birth floats its whole
	// group)
	want := []string{"s5", "s2", "s4", "s1", "s3"}
	for pass := range 2 {
		data, err := mod.Poll(context.Background(), params)
		if err != nil {
			t.Fatalf("pass %d Poll: %v", pass, err)
		}
		if len(data.Rows) != len(want) {
			t.Fatalf("pass %d Rows = %+v, want %d rows", pass, data.Rows, len(want))
		}
		for i, w := range want {
			if got := rowName(data.Rows[i]); got != w {
				t.Errorf("pass %d row %d = %q, want %q", pass, i, got, w)
			}
		}
	}
}

// A newborn session with an unparseable head (zero start) must not sink
// its whole cwd group: zero starts are excluded from the group-newest
// fold, so the group keeps its slot and the newborn rows within it.
func TestPollGroupZeroStartDoesNotSinkGroup(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	pdir := filepath.Join(projects, "p")
	mk := func(id, name, dir, start string) {
		body := "{}"
		if start != "" {
			body = tsEntry(start) + "\n"
		}
		touch(t, filepath.Join(pdir, id+".jsonl"), body, now)
		sp := fmt.Sprintf(`{"session_id":%q,"session_name":%q,"workspace":{"current_dir":%q}}`, id, name, dir)
		touch(t, filepath.Join(spool, id+".json"), sp, now)
		regLive(t, sessionsDir, id)
	}
	mk("aaaaaaaa-1111-2222-3333-444444444444", "s1", "/x/one", "2026-07-03T10:00:00Z")
	mk("cccccccc-1111-2222-3333-444444444444", "s3", "/x/three", "2026-07-03T12:00:00Z")
	mk("ffffffff-1111-2222-3333-444444444444", "sN", "/x/three", "")

	params := map[string]any{"projectsDir": projects, "dir": spool, "sessionsDir": sessionsDir}
	data, err := New().Poll(context.Background(), params)
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	want := []string{"s3", "sN", "s1"}
	if len(data.Rows) != len(want) {
		t.Fatalf("Rows = %+v, want %d rows", data.Rows, len(want))
	}
	for i, w := range want {
		if got := rowName(data.Rows[i]); got != w {
			t.Errorf("row %d = %q, want %q", i, got, w)
		}
	}
}

// Transcripts with no head timestamp order by id, still ignoring mtime.
func TestPollOrderFallsBackToID(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	pdir := filepath.Join(projects, "p")
	touch(t, filepath.Join(pdir, "cccccccc-x.jsonl"), "{}", now.Add(-time.Minute))
	touch(t, filepath.Join(pdir, "aaaaaaaa-x.jsonl"), "{}", now.Add(-3*time.Minute))
	touch(t, filepath.Join(pdir, "bbbbbbbb-x.jsonl"), "{}", now.Add(-2*time.Minute))
	for _, id := range []string{"aaaaaaaa-x", "bbbbbbbb-x", "cccccccc-x"} {
		regLive(t, sessionsDir, id)
	}

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects, "sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	want := []string{"aaaaaaaa", "bbbbbbbb", "cccccccc"}
	if len(data.Rows) != 3 {
		t.Fatalf("Rows = %+v", data.Rows)
	}
	for i, w := range want {
		if got := rowIdent(data.Rows[i]); !strings.HasPrefix(got, w) {
			t.Errorf("row %d ident = %q, want prefix %q", i, got, w)
		}
	}
}

func TestPollFleetDrivesLiveness(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	pdir := filepath.Join(projects, "p")
	touch(t, filepath.Join(pdir, id+".jsonl"), "{}", now.Add(-5*time.Minute))
	touch(t, filepath.Join(pdir, id, "subagents", "agent-a.jsonl"), "{}", now)
	regLive(t, sessionsDir, id)

	data, err := New().Poll(context.Background(), map[string]any{"projectsDir": projects, "sessionsDir": sessionsDir})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "claude 1/1" || len(data.Rows) != 1 {
		t.Fatalf("Title = %q Rows = %+v, want one live session", data.Title, data.Rows)
	}
	row := data.Rows[0]
	if row.Style != module.StyleAccent || row.Spans[spanAge].Style != module.StyleAccent {
		t.Errorf("row = %+v, want accent row and age column", row)
	}
	if !strings.Contains(lineText(row), " \uf013 1") {
		t.Errorf("line = %q, want the agent glyph count", lineText(row))
	}
}

func TestPollWorkflowFileMtimeDrivesLiveness(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "bbbbbbbb-1111-2222-3333-444444444444"
	pdir := filepath.Join(projects, "p")
	touch(t, filepath.Join(pdir, id+".jsonl"), "{}", now.Add(-5*time.Minute))
	wf := filepath.Join(pdir, id, "subagents", "workflows", "wf_x")
	touch(t, filepath.Join(wf, "log.jsonl"), "{}", now)
	// appends touch files, not the dir: an old dir mtime must not hide a
	// running stage
	touchDir(t, wf, now.Add(-10*time.Minute))
	regLive(t, sessionsDir, id)

	data, err := New().Poll(context.Background(), map[string]any{"projectsDir": projects, "sessionsDir": sessionsDir})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "claude 1/1" || len(data.Rows) != 1 {
		t.Fatalf("Title = %q Rows = %+v, want one live session", data.Title, data.Rows)
	}
	row := data.Rows[0]
	if row.Style != module.StyleAccent || !strings.Contains(lineText(row), " \uf0e8 1") {
		t.Errorf("row = %+v, want accent with the workflow glyph count", row)
	}
}

func TestPollCrossProjectSatellites(t *testing.T) {
	projects := t.TempDir()
	now := time.Now()
	sid := "cccccccc-1111-2222-3333-444444444444"
	orphan := "dddddddd-1111-2222-3333-444444444444"
	touch(t, filepath.Join(projects, "projectA", sid+".jsonl"), "{}", now)
	sat := filepath.Join(projects, "projectB", sid)
	touch(t, filepath.Join(sat, "subagents", "agent-1.jsonl"), "{}", now)
	touch(t, filepath.Join(sat, "subagents", "workflows", "wf_1", "log.jsonl"), "{}", now)
	// a satellite with no transcript anywhere is not a session, live
	// registry record or not
	touch(t, filepath.Join(projects, "projectC", orphan, "subagents", "agent-2.jsonl"), "{}", now)
	sessionsDir := t.TempDir()
	regLive(t, sessionsDir, sid)
	regLive(t, sessionsDir, orphan)

	data, err := New().Poll(context.Background(), map[string]any{"projectsDir": projects, "sessionsDir": sessionsDir})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if data.Title != "claude 1/1" || len(data.Rows) != 1 {
		t.Fatalf("Title = %q Rows = %+v, want single live session", data.Title, data.Rows)
	}
	if text := lineText(data.Rows[0]); !strings.Contains(text, "\uf013 1") || !strings.Contains(text, "\uf0e8 1") {
		t.Errorf("line = %q, want cross-project fleet counted", text)
	}
}

func TestPollMalformedSpoolFallsBack(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "cccccccc-1111"
	touch(t, filepath.Join(projects, "p", id+".jsonl"), "{}", now.Add(-time.Minute))
	touch(t, filepath.Join(spool, id+".json"), fixtureMalformed, now)
	regLive(t, sessionsDir, id)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 || !strings.HasPrefix(rowIdent(data.Rows[0]), "cccccccc") {
		t.Errorf("Rows = %+v, want the malformed-spool session surfaced", data.Rows)
	}
}

func TestPollSkipsNonRegularSpool(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	id := "dddddddd-1111"
	touch(t, filepath.Join(projects, "p", id+".jsonl"), "{}", now.Add(-time.Minute))
	regLive(t, sessionsDir, id)
	// a FIFO with no writer blocks open-for-read forever; overlay must skip
	// it.
	if err := syscall.Mkfifo(filepath.Join(spool, id+".json"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 1 || !strings.HasPrefix(rowIdent(data.Rows[0]), "dddddddd") {
		t.Errorf("Rows = %+v, want the session surfaced despite the odd spool", data.Rows)
	}
}

func TestPollMissingProjectsDir(t *testing.T) {
	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": filepath.Join(t.TempDir(), "nope"),
	})
	if err != nil {
		t.Fatalf("Poll(missing projects dir): %v", err)
	}
	if data.Title != "claude" || len(data.Rows) != 1 || data.Rows[0].Text != "no active sessions" {
		t.Errorf("Title = %q Rows = %+v", data.Title, data.Rows)
	}
}

func TestPollCancelledCtx(t *testing.T) {
	projects := t.TempDir()
	touch(t, filepath.Join(projects, "p", "eeeeeeee-1111.jsonl"), "{}", time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Poll(ctx, map[string]any{"projectsDir": projects}); err == nil {
		t.Error("Poll(cancelled ctx): want error, got nil")
	}
}

func TestDefaultMax(t *testing.T) {
	if defaultMax != 7 {
		t.Errorf("defaultMax = %d, want 7 (one row per session + more row)", defaultMax)
	}
}

// The cwd column shows the git-repo-relative path (repo dir basename +
// relative subpath); worktree .git FILES count; non-repo dirs fall back
// to the ~-compacted absolute path.
func TestRepoRel(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "homies", "kb")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	base := filepath.Base(root)
	if got, ok := repoRel(root); !ok || got != base {
		t.Errorf("repoRel(root) = %q,%v want %q", got, ok, base)
	}
	if got, ok := repoRel(sub); !ok || got != base+"/homies/kb" {
		t.Errorf("repoRel(sub) = %q,%v want %q", got, ok, base+"/homies/kb")
	}

	// worktree: .git is a FILE
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := repoRel(wt); !ok || got != filepath.Base(wt) {
		t.Errorf("repoRel(worktree) = %q,%v want %q", got, ok, filepath.Base(wt))
	}

	// no enclosing repo (tmp roots have no .git up-tree in the sandbox;
	// if this machine's tmp ever gains one, the fallback still holds)
	if _, ok := repoRel(string(filepath.Separator) + "nonexistent-khudson-test"); ok {
		t.Skip("an enclosing .git exists above /; environment-specific")
	}
}

// displayDirs feeds the cwd column: repo-relative for repo dirs, cached
// by raw dir across polls.
func TestDisplayDirsRepoRelative(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(repo, "src")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	id := "aaaaaaaa-1111-2222-3333-444444444444"
	touch(t, filepath.Join(projects, "p", id+".jsonl"), tsEntry("2026-07-03T10:00:00Z")+"\n", now)
	sp := fmt.Sprintf(`{"session_id":%q,"session_name":"s1","workspace":{"current_dir":%q}}`, id, work)
	touch(t, filepath.Join(spool, id+".json"), sp, now)
	sessionsDir := t.TempDir()
	regLive(t, sessionsDir, id)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects, "dir": spool, "sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	want := filepath.Base(repo) + "/src"
	if text := lineText(data.Rows[0]); !strings.Contains(text, abbrevPath(want, cwdWidth)) {
		t.Errorf("line = %q, want the repo-relative cwd %q", text, want)
	}
}
