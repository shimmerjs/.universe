package claudesessions

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

func focusOf(sid string) string { return strings.Join(focusArgv(sid), " ") }

func actOf(r module.Row) string { return strings.Join(r.Act, " ") }

// The occupied panel is two geometrically immutable zones: detail rows 1-8
// (header+outcome carrying the focus Act, blank padding), then the list in
// fixed order (every row a focus Act), then "+N more".
func TestPanelGeometryOccupied(t *testing.T) {
	now := time.Now()
	sessions := []session{
		{id: "aaaaaaaa-live", mtime: now, prompt: "go"},
		{id: "bbbbbbbb-idle", mtime: now.Add(-30 * time.Minute)},
	}
	title, rows := renderPanel(sessions, 0, nil, panelListMax, now)
	if title != "claude 1/2" {
		t.Errorf("title = %q", title)
	}
	if len(rows) != panelDetailRows+2 {
		t.Fatalf("len(rows) = %d, want %d detail + 2 list", len(rows), panelDetailRows+2)
	}
	if actOf(rows[0]) != focusOf("aaaaaaaa-live") || actOf(rows[1]) != focusOf("aaaaaaaa-live") {
		t.Errorf("header/outcome acts = %q / %q, want the focus verb on both (one tap target)",
			actOf(rows[0]), actOf(rows[1]))
	}
	// row 2 is the dim "no agents" hint (the reserved zone stays legible);
	// the rest of the zone pads blank and actless
	if rows[2].Text != "    no agents" || rows[2].Style != module.StyleDim || len(rows[2].Act) != 0 {
		t.Errorf("agents hint row = %+v, want the dim no-agents hint", rows[2])
	}
	for i := 3; i < panelDetailRows; i++ {
		if rows[i].Text != "" || len(rows[i].Act) != 0 {
			t.Errorf("detail pad row %d = %+v, want blank actless", i, rows[i])
		}
	}
	if actOf(rows[panelDetailRows]) != focusOf("aaaaaaaa-live") {
		t.Errorf("occupant list row act = %q, want the occupant to KEEP its list row", actOf(rows[panelDetailRows]))
	}
	if actOf(rows[panelDetailRows+1]) != focusOf("bbbbbbbb-idle") {
		t.Errorf("list row act = %q", actOf(rows[panelDetailRows+1]))
	}
}

// 21-row budget: 8 detail + 12 list + "+N more" at 15 sessions.
func TestPanelGeometryOverflow(t *testing.T) {
	now := time.Now()
	var sessions []session
	for i := range 15 {
		sessions = append(sessions, session{
			id: fmt.Sprintf("%08d-x", i), mtime: now.Add(-time.Duration(i) * time.Minute)})
	}
	_, rows := renderPanel(sessions, 0, nil, panelListMax, now)
	if len(rows) != panelDetailRows+panelListMax+1 {
		t.Fatalf("len(rows) = %d, want %d", len(rows), panelDetailRows+panelListMax+1)
	}
	last := rows[len(rows)-1]
	if last.Text != "+3 more" || last.Style != module.StyleDim || len(last.Act) != 0 {
		t.Errorf("overflow row = %+v, want dim actless +3 more", last)
	}
}

// Empty detail zone (single idle session): one dim placeholder, the list
// grows upward -- the only geometry change.
func TestPanelEmptyDetail(t *testing.T) {
	now := time.Now()
	sessions := []session{{id: "aaaaaaaa-idle", mtime: now.Add(-30 * time.Minute)}}
	_, rows := renderPanel(sessions, -1, nil, panelListMax, now)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want placeholder + one list row", rows)
	}
	if rows[0].Text != "no session in focus" || rows[0].Style != module.StyleDim || len(rows[0].Act) != 0 {
		t.Errorf("placeholder = %+v", rows[0])
	}
	if actOf(rows[1]) != focusOf("aaaaaaaa-idle") {
		t.Errorf("list row act = %q", actOf(rows[1]))
	}
}

