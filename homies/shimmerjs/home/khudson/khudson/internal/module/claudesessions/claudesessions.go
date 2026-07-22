// Package claudesessions is the Claude heads-up: sessions discovered by
// scanning the projects tree (<projectsDir>/*/<session id>.jsonl) and
// JOINed against the session registry (<sessionsDir>/<pid>.json, written
// by Claude Code per running process) -- only sessions whose registry pid
// is a live process render AT ALL (glass-directed: dead sessions are
// noise, whatever their transcript age). The registry is also the
// needs-user ground truth (status "waiting" vs "busy", flipped live by
// Claude Code itself) and the name source; derived registry names
// (nameSource "derived", "can-9b"-style handles) are dropped in favor of
// the spool session_title. Live fleet counts from each session's
// subagents dir; the hook-written spool (<dir>/<session id>.json) fills
// the rest. The last prompt reads from the spool first (the
// UserPromptSubmit hook writes it); the transcript tail fills spool-less
// sessions and corrects a stale spool -- mid-turn steering never fires
// the hook, so a strictly newer tail entry replaces the prompt and dates
// attention staleness. One spans row per session, grouped by cwd (groups
// newest-first by newest member start, newest start first within) --
// static-width columns (relative age, state glyph, fleet counts,
// abbreviated cwd) left of the variable-length identifier (long session
// name) -- with the live/recent tally riding the widget title.
package claudesessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/hookspool"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"golang.org/x/sys/unix"
)

const (
	defaultWindow = 6 * time.Hour // listing window (params.window)
	liveWithin    = 60 * time.Second
	// sessions emit one row each, so 7 keeps 7 one-liners + "+N more"
	// within the region budget.
	defaultMax = 7
	// timeWidth is the fixed relative-age column: ages right-align to it
	// so the static-left columns cannot drift.
	timeWidth = 3
	lineWidth = 60
	// cwdWidth is the fixed cwd column: paths abbreviate into it
	// (abbrevPath) so the name column's start cannot drift.
	cwdWidth = 20
	// tailBytes bounds the transcript read: the last exchange lives near
	// the end, and whole transcripts run to tens of MB.
	tailBytes = 64 * 1024
	// headBytes bounds the start-time read: the first entry lives at the
	// head.
	headBytes = 64 * 1024
)

// Nerd Font PUA glyphs (single cell in FiraCode Nerd Font Mono).
const (
	glyphAgents    = "\uf013" // gear
	glyphWorkflows = "\uf0e8" // sitemap
	glyphAttention = "\uf0f3" // bell: notification awaiting the user
	glyphDone      = "\uf00c" // check: turn complete, idle at prompt
	glyphPerm      = "\uf071" // warning triangle: permission prompt (actionable)
	glyphDialog    = "\uf075" // comment bubble: a user-opened dialog (/btw aside), not a prompt
	glyphError     = "\uf00d" // cross: turn ended in an API error (StopFailure)
	glyphFoldOpen  = "\uf078" // chevron-down: fleet tree expanded
	glyphFoldShut  = "\uf054" // chevron-right: fleet tree folded to its root
)

// Mod implements module.Module. The singleton caches each session's start
// time across Poll calls (rows sort by start, never by activity, so the
// list cannot reshuffle between polls while mtimes churn) and each
// transcript's tail-prompt read keyed by mtime (an unchanged transcript is
// never re-read).
type Mod struct {
	mu        sync.Mutex
	starts    map[string]time.Time
	tails     map[string]tailEntry
	dirDisp   map[string]string
	tailReads int // test seam: cache-missing tail reads

	// live-registry grace state: sessions alive last poll, and which of
	// them already spent their one-poll grace (aliveSessions)
	alivePrev   map[string]bool
	aliveGraced map[string]bool

	// constant-cost seams (fscache.go): the projects-tree layout index and
	// the fleet/agent-dir cache. Shared with the panel (NewPanel(m)).
	idx projIndex
	fs  fleetCache
	// alive ids with no transcript, already rescued once: a durably
	// transcript-less session (registered before its first write, or
	// pruned) must not re-open a per-tick corpus walk. Clears on resync.
	missingTx map[string]bool
}

// tailEntry memoizes one lastPromptEntry result against the transcript
// mtime observed at discovery.
type tailEntry struct {
	mtime time.Time
	text  string
	ts    time.Time
	ok    bool
}

// New returns the module singleton for the registry.
func New() *Mod {
	return &Mod{starts: map[string]time.Time{}, tails: map[string]tailEntry{}}
}

func (*Mod) Name() string { return "claude-sessions" }

// Essential opts the strip out of load shedding: its poll is O(live
// sessions + hot files) by design, and it is the surface that reports the
// workflow fan-outs that drive load past the shed threshold -- freezing
// it under shed would blind the user exactly when steering matters.
func (*Mod) Essential() {}

// pollParams resolves the source params shared by the claude-sessions and
// claude-panel modules: the projects tree, the hook-written spool dir, the
// session registry dir, and the listing window. The window is vestigial
// since the live-registry gate (discover) replaced age pruning; it still
// parses so a configured value keeps vetting instead of erroring.
func pollParams(params map[string]any) (root, spoolDir, sessionsDir string, window time.Duration, err error) {
	root, _ = params["projectsDir"].(string)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", "", 0, err
		}
		root = filepath.Join(home, ".claude", "projects")
	}
	spoolDir, _ = params["dir"].(string)
	sessionsDir, _ = params["sessionsDir"].(string)
	if sessionsDir == "" {
		// the registry is the visibility gate: an unresolvable home must
		// error, or the widget renders the legit empty state on a fault
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", "", 0, err
		}
		sessionsDir = filepath.Join(home, ".claude", "sessions")
	}
	window = defaultWindow
	if s, ok := params["window"].(string); ok && s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return "", "", "", 0, fmt.Errorf("bad window: %w", err)
		}
		window = d
	}
	return root, spoolDir, sessionsDir, window, nil
}

