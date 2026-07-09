// Panel is the claude control-panel surface (module "claude-panel"): the
// full-fill widget behind the nav-tray "claude" target. Two geometrically
// immutable zones per poll (the touch-safety invariant): a pinned detail
// zone (one expanded session: header + turn outcome as one focus tap
// target, then the fleet tree -- one root per workflow plus one for loose
// subagents, children indented under it, blank-padded to a fixed height)
// above a list zone in fixed start order (the A4 collapsed line, focus Act
// per row) and a "+N more" overflow row. Zone occupancy moves on discrete
// state transitions -- attention (oldest notification first) > live
// (sticky incumbent) > newest start -- never on per-poll mtime churn.
// Tapping anywhere in a fleet tree folds it to its root as a one-line
// summary (and back): the fold act is handled in-process (ActHandler),
// never exec'd, and the fold map is the only tap-mutable render state.
// Rows carrying live attention are flagged Row.Attention so the dock's
// input-requested emphasis lands on the exact session, not just the frame.
// It shares the claudesessions package's discovery, spool parsing, and row
// primitives rather than duplicating them.
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
	// panelPromptWidth caps variable tails (prompt, outcome, description)
	// for the wide panel; the true cell fit stays dock-side (fitCell).
	panelPromptWidth = 100
)

// Panel implements module.Module for "claude-panel". It composes a Mod for
// discovery + fixed ordering and keeps the sticky detail-zone occupant and
// the fleet-tree fold map across polls.
type Panel struct {
	mod      *Mod
	mu       sync.Mutex
	occupant string          // session id holding the detail zone
	folded   map[string]bool // "<sid>/<node key>" -> tree folded to its root
}

// NewPanel returns the panel module over m's discovery/ordering. Pass a
// DEDICATED Mod: the start cache evicts by the caller's window, so sharing
// an instance with a differently-windowed claude-sessions widget thrashes
// it (order itself cannot diverge -- transcript heads are immutable).
func NewPanel(m *Mod) *Panel { return &Panel{mod: m} }

func (*Panel) Name() string { return "claude-panel" }

// foldVerb is the fleet-tree toggle act: published on every row of a tree,
// handled in-process by HandleAct (never exec'd -- argv[0] is deliberately
// not a command name, so a bus that missed the dispatch fails loudly).
const foldVerb = "panel:fold"

func foldArgv(sid, node string) []string { return []string{foldVerb, sid, node} }

var _ module.ActHandler = (*Panel)(nil)

// HandleAct toggles a fleet tree's fold state. The bus repolls the widget
// right after a handled act, so the flip lands on glass within a scheduler
// tick instead of the poll cadence.
func (p *Panel) HandleAct(argv []string) bool {
	if len(argv) != 3 || argv[0] != foldVerb {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.folded == nil {
		p.folded = map[string]bool{}
	}
	k := argv[1] + "/" + argv[2]
	if p.folded[k] {
		delete(p.folded, k)
	} else {
		p.folded[k] = true
	}
	return true
}

// foldSnapshot prunes fold state to the discovered session set (a dead
// session's folds must not leak forever) and returns a render-safe copy:
// HandleAct mutates the live map concurrently on the bus input worker.
func (p *Panel) foldSnapshot(sessions []session) map[string]bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.folded) == 0 {
		return nil
	}
	live := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		live[s.id] = true
	}
	out := make(map[string]bool, len(p.folded))
	for k, v := range p.folded {
		sid, _, ok := strings.Cut(k, "/")
		if !ok || !live[sid] {
			delete(p.folded, k)
			continue
		}
		out[k] = v
	}
	return out
}

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
	var nodes []fleetNode
	if occ >= 0 {
		nodes = fleetNodes(sessions[occ], spoolDir, now)
	}
	folded := p.foldSnapshot(sessions)
	title, rows := renderPanel(sessions, occ, nodes, folded, listMax, now)
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
func renderPanel(sessions []session, occ int, nodes []fleetNode, folded map[string]bool, listMax int, now time.Time) (string, []module.Row) {
	// "clod" is the panel's hauz name (the tray tab already says so)
	if len(sessions) == 0 {
		return "clod", []module.Row{{Kind: module.RowText, Text: "no active sessions", Style: module.StyleDim}}
	}
	live, ag, wf := 0, 0, 0
	for _, s := range sessions {
		if isLive(s.mtime, now) {
			live++
		}
		ag += s.agents
		wf += s.workflows
	}
	title := fmt.Sprintf("clod %d/%d", live, len(sessions))
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
		rows = append(rows, detailRows(sessions[occ], nodes, folded, now)...)
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
		// the input-requested emphasis rides the exact row awaiting input,
		// not just the widget frame
		r.Attention = s.attentionLive(now)
		rows = append(rows, r)
	}
	if n := len(sessions) - len(shown); n > 0 {
		rows = append(rows, module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d more", n), Style: module.StyleDim})
	}
	return title, rows
}