func TestPanelNoSessions(t *testing.T) {
	title, rows := renderPanel(nil, -1, nil, panelListMax, time.Now())
	if title != "claude" || len(rows) != 1 || rows[0].Text != "no active sessions" {
		t.Errorf("empty panel = %q %+v", title, rows)
	}
}

// The border title carries the machine rollup: tally + live fleet counts.
func TestPanelTitleRollup(t *testing.T) {
	now := time.Now()
	sessions := []session{
		{id: "aaaaaaaa-x", mtime: now, agents: 12, workflows: 2},
		{id: "bbbbbbbb-x", mtime: now.Add(-time.Hour), agents: 2},
	}
	title, _ := renderPanel(sessions, 0, nil, panelListMax, now)
	if want := "claude 1/2 . " + glyphAgents + "14 " + glyphWorkflows + "2"; title != want {
		t.Errorf("title = %q, want %q", title, want)
	}
}

// Occupancy: attention (oldest notification first) > live (sticky
// incumbent) > newest start; empty only at zero/one-idle states.
func TestPickOccupant(t *testing.T) {
	now := time.Now()
	p := NewPanel(New())

	// attention beats live; oldest notification wins
	sessions := []session{
		{id: "aa", mtime: now},
		{id: "bb", mtime: now, attention: true, notified: now.Add(-time.Minute)},
		{id: "cc", mtime: now, attention: true, notified: now.Add(-time.Hour)},
	}
	if got := p.pickOccupant(sessions, now); got != 2 {
		t.Errorf("attention pick = %d, want the oldest notification (2)", got)
	}

	// live class: newest mtime when no incumbent
	p = NewPanel(New())
	sessions = []session{
		{id: "aa", mtime: now.Add(-30 * time.Second)},
		{id: "bb", mtime: now.Add(-5 * time.Second)},
		{id: "cc", mtime: now.Add(-2 * time.Hour)},
	}
	if got := p.pickOccupant(sessions, now); got != 1 {
		t.Errorf("live pick = %d, want the most recently live (1)", got)
	}
	// stickiness: the incumbent keeps the zone while live, even when another
	// session's mtime pulls ahead (no per-poll churn)
	sessions[0].mtime = now
	if got := p.pickOccupant(sessions, now); got != 1 {
		t.Errorf("sticky pick = %d, want the incumbent (1)", got)
	}
	// incumbent goes stale: the zone moves to the live session
	sessions[1].mtime = now.Add(-10 * time.Minute)
	if got := p.pickOccupant(sessions, now); got != 0 {
		t.Errorf("post-incumbent pick = %d, want the live session (0)", got)
	}

	// all idle, multiple sessions: newest start, wherever it sits in the
	// cwd-grouped list order
	p = NewPanel(New())
	sessions = []session{
		{id: "aa", mtime: now.Add(-time.Hour), start: now.Add(-2 * time.Hour)},
		{id: "bb", mtime: now.Add(-2 * time.Hour), start: now.Add(-time.Hour)},
	}
	if got := p.pickOccupant(sessions, now); got != 1 {
		t.Errorf("idle pick = %d, want the newest start (1)", got)
	}
	// equal (zero) starts: id tiebreak
	p = NewPanel(New())
	sessions = []session{
		{id: "bb", mtime: now.Add(-time.Hour)},
		{id: "aa", mtime: now.Add(-2 * time.Hour)},
	}
	if got := p.pickOccupant(sessions, now); got != 1 {
		t.Errorf("idle tiebreak pick = %d, want the lowest id (1)", got)
	}

	// single idle session: empty detail zone
	p = NewPanel(New())
	if got := p.pickOccupant([]session{{id: "aa", mtime: now.Add(-time.Hour)}}, now); got != -1 {
		t.Errorf("single idle pick = %d, want -1", got)
	}
	// but a single ATTENTION session occupies
	if got := p.pickOccupant([]session{{id: "aa", mtime: now.Add(-time.Hour), attention: true}}, now); got != 0 {
		t.Errorf("single attention pick = %d, want 0", got)
	}
	if got := p.pickOccupant(nil, now); got != -1 {
		t.Errorf("no-session pick = %d, want -1", got)
	}
}