func (m *Mod) Poll(ctx context.Context, params map[string]any) (module.Data, error) {
	root, spoolDir, sessionsDir, _, err := pollParams(params)
	if err != nil {
		return module.Data{}, fmt.Errorf("claude-sessions: %w", err)
	}
	maxRows := module.IntParam(params, "max", defaultMax)

	now := time.Now()
	sessions, err := m.discover(ctx, root, spoolDir, sessionsDir, now)
	if err != nil {
		return module.Data{}, err
	}
	m.orderSessions(sessions)
	m.displayDirs(sessions)
	// the strip renders only the head: freshening past the cap would tail-
	// read every off-screen active session per poll for rows nobody sees
	// (the panel freshens ALL -- pickOccupant needs staleness)
	m.freshenPrompts(sessions[:min(len(sessions), max(0, maxRows))])
	title, rows := render(sessions, maxRows, now)
	return module.Data{Title: title, Rows: rows}, nil
}

// orderSessions groups sessions by raw cwd (all dir-less sessions form the
// one "" group) and sorts groups newest-first by their NEWEST member's
// start (starting a session floats its whole group up -- a discrete
// birth transition, same class as membership change),
// raw dir string breaking group ties; within a group newest start first,
// session id breaking ties and standing in when no start is known. The
// start is read once from the transcript head and cached; the head of an
// append-only transcript never changes, so the order cannot flap on
// activity. Zero starts from not-yet-parseable heads are excluded from
// the group fold, so an unparsed newborn moves nothing until its head
// parses (then one discrete move). A cwd rewrite from a UserPromptSubmit
// spool update migrates that session's group -- rare, discrete, accepted
// like birth/death.
func (m *Mod) orderSessions(sessions []session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]time.Time, len(sessions))
	for i := range sessions {
		s := &sessions[i]
		start, ok := m.starts[s.id]
		if !ok {
			start = startTime(s.transcript)
		}
		if !start.IsZero() {
			seen[s.id] = start
		}
		s.start = start
	}
	// keep only live window entries so the caches cannot grow unbounded
	m.starts = seen
	tails := make(map[string]tailEntry, len(sessions))
	for i := range sessions {
		if e, ok := m.tails[sessions[i].transcript]; ok {
			tails[sessions[i].transcript] = e
		}
	}
	m.tails = tails
	if m.dirDisp != nil {
		live := make(map[string]string, len(sessions))
		for i := range sessions {
			if d, ok := m.dirDisp[sessions[i].dir]; ok {
				live[sessions[i].dir] = d
			}
		}
		m.dirDisp = live
	}
	newest := make(map[string]time.Time, len(sessions))
	for _, s := range sessions {
		if s.start.IsZero() {
			continue
		}
		if e, ok := newest[s.dir]; !ok || s.start.After(e) {
			newest[s.dir] = s.start
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		si, sj := &sessions[i], &sessions[j]
		if si.dir != sj.dir {
			ei, ej := newest[si.dir], newest[sj.dir]
			if !ei.Equal(ej) {
				return ei.After(ej)
			}
			return si.dir < sj.dir
		}
		if !si.start.Equal(sj.start) {
			return si.start.After(sj.start)
		}
		return si.id < sj.id
	})
}

// startTime is the fixed ordering key: the timestamp of the first entry in
// the transcript head. Zero when no entry in the head parses (the session
// then orders by id, and the read retries next poll).
func startTime(path string) time.Time {
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}
	}
	defer f.Close()
	buf := make([]byte, headBytes)
	n, _ := io.ReadFull(f, buf)
	if n == 0 {
		return time.Time{}
	}
	for line := range strings.SplitSeq(string(buf[:n]), "\n") {
		var e struct {
			Timestamp time.Time `json:"timestamp"`
		}
		if json.Unmarshal([]byte(line), &e) == nil && !e.Timestamp.IsZero() {
			return e.Timestamp
		}
	}
	return time.Time{}
}

// freshenPrompts is the transcript-corrective pass: the spool prompt
// (hook-written) stays primary, but mid-turn steering never fires
// UserPromptSubmit, so the transcript tail supplies a strictly newer
// prompt (and dates attention staleness via tailPromptTS). The read is
// gated to the stale set -- sessions with no prompt yet (today's
// fallback), no spool, or a transcript newer than their spool -- and
// memoized by transcript mtime, so an unchanged transcript costs one map
// lookup per poll. A failed tail read changes nothing: the spool stays.
func (m *Mod) freshenPrompts(sessions []session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range sessions {
		s := &sessions[i]
		// the Stop/StopFailure hooks rewrite the spool at turn end
		// (bumping spoolMtime past the transcript) WITHOUT a new prompt,
		// so gating the compare on spool freshness would revert the
		// steering prompt to the stale spool text the moment a steered
		// turn ended. The compare is a map lookup; only the READ stays
		// gated.
		e, hit := m.tails[s.transcript]
		if !hit || !e.mtime.Equal(s.transcriptMtime) {
			if s.prompt != "" && !s.spoolMtime.IsZero() && !s.transcriptMtime.After(s.spoolMtime) {
				if !hit {
					continue
				}
				// stale-mtime cache hit under the gate: compare with the cached
				// values -- append-only transcripts make the cached newest-user
				// ts a valid lower bound
			} else {
				e.text, e.ts, e.ok = m.lastPromptEntryCached(s.transcript, s.transcriptMtime)
			}
		}
		if !e.ok || e.text == "" {
			continue
		}
		if s.prompt == "" {
			// spool-less fallback: the tail text stands regardless of ts
			s.prompt = e.text
		}
		if e.ts.After(s.promptTS) {
			// strictly newer only: the spool wins ties and failed reads
			s.prompt = e.text
			s.tailPromptTS = e.ts
		}
	}
}

