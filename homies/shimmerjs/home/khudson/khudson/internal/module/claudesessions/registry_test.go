// Registry-driven behavior: the live-pid discovery gate, the status
// needs-user signal, and derived-name suppression. The spool-heuristic
// fallback (attentionLive) keeps its own tests in claudesessions_test.go.
package claudesessions

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// Only sessions with a live-pid registry record render: no record and
// dead-pid record both filter out, whatever the transcript mtime -- and a
// live record renders however old the transcript is (the window prune is
// gone).
func TestDiscoverLiveRegistryGate(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	liveID := "aaaaaaaa-1111-2222-3333-444444444444"
	deadID := "bbbbbbbb-1111-2222-3333-444444444444"
	bareID := "cccccccc-1111-2222-3333-444444444444"

	p := filepath.Join(projects, "-Users-x-dev-foo")
	// the live session's transcript is 10 DAYS old: liveness comes from
	// the registry, not activity
	touch(t, filepath.Join(p, liveID+".jsonl"), tsEntry("2026-06-30T11:00:00Z")+"\n", now.Add(-240*time.Hour))
	touch(t, filepath.Join(p, deadID+".jsonl"), tsEntry("2026-07-03T11:00:00Z")+"\n", now)
	touch(t, filepath.Join(p, bareID+".jsonl"), tsEntry("2026-07-03T12:00:00Z")+"\n", now)

	regLive(t, sessionsDir, liveID)
	// pid -1 can never be a running process: a leftover record
	touch(t, filepath.Join(sessionsDir, "dead.json"),
		fmt.Sprintf(`{"sessionId":%q,"pid":-1,"status":"busy","updatedAt":1}`, deadID), now)

	sessions, err := discover(context.Background(), projects, "", sessionsDir, now)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(sessions) != 1 || sessions[0].id != liveID {
		t.Fatalf("sessions = %+v, want only the live-registry one", sessions)
	}
}

// needsUser: the registry status is ground truth when present -- waiting
// is true with no spool attention at all, busy is false over a live
// unanswered notification -- and status-less sessions fall back to the
// spool heuristic.
func TestNeedsUserRegistryStatus(t *testing.T) {
	now := time.Now()
	waiting := session{regStatus: "waiting"}
	if !waiting.needsUser(now) {
		t.Error("registry waiting must need the user without any spool state")
	}
	busy := session{
		regStatus: "busy",
		attention: true,
		notified:  now.Add(-time.Minute),
	}
	if busy.needsUser(now) {
		t.Error("registry busy must override a live spool notification")
	}
	fallback := session{
		attention: true,
		notified:  now.Add(-time.Minute),
	}
	if !fallback.needsUser(now) {
		t.Error("status-less session must fall back to attentionLive")
	}
}

// needSince dates the need by the NEWER of the notification and the
// registry flip: an old bell must not date a new gate, and a gate without
// a notification still gets its flip time.
func TestNeedSincePrefersNewer(t *testing.T) {
	old := time.Unix(1000, 0)
	fresh := time.Unix(2000, 0)
	if s := (session{notified: fresh, regSince: old}); !s.needSince().Equal(fresh) {
		t.Errorf("needSince = %v, want the newer notification", s.needSince())
	}
	if s := (session{notified: old, regSince: fresh}); !s.needSince().Equal(fresh) {
		t.Errorf("needSince = %v, want the newer registry flip", s.needSince())
	}
	if s := (session{regSince: fresh}); !s.needSince().Equal(fresh) {
		t.Errorf("needSince = %v, want the flip when no notification fired", s.needSince())
	}
}

// attentionGlyph types from the registry waitingFor when no notification
// fired -- and RE-types when the registry flip postdates a stale bell.
func TestAttentionGlyphFromWaitingFor(t *testing.T) {
	pure := session{regStatus: "waiting", regWaiting: "permission prompt", regSince: time.Unix(2000, 0)}
	if g, st := pure.attentionGlyph(); g != glyphPerm || st != module.StyleWarn {
		t.Errorf("pure registry gate glyph = %q/%q, want warn perm triangle", g, st)
	}
	stale := session{
		regStatus:  "waiting",
		regWaiting: "permission prompt",
		regSince:   time.Unix(2000, 0),
		notifType:  "idle_prompt",
		notified:   time.Unix(1000, 0),
	}
	if g, st := stale.attentionGlyph(); g != glyphPerm || st != module.StyleWarn {
		t.Errorf("stale-bell glyph = %q/%q, want the new gate's perm triangle", g, st)
	}
	current := session{
		regStatus:  "waiting",
		regWaiting: "permission prompt",
		regSince:   time.Unix(1000, 0),
		notifType:  "idle_prompt",
		notified:   time.Unix(2000, 0),
	}
	if g, _ := current.attentionGlyph(); g != glyphAttention {
		t.Errorf("current-bell glyph = %q, want the notification's bell", g)
	}
}

