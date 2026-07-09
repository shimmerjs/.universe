// Panel is the claude control-panel surface (module "claude-panel"): the
// full-fill widget behind the nav-tray "claude" target. Two geometrically
// immutable zones per poll (the touch-safety invariant): a pinned detail
// zone (one expanded session: header + turn outcome as one focus tap
// target, up to panelAgentRows agent rows, "+N agents" overflow, blank-
// padded to a fixed height) above a list zone in fixed start order (the A4
// collapsed line, focus Act per row) and a "+N more" overflow row. Zone
// occupancy moves on discrete state transitions -- attention (oldest
// notification first) > live (sticky incumbent) > newest start -- never on
// per-poll mtime churn. It shares the claudesessions package's discovery,
// spool parsing, and row primitives rather than duplicating them.
package claudesessions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

const (
	// panelDetailRows is the detail zone height (rows 1-8): fixed while
	// occupied so the list rows below cannot shift under a finger.
	panelDetailRows = 8
	// panelListMax caps the list zone (rows 9-20); row 21 is "+N more".
	panelListMax = 12
	// panelAgentRows caps agent rows inside the detail zone.
	panelAgentRows = 5
	// panelPromptWidth caps variable tails (prompt, outcome, description)
	// for the wide panel; the true cell fit stays dock-side (fitCell).
	panelPromptWidth = 100
)

// Panel implements module.Module for "claude-panel". It composes a Mod for
// discovery + fixed ordering and keeps the sticky detail-zone occupant
// across polls.
type Panel struct {
	mod      *Mod
	mu       sync.Mutex
	occupant string // session id holding the detail zone
}

// NewPanel returns the panel module over m's discovery/ordering. Pass a
// DEDICATED Mod: the start cache evicts by the caller's window, so sharing
// an instance with a differently-windowed claude-sessions widget thrashes
// it (order itself cannot diverge -- transcript heads are immutable).
func NewPanel(m *Mod) *Panel { return &Panel{mod: m} }

func (*Panel) Name() string { return "claude-panel" }

func (p *Panel) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	root, spoolDir, sessionsDir, window, err := pollParams(params)
	if err != nil {
		return module.Data{}, fmt.Errorf("claude-panel: %w", err)
	}
	listMax := module.IntParam(params, "max", panelListMax)
	now := time.Now()
	sessions, err := discover(ctx, root, spoolDir, sessionsDir, window, now)
	if err != nil {
		return module.Data{}, err
	}
	p.mod.orderSessions(sessions)
	// freshness BEFORE the pick: an answered notification (attention
	// staleness) must be known at pick time or it pins the detail zone
	p.mod.freshenPrompts(sessions)
	p.mod.displayDirs(sessions)
	occ := p.pickOccupant(sessions, now)
	var agents []agentRow
	if occ >= 0 {
		agents = agentRows(sessions[occ], spoolDir, now)
	}
	title, rows := renderPanel(sessions, occ, agents, listMax, now)
	d := module.Data{Title: title, Rows: rows}
	for _, s := range sessions {
		// live attention always pins the detail zone (pickOccupant), so the
		// bit tracks attention that is actually on glass
		if s.attentionLive(now) {
			d.Attention = true
			break
		}
	}
	return d, nil
}