// displayDirs resolves each session's cwd column text, memoized by raw
// dir (a stat walk per NEW dir only; the map prunes to the live set in
// orderSessions alongside the other caches).
func (m *Mod) displayDirs(sessions []session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dirDisp == nil {
		m.dirDisp = map[string]string{}
	}
	for i := range sessions {
		s := &sessions[i]
		if s.dir == "" {
			continue
		}
		d, ok := m.dirDisp[s.dir]
		if !ok {
			if rel, found := repoRel(s.dir); found {
				d = rel
			} else {
				d = compactPath(s.dir)
			}
			m.dirDisp[s.dir] = d
		}
		s.dirDisplay = d
	}
}

// lastPromptEntryCached memoizes lastPromptEntry by transcript mtime.
// Caller holds m.mu.
func (m *Mod) lastPromptEntryCached(path string, mtime time.Time) (string, time.Time, bool) {
	if e, ok := m.tails[path]; ok && e.mtime.Equal(mtime) {
		return e.text, e.ts, e.ok
	}
	m.tailReads++
	text, ts, ok := lastPromptEntry(path)
	m.tails[path] = tailEntry{mtime: mtime, text: text, ts: ts, ok: ok}
	return text, ts, ok
}

// session is one discovered transcript, optionally spool- and
// registry-enriched.
type session struct {
	id         string
	transcript string
	dirs       []string  // session dirs across project dirs (satellites too)
	dir        string    // spool cwd, else registry cwd
	dirDisplay string    // cwd column text: repo-relative, else ~-compacted
	name       string    // explicit registry name over spool session_name
	start      time.Time // fixed ordering key (transcript head)
	mtime      time.Time // effective activity: max of transcript, fleet, spool times

	// registry state (the live-process record; "" when the record lacks it)
	regStatus  string    // "waiting" | "busy" -- Claude Code's own signal
	regWaiting string    // what a waiting session waits for ("permission prompt")
	regSince   time.Time // statusUpdatedAt: when regStatus last flipped

	// freshness gate inputs: the raw file mtimes, before any maxing.
	transcriptMtime time.Time // transcript mtime as discovered
	spoolMtime      time.Time // spool file mtime (zero when no spool parsed)

	agents       int
	workflows    int
	prompt       string    // last user prompt, first line (spool over newer tail)
	promptTS     time.Time // spool ts: when the prompt was typed
	tailPromptTS time.Time // tail-observed prompt ts, when it beat the spool's
	stopped      time.Time // spool stopped_ts: last turn completion
	notified     time.Time // spool notification_ts
	attention    bool      // spool attention flag (notification unanswered)

	// rank-1/2 hook fields (panel detail zone; zero-valued on old spools)
	sessionTitle  string // spool session_title (SessionStart/UserPromptSubmit)
	notification  string // spool notification message
	notifType     string // spool notification_type (typed attention)
	notifTitle    string // spool notification_title
	lastAssistant string // spool last_assistant (Stop, first line)
	effort        string // spool effort (Stop, effort.level)
	errMsg        string // spool error (StopFailure reason)
	model         string // spool model (SessionStart)
	bgTasks       int    // spool bg_tasks (Stop)
	crons         int    // spool crons (Stop)
}

func isLive(mtime, now time.Time) bool { return now.Sub(mtime) <= liveWithin }

// active reports the session working RIGHT NOW: file activity within the
// live window, or the registry saying "busy" -- a turn parked inside one
// long tool call (a minutes-long codex exec, a big build) appends nothing
// to transcript/fleet/spool, so mtime alone reads a working session as
// idle (glass-reported). The age column stays honest (last observable
// output); active drives only the tone and the live tally.
func (s session) active(now time.Time) bool {
	return isLive(s.mtime, now) || s.regStatus == "busy"
}

