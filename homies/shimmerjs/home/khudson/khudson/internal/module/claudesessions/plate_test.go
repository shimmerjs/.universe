// Prompt name-plates: the workflow definitions publish leg and run names
// by prefixing every agent prompt with [<wf>:<leg>]; the panel reads the
// plate from the transcript head (the only on-disk channel -- workflow
// agent metas carry no description and the label option never leaves the
// in-app UI).
package claudesessions

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// plated is one transcript whose first line is a user entry carrying a
// plated prompt, the shape the Workflow runtime writes.
func plated(wf, leg, rest string) string {
	return fmt.Sprintf(`{"type":"user","message":{"content":"[%s:%s] %s"}}`+"\n", wf, leg, rest)
}

func TestPromptPlate(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		touch(t, p, body, now)
		return p
	}
	if wf, leg := promptPlate(write("a.jsonl", plated("aw-review", "verify:coverage", "check the docs"))); wf != "aw-review" || leg != "verify:coverage" {
		t.Errorf("plate = %q/%q, want aw-review/verify:coverage", wf, leg)
	}
	// unplated prompt: both empty, row renders as before
	if wf, leg := promptPlate(write("b.jsonl", `{"type":"user","message":{"content":"plain prompt"}}`+"\n")); wf != "" || leg != "" {
		t.Errorf("unplated = %q/%q, want empty", wf, leg)
	}
	// a plate PAST the head bound must not match; the head is what we pay for
	long := `{"type":"user","message":{"content":"` + strings.Repeat("x", plateHeadBytes) + `[aw-review:leg] tail"}}`
	if wf, _ := promptPlate(write("c.jsonl", long)); wf != "" {
		t.Errorf("plate past the head bound matched: %q", wf)
	}
	// missing transcript: empty, no error
	if wf, leg := promptPlate(filepath.Join(dir, "nope.jsonl")); wf != "" || leg != "" {
		t.Errorf("missing = %q/%q, want empty", wf, leg)
	}
	// cache: a second read of the same path returns the memo (the head of an
	// append-only transcript is immutable)
	p := write("d.jsonl", plated("aw-audit", "audit:pkg", "go"))
	promptPlate(p)
	os.Remove(p)
	if wf, _ := promptPlate(p); wf != "aw-audit" {
		t.Errorf("cache miss after delete: %q, want the memoized plate", wf)
	}
}

// A workflow tree's root takes its run name from any child's plate; the
// fold key stays the run-id dir name, and each child row's desc is its leg.
func TestFleetNodesPlatedNames(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	wfDir := filepath.Join(dir, "subagents", "workflows", "wf_abc123-99")
	touch(t, filepath.Join(wfDir, "agent-a1.meta.json"), `{"agentType":"skeptic"}`, now)
	touch(t, filepath.Join(wfDir, "agent-a1.jsonl"), plated("aw-review", "verify:coverage", "go"), now)
	touch(t, filepath.Join(wfDir, "agent-a2.meta.json"), `{"agentType":"reviewer"}`, now)
	touch(t, filepath.Join(wfDir, "agent-a2.jsonl"), plated("aw-review", "review:security:r1", "go"), now)

	s := session{id: "sid", dirs: []string{dir}}
	nodes := NewPanel(New()).fleetNodes(s, "", now)
	if len(nodes) != 1 {
		t.Fatalf("nodes = %+v, want the one workflow tree", nodes)
	}
	n := nodes[0]
	if n.key != "wf:wf_abc123-99" {
		t.Errorf("fold key = %q, want the run-id key (stable across renames)", n.key)
	}
	if n.label != "aw-review" {
		t.Errorf("root label = %q, want the plate's workflow name", n.label)
	}
	descs := map[string]bool{}
	for _, a := range n.rows {
		descs[a.desc] = true
	}
	if !descs["verify:coverage"] || !descs["review:security:r1"] {
		t.Errorf("row descs = %v, want the plated leg names", descs)
	}
}