// detailRows is the expanded block: header (the collapsed columns + model
// and effort appended dim) and the turn-outcome line, BOTH carrying the
// focus Act so header+outcome behave as one tap target and both flagged
// Attention while the occupant's notification is live; then the fleet tree
// (fleetRows), blank-padded to exactly panelDetailRows rows so the list
// zone below never shifts while the zone is occupied.
func detailRows(s session, nodes []fleetNode, folded map[string]bool, now time.Time) []module.Row {
	act := focusArgv(s.id)
	attn := s.attentionLive(now)
	rows := make([]module.Row, 0, panelDetailRows)
	h := s.lineW(now, panelPromptWidth)
	if extra := strings.TrimSpace(s.model + " " + s.effort); extra != "" {
		h.Spans = append(h.Spans, module.Span{Text: " " + extra, Style: module.StyleDim})
	}
	h.Act = act
	h.Attention = attn
	rows = append(rows, h)
	o := s.outcomeRow(now)
	o.Act = act
	o.Attention = attn
	rows = append(rows, o)
	rows = append(rows, fleetRows(s.id, nodes, folded, panelDetailRows-len(rows), now)...)
	for len(rows) < panelDetailRows {
		rows = append(rows, module.Row{Kind: module.RowText, Text: ""})
	}
	return rows
}

// Tree vocabulary: connectors are written as escapes (rendered box glyphs,
// ASCII source) like the Nerd Font consts.
const (
	treeMid = "\u251c\u2500" // mid-child connector
	treeEnd = "\u2514\u2500" // last-child connector
)

// fleetNode is one fleet tree of the expanded session: a workflow's agents
// or the loose subagents. key is the fold identity ("wf:<dir>" / "agents"),
// stable across polls so fold state survives membership churn.
type fleetNode struct {
	key   string
	label string
	glyph string
	rows  []agentRow
	live  int
	ts    time.Time // newest child activity
}

func newFleetNode(key, label, glyph string, rows []agentRow) fleetNode {
	n := fleetNode{key: key, label: label, glyph: glyph, rows: rows}
	for _, a := range rows {
		if a.running {
			n.live++
		}
		if a.ts.After(n.ts) {
			n.ts = a.ts
		}
	}
	return n
}

// typeSummary is the collapsed root's payload: agent counts by type, most
// first ("3 reviewer 2 skeptic"), name tiebreak for a stable line.
func (n fleetNode) typeSummary() string {
	counts := map[string]int{}
	for _, a := range n.rows {
		typ := a.typ
		if typ == "" {
			typ = "agent"
		}
		counts[typ]++
	}
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Slice(types, func(i, j int) bool {
		if counts[types[i]] != counts[types[j]] {
			return counts[types[i]] > counts[types[j]]
		}
		return types[i] < types[j]
	})
	parts := make([]string, 0, len(types))
	for _, t := range types {
		parts = append(parts, fmt.Sprintf("%d %s", counts[t], t))
	}
	return strings.Join(parts, " ")
}

// rootRow is a tree's root line: [age][fold chevron][glyph] label live/total,
// plus the type summary while folded (the promised one-line summary). Live
// children accent the row; the label hue keys off the node identity.
func (n fleetNode) rootRow(now time.Time, folded bool, act []string) module.Row {
	style := module.StyleDim
	if n.live > 0 {
		style = module.StyleAccent
	}
	ageText := strings.Repeat(" ", timeWidth)
	if !n.ts.IsZero() {
		ageText = fmt.Sprintf("%*s", timeWidth, relTime(now.Sub(n.ts)))
	}
	chev := glyphFoldOpen
	if folded {
		chev = glyphFoldShut
	}
	spans := []module.Span{
		{Text: ageText, Style: style},
		{Text: " " + chev, Style: module.StyleDim},
		{Text: " " + n.glyph, Style: style},
		{Text: " " + n.label, Style: module.StyleTitle, Ident: n.label},
		{Text: fmt.Sprintf(" %d/%d", n.live, len(n.rows)), Style: style},
	}
	if folded {
		if sum := n.typeSummary(); sum != "" {
			spans = append(spans, module.Span{Text: " " + truncate(sum, panelPromptWidth), Style: module.StyleDim})
		}
	}
	r := module.SpansRow(spans...)
	r.Style = style
	r.Act = act
	return r
}