// pickOccupant chooses the detail-zone session: attention (oldest
// notification first, id tiebreak) > live (the incumbent keeps the zone
// while it stays live, else the most recently live) > newest start. -1
// collapses the zone to a placeholder -- only at no sessions or a single
// idle one, the states where list geometry may shift because tap races
// cannot matter. Transitions are discrete (notification arrival/answer,
// liveness edges, session birth/death), never per-poll mtime churn.
func (p *Panel) pickOccupant(sessions []session, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	att := -1
	for i, s := range sessions {
		// an answered or horizon-stale notification cannot pin the zone:
		// the session falls through to the live/newest branches
		if !s.attentionLive(now) {
			continue
		}
		if att == -1 || s.notified.Before(sessions[att].notified) ||
			(s.notified.Equal(sessions[att].notified) && s.id < sessions[att].id) {
			att = i
		}
	}
	if att >= 0 {
		p.occupant = sessions[att].id
		return att
	}
	live, incumbent := -1, -1
	for i, s := range sessions {
		if !isLive(s.mtime, now) {
			continue
		}
		if s.id == p.occupant {
			incumbent = i
		}
		if live == -1 || s.mtime.After(sessions[live].mtime) {
			live = i
		}
	}
	if incumbent >= 0 {
		live = incumbent
	}
	if live >= 0 {
		p.occupant = sessions[live].id
		return live
	}
	if len(sessions) == 0 || (len(sessions) == 1 && !sessions[0].attention) {
		p.occupant = ""
		return -1
	}
	// newest start, id tiebreak: under cwd grouping the list head no
	// longer means newest start, so scan explicitly
	newest := 0
	for i := 1; i < len(sessions); i++ {
		if sessions[i].start.After(sessions[newest].start) ||
			(sessions[i].start.Equal(sessions[newest].start) && sessions[i].id < sessions[newest].id) {
			newest = i
		}
	}
	p.occupant = sessions[newest].id
	return newest
}

// focusArgv is the row Act: handleRowAct execs it on the bus host (vetting
// the argv against the published acts and surfacing a nonzero exit, nothing
// more), so all smarts -- fresh ls, the resolution chain, miss logging --
// live in the `khudson claude focus` wrapper (internal/bus/claudeverb.go).
func focusArgv(sid string) []string { return []string{"khudson", "claude", "focus", sid} }