// A stale notification cannot pin the detail zone: the attention loop
// skips answered AND horizon-stale sessions, which fall through to the
// live/newest branches.
func TestPickOccupantSkipsStaleAttention(t *testing.T) {
	now := time.Now()
	p := NewPanel(New())
	sessions := []session{
		{id: "aa", mtime: now},
		{id: "bb", mtime: now.Add(-time.Second), attention: true,
			notified: now.Add(-30 * time.Minute), tailPromptTS: now.Add(-time.Minute)},
	}
	if got := p.pickOccupant(sessions, now); got != 0 {
		t.Errorf("pick = %d, want the live session (0), not the answered notification", got)
	}
	// the same notification unanswered (and within the horizon) still pins
	// the zone
	p = NewPanel(New())
	sessions[1].tailPromptTS = time.Time{}
	if got := p.pickOccupant(sessions, now); got != 1 {
		t.Errorf("pick = %d, want the attention session (1)", got)
	}
	// an unanswered notification past the horizon cannot pin either
	p = NewPanel(New())
	sessions[1].notified = now.Add(-17 * time.Hour)
	if got := p.pickOccupant(sessions, now); got != 0 {
		t.Errorf("pick = %d, want the live session (0), not the horizon-stale bell", got)
	}
}

// An answered notification falls through to the turn outcome in the detail
// zone (the stale 11h-bell bug): the turn state renders, not the
// notification.
func TestOutcomeRowStaleAttention(t *testing.T) {
	now := time.Now()
	s := session{attention: true, notifType: "permission_prompt",
		notification: "needs Bash", notified: now.Add(-11 * time.Hour),
		promptTS: now.Add(-3 * time.Minute), tailPromptTS: now.Add(-2 * time.Minute),
		stopped: now.Add(-time.Minute), lastAssistant: "all green"}
	text := lineText(s.outcomeRow(now))
	if strings.Contains(text, "needs Bash") || !strings.Contains(text, glyphDone) || !strings.Contains(text, "> all green") {
		t.Errorf("stale-attention outcome = %q, want the turn outcome, not the notification", text)
	}
}

// Typed state glyphs from notification_type, default branch mandatory.
// notified is fresh so the attention is horizon-live.
func TestAttentionGlyphTyped(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		typ       string
		wantGlyph string
		wantStyle string
	}{
		{"permission_prompt", glyphPerm, module.StyleWarn},
		{"idle_prompt", glyphAttention, module.StyleWarn},
		{"agent_needs_input", glyphAttention, module.StyleWarn},
		{"", glyphAttention, module.StyleWarn}, // pre-rank-1 spools keep A4
		{"auth_success", glyphAttention, module.StyleDim},
		{"some_future_type", glyphAttention, module.StyleDim},
	} {
		s := session{attention: true, notifType: tt.typ, notified: now}
		if got := s.stateSpan(now); got.Text != " "+tt.wantGlyph || got.Style != tt.wantStyle {
			t.Errorf("stateSpan(type=%q) = %+v, want %q %s", tt.typ, got, tt.wantGlyph, tt.wantStyle)
		}
	}
}

