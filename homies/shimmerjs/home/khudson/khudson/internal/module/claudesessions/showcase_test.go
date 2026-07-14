package claudesessions

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Env-gated showcase (KHUDSON_PANEL_SHOWCASE=1): renders curated panel
// states through the REAL renderer and dumps them as JSON, for docs and
// annotation work (the kb TestKeyboardRenderRealDB precedent). Not a
// correctness test -- the scenarios sweep the conditional surface: wash,
// state glyphs, outcome variants, name fallbacks, the fleet accordion,
// and the overflow rows.
func TestPanelShowcase(t *testing.T) {
	if os.Getenv("KHUDSON_PANEL_SHOWCASE") == "" {
		t.Skip("set KHUDSON_PANEL_SHOWCASE=1 to dump showcase renders")
	}
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.Local)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }

	waiting := session{
		id: "aaaaaaaa-0000-0000-0000-000000000001", dir: "/x/dev/cg/can",
		dirDisplay: "can-work/containers/can", sessionTitle: "rules/go perf dev plan",
		start: ago(40 * time.Minute), mtime: ago(3 * time.Minute),
		regStatus: "waiting", regWaiting: "permission prompt", regSince: ago(90 * time.Second),
		notifType: "permission_prompt", notification: "Claude needs your permission to use Bash",
		notified: ago(90 * time.Second), promptTS: ago(40 * time.Minute),
		prompt: "pick up handoff for rules/go performance improvements",
		model:  "claude-fable-5", effort: "xhigh",
	}
	busyFleet := session{
		id: "bbbbbbbb-0000-0000-0000-000000000002", dir: "/x/universe",
		dirDisplay: "universe", name: "panel-work",
		start: ago(2 * time.Hour), mtime: ago(10 * time.Second),
		regStatus: "busy", regSince: ago(5 * time.Minute),
		promptTS: ago(5 * time.Minute), prompt: "fix clod panel attention semantics",
		agents: 3, workflows: 1, model: "claude-fable-5", effort: "xhigh",
	}
	busyParked := session{
		id: "cccccccc-0000-0000-0000-000000000003", dir: "/x/dev/cg/can",
		dirDisplay: "can-work/containers/can",
		start:      ago(3 * time.Hour), mtime: ago(4 * time.Minute),
		regStatus: "busy", regSince: ago(6 * time.Minute),
		promptTS: ago(6 * time.Minute), prompt: "consult codex on the migration",
	}
	idleDone := session{
		id: "dddddddd-0000-0000-0000-000000000004", dir: "/x/universe",
		dirDisplay: "universe", sessionTitle: "statusline tweaks",
		start: ago(5 * time.Hour), mtime: ago(25 * time.Minute),
		regStatus: "idle", regSince: ago(25 * time.Minute),
		promptTS: ago(30 * time.Minute), stopped: ago(25 * time.Minute),
		prompt: "bump the beps", lastAssistant: "Done -- both flakes updated and built.",
		bgTasks: 2, crons: 1,
	}
	errored := session{
		id: "eeeeeeee-0000-0000-0000-000000000005", dir: "/x/scratch",
		dirDisplay: "~/scratch",
		start:      ago(50 * time.Minute), mtime: ago(8 * time.Minute),
		regStatus: "idle", regSince: ago(8 * time.Minute),
		promptTS: ago(12 * time.Minute), stopped: ago(8 * time.Minute),
		prompt: "run the flaky suite", errMsg: "API error 529 (overloaded)",
	}

	// workflow children carry plated leg names as desc (promptPlate fills
	// these from transcript heads in production); loose agents carry the
	// Agent-tool description from their meta
	wfAgents := func(running int, legs ...string) []agentRow {
		types := []string{"reviewer", "skeptic", "researcher", "mapper"}
		rows := make([]agentRow, len(legs))
		for i, leg := range legs {
			rows[i] = agentRow{
				id: string(rune('a' + i)), typ: types[i%len(types)],
				desc: leg, wfName: "aw-review",
				ts: ago(time.Duration(20*(i+1)) * time.Second), running: i < running,
			}
		}
		return rows
	}
	nodes := []fleetNode{
		// root label = the plate's workflow name (fold key stays the run id)
		newFleetNode("wf:wf_7fc5bc03-3c4", "aw-review", glyphWorkflows,
			wfAgents(2, "review:security:r1", "review:codex:r1", "verify:panel.go", "synthesize")),
		newFleetNode("agents", "agents", glyphAgents, []agentRow{
			{id: "a1", typ: "general-purpose", desc: "Sweep panel_test.go fixtures", ts: ago(30 * time.Second), running: true},
			{id: "a2", typ: "claude-code-guide", desc: "Registry status enum research", ts: ago(3 * time.Minute)},
		}),
		newFleetNode("wf:wf_1890d4f6-4cc", "khudson-ledger-map", glyphWorkflows,
			wfAgents(0, "map:STATUS.md", "map:DESIGN.md", "map:git-history")),
	}

	dump := func(name, title string, rows []moduleRowJSON) {
		b, err := json.MarshalIndent(map[string]any{"scenario": name, "title": title, "rows": rows}, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("SHOWCASE %s\n%s", name, b)
	}

	scenarios := []struct {
		name     string
		sessions []session
		occ      int
		nodes    []fleetNode
		folded   map[string]bool
		listMax  int
	}{
		{"waiting-pins-detail", []session{waiting, busyFleet, busyParked, idleDone, errored}, 0, nil, nil, 12},
		{"busy-fleet-detail-accordion", []session{busyFleet, idleDone}, 0, nodes, nil, 12},
		{"fleet-first-tree-folded", []session{busyFleet, idleDone}, 0, nodes,
			map[string]bool{busyFleet.id + "/wf:wf_7fc5bc03-3c4": true}, 12},
		{"list-overflow", []session{waiting, busyFleet, busyParked, idleDone, errored}, 0, nil, nil, 2},
		{"empty", nil, -1, nil, nil, 12},
	}
	for _, sc := range scenarios {
		title, rows := renderPanel(sc.sessions, sc.occ, sc.nodes, sc.folded, sc.listMax, now)
		out := make([]moduleRowJSON, 0, len(rows))
		for _, r := range rows {
			j := moduleRowJSON{Kind: string(r.Kind), Text: r.Text, Style: r.Style,
				Attention: r.Attention, Tappable: len(r.Act) > 0}
			for _, s := range r.Spans {
				j.Spans = append(j.Spans, spanJSON{Text: s.Text, Style: s.Style, Ident: s.Ident})
			}
			out = append(out, j)
		}
		dump(sc.name, title, out)
	}
}

type spanJSON struct {
	Text  string `json:"text"`
	Style string `json:"style,omitempty"`
	Ident string `json:"ident,omitempty"`
}

type moduleRowJSON struct {
	Kind      string     `json:"kind,omitempty"`
	Text      string     `json:"text,omitempty"`
	Style     string     `json:"style,omitempty"`
	Attention bool       `json:"attention,omitempty"`
	Tappable  bool       `json:"tappable,omitempty"`
	Spans     []spanJSON `json:"spans,omitempty"`
}