// renderPanel lays out the fixed zones. The border title carries the
// machine rollup (live/total tally + live fleet counts) so no row is spent
// on a header.
func renderPanel(sessions []session, occ int, agents []agentRow, listMax int, now time.Time) (string, []module.Row) {
	if len(sessions) == 0 {
		return "claude", []module.Row{{Kind: module.RowText, Text: "no active sessions", Style: module.StyleDim}}
	}
	live, ag, wf := 0, 0, 0
	for _, s := range sessions {
		if isLive(s.mtime, now) {
			live++
		}
		ag += s.agents
		wf += s.workflows
	}
	title := fmt.Sprintf("claude %d/%d", live, len(sessions))
	rollup := ""
	if ag > 0 {
		rollup += fmt.Sprintf(" %s%d", glyphAgents, ag)
	}
	if wf > 0 {
		rollup += fmt.Sprintf(" %s%d", glyphWorkflows, wf)
	}
	if rollup != "" {
		title += " ." + rollup
	}

	rows := make([]module.Row, 0, panelDetailRows+listMax+1)
	if occ >= 0 {
		rows = append(rows, detailRows(sessions[occ], agents, now)...)
	} else {
		// empty detail zone: a dim placeholder, list grows upward -- the
		// only geometry change, gated to the zero/one-session states above
		rows = append(rows, module.Row{Kind: module.RowText, Text: "no session in focus", Style: module.StyleDim})
	}
	listMax = max(listMax, 0)
	shown := sessions
	if len(shown) > listMax {
		shown = shown[:listMax]
	}
	for _, s := range shown {
		r := s.lineW(now, panelPromptWidth)
		r.Act = focusArgv(s.id)
		rows = append(rows, r)
	}
	if n := len(sessions) - len(shown); n > 0 {
		rows = append(rows, module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d more", n), Style: module.StyleDim})
	}
	return title, rows
}

// detailRows is the expanded block: header (the collapsed columns + model
// and effort appended dim) and the turn-outcome line, BOTH carrying the
// focus Act so header+outcome behave as one tap target; then up to
// panelAgentRows agent rows (no Act) and a "+N agents" overflow, blank-
// padded to exactly panelDetailRows rows so the list zone below never
// shifts while the zone is occupied.
func detailRows(s session, agents []agentRow, now time.Time) []module.Row {
	act := focusArgv(s.id)
	rows := make([]module.Row, 0, panelDetailRows)
	h := s.lineW(now, panelPromptWidth)
	if extra := strings.TrimSpace(s.model + " " + s.effort); extra != "" {
		h.Spans = append(h.Spans, module.Span{Text: " " + extra, Style: module.StyleDim})
	}
	h.Act = act
	rows = append(rows, h)
	o := s.outcomeRow(now)
	o.Act = act
	rows = append(rows, o)
	shown := agents
	if len(shown) > panelAgentRows {
		shown = shown[:panelAgentRows]
	}
	if len(shown) == 0 {
		// the zone stays panelDetailRows tall (geometry doctrine), but
		// five bare rows read as a dead gap on glass -- one dim hint
		// makes the reserved space legible
		rows = append(rows, module.Row{Kind: module.RowText, Text: "    no agents", Style: module.StyleDim})
	}
	for _, a := range shown {
		rows = append(rows, a.row(now))
	}
	if n := len(agents) - len(shown); n > 0 {
		rows = append(rows, module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d agents", n), Style: module.StyleDim})
	}
	for len(rows) < panelDetailRows {
		rows = append(rows, module.Row{Kind: module.RowText, Text: ""})
	}
	return rows
}

// outcomeRow is the state-detail line under the header: the unanswered
// notification (typed glyph + title/message), an error-ended turn
// (StopFailure reason), a finished turn's last assistant line plus parked
// background work, or a dim in-flight marker. The age column leads at the
// header's fixed width.
func (s session) outcomeRow(now time.Time) module.Row {
	age := func(t time.Time, style string) module.Span {
		return module.Span{Text: fmt.Sprintf("%*s", timeWidth, relTime(now.Sub(t))), Style: style}
	}
	switch {
	// answered or horizon-stale notifications fall through to the turn
	// outcome
	case s.attentionLive(now):
		g, st := s.attentionGlyph()
		txt := s.notifTitle
		if txt == "" {
			txt = s.notification
		}
		if txt == "" {
			txt = "notification waiting"
		}
		ts := s.notified
		if ts.IsZero() {
			ts = s.mtime
		}
		return module.SpansRow(
			age(ts, st),
			module.Span{Text: " " + g, Style: st},
			module.Span{Text: " " + truncate(txt, panelPromptWidth), Style: st},
		)
	case s.turnDone() && s.errMsg != "":
		return module.SpansRow(
			age(s.stopped, module.StyleWarn),
			module.Span{Text: " " + glyphError, Style: module.StyleWarn},
			module.Span{Text: " " + truncate(s.errMsg, panelPromptWidth), Style: module.StyleWarn},
		)
	case s.turnDone():
		spans := []module.Span{
			age(s.stopped, module.StyleDim),
			{Text: " " + glyphDone, Style: module.StyleDim},
		}
		if s.lastAssistant != "" {
			spans = append(spans, module.Span{Text: " > " + truncate(s.lastAssistant, panelPromptWidth), Style: module.StyleDim})
		}
		if s.bgTasks > 0 {
			spans = append(spans, module.Span{Text: fmt.Sprintf(" bg:%d parked", s.bgTasks), Style: module.StyleHighlight})
		}
		if s.crons > 0 {
			spans = append(spans, module.Span{Text: fmt.Sprintf(" cron:%d", s.crons), Style: module.StyleDim})
		}
		return module.SpansRow(spans...)
	case !s.promptTS.IsZero():
		return module.SpansRow(
			age(s.promptTS, module.StyleAccent),
			module.Span{Text: " turn running", Style: module.StyleDim},
		)
	default:
		return module.Row{Kind: module.RowText, Text: "no turns recorded", Style: module.StyleDim}
	}
}

// agentRow is one subagent of the expanded session: identity from
// agent-<id>.meta.json (agentType + description), activity from the agent
// transcript mtime, refined to a hard running/done bit by the rank-3 spool
// sidecar (<spool>/<sid>.agents/<id>.json) when one exists -- those hooks
// are staged, so the sidecar dir is usually absent and mtime stands in.
type agentRow struct {
	id      string
	typ     string
	desc    string
	ts      time.Time // last activity: sidecar stop over transcript mtime
	running bool
}

// row is one agent line inside the detail zone: [age][gear] agentType +
// description tail. No Act -- agents are not tap targets.
func (a agentRow) row(now time.Time) module.Row {
	style := module.StyleDim
	if a.running {
		style = module.StyleAccent
	}
	ageText := strings.Repeat(" ", timeWidth)
	if !a.ts.IsZero() {
		ageText = fmt.Sprintf("%*s", timeWidth, relTime(now.Sub(a.ts)))
	}
	typ := a.typ
	if typ == "" {
		typ = a.id
	}
	spans := []module.Span{
		{Text: ageText, Style: style},
		{Text: " " + glyphAgents, Style: style},
		{Text: " " + typ, Style: module.StyleTitle, Ident: typ},
	}
	if a.desc != "" {
		spans = append(spans, module.Span{Text: " " + truncate(a.desc, panelPromptWidth), Style: module.StyleDim})
	}
	r := module.SpansRow(spans...)
	r.Style = style
	return r
}

// agentRows collects the expanded session's subagents across all its
// session dirs (satellites included). Bounded reads, one session only: the
// meta files are one small JSON each and the list caps at panelAgentRows
// plus an overflow count. Running agents sort first, then newest activity.
func agentRows(s session, spoolDir string, now time.Time) []agentRow {
	side := readAgentSidecars(spoolDir, s.id)
	var rows []agentRow
	for _, dir := range s.dirs {
		sub := filepath.Join(dir, "subagents")
		entries, err := os.ReadDir(sub)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			id, ok := strings.CutPrefix(e.Name(), "agent-")
			id, ok2 := strings.CutSuffix(id, ".meta.json")
			if !ok || !ok2 {
				continue
			}
			var meta struct {
				AgentType   string `json:"agentType"`
				Description string `json:"description"`
			}
			if b, err := os.ReadFile(filepath.Join(sub, e.Name())); err != nil || json.Unmarshal(b, &meta) != nil {
				continue
			}
			r := agentRow{id: id, typ: meta.AgentType, desc: meta.Description}
			if info, err := os.Stat(filepath.Join(sub, "agent-"+id+".jsonl")); err == nil {
				r.ts = info.ModTime()
				r.running = isLive(info.ModTime(), now)
			} else if info, err := e.Info(); err == nil {
				r.ts = info.ModTime()
			}
			if sc, ok := side[id]; ok {
				r.running = sc.stopped.IsZero()
				if !sc.stopped.IsZero() {
					if sc.stopped.After(r.ts) {
						r.ts = sc.stopped
					}
				} else if r.ts.IsZero() {
					r.ts = sc.started
				}
				if r.typ == "" {
					r.typ = sc.typ
				}
			}
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].running != rows[j].running {
			return rows[i].running
		}
		if !rows[i].ts.Equal(rows[j].ts) {
			return rows[i].ts.After(rows[j].ts)
		}
		return rows[i].id < rows[j].id
	})
	return rows
}