// attentionLive: any prompt (spool or tail) strictly after notification_ts
// answers the notification, and a signal whose age (newest of notified/
// prompts/stopped; all-zero = stale) exceeds attentionHorizon is stale
// even unanswered -- ages AT the horizon stay live, past it stale.
func TestAttentionLive(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name string
		s    session
		want bool
	}{
		{"no attention", session{}, false},
		{"unanswered", session{attention: true, notified: now}, true},
		{"unanswered 5m ago stays live", session{attention: true, notified: now.Add(-5 * time.Minute)}, true},
		{"tail prompt answers", session{attention: true, notified: now.Add(-time.Minute), tailPromptTS: now}, false},
		{"spool prompt answers", session{attention: true, notified: now.Add(-time.Minute), promptTS: now}, false},
		{"prompt at the notification instant stays live", session{attention: true, notified: now, promptTS: now}, true},
		{"prompt before the notification stays live", session{attention: true, notified: now, promptTS: now.Add(-time.Minute)}, true},
		{"no notification_ts, recent prompt stays live", session{attention: true, promptTS: now}, true},
		{"no notification_ts, old prompt goes stale", session{attention: true, promptTS: now.Add(-2 * time.Hour)}, false},
		{"unanswered 17h ago goes stale", session{attention: true, notified: now.Add(-17 * time.Hour)}, false},
		{"answered within the horizon stays answered", session{attention: true, notified: now.Add(-50 * time.Minute), tailPromptTS: now.Add(-5 * time.Minute)}, false},
		{"all-zero timestamps are stale", session{attention: true}, false},
		{"at exactly the horizon stays live", session{attention: true, notified: now.Add(-attentionHorizon)}, true},
		{"just past the horizon goes stale", session{attention: true, notified: now.Add(-attentionHorizon - time.Second)}, false},
		{"a recent stop keeps an old bell inside the horizon", session{attention: true, notified: now.Add(-17 * time.Hour), stopped: now.Add(-time.Minute)}, true},
	} {
		if got := tt.s.attentionLive(now); got != tt.want {
			t.Errorf("%s: attentionLive = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// An answered or horizon-stale notification lowers the bell to dim -- not
// a warn badge and not a blank.
func TestStateSpanStaleAttention(t *testing.T) {
	now := time.Now()
	s := session{attention: true, notifType: "permission_prompt",
		notified: now.Add(-time.Minute), tailPromptTS: now, stopped: now}
	if got := s.stateSpan(now); got != (module.Span{Text: " " + glyphAttention, Style: module.StyleDim}) {
		t.Errorf("stale stateSpan = %+v, want the dim bell", got)
	}
	// live attention keeps the typed warn glyph
	s.tailPromptTS = time.Time{}
	if got := s.stateSpan(now); got != (module.Span{Text: " " + glyphPerm, Style: module.StyleWarn}) {
		t.Errorf("live stateSpan = %+v, want the typed warn glyph", got)
	}
	// an unanswered notification past the horizon dims exactly the same
	s = session{attention: true, notifType: "permission_prompt",
		notified: now.Add(-17 * time.Hour)}
	if got := s.stateSpan(now); got != (module.Span{Text: " " + glyphAttention, Style: module.StyleDim}) {
		t.Errorf("horizon-stale stateSpan = %+v, want the dim bell", got)
	}
}

// An error-ended turn (StopFailure) renders the cross, warn.
func TestStateSpanError(t *testing.T) {
	now := time.Now()
	s := session{stopped: now, errMsg: "rate_limit"}
	if got := s.stateSpan(now); got.Text != " "+glyphError || got.Style != module.StyleWarn {
		t.Errorf("error stateSpan = %+v", got)
	}
	// a new prompt puts the turn back in flight: blank again
	s.promptTS = now.Add(time.Minute)
	if got := s.stateSpan(now); got.Text != "  " {
		t.Errorf("in-flight-after-error stateSpan = %+v, want blank", got)
	}
}

func TestOutcomeRow(t *testing.T) {
	now := time.Now()

	// attention: typed glyph + title over message
	s := session{attention: true, notifType: "permission_prompt",
		notification: "msg body", notifTitle: "needs Bash", notified: now.Add(-3 * time.Minute)}
	text := lineText(s.outcomeRow(now))
	if !strings.Contains(text, glyphPerm) || !strings.Contains(text, "needs Bash") || !strings.Contains(text, " 3m") {
		t.Errorf("attention outcome = %q", text)
	}
	s.notifTitle = ""
	if text = lineText(s.outcomeRow(now)); !strings.Contains(text, "msg body") {
		t.Errorf("attention outcome fallback = %q, want the message", text)
	}

	// error-ended turn
	s = session{stopped: now.Add(-42 * time.Second), errMsg: "rate_limit"}
	text = lineText(s.outcomeRow(now))
	if !strings.Contains(text, glyphError) || !strings.Contains(text, "rate_limit") || !strings.Contains(text, "42s") {
		t.Errorf("error outcome = %q", text)
	}

	// done: check + last assistant + parked background work
	s = session{stopped: now.Add(-42 * time.Second), promptTS: now.Add(-2 * time.Minute),
		lastAssistant: "done, tests green", bgTasks: 2, crons: 1}
	text = lineText(s.outcomeRow(now))
	for _, want := range []string{glyphDone, "42s", "> done, tests green", "bg:2 parked", "cron:1"} {
		if !strings.Contains(text, want) {
			t.Errorf("done outcome = %q, want %q in it", text, want)
		}
	}

	// in flight
	s = session{promptTS: now.Add(-10 * time.Second)}
	text = lineText(s.outcomeRow(now))
	if !strings.Contains(text, "turn running") || !strings.Contains(text, "10s") {
		t.Errorf("in-flight outcome = %q", text)
	}

	// nothing on record
	s = session{}
	if r := s.outcomeRow(now); r.Text != "no turns recorded" || r.Style != module.StyleDim {
		t.Errorf("empty outcome = %+v", r)
	}
}

// Header row: collapsed columns + model/effort appended dim.
func TestDetailHeaderModelEffort(t *testing.T) {
	now := time.Now()
	s := session{id: "aaaaaaaa-x", mtime: now, model: "Opus 4.8", effort: "xhigh"}
	rows := detailRows(s, nil, now)
	if len(rows) != panelDetailRows {
		t.Fatalf("detail rows = %d, want %d", len(rows), panelDetailRows)
	}
	h := rows[0]
	last := h.Spans[len(h.Spans)-1]
	if last.Text != " Opus 4.8 xhigh" || last.Style != module.StyleDim {
		t.Errorf("header tail span = %+v, want dim model+effort", last)
	}
	// no model/effort: no empty span appended
	rows = detailRows(session{id: "bbbbbbbb-x", mtime: now}, nil, now)
	h = rows[0]
	if got := h.Spans[len(h.Spans)-1].Text; strings.TrimSpace(got) == "" {
		t.Errorf("header tail span = %q, want no blank tail", got)
	}
}

// Agent rows cap at panelAgentRows with a "+N agents" overflow; running
// agents lead.
func TestDetailAgentRows(t *testing.T) {
	now := time.Now()
	var agents []agentRow
	for i := range 7 {
		agents = append(agents, agentRow{
			id: fmt.Sprintf("a%d", i), typ: "reviewer",
			desc: fmt.Sprintf("lens %d", i), ts: now.Add(-time.Duration(i) * time.Minute),
			running: i < 2,
		})
	}
	s := session{id: "aaaaaaaa-x", mtime: now}
	rows := detailRows(s, agents, now)
	if len(rows) != panelDetailRows {
		t.Fatalf("detail rows = %d, want %d", len(rows), panelDetailRows)
	}
	for i := range panelAgentRows {
		r := rows[2+i]
		if r.Kind != module.RowSpans || len(r.Act) != 0 {
			t.Fatalf("agent row %d = %+v, want actless spans", i, r)
		}
		if text := lineText(r); !strings.Contains(text, "reviewer") || !strings.Contains(text, glyphAgents) {
			t.Errorf("agent row %d = %q", i, text)
		}
	}
	if rows[2].Style != module.StyleAccent || rows[4].Style != module.StyleDim {
		t.Errorf("agent row styles = %q/%q, want running accent, done dim", rows[2].Style, rows[4].Style)
	}
	over := rows[2+panelAgentRows]
	if over.Text != "+2 agents" || over.Style != module.StyleDim {
		t.Errorf("agent overflow row = %+v", over)
	}
}

// agentRows: meta.json identity, transcript-mtime activity, sidecar
// refinement to a hard running/done bit, running-first ordering.
func TestAgentRowsFromMetaAndSidecars(t *testing.T) {
	dir := t.TempDir()
	spool := t.TempDir()
	now := time.Now()
	sid := "aaaaaaaa-1111-2222-3333-444444444444"
	sub := filepath.Join(dir, "sess", "subagents")
	// live by mtime
	touch(t, filepath.Join(sub, "agent-a1.meta.json"),
		`{"agentType":"reviewer","description":"lens A"}`, now)
	touch(t, filepath.Join(sub, "agent-a1.jsonl"), "{}", now)
	// stale by mtime, but the sidecar has no stopped_ts: hard-running
	touch(t, filepath.Join(sub, "agent-a2.meta.json"),
		`{"agentType":"skeptic","description":"refute"}`, now.Add(-10*time.Minute))
	touch(t, filepath.Join(sub, "agent-a2.jsonl"), "{}", now.Add(-10*time.Minute))
	touch(t, filepath.Join(spool, sid+".agents", "a2.json"),
		fmt.Sprintf(`{"agent_type":"skeptic","started_ts":%d}`, now.Add(-10*time.Minute).Unix()), now)
	// live by mtime, but the sidecar says stopped: hard-done
	touch(t, filepath.Join(sub, "agent-a3.meta.json"),
		`{"agentType":"mapper","description":"map"}`, now)
	touch(t, filepath.Join(sub, "agent-a3.jsonl"), "{}", now)
	touch(t, filepath.Join(spool, sid+".agents", "agent-a3.json"),
		fmt.Sprintf(`{"agent_type":"mapper","started_ts":%d,"stopped_ts":%d}`,
			now.Add(-5*time.Minute).Unix(), now.Add(-time.Minute).Unix()), now)
	// garbage meta is skipped
	touch(t, filepath.Join(sub, "agent-a4.meta.json"), "{nope", now)

	s := session{id: sid, dirs: []string{filepath.Join(dir, "sess")}}
	rows := agentRows(s, spool, now)
	if len(rows) != 3 {
		t.Fatalf("agentRows = %+v, want 3", rows)
	}
	byID := map[string]agentRow{}
	for _, r := range rows {
		byID[r.id] = r
	}
	if !byID["a1"].running || byID["a1"].typ != "reviewer" {
		t.Errorf("a1 = %+v, want running reviewer", byID["a1"])
	}
	if !byID["a2"].running {
		t.Errorf("a2 = %+v, want sidecar to hard-mark running over stale mtime", byID["a2"])
	}
	if byID["a3"].running {
		t.Errorf("a3 = %+v, want sidecar stopped_ts to hard-mark done over live mtime", byID["a3"])
	}
	// running first
	if !rows[0].running || !rows[1].running || rows[2].running {
		t.Errorf("order = %+v, want running agents first", rows)
	}
}

// End-to-end Poll: fs + spool -> zones, occupant agents, acts.
func TestPanelPoll(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	now := time.Now()
	live := "aaaaaaaa-1111-2222-3333-444444444444"
	idle := "bbbbbbbb-1111-2222-3333-444444444444"

	// starts pin group order: the live session's cwd group (spool dir)
	// starts after the dir-less idle one, so it leads the list
	pdir := filepath.Join(projects, "-Users-x-dev-foo")
	touch(t, filepath.Join(pdir, live+".jsonl"), tsEntry("2026-07-03T11:00:00Z")+"\n", now)
	touch(t, filepath.Join(pdir, live, "subagents", "agent-a1.meta.json"),
		`{"agentType":"reviewer","description":"lens A"}`, now)
	touch(t, filepath.Join(pdir, live, "subagents", "agent-a1.jsonl"), "{}", now)
	touch(t, filepath.Join(pdir, idle+".jsonl"), tsEntry("2026-07-03T10:00:00Z")+"\n", now.Add(-30*time.Minute))
	sp := fmt.Sprintf(`{"session_id":%q,"prompt":"fix the bug","ts":%d,
		"workspace":{"current_dir":"/Users/x/dev/foo"},
		"stopped_ts":%d,"last_assistant":"all green","effort":"xhigh","model":"Opus 4.8",
		"bg_tasks":1,"crons":0}`, live, now.Add(-2*time.Minute).Unix(), now.Add(-time.Minute).Unix())
	touch(t, filepath.Join(spool, live+".json"), sp, now)

	data, err := NewPanel(New()).Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if want := "claude 1/2 . " + glyphAgents + "1"; data.Title != want {
		t.Errorf("Title = %q, want %q", data.Title, want)
	}
	if len(data.Rows) != panelDetailRows+2 {
		t.Fatalf("Rows = %d, want %d", len(data.Rows), panelDetailRows+2)
	}
	header := data.Rows[0]
	if actOf(header) != focusOf(live) {
		t.Errorf("header act = %q", actOf(header))
	}
	if text := lineText(header); !strings.Contains(text, "Opus 4.8 xhigh") || !strings.Contains(text, "> fix the bug") {
		t.Errorf("header = %q", text)
	}
	outcome := lineText(data.Rows[1])
	if !strings.Contains(outcome, "> all green") || !strings.Contains(outcome, "bg:1 parked") {
		t.Errorf("outcome = %q", outcome)
	}
	if agent := lineText(data.Rows[2]); !strings.Contains(agent, "reviewer") || !strings.Contains(agent, "lens A") {
		t.Errorf("agent row = %q", agent)
	}
	if actOf(data.Rows[panelDetailRows+1]) != focusOf(idle) {
		t.Errorf("idle list act = %q", actOf(data.Rows[panelDetailRows+1]))
	}
}

// End-to-end: a steering entry newer than notification_ts (transcript
// mtime past the spool's) marks the notification answered BEFORE the
// occupant pick -- the freshness pass runs ahead of pickOccupant, so the
// stale bell neither pins the detail zone nor stays warn in the list.
func TestPanelPollSteeringAnswersNotification(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	now := time.Now()
	stale := "aaaaaaaa-1111-2222-3333-444444444444"
	other := "bbbbbbbb-1111-2222-3333-444444444444"
	pdir := filepath.Join(projects, "p")
	body := tsEntry("2026-07-03T10:00:00Z") + "\n" +
		fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":"answered it"}}`+"\n",
			now.Add(-time.Minute).UTC().Format(time.RFC3339))
	touch(t, filepath.Join(pdir, stale+".jsonl"), body, now.Add(-30*time.Second))
	sp := fmt.Sprintf(`{"session_id":%q,"prompt":"old prompt","ts":%d,"attention":true,"notification":"needs input","notification_ts":%d}`,
		stale, now.Add(-time.Hour).Unix(), now.Add(-30*time.Minute).Unix())
	touch(t, filepath.Join(spool, stale+".json"), sp, now.Add(-30*time.Minute))
	touch(t, filepath.Join(pdir, other+".jsonl"), tsEntry("2026-07-03T11:00:00Z")+"\n", now)

	data, err := NewPanel(New()).Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != panelDetailRows+2 {
		t.Fatalf("Rows = %d, want %d", len(data.Rows), panelDetailRows+2)
	}
	// the answered notification does not take the zone; the live pick does
	if got := actOf(data.Rows[0]); got != focusOf(other) {
		t.Errorf("occupant act = %q, want %q (stale attention must not pin the zone)", got, focusOf(other))
	}
	// list order: one dir-less group, newest start first -> other, stale
	row := data.Rows[panelDetailRows+1]
	if actOf(row) != focusOf(stale) {
		t.Fatalf("list row act = %q, want the stale session", actOf(row))
	}
	if got := row.Spans[spanState]; got != (module.Span{Text: " " + glyphAttention, Style: module.StyleDim}) {
		t.Errorf("stale bell = %+v, want dim (answered)", got)
	}
	// and the corrected prompt rides the row
	if text := lineText(row); !strings.HasSuffix(text, " > answered it") {
		t.Errorf("stale row = %q, want the tail prompt", text)
	}
}

// End-to-end horizon: an unanswered notification older than
// attentionHorizon neither pins the detail zone nor sets Data.Attention,
// and its list bell dims; a recent one pins, warns, and sets the bit.
func TestPanelPollAttentionHorizon(t *testing.T) {
	now := time.Now()
	bell := "aaaaaaaa-1111-2222-3333-444444444444"
	other := "bbbbbbbb-1111-2222-3333-444444444444"

	poll := func(notifiedAgo time.Duration) module.Data {
		t.Helper()
		projects := t.TempDir()
		spool := t.TempDir()
		pdir := filepath.Join(projects, "p")
		touch(t, filepath.Join(pdir, bell+".jsonl"), tsEntry("2026-07-03T10:00:00Z")+"\n", now.Add(-5*time.Minute))
		sp := fmt.Sprintf(`{"session_id":%q,"prompt":"stuck","ts":%d,"attention":true,"notification":"needs input","notification_ts":%d}`,
			bell, now.Add(-notifiedAgo-time.Hour).Unix(), now.Add(-notifiedAgo).Unix())
		touch(t, filepath.Join(spool, bell+".json"), sp, now.Add(-notifiedAgo))
		touch(t, filepath.Join(pdir, other+".jsonl"), tsEntry("2026-07-03T11:00:00Z")+"\n", now)
		data, err := NewPanel(New()).Poll(context.Background(), map[string]any{
			"projectsDir": projects,
			"dir":         spool,
			"sessionsDir": t.TempDir(),
		})
		if err != nil {
			t.Fatalf("Poll: %v", err)
		}
		return data
	}
	bellSpan := func(data module.Data) module.Span {
		t.Helper()
		for i := panelDetailRows; i < len(data.Rows); i++ {
			if actOf(data.Rows[i]) == focusOf(bell) {
				return data.Rows[i].Spans[spanState]
			}
		}
		t.Fatal("bell session missing from the list")
		return module.Span{}
	}

	// 17h-old bell: the live session takes the zone, the bit stays clear,
	// the bell dims in the list
	data := poll(17 * time.Hour)
	if got := actOf(data.Rows[0]); got != focusOf(other) {
		t.Errorf("occupant act = %q, want %q (horizon-stale attention must not pin)", got, focusOf(other))
	}
	if data.Attention {
		t.Error("horizon-stale attention set Data.Attention")
	}
	if got := bellSpan(data); got != (module.Span{Text: " " + glyphAttention, Style: module.StyleDim}) {
		t.Errorf("horizon-stale bell = %+v, want dim", got)
	}

	// 5m-old bell: pins the zone, warns, sets the bit
	data = poll(5 * time.Minute)
	if got := actOf(data.Rows[0]); got != focusOf(bell) {
		t.Errorf("occupant act = %q, want %q (live attention pins)", got, focusOf(bell))
	}
	if !data.Attention {
		t.Error("live attention did not set Data.Attention")
	}
	if got := bellSpan(data); got != (module.Span{Text: " " + glyphAttention, Style: module.StyleWarn}) {
		t.Errorf("live bell = %+v, want warn", got)
	}
}

func TestPanelPollBadWindow(t *testing.T) {
	if _, err := NewPanel(New()).Poll(context.Background(), map[string]any{
		"projectsDir": t.TempDir(), "window": "soon",
	}); err == nil {
		t.Error("Poll(bad window): want error, got nil")
	}
}