// fleetRows lays the trees into the detail zone's remaining budget. Roots
// first-class: when trees outnumber the budget the tail truncates into a
// dim "+N trees" row, and children (order: the trees' order, greedy) share
// what the roots leave. EVERY row of a tree -- root, child, child overflow
// -- carries the tree's fold act, so touching the tree anywhere toggles it.
func fleetRows(sid string, nodes []fleetNode, folded map[string]bool, budget int, now time.Time) []module.Row {
	if budget < 1 {
		return nil
	}
	if len(nodes) == 0 {
		// the zone stays panelDetailRows tall (geometry doctrine), but
		// bare rows read as a dead gap on glass -- one dim hint makes the
		// reserved space legible
		return []module.Row{{Kind: module.RowText, Text: "    no agents", Style: module.StyleDim}}
	}
	shown, trimmed := nodes, 0
	if len(nodes) > budget {
		shown = nodes[:budget-1]
		trimmed = len(nodes) - len(shown)
	}
	spare := budget - len(shown)
	if trimmed > 0 {
		spare-- // the "+N trees" row below
	}
	rows := make([]module.Row, 0, budget)
	for _, n := range shown {
		fold := folded[sid+"/"+n.key]
		act := foldArgv(sid, n.key)
		rows = append(rows, n.rootRow(now, fold, act))
		if fold || spare < 1 {
			continue
		}
		kids, over := n.rows, 0
		if len(kids) > spare {
			kids = kids[:spare-1]
			over = len(n.rows) - len(kids)
		}
		for j, a := range kids {
			r := a.row(now)
			conn := treeMid
			if j == len(kids)-1 && over == 0 {
				conn = treeEnd
			}
			r.Spans = append([]module.Span{{Text: " " + conn, Style: module.StyleDim}}, r.Spans...)
			r.Act = act
			rows = append(rows, r)
		}
		spare -= len(kids)
		if over > 0 {
			rows = append(rows, module.Row{Kind: module.RowText,
				Text: fmt.Sprintf(" %s +%d agents", treeEnd, over), Style: module.StyleDim, Act: act})
			spare--
		}
	}
	if trimmed > 0 {
		rows = append(rows, module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d trees", trimmed), Style: module.StyleDim})
	}
	return rows
}

// fleetNodes builds the expanded session's trees: the loose subagents as
// one "agents" node, then one node per workflow dir across all its session
// dirs (satellites included). Trees with live children lead, key order
// within each class: every tree row carries a fold act, so the order may
// move ONLY on discrete transitions (liveness edges, membership) -- a
// newest-activity sort would leapfrog two live trees on per-poll mtime
// churn and re-key the act under a finger. Reads are one session's dirs
// per poll (metas are one small JSON each; discovery's fleet() already
// walks the same wf dirs), and rendering caps at the zone budget.
func fleetNodes(s session, spoolDir string, now time.Time) []fleetNode {
	var nodes []fleetNode
	if loose := agentRows(s, spoolDir, now); len(loose) > 0 {
		nodes = append(nodes, newFleetNode("agents", "agents", glyphAgents, loose))
	}
	idx := map[string]int{}
	for _, dir := range s.dirs {
		wfRoot := filepath.Join(dir, "subagents", "workflows")
		entries, err := os.ReadDir(wfRoot)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasPrefix(e.Name(), "wf_") {
				continue
			}
			agents := scanAgentDir(filepath.Join(wfRoot, e.Name()), now)
			if len(agents) == 0 {
				continue
			}
			key := "wf:" + e.Name()
			if i, ok := idx[key]; ok {
				// the same run id in two satellite dirs is ONE workflow whose
				// cwd moved mid-run: one tree, or two roots would share a
				// fold key and toggle together
				agents = append(nodes[i].rows, agents...)
			} else {
				idx[key] = len(nodes)
				nodes = append(nodes, fleetNode{})
			}
			sortAgentRows(agents)
			nodes[idx[key]] = newFleetNode(key, e.Name(), glyphWorkflows, agents)
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if (nodes[i].live > 0) != (nodes[j].live > 0) {
			return nodes[i].live > 0
		}
		return nodes[i].key < nodes[j].key
	})
	return nodes
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

// agentRows collects the expanded session's loose subagents across all its
// session dirs (satellites included). Bounded reads, one session only: the
// meta files are one small JSON each and rendering caps at the zone
// budget. Running agents sort first, then newest activity.
func agentRows(s session, spoolDir string, now time.Time) []agentRow {
	side := readAgentSidecars(spoolDir, s.id)
	var rows []agentRow
	for _, dir := range s.dirs {
		rows = append(rows, scanAgentDir(filepath.Join(dir, "subagents"), now)...)
	}
	for i := range rows {
		r := &rows[i]
		if sc, ok := side[r.id]; ok {
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
	}
	sortAgentRows(rows)
	return rows
}

// scanAgentDir reads one dir's agent-<id>.meta.json identities plus their
// transcript mtimes: the shared scan under both the loose subagents dir
// and each workflow dir (whose metas carry agentType but no description,
// and get no sidecar refinement -- the rank-3 hooks cover top-level agents
// only).
func scanAgentDir(dir string, now time.Time) []agentRow {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var rows []agentRow
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
		if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err != nil || json.Unmarshal(b, &meta) != nil {
			continue
		}
		r := agentRow{id: id, typ: meta.AgentType, desc: meta.Description}
		if info, err := os.Stat(filepath.Join(dir, "agent-"+id+".jsonl")); err == nil {
			r.ts = info.ModTime()
			r.running = isLive(info.ModTime(), now)
		} else if info, err := e.Info(); err == nil {
			r.ts = info.ModTime()
		}
		rows = append(rows, r)
	}
	return rows
}

// sortAgentRows orders one tree's children: running first, then newest
// activity, id tiebreak.
func sortAgentRows(rows []agentRow) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].running != rows[j].running {
			return rows[i].running
		}
		if !rows[i].ts.Equal(rows[j].ts) {
			return rows[i].ts.After(rows[j].ts)
		}
		return rows[i].id < rows[j].id
	})
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