// sessionDirRe matches uuid-named session dirs. Satellites live under the
// project dir of the workflow cwd, which need not be the transcript's.
var sessionDirRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// aliveSessions computes the live-registry set (regAlive) with a
// one-poll grace: a session alive LAST poll that misses this poll -- a
// torn read of a live-rewritten registry file, a transient I/O fault --
// stays alive for ONE more poll before dropping. The gate is a hard
// visibility gate, so a single bad read would otherwise flicker the list,
// destroy the session's fold state (foldSnapshot prunes to the discovered
// set), and hand the detail zone to another incumbent. A second
// consecutive miss drops it for real; a fresh read clears the grace.
func (m *Mod) aliveSessions(names map[string]reg, now time.Time) map[string]bool {
	alive := make(map[string]bool, len(names))
	for sid, r := range names {
		if regAlive(r, now) {
			alive[sid] = true
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	graced := map[string]bool{}
	for sid := range m.alivePrev {
		if alive[sid] || m.aliveGraced[sid] {
			continue
		}
		alive[sid] = true
		graced[sid] = true
	}
	m.aliveGraced = graced
	prev := make(map[string]bool, len(alive))
	maps.Copy(prev, alive)
	m.alivePrev = prev
	return alive
}

// discover lists sessions with a LIVE registry record -- a registry file
// whose pid is verifiably the recorded process (regAlive). Sessions
// without one do not render at all (glass-directed: a dead session is
// noise, whatever its transcript age -- and the live process is the
// cheap, socket-free stand-in for "in a live kitty window right now").
// The projects-tree layout comes from the mtime-gated index and the
// fleet sums from the fleet cache (fscache.go), so a tick costs
// O(project dirs + live sessions + hot files), never a corpus walk.
// Fleet sums over every session dir carrying that id in any project dir.
// A session's effective mtime is the max of its transcript and fleet
// mtimes, so fleet activity keeps a blocked parent live. A missing root
// is empty, not an error; a failed registry read is one (readNames).
func (m *Mod) discover(ctx context.Context, root, spoolDir, sessionsDir string, now time.Time) ([]session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := readNames(sessionsDir)
	if err != nil {
		return nil, err
	}
	alive := m.aliveSessions(names, now)
	force := m.fs.resyncDue(now)
	transcripts, sessionDirs, err := m.idx.lookup(root, force)
	if err != nil {
		return nil, err
	}
	// a live session the index does not know yet (its transcript landed
	// between project-dir stats) gets ONE forced re-read -- memoized, so a
	// durably transcript-less session (registered before its first write,
	// or pruned while alive) cannot re-open a per-tick corpus walk. The
	// memo clears on the resync cadence, bounding retry cost to the same
	// class as the resync itself.
	m.mu.Lock()
	if force || m.missingTx == nil {
		m.missingTx = map[string]bool{}
	}
	rescue := false
	for id := range alive {
		if _, ok := transcripts[id]; !ok && !m.missingTx[id] {
			rescue = true
			break
		}
	}
	m.mu.Unlock()
	if rescue {
		if transcripts, sessionDirs, err = m.idx.lookup(root, true); err != nil {
			return nil, err
		}
		m.mu.Lock()
		for id := range alive {
			if _, ok := transcripts[id]; !ok {
				m.missingTx[id] = true
			}
		}
		m.mu.Unlock()
	}
	var sessions []session
	var keepRoots []string
	for id := range alive {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		path, ok := transcripts[id]
		if !ok {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		sessions = append(sessions, session{
			id:              id,
			transcript:      path,
			mtime:           info.ModTime(),
			transcriptMtime: info.ModTime(),
		})
	}
	for i := range sessions {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s := &sessions[i]
		s.dirs = sessionDirs[s.id]
		keepRoots = append(keepRoots, s.dirs...)
		for _, dir := range s.dirs {
			agents, workflows, newest := m.fs.fleetCached(dir, now, force)
			s.agents += agents
			s.workflows += workflows
			if newest.After(s.mtime) {
				s.mtime = newest
			}
		}
		overlay(s, spoolDir)
		if r, ok := names[s.id]; ok {
			// derived names are auto-generated short handles ("can-9b"):
			// never display them -- the spool session_title or the cwd
			// basename reads better (glass-directed)
			if r.Name != "" && r.NameSource != "derived" {
				s.name = r.Name
			}
			if s.dir == "" {
				s.dir = r.Cwd
			}
			s.regStatus = r.Status
			// external string to glass: same sanitizing bar as every
			// spool field (scrubControl's none-may-reach-a-span invariant)
			s.regWaiting = scrubControl(firstLine(r.WaitingFor))
			if r.StatusUpdatedAt > 0 {
				s.regSince = time.UnixMilli(r.StatusUpdatedAt)
			}
		}
		// hook-written spool times are activity too: a turn completion or
		// notification dates the session even when nothing touched the
		// transcript since. Discovery already pruned by transcript mtime,
		// so a stale spool cannot resurrect an out-of-window session.
		for _, t := range []time.Time{s.promptTS, s.stopped, s.notified} {
			if t.After(s.mtime) {
				s.mtime = t
			}
		}
	}
	// the cache tracks the rendered set, never the corpus: dirs of sessions
	// that left the live set drop here and re-fill cold if they return
	m.fs.prune(keepRoots)
	return sessions, nil
}

// overlay enriches a session from <spoolDir>/<id>.json; a missing or
// malformed spool leaves fs-derived fields alone.
func overlay(s *session, spoolDir string) {
	if spoolDir == "" {
		return
	}
	p := filepath.Join(spoolDir, s.id+".json")
	info, err := os.Stat(p)
	// skip non-regular files: reading a FIFO would block past the poll
	// timeout.
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	sp, err := parseSpool(b)
	if err != nil {
		return
	}
	s.dir, s.name = sp.dir, sp.name
	s.spoolMtime = info.ModTime()
	s.prompt = sp.prompt
	s.promptTS, s.stopped, s.notified = sp.promptTS, sp.stopped, sp.notified
	s.attention = sp.attention
	s.sessionTitle = sp.sessionTitle
	s.notification, s.notifType, s.notifTitle = sp.notification, sp.notifType, sp.notifTitle
	s.lastAssistant, s.effort, s.errMsg = sp.lastAssistant, sp.effort, sp.errMsg
	s.model = sp.model
	s.bgTasks, s.crons = sp.bgTasks, sp.crons
}

// parseSpool decodes one hook-written spool payload: identity (session_name,
// workspace cwd), the typed prompt + its ts (UserPromptSubmit), the
// attention/turn state (Notification + Stop/StopFailure hooks), and the
// rank-1/2 detail fields (typed notification, last assistant line, effort,
// error, bg/cron counts, model, session title). Missing fields stay
// zero-valued -- old spools parse fine. A foreign spool_version stamp is
// parse-garbage under this shape: it errors like malformed, so overlay
// leaves the fs-derived fields alone; an absent stamp is legacy and parses.
func parseSpool(b []byte) (session, error) {
	var raw struct {
		SpoolVersion      int             `json:"spool_version"`
		SessionName       string          `json:"session_name"`
		SessionTitle      string          `json:"session_title"`
		Prompt            string          `json:"prompt"`
		TS                int64           `json:"ts"`
		Attention         bool            `json:"attention"`
		Notification      string          `json:"notification"`
		NotificationTS    int64           `json:"notification_ts"`
		NotificationType  string          `json:"notification_type"`
		NotificationTitle string          `json:"notification_title"`
		StoppedTS         int64           `json:"stopped_ts"`
		LastAssistant     string          `json:"last_assistant"`
		Effort            string          `json:"effort"`
		Error             string          `json:"error"`
		BgTasks           int             `json:"bg_tasks"`
		Crons             int             `json:"crons"`
		Model             json.RawMessage `json:"model"`
		Workspace         struct {
			CurrentDir string `json:"current_dir"`
		} `json:"workspace"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return session{}, err
	}
	if raw.SpoolVersion != 0 && raw.SpoolVersion != hookspool.Version {
		return session{}, fmt.Errorf("spool version %d, want %d", raw.SpoolVersion, hookspool.Version)
	}
	s := session{
		dir:          raw.Workspace.CurrentDir,
		name:         raw.SessionName,
		sessionTitle: raw.SessionTitle,
		// the UserPromptSubmit hook also fires for harness-injected
		// wakeups, so spool prompts carry machinery exactly like tail
		// entries -- same filter, or '<task-notification>' renders as a
		// typed prompt
		prompt:        promptLine(raw.Prompt),
		attention:     raw.Attention,
		notification:  firstLine(raw.Notification),
		notifType:     raw.NotificationType,
		notifTitle:    firstLine(raw.NotificationTitle),
		lastAssistant: firstLine(raw.LastAssistant),
		effort:        raw.Effort,
		errMsg:        firstLine(raw.Error),
		model:         modelName(raw.Model),
		bgTasks:       raw.BgTasks,
		crons:         raw.Crons,
	}
	if raw.TS > 0 {
		s.promptTS = time.Unix(raw.TS, 0)
	}
	if raw.StoppedTS > 0 {
		s.stopped = time.Unix(raw.StoppedTS, 0)
	}
	if raw.NotificationTS > 0 {
		s.notified = time.Unix(raw.NotificationTS, 0)
	}
	return s, nil
}

// modelName tolerates both spool model shapes: the hook writes a plain
// string, statusline-era spools carry {display_name, id}. Anything else is
// "" -- schema drift degrades to blank, never an error.
func modelName(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj struct {
		DisplayName string `json:"display_name"`
		ID          string `json:"id"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		if obj.DisplayName != "" {
			return obj.DisplayName
		}
		return obj.ID
	}
	return ""
}

// reg is one session-registry record (<sessionsDir>/<pid>.json), written
// per running Claude Code process. status/waitingFor are Claude Code's
// own needs-user signal ("waiting" at a permission gate, plan approval,
// or question; "busy" mid-turn; "idle" at the prompt -- all three
// observed live), flipped live -- no hook, no heuristic.
// startedAt (epoch ms) is the process-identity anchor: procStart renders
// the same instant as a TIMEZONE-LESS string (observed UTC against a
// local-time kernel lstart), so the epoch field is the one safe to
// compare against the kernel start time.
type reg struct {
	SessionID       string `json:"sessionId"`
	PID             int    `json:"pid"`
	Name            string `json:"name"`
	NameSource      string `json:"nameSource"`
	Cwd             string `json:"cwd"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"`
	StartedAt       int64  `json:"startedAt"`
	UpdatedAt       int64  `json:"updatedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

// readNames loads the registry into id -> record. Files for dead pids can
// linger, so the newest updatedAt wins per session id. A missing dir is
// empty; any OTHER ReadDir failure is an error -- the registry is a hard
// visibility gate now, so a permission or I/O fault must surface as a
// Poll error, never render as the legit "no active sessions" state.
// Malformed records are skipped (the discover grace absorbs a torn
// concurrent rewrite). A record without a pid field takes it from the
// filename stem (the file is named for the process).
func readNames(dir string) (map[string]reg, error) {
	m := map[string]reg{}
	if dir == "" {
		return m, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return m, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		// skip non-regular files: reading a FIFO would block past the
		// poll timeout.
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r reg
		if err := json.Unmarshal(b, &r); err != nil || r.SessionID == "" {
			continue
		}
		if r.PID == 0 {
			if n, err := strconv.Atoi(strings.TrimSuffix(e.Name(), ".json")); err == nil {
				r.PID = n
			}
		}
		if prev, ok := m[r.SessionID]; !ok || r.UpdatedAt > prev.UpdatedAt {
			m[r.SessionID] = r
		}
	}
	return m, nil
}

// pidAlive reports whether pid is a running process OF THIS USER: signal
// 0 probes without touching it, and EPERM -- someone else's process at
// that pid -- counts as dead, because every Claude Code session here runs
// as this user; a same-pid process we cannot signal is a recycle, not the
// session. Var: test seam.
var pidAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// sysctlProcStart reads pid's kernel start time (kern.proc.pid); ok=false
// when the process is gone or the sysctl is unavailable.
func sysctlProcStart(pid int) (time.Time, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return time.Time{}, false
	}
	tv := kp.Proc.P_starttime
	return time.Unix(tv.Sec, int64(tv.Usec)*1000), true
}

// procStartTime is the process-identity seam over sysctlProcStart.
var procStartTime = sysctlProcStart

// regStartSlack tolerates the gap between the kernel start time and the
// app-level startedAt stamp (observed ~3s on this machine).
const regStartSlack = time.Minute

// regMaxAge bounds identity-UNVERIFIABLE records (no startedAt, or the
// sysctl failed): past it a lingering record cannot gate a session alive.
// Identity-verified records get no age bound -- a session parked idle for
// days in a live kitty tab must keep rendering.
const regMaxAge = 7 * 24 * time.Hour

// regAlive reports whether r's pid is the SAME live process that wrote
// the record. A bare pid probe resurrects dead sessions on pid reuse
// (registry files linger; the 6h window prune that used to age them out
// is gone), so the kernel start time is cross-checked against startedAt;
// records too old to verify fall to the updatedAt age backstop.
func regAlive(r reg, now time.Time) bool {
	if !pidAlive(r.PID) {
		return false
	}
	if r.StartedAt > 0 {
		if st, ok := procStartTime(r.PID); ok {
			d := st.Sub(time.UnixMilli(r.StartedAt))
			if d < 0 {
				d = -d
			}
			return d <= regStartSlack
		}
	}
	return r.UpdatedAt <= 0 || now.Sub(time.UnixMilli(r.UpdatedAt)) <= regMaxAge
}

// lastPromptEntry tails a transcript for the newest typed user prompt:
// first surviving line plus the entry's timestamp -- zero when absent or
// unparseable, and a zero ts never wins a recency compare. Message
// internals drift across Claude Code versions, so every failure yields
// ok=false, never an error.
func lastPromptEntry(path string) (string, time.Time, bool) {
	if path == "" {
		return "", time.Time{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", time.Time{}, false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return "", time.Time{}, false
	}
	off := max(st.Size()-tailBytes, 0)
	buf := make([]byte, st.Size()-off)
	n, err := f.ReadAt(buf, off)
	if n == 0 && err != nil && !errors.Is(err, io.EOF) {
		return "", time.Time{}, false
	}
	lines := strings.Split(string(buf[:n]), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // the byte offset tears the first line
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var e struct {
			Type        string          `json:"type"`
			IsSidechain bool            `json:"isSidechain"`
			Timestamp   json.RawMessage `json:"timestamp"`
			Message     struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(lines[i]), &e) != nil || e.IsSidechain || e.Type != "user" {
			continue
		}
		if prompt := userText(e.Message.Content); prompt != "" {
			return prompt, parseTS(e.Timestamp), true
		}
	}
	return "", time.Time{}, false
}

// parseTS decodes an entry timestamp, zero on anything unparseable -- a
// bad timestamp must not cost the entry its text. Truncated to whole
// seconds: the spool's timestamps are `date +%s` seconds, so a
// sub-second transcript ts would read "strictly newer" than the SAME
// physical prompt (the spool would never win a tie) and a notification
// landing in the same wall-clock second as the last prompt would be
// born answered -- the actionable bell, suppressed.
func parseTS(raw json.RawMessage) time.Time {
	var t time.Time
	if json.Unmarshal(raw, &t) != nil {
		return time.Time{}
	}
	return t.Truncate(time.Second)
}

// userText extracts a typed prompt from a user entry's content: a plain
// string, or the text blocks of a content array (tool_result-only entries
// have none).
func userText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return promptLine(s)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" {
			if t := promptLine(b.Text); t != "" {
				return t
			}
		}
	}
	return ""
}

// Harness-injected wrappers (<command-name>, <local-command-stdout>,
// <task-notification>, <system-reminder>, ...) are user entries on the
// wire, but not typed prompts. Steering text arrives WRAPPED in them, so
// rejecting on a leading tag loses it: promptLine strips complete spans
// and machinery lines instead.
var (
	envelopeOpenRe = regexp.MustCompile(`<([a-z][a-z0-9-]*)>`)
	envelopeLineRe = regexp.MustCompile(`^</?[a-z][a-z0-9-]*>`)
)

// stripEnvelopes removes <name>...</name> spans by matching the close
// tag's NAME to its open tag. A single unanchored span regex pairs an
// open tag with the nearest close tag of ANY name (RE2 has no
// backreferences) and tears on close tags INSIDE the body -- HTML in
// command stdout, "</result>" quoted in a reminder -- leaking machinery
// text as a typed prompt, which can falsely answer a live
// notification. An unclosed open tag truncates the remainder
// as machinery.
func stripEnvelopes(s string) string {
	var b strings.Builder
	for {
		m := envelopeOpenRe.FindStringSubmatchIndex(s)
		if m == nil {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:m[0]])
		name := s[m[2]:m[3]]
		rest := s[m[1]:]
		j := strings.Index(rest, "</"+name+">")
		if j < 0 {
			return b.String()
		}
		s = rest[j+len("</"+name+">"):]
	}
}

// scrubControl drops control bytes: transcript payloads carry raw
// command stdout (ANSI escapes included), and none of it may reach a
// rendered span.
func scrubControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// promptLine extracts the typed text from a possibly-wrapped payload:
// complete envelope spans are stripped, machinery lines (bare or torn
// open/close tags) skipped, and the first surviving non-empty line
// returned. Nothing surviving means the entry was machinery-only.
func promptLine(s string) string {
	s = stripEnvelopes(s)
	for line := range strings.SplitSeq(s, "\n") {
		t := strings.TrimSpace(scrubControl(line))
		if t == "" || envelopeLineRe.MatchString(t) {
			continue
		}
		return t
	}
	return ""
}

// repoRel resolves dir against its enclosing git repo: the repo dir's
// basename plus the path relative to the root ("universe/homies/kb"),
// true when a repo encloses dir. Worktrees carry a .git FILE, so any
// .git entry counts. The repo-relative path is the working identity;
// a fish-abbreviated absolute path is noise.
func repoRel(dir string) (string, bool) {
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			name := filepath.Base(d)
			rel, err := filepath.Rel(d, dir)
			if err != nil || rel == "." {
				return name, true
			}
			return name + "/" + filepath.ToSlash(rel), true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", false
		}
		d = parent
	}
}

// firstLine is the trimmed first non-empty line of s.
func firstLine(s string) string {
	for line := range strings.SplitSeq(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// render maps ordered sessions to one spans row each, capped at max with a
// dim "+N more"; the live/recent tally rides the title so no row is spent
// on a header. The order is the caller's fixed order, never recency.
func render(sessions []session, max int, now time.Time) (string, []module.Row) {
	if len(sessions) == 0 {
		return "claude", []module.Row{{Kind: module.RowText, Text: "no active sessions", Style: module.StyleDim}}
	}
	live := 0
	for _, s := range sessions {
		if s.active(now) {
			live++
		}
	}
	if max < 0 {
		max = 0
	}
	shown := sessions
	if len(shown) > max {
		shown = shown[:max]
	}
	rows := make([]module.Row, 0, len(shown)+1)
	for _, s := range shown {
		rows = append(rows, s.line(now))
	}
	if n := len(sessions) - len(shown); n > 0 {
		rows = append(rows, module.Row{Kind: module.RowText, Text: fmt.Sprintf("+%d more", n), Style: module.StyleDim})
	}
	return fmt.Sprintf("claude %d/%d", live, len(sessions)), rows
}

// line is one session as a single spans row. Static-width columns lead --
// relative age (accent while ACTIVE -- fresh files or registry-busy --
// dim otherwise: activity is the column's color, not a badge; the text
// stays the honest last-output age), one state glyph (typed attention,
// error, check),
// the two fleet counts (blanked at zero), and the abbreviated cwd padded
// to cwdWidth -- so the variable-length identifier (long session name, hue
// keyed by session id) plus the prompt snippet flow free on the right.
func (s session) line(now time.Time) module.Row {
	return s.lineW(now, lineWidth)
}

// lineW is line with the prompt-tail cap as a parameter: the panel fits
// rows to its own width instead of the home strip's lineWidth. The real
// cell fit stays dock-side (fitCell); the cap only bounds the payload.
func (s session) lineW(now time.Time, promptW int) module.Row {
	style := module.StyleDim
	if s.active(now) {
		style = module.StyleAccent
	}
	// the cwd column sits after the fixed lead block and before the name:
	// the age column keeps the left edge as the liveness signal, and
	// padding the cwd to cwdWidth keeps every column boundary static, the
	// name's start included. An empty dir renders blank at full width so
	// the column never collapses.
	cwd := ""
	if s.dir != "" {
		d := s.dirDisplay
		if d == "" {
			d = compactPath(s.dir)
		}
		cwd = abbrevPath(d, cwdWidth)
	}
	spans := []module.Span{
		{Text: fmt.Sprintf("%*s", timeWidth, relTime(now.Sub(s.mtime))), Style: style},
		s.stateSpan(now),
		countSpan(glyphAgents, s.agents),
		countSpan(glyphWorkflows, s.workflows),
		{Text: fmt.Sprintf(" %-*s", cwdWidth, cwd), Style: module.StyleDim},
		// hue keys off the session id, not the displayed key: a name
		// appearing mid-session cannot flap the color
		{Text: " " + s.key(), Style: module.StyleTitle, Ident: s.id},
	}
	if s.prompt != "" {
		spans = append(spans, module.Span{Text: " > " + truncate(s.prompt, promptW), Style: module.StyleDim})
	}
	r := module.SpansRow(spans...)
	r.Style = style
	return r
}

// turnDone reports a recorded turn end (Stop or StopFailure) at or after
// the last prompt.
func (s session) turnDone() bool {
	return !s.stopped.IsZero() && !s.stopped.Before(s.promptTS)
}

// attentionHorizon bounds how long an unanswered notification stays
// actionable: past it the attention is stale -- the bell dims, the panel
// detail zone unpins, and Data.Attention clears (an abandoned session must
// not ring, pin, or march the border forever).
const attentionHorizon = time.Hour

// attentionLive reports an unanswered notification still within the
// liveness horizon: any user prompt -- spool-recorded or tail-observed --
// strictly after notification_ts answers it, and a signal whose age
// (now minus the NEWEST of notified/promptTS/tailPromptTS/stopped; all
// four zero counts as stale) exceeds attentionHorizon is stale even
// unanswered. Ages AT the horizon are still live; only past it is stale.
//
// Mid-turn gates (permission_prompt, agent_needs_input) resolve WITHOUT
// any hook: granting fires no UserPromptSubmit and the turn keeps running,
// so the bell would ring a working session until its next Stop (observed
// live: transcript moving +12s after notification_ts while attention held).
// The main transcript is silent while the gate blocks (its tool_use entry
// lands before the notification fires), so transcript activity strictly
// after notification_ts -- or a turn end at/after it -- marks the gate
// answered. Truncated to seconds like every spool compare: a same-second
// write must not answer the bell it announced. idle_prompt keeps
// prompt-only answering -- the session really is waiting for the user --
// UNLESS the turn parked background work: the harness fires idle_prompt
// in the gap between a turn end and the next background wakeup, and the
// wakeup, not the user, answers it (observed live: bell 61s after a Stop
// that recorded bg tasks, washed 56s later by the task notification). A
// session with bg_tasks from its last Stop or live fleet is waiting on
// its fleet, not the user.
func (s session) attentionLive(now time.Time) bool {
	if !s.attention {
		return false
	}
	newest := s.notified
	for _, t := range []time.Time{s.promptTS, s.tailPromptTS, s.stopped} {
		if t.After(newest) {
			newest = t
		}
	}
	if newest.IsZero() || now.Sub(newest) > attentionHorizon {
		return false
	}
	if s.notified.IsZero() {
		return true
	}
	switch s.notifType {
	case "permission_prompt", "agent_needs_input":
		if s.transcriptMtime.Truncate(time.Second).After(s.notified) {
			return false
		}
		if s.stopped.After(s.notified) {
			return false
		}
	case "idle_prompt":
		if s.bgTasks > 0 || s.agents > 0 || s.workflows > 0 {
			return false
		}
	}
	latest := s.promptTS
	if s.tailPromptTS.After(latest) {
		latest = s.tailPromptTS
	}
	return !latest.After(s.notified)
}

// regKnown reports a registry status this code understands (all three
// observed live on this machine). Anything else -- a future Claude Code
// enum value -- must NOT silently read as not-needing-user: unknown
// statuses fall back to the spool heuristic wholesale (the
// attentionGlyph default-branch doctrine).
func (s session) regKnown() bool {
	return s.regStatus == "waiting" || s.regStatus == "busy" || s.regStatus == "idle"
}

// dialogOpen reports a registry wait the USER raised (the /btw aside,
// /config -- waitingFor reads "dialog open"): the session is technically
// waiting, but on a dialog the user opened themselves, so it must not
// read as the session prompting for input. Prefix match: the registry
// vocabulary is prose and may grow qualifiers.
func (s session) dialogOpen() bool {
	return s.regStatus == "waiting" && strings.HasPrefix(s.regWaiting, "dialog")
}

// needsUser reports the session blocked on the user RIGHT NOW. The
// registry status is ground truth when it speaks our vocabulary: Claude
// Code itself flips "waiting" (permission gate, plan approval, question),
// "busy" (turn running), and "idle" (at the prompt, nothing pending)
// live, so a granted gate un-washes on the next poll with none of the
// transcript-activity heuristics attentionLive needs (glass-reported:
// the wash tracked activity, not need). A "waiting" whose waitingFor is a
// user-opened dialog is carved out: the user raised it, nothing is asking
// for them (stateSpan still marks it with the dim dialog glyph). idle is
// hard-false: gates only fire mid-turn, and an unanswered idle_prompt
// bell over a finished session is exactly the resting state the wash must
// not mark. Two escapes to the spool heuristic: an unknown/absent status,
// and a notification STRICTLY newer than the busy flip -- a gate the
// registry has not caught up to must not be silenced by the stale busy.
func (s session) needsUser(now time.Time) bool {
	switch s.regStatus {
	case "waiting":
		return !s.dialogOpen()
	case "idle":
		return false
	case "busy":
		if !s.notified.After(s.regSince) {
			return false
		}
	}
	return s.attentionLive(now)
}

// needSince dates the needs-user state for oldest-first ordering: the
// newer of the notification ts and the registry status flip -- a registry
// wait can postdate (or never have had) a notification, and an old bell
// must not date a new gate.
func (s session) needSince() time.Time {
	if s.regSince.After(s.notified) {
		return s.regSince
	}
	return s.notified
}

// attentionGlyph types the attention badge from notification_type, or
// from the registry's waitingFor when no notification fired -- or when
// the registry wait postdates the bell (a gate can flip the registry
// before, after, or without the Notification hook, and an old bell must
// not type a new gate). The notification enum has 8 documented values in
// 2.1.201 and may grow, so the default branch is mandatory: unknown types
// render a dim bell, never a blank or a panic.
func (s session) attentionGlyph() (glyph, style string) {
	typ := s.notifType
	if s.regWaiting != "" && s.regSince.After(s.notified) {
		typ = ""
	}
	switch typ {
	case "permission_prompt":
		return glyphPerm, module.StyleWarn // the actionable one
	case "idle_prompt", "agent_needs_input":
		return glyphAttention, module.StyleWarn
	case "":
		if strings.HasPrefix(s.regWaiting, "permission") {
			return glyphPerm, module.StyleWarn
		}
		return glyphAttention, module.StyleWarn
	default:
		return glyphAttention, module.StyleDim
	}
}

// stateSpan is the one-glyph state column: needs-user (registry "waiting",
// or the spool heuristic on records without a KNOWN status; glyph typed
// from notification_type/waitingFor) outranks an error-ended turn
// (StopFailure) outranks turn-complete (a Stop at or after the last
// prompt); a turn in flight shows blank -- the age column's accent
// already carries activity. Without a known status an answered or
// horizon-stale notification lowers to a dim bell, never warn; a known
// busy is itself the answer.
func (s session) stateSpan(now time.Time) module.Span {
	if s.needsUser(now) {
		g, st := s.attentionGlyph()
		return module.Span{Text: " " + g, Style: st}
	}
	if s.dialogOpen() {
		// visible but quiet: the user opened this dialog themselves
		return module.Span{Text: " " + glyphDialog, Style: module.StyleDim}
	}
	if !s.regKnown() && s.attention {
		return module.Span{Text: " " + glyphAttention, Style: module.StyleDim}
	}
	if s.turnDone() {
		if s.errMsg != "" {
			return module.Span{Text: " " + glyphError, Style: module.StyleWarn}
		}
		return module.Span{Text: " " + glyphDone, Style: module.StyleDim}
	}
	return module.Span{Text: "  ", Style: module.StyleDim}
}

// countSpan is one fixed-width fleet column: glyph + right-aligned count,
// blanked at zero so the identifier column cannot drift.
func countSpan(glyph string, n int) module.Span {
	if n <= 0 {
		return module.Span{Text: "    ", Style: module.StyleDim}
	}
	return module.Span{Text: fmt.Sprintf(" %s%2d", glyph, n), Style: module.StyleHighlight}
}

// relTime is the compact relative-age scheme: 15s, 3m, 2h, 4d -- largest
// single unit, no "ago" suffix, so the column stays narrow and consistent.
func relTime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// abbrevPath fits an already ~-compacted path into budget runes,
// deterministic in (p, budget): pass through under budget, else
// fish-abbreviate (every segment but the last collapses to its first
// rune), else drop leading segments behind "..." keeping the tail intact
// (longest fitting suffix), else hard rune-truncate -- so the fixed-width
// cwd column cannot overflow.
func abbrevPath(p string, budget int) string {
	if len([]rune(p)) <= budget {
		return p
	}
	segs := strings.Split(p, "/")
	for i := range len(segs) - 1 {
		if r := []rune(segs[i]); len(r) > 1 {
			segs[i] = string(r[0])
		}
	}
	if s := strings.Join(segs, "/"); len([]rune(s)) <= budget {
		return s
	}
	for i := 1; i < len(segs); i++ {
		if s := ".../" + strings.Join(segs[i:], "/"); len([]rune(s)) <= budget {
			return s
		}
	}
	return truncate(".../"+segs[len(segs)-1], budget)
}

// compactPath renders a cwd ~-style: the home prefix collapses to ~.
func compactPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == "/" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

func (s session) key() string {
	if s.name != "" {
		return s.name
	}
	if s.sessionTitle != "" {
		return s.sessionTitle
	}
	if s.dir != "" {
		return filepath.Base(s.dir)
	}
	// no name, no title, no cwd: nothing human-meaningful to show -- the
	// raw session id is noise, not context
	return "-"
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-3]) + "..."
}