// outcomeRow captions a registry wait that postdates the bell with
// waitingFor, not the stale notification text; a wait with neither shows
// the placeholder.
func TestOutcomeRowRegistryWaitCaption(t *testing.T) {
	now := time.Now()
	stale := session{
		regStatus:    "waiting",
		regWaiting:   "permission prompt",
		regSince:     now.Add(-time.Minute),
		notification: "old bell text",
		notified:     now.Add(-time.Hour),
		promptTS:     now.Add(-2 * time.Hour),
	}
	if text := lineText(stale.outcomeRow(now)); !strings.Contains(text, "permission prompt") ||
		strings.Contains(text, "old bell text") {
		t.Errorf("outcome = %q, want waitingFor over the stale bell", text)
	}
	bare := session{regStatus: "waiting"}
	if text := lineText(bare.outcomeRow(now)); !strings.Contains(text, "input requested") {
		t.Errorf("outcome = %q, want the placeholder", text)
	}
}

// A derived registry name ("can-9b" auto-handles) never displays: the
// spool session_title wins, then the cwd basename.
func TestDerivedNameDropped(t *testing.T) {
	projects := t.TempDir()
	spool := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	titled := "aaaaaaaa-1111-2222-3333-444444444444"
	bare := "bbbbbbbb-1111-2222-3333-444444444444"

	p := filepath.Join(projects, "-Users-x-dev-foo")
	touch(t, filepath.Join(p, titled+".jsonl"), tsEntry("2026-07-03T11:00:00Z")+"\n", now)
	touch(t, filepath.Join(p, bare+".jsonl"), tsEntry("2026-07-03T10:00:00Z")+"\n", now)
	derived := func(id, name string) string {
		return fmt.Sprintf(`{"sessionId":%q,"pid":%d,"name":%q,"nameSource":"derived","cwd":"/x/can","status":"busy","updatedAt":1}`,
			id, os.Getpid(), name)
	}
	touch(t, filepath.Join(sessionsDir, "1.json"), derived(titled, "can-9b"), now)
	touch(t, filepath.Join(sessionsDir, "2.json"), derived(bare, "can-2c"), now)
	touch(t, filepath.Join(spool, titled+".json"),
		fmt.Sprintf(`{"session_id":%q,"session_title":"real title"}`, titled), now)

	data, err := New().Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"dir":         spool,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(data.Rows) != 2 {
		t.Fatalf("Rows = %+v, want both sessions", data.Rows)
	}
	names := map[string]string{}
	for _, r := range data.Rows {
		names[rowIdent(r)] = rowName(r)
	}
	if names[titled] != "real title" {
		t.Errorf("titled session shows %q, want the session_title", names[titled])
	}
	if names[bare] != "can" {
		t.Errorf("title-less session shows %q, want the cwd basename", names[bare])
	}
}

// The panel washes exactly the waiting session's rows: Row.Attention and
// Data.Attention ride the registry status, and the waiting session pins
// the detail zone over a busier, more recently active one.
func TestPanelWashFollowsRegistryWaiting(t *testing.T) {
	projects := t.TempDir()
	sessionsDir := t.TempDir()
	now := time.Now()
	waiting := "aaaaaaaa-1111-2222-3333-444444444444"
	busy := "bbbbbbbb-1111-2222-3333-444444444444"

	p := filepath.Join(projects, "-Users-x-dev-foo")
	// the busy session is the more recently active one
	touch(t, filepath.Join(p, waiting+".jsonl"), tsEntry("2026-07-03T11:00:00Z")+"\n", now.Add(-10*time.Minute))
	touch(t, filepath.Join(p, busy+".jsonl"), tsEntry("2026-07-03T10:00:00Z")+"\n", now)
	touch(t, filepath.Join(sessionsDir, "1.json"),
		regStatusRecord(waiting, "waiting", "permission prompt", now.Add(-time.Minute).UnixMilli()), now)
	touch(t, filepath.Join(sessionsDir, "2.json"), regStatusRecord(busy, "busy", "", 0), now)

	panel := NewPanel(New())
	data, err := panel.Poll(context.Background(), map[string]any{
		"projectsDir": projects,
		"sessionsDir": sessionsDir,
	})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !data.Attention {
		t.Error("Data.Attention = false with a registry-waiting session")
	}
	// identOf is rowIdent for rows that may carry no name span (outcome,
	// text, blank-pad rows)
	identOf := func(r module.Row) string {
		if len(r.Spans) > spanName {
			return r.Spans[spanName].Ident
		}
		return ""
	}
	var washed []string
	for _, r := range data.Rows {
		if r.Attention {
			washed = append(washed, identOf(r))
		}
	}
	if len(washed) == 0 {
		t.Fatal("no washed rows; want the waiting session's")
	}
	for _, id := range washed {
		// detail-zone rows (outcome line) carry an empty ident; the
		// ident-bearing ones must all be the waiting session
		if id != "" && id != waiting {
			t.Errorf("washed row for %q, want only the waiting session", id)
		}
	}
	// the waiting session holds the detail zone: its header leads row 0
	if got := identOf(data.Rows[0]); got != waiting {
		t.Errorf("detail occupant = %q, want the waiting session", got)
	}
	// and the busy session's rows carry no wash
	for _, r := range data.Rows {
		if identOf(r) == busy && r.Attention {
			t.Error("busy session's row washed; the wash must track need, not activity")
		}
	}
}
