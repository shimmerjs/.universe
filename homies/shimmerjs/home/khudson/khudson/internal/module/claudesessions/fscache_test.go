package claudesessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProjIndexInvalidation: the index re-reads a project dir only when
// its mtime moves (create/delete bump it), and picks up new project dirs
// via the root mtime -- the seam that replaced the per-tick corpus walk.
func TestProjIndexInvalidation(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	sidA := "aaaaaaaa-1111-2222-3333-444444444444"
	sidB := "bbbbbbbb-1111-2222-3333-444444444444"
	sidC := "cccccccc-1111-2222-3333-444444444444"
	pdir := filepath.Join(root, "-proj")
	touch(t, filepath.Join(pdir, sidA+".jsonl"), "{}", now)

	var x projIndex
	tx, _, err := x.lookup(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if tx[sidA] == "" {
		t.Fatal("cold lookup missed the transcript")
	}

	// a new transcript in a known project dir: the dir mtime bump is the signal
	touch(t, filepath.Join(pdir, sidB+".jsonl"), "{}", now)
	tx, _, err = x.lookup(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if tx[sidB] == "" {
		t.Fatal("same-dir create not picked up via dir mtime")
	}

	// a whole new project dir: the root mtime bump is the signal
	touch(t, filepath.Join(root, "-proj2", sidC+".jsonl"), "{}", now)
	tx, dirs, err := x.lookup(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if tx[sidC] == "" {
		t.Fatal("new project dir not picked up via root mtime")
	}

	// a session dir (satellite) registers the same way
	touch(t, filepath.Join(pdir, sidA, "subagents", "agent-x.jsonl"), "{}", now)
	_, dirs, err = x.lookup(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs[sidA]) != 1 {
		t.Fatalf("sessionDirs[%s] = %v, want the one session dir", sidA, dirs[sidA])
	}
}

// TestFleetCacheParseOnceAndHotCold pins the constant-cost contract at the
// cache seam: metas parse once per file ever; an unchanged tree costs no
// meta reads; a NEW file lands via the dir-mtime signal; an append to a
// COLD file is invisible until the forced resync pass (the accepted
// staleness window); an append to a HOT file lands immediately.
func TestFleetCacheParseOnceAndHotCold(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	sub := filepath.Join(dir, "subagents")
	touch(t, filepath.Join(sub, "agent-a1.jsonl"), "{}", now)
	touch(t, filepath.Join(sub, "agent-a1.meta.json"), `{"agentType":"reviewer"}`, now)
	touch(t, filepath.Join(sub, "workflows", "wf_1", "journal.jsonl"), "", now)

	var c fleetCache
	agents, workflows, _ := c.fleetCached(dir, now, false)
	if agents != 1 || workflows != 1 {
		t.Fatalf("cold pass: agents=%d workflows=%d, want 1/1", agents, workflows)
	}
	if c.metaReads != 1 {
		t.Fatalf("metaReads = %d after cold pass, want 1", c.metaReads)
	}

	// unchanged tree: no meta re-read, counts stable
	if agents, _, _ = c.fleetCached(dir, now, false); agents != 1 || c.metaReads != 1 {
		t.Fatalf("unchanged tree re-read: agents=%d metaReads=%d", agents, c.metaReads)
	}

	// new agent file: parent dir mtime moves, membership catches it
	touch(t, filepath.Join(sub, "agent-a2.jsonl"), "{}", now)
	if agents, _, _ = c.fleetCached(dir, now, false); agents != 2 {
		t.Fatalf("new agent file missed: agents=%d, want 2", agents)
	}

	// rows through the same cache: identity from the parsed-once meta
	rows := c.scanAgentDirCached(sub, now, false)
	if len(rows) != 1 || rows[0].typ != "reviewer" || !rows[0].running {
		t.Fatalf("scanAgentDirCached rows = %+v", rows)
	}
	if c.metaReads != 1 {
		t.Fatalf("row build re-read a meta: metaReads=%d", c.metaReads)
	}
}

func TestFleetCacheColdAppendHealsOnResync(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	sub := filepath.Join(dir, "subagents")
	old := now.Add(-time.Hour) // far past hotFor: a cold file
	touch(t, filepath.Join(sub, "agent-a1.jsonl"), "{}", old)
	touchDir(t, sub, old)

	var c fleetCache
	if agents, _, _ := c.fleetCached(dir, now, false); agents != 0 {
		t.Fatalf("stale agent counted live: %d", agents)
	}

	// an append bumps the file mtime but NOT the dir mtime: the cheap
	// signals cannot see it, and the cached cold mtime stands...
	if err := os.Chtimes(filepath.Join(sub, "agent-a1.jsonl"), now, now); err != nil {
		t.Fatal(err)
	}
	touchDir(t, sub, old)
	if agents, _, _ := c.fleetCached(dir, now, false); agents != 0 {
		t.Fatal("cold append visible without resync -- hot-set logic is not gating stats")
	}
	// ...until the periodic force pass self-heals it
	if agents, _, _ := c.fleetCached(dir, now, true); agents != 1 {
		t.Fatal("forced resync did not surface the cold append")
	}
}

func TestFleetCacheHotAppendLandsImmediately(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	sub := filepath.Join(dir, "subagents")
	warm := now.Add(-2 * liveWithin) // inside hotFor, outside liveWithin
	touch(t, filepath.Join(sub, "agent-a1.jsonl"), "{}", warm)
	touchDir(t, sub, warm)

	var c fleetCache
	if agents, _, _ := c.fleetCached(dir, now, false); agents != 0 {
		t.Fatalf("warm agent counted live: %d", agents)
	}
	if err := os.Chtimes(filepath.Join(sub, "agent-a1.jsonl"), now, now); err != nil {
		t.Fatal(err)
	}
	touchDir(t, sub, warm)
	if agents, _, _ := c.fleetCached(dir, now, false); agents != 1 {
		t.Fatal("hot append missed: the hot set must re-stat recently-written files")
	}
}

// TestDiscoverMissingTranscriptMemo: a registry-alive session with no
// transcript on disk gets ONE forced index rescue, not one per poll -- the
// per-tick corpus walk that would reintroduce is the incident class the
// whole cache exists to forbid (review-confirmed regression).
func TestDiscoverMissingTranscriptMemo(t *testing.T) {
	root := t.TempDir()
	sessions := t.TempDir()
	now := time.Now()
	sid := "aaaaaaaa-1111-2222-3333-444444444444"
	// a project dir must exist for the index to have something to walk
	touch(t, filepath.Join(root, "-proj", "bbbbbbbb-1111-2222-3333-444444444444.jsonl"), "{}", now)
	// live registry record, no transcript for it anywhere
	touch(t, filepath.Join(sessions, "123.json"),
		`{"sessionId":"`+sid+`","pid":123,"updatedAt":`+
			strconvItoa(now.UnixMilli())+`}`, now)
	m := New()
	oldAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == 123 }
	defer func() { pidAlive = oldAlive }()

	poll := func() {
		t.Helper()
		if _, err := m.discover(context.Background(), root, "", sessions, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	poll()
	base := m.idx.forcedReads
	poll()
	poll()
	if m.idx.forcedReads != base {
		t.Fatalf("forced index reads grew %d -> %d across polls with a durably transcript-less session",
			base, m.idx.forcedReads)
	}
}

func strconvItoa(n int64) string { return fmt.Sprintf("%d", n) }

func TestFleetCacheResyncDueArms(t *testing.T) {
	var c fleetCache
	now := time.Now()
	if !c.resyncDue(now) {
		t.Fatal("first call must force a resync")
	}
	if c.resyncDue(now.Add(time.Second)) {
		t.Fatal("armed window must not re-fire")
	}
	if !c.resyncDue(now.Add(resyncEvery + time.Second)) {
		t.Fatal("expired window must re-fire")
	}
}