// sidecar is one rank-3 SubagentStart/Stop record. Missing stopped_ts means
// the agent is running -- a hard bit, unlike the mtime heuristic.
type sidecar struct {
	typ     string
	started time.Time
	stopped time.Time
}

// readAgentSidecars loads <spool>/<sid>.agents/*.json keyed by agent id
// (an optional "agent-" filename prefix is tolerated). A missing dir --
// the sidecar hooks are staged behind a later milestone -- is empty.
func readAgentSidecars(spoolDir, sid string) map[string]sidecar {
	out := map[string]sidecar{}
	if spoolDir == "" {
		return out
	}
	entries, err := os.ReadDir(filepath.Join(spoolDir, sid+".agents"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		id = strings.TrimPrefix(id, "agent-")
		b, err := os.ReadFile(filepath.Join(spoolDir, sid+".agents", e.Name()))
		if err != nil {
			continue
		}
		var raw struct {
			AgentType string `json:"agent_type"`
			StartedTS int64  `json:"started_ts"`
			StoppedTS int64  `json:"stopped_ts"`
		}
		if json.Unmarshal(b, &raw) != nil {
			continue
		}
		sc := sidecar{typ: raw.AgentType}
		if raw.StartedTS > 0 {
			sc.started = time.Unix(raw.StartedTS, 0)
		}
		if raw.StoppedTS > 0 {
			sc.stopped = time.Unix(raw.StoppedTS, 0)
		}
		out[id] = sc
	}
	return out
}
