// Panel is the claude control-panel surface (module "claude-panel"): the
// full-fill widget behind the nav-tray "claude" target. Two geometrically
// immutable zones per poll (the touch-safety invariant): a pinned detail
// zone (one expanded session: header + turn outcome as one focus tap
// target, then the fleet tree -- one root per workflow plus one for loose
// subagents, children indented under it, blank-padded to a fixed height)
// above a list zone in fixed start order (the A4 collapsed line, focus Act
// per row) and a "+N more" overflow row. Zone occupancy moves on discrete
// state transitions -- needs-user (oldest need first) > live (sticky
// incumbent) > newest start -- never on per-poll mtime churn.
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
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

// Essential opts the panel out of load shedding, same rationale as the
// strip's marker: O(bounded) poll, and the fan-out steering surface must
// not freeze during the loads fan-outs cause.
func (*Panel) Essential() {}

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
	root, spoolDir, sessionsDir, _, err := pollParams(params)
	if err != nil {
		return module.Data{}, fmt.Errorf("claude-panel: %w", err)
	}
	listMax := module.IntParam(params, "max", panelListMax)
	now := time.Now()
	sessions, err := p.mod.discover(ctx, root, spoolDir, sessionsDir, now)
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
		nodes = p.fleetNodes(sessions[occ], spoolDir, now)
	}
	folded := p.foldSnapshot(sessions)
	title, rows := renderPanel(sessions, occ, nodes, folded, listMax, now)
	d := module.Data{Title: title, Rows: rows}
	for _, s := range sessions {
		// a needs-user session always pins the detail zone (pickOccupant),
		// so the bit tracks attention that is actually on glass
		if s.needsUser(now) {
			d.Attention = true
			break
		}
	}
	return d, nil
}

// pickOccupant chooses the detail-zone session: needs-user (oldest need
// first, id tiebreak) > live (the incumbent keeps the zone while it stays
// live, else the most recently live) > newest start. -1 collapses the
// zone to a placeholder -- only at no sessions or a single idle one, the
// states where list geometry may shift because tap races cannot matter.
// Transitions are discrete (a status flip, notification arrival/answer,
// liveness edges, session birth/death), never per-poll mtime churn.
func (p *Panel) pickOccupant(sessions []session, now time.Time) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	att := -1
	for i, s := range sessions {
		// an answered or horizon-stale need cannot pin the zone: the
		// session falls through to the live/newest branches
		if !s.needsUser(now) {
			continue
		}
		if att == -1 || s.needSince().Before(sessions[att].needSince()) ||
			(s.needSince().Equal(sessions[att].needSince()) && s.id < sessions[att].id) {
			att = i
		}
	}
	if att >= 0 {
		p.occupant = sessions[att].id
		return att
	}
	live, incumbent := -1, -1
	for i, s := range sessions {
		if !s.active(now) {
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
		if s.active(now) {
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
		for _, r := range detailRows(sessions[occ], nodes, folded, now) {
			rows = append(rows, railRow(r, sessions[occ].id))
		}
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
		r.Attention = s.needsUser(now)
		rows = append(rows, r)
	}
	if n := len(sessions) - len(shown); n > 0 {
		rows = append(rows, module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d more", n), Style: module.StyleDim})
	}
	return title, rows
}

// railGlyph is the detail zone's left edge: a one-eighth block (escape-
// written, ASCII source) -- thin enough to read as a card edge, distinct
// from the tree connectors' box-drawing family.
const railGlyph = "\u258f"

// railRow prefixes one detail-zone row with the occupant-hued rail: the
// zone reads as one contiguous card, visually split from the list below,
// and the rail's hue names WHOSE detail it is (identity-as-data -- the
// same hue as the session's name). Every zone row carries it, blank pads
// included, so the card's extent is visible whatever the fleet fills.
// Text rows convert to spans so the rail can lead; Act/Attention/style
// ride along (the attention wash paints the rail cell too, coherently).
func railRow(r module.Row, sid string) module.Row {
	rail := module.Span{Text: railGlyph + " ", Ident: sid}
	if r.Kind == module.RowSpans {
		r.Spans = append([]module.Span{rail}, r.Spans...)
		return r
	}
	out := module.SpansRow(rail, module.Span{Text: r.Text, Style: r.Style})
	out.Style, out.Act, out.Attention, out.MinHeight = r.Style, r.Act, r.Attention, r.MinHeight
	return out
}

// detailRows is the expanded block: header (the collapsed columns + model
// and effort appended dim) and the turn-outcome line, BOTH carrying the
// focus Act so header+outcome behave as one tap target and both flagged
// Attention while the occupant's notification is live; then the fleet tree
// (fleetRows), blank-padded to exactly panelDetailRows rows so the list
// zone below never shifts while the zone is occupied.
func detailRows(s session, nodes []fleetNode, folded map[string]bool, now time.Time) []module.Row {
	act := focusArgv(s.id)
	attn := s.needsUser(now)
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

// rootRow is a tree's root line: [age][fold chevron][glyph] label live/total.
// summary appends the type breakdown -- the folded one-liner, and the
// squeezed fallback when an expanded tree gets no child rows (the info
// must not vanish with the rows). Live children accent the row; the label
// hue keys off the node identity.
func (n fleetNode) rootRow(now time.Time, folded, summary bool, act []string) module.Row {
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
	if summary {
		if sum := n.typeSummary(); sum != "" {
			spans = append(spans, module.Span{Text: " " + truncate(sum, panelPromptWidth), Style: module.StyleDim})
		}
	}
	r := module.SpansRow(spans...)
	r.Style = style
	r.Act = act
	return r
}

// fleetRows lays the trees into the detail zone's remaining budget as a
// greedy accordion: each tree IN ORDER takes its root plus (unfolded) its
// children while rows remain, and every tree past the cutoff collapses
// into one dim "+N trees" row. Expansion is VERTICAL and it outranks later
// roots -- a fleet of many trees would otherwise fill the zone with roots
// and a fold toggle could only mutate its own line (glass-reported).
// Folding the leading tree hands its rows to the next one. One row stays
// reserved for the "+N trees" marker while trees remain, so the cutoff is
// never silent. EVERY row of a tree -- root, child, child overflow --
// carries the tree's fold act, so touching the tree anywhere toggles it.
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
	rows := make([]module.Row, 0, budget)
	i := 0
	for ; i < len(nodes); i++ {
		n := nodes[i]
		reserve := 0
		if i < len(nodes)-1 {
			reserve = 1 // the "+N trees" row, should this tree be the last shown
		}
		left := budget - len(rows) - reserve
		if left < 1 {
			break
		}
		fold := folded[sid+"/"+n.key]
		act := foldArgv(sid, n.key)
		kids, over := n.rows, 0
		kidBudget := left - 1
		if fold || kidBudget < 1 {
			kids = nil
		} else if len(kids) > kidBudget {
			kids = kids[:kidBudget-1]
			over = len(n.rows) - len(kids)
		}
		// squeezed out of child rows entirely: keep the summary inline
		rows = append(rows, n.rootRow(now, fold, fold || len(kids) == 0, act))
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
		if over > 0 {
			rows = append(rows, module.Row{Kind: module.RowText,
				Text: fmt.Sprintf(" %s +%d agents", treeEnd, over), Style: module.StyleDim, Act: act})
		}
	}
	if i < len(nodes) {
		rows = append(rows, module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d trees", len(nodes)-i), Style: module.StyleDim})
	}
	return rows
}

// fleetNodes builds the expanded session's trees: the loose subagents as
// one "agents" node, then one node per workflow dir across all its session
// dirs (satellites included). Trees with live children lead, key order
// within each class: every tree row carries a fold act, so the order may
// move ONLY on discrete transitions (liveness edges, membership) -- a
// newest-activity sort would leapfrog two live trees on per-poll mtime
// churn and re-key the act under a finger. All reads ride the shared
// fleet cache (fscache.go): discovery's fleetCached already synced these
// dirs this tick, so building the trees re-reads no meta and re-stats no
// cold file, whatever the fan-out history on disk.
func (p *Panel) fleetNodes(s session, spoolDir string, now time.Time) []fleetNode {
	var nodes []fleetNode
	if loose := p.agentRows(s, spoolDir, now); len(loose) > 0 {
		nodes = append(nodes, newFleetNode("agents", "agents", glyphAgents, loose))
	}
	idx := map[string]int{}
	for _, dir := range s.dirs {
		wfRoot := filepath.Join(dir, "subagents", "workflows")
		wroot := p.mod.fs.sync(wfRoot, now, false)
		if wroot == nil {
			continue
		}
		for _, name := range wroot.subs {
			agents := p.mod.fs.scanAgentDirCached(filepath.Join(wfRoot, name), now, false)
			if len(agents) == 0 {
				continue
			}
			key := "wf:" + name
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
			// the run-id dir name is opaque on glass; any child's prompt
			// plate names the run (the fold KEY stays the run id, so two
			// concurrent runs of one workflow fold independently)
			label := name
			for _, a := range agents {
				if a.wfName != "" {
					label = a.wfName
					break
				}
			}
			nodes[idx[key]] = newFleetNode(key, label, glyphWorkflows, agents)
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

// outcomeRow is the state-detail line under the header: the needs-user
// state (typed glyph + notification title/message, else the registry's
// waitingFor), an error-ended turn (StopFailure reason), a finished
// turn's last assistant line plus parked background work, or a dim
// in-flight marker. The age column leads at the header's fixed width.
func (s session) outcomeRow(now time.Time) module.Row {
	age := func(t time.Time, style string) module.Span {
		return module.Span{Text: fmt.Sprintf("%*s", timeWidth, relTime(now.Sub(t))), Style: style}
	}
	switch {
	// answered or horizon-stale needs fall through to the turn outcome
	case s.needsUser(now):
		g, st := s.attentionGlyph()
		txt := s.notifTitle
		if txt == "" {
			txt = s.notification
		}
		if s.regWaiting != "" && s.regSince.After(s.notified) {
			// the registry wait postdates the bell: an old notification
			// must not caption a new gate
			txt = s.regWaiting
		}
		if txt == "" {
			txt = "input requested"
		}
		ts := s.needSince()
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
// Workflow agents carry NO meta description; their prompt name-plate
// (promptPlate) fills desc and names the tree root.
type agentRow struct {
	id      string
	typ     string
	desc    string
	wfName  string    // prompt-plate workflow name ("" without a plate)
	ts      time.Time // last activity: sidecar stop over transcript mtime
	running bool
}

// plateRe matches the leading name-plate of a workflow agent prompt inside
// the transcript's FIRST line -- `[<workflow>:<leg>]` at the head of the
// user entry's content string. Matched textually, never by JSON decode:
// the first line is the whole prompt entry and can exceed any sane read
// bound, so a truncated read must still match (the plate sits at the
// string's head). Plates are self-authored ASCII labels; the tight
// character classes keep arbitrary prompt text from matching.
var plateRe = regexp.MustCompile(`"content"\s*:\s*"\[([a-z][a-z0-9-]*):([^\]"\\]{1,80})\]`)

// plateHeadBytes bounds the transcript head read; the plate sits in the
// first content string, well inside this.
const plateHeadBytes = 4096

// plateCache memoizes promptPlate by transcript path: the first line of an
// append-only transcript is immutable, so entries never invalidate. Reset
// wholesale past plateCacheMax -- a crude bound beats a leak.
var (
	plateCache    sync.Map // path -> [2]string{wf, leg}
	plateCacheN   atomic.Int64
	plateCacheMax = int64(4096)
)

// promptPlate extracts the name-plate from an agent transcript head.
// ("", "") when absent, unreadable, or unplated -- the row then renders
// exactly as before (agentType only).
func promptPlate(path string) (wf, leg string) {
	if v, ok := plateCache.Load(path); ok {
		p := v.([2]string)
		return p[0], p[1]
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	buf := make([]byte, plateHeadBytes)
	n, _ := io.ReadFull(f, buf)
	if m := plateRe.FindSubmatch(buf[:n]); m != nil {
		wf, leg = string(m[1]), string(m[2])
	}
	if plateCacheN.Add(1) > plateCacheMax {
		plateCache.Clear()
		plateCacheN.Store(0)
	}
	plateCache.Store(path, [2]string{wf, leg})
	return wf, leg
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
// session dirs (satellites included), through the shared fleet cache.
// Running agents sort first, then newest activity.
func (p *Panel) agentRows(s session, spoolDir string, now time.Time) []agentRow {
	side := readAgentSidecars(spoolDir, s.id)
	var rows []agentRow
	for _, dir := range s.dirs {
		rows = append(rows, p.mod.fs.scanAgentDirCached(filepath.Join(dir, "subagents"), now, false)...)
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
