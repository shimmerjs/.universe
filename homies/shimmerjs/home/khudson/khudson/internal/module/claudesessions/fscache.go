package claudesessions

// fscache.go is the constant-cost seam (the 2026-07-14 incident: a 3s
// poll re-walking a 15k-transcript projects tree and stat-walking every
// fleet file compounded with workflow fan-out until the machine drowned).
// Both discovery and fleet counting now go through caches invalidated by
// directory mtimes -- creates/deletes/renames bump the parent dir; file
// APPENDS do not, so per-tick file stats are limited to the hot set
// (recently-written files, the only ones whose liveness can change), and
// a periodic full resync self-heals what the cheap signals miss (an
// append to a cold file: a resumed agent, a revived workflow journal).
// Per tick the whole module costs O(project dirs + live sessions + hot
// files), independent of corpus size, agent history, or panel depth.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// resyncEvery bounds staleness from missed invalidations: a full
	// uncached pass runs this often, so a cold-file append surfaces within
	// a minute instead of never.
	resyncEvery = time.Minute
	// hotFor keeps a file in the per-tick stat set after its last observed
	// write; past it the cached mtime stands until membership change or
	// resync. Generous vs liveWithin so liveness decays before heat does.
	hotFor = 3 * liveWithin
)

// projIndex caches the projects-tree layout: transcript path and session
// dirs (satellites included) per session id. Per tick it stats the root
// and each project dir -- bounded by distinct cwds ever used, not corpus
// size -- and re-reads only dirs whose mtime moved.
type projIndex struct {
	mu         sync.Mutex
	root       string
	rootMtime  time.Time
	projMtimes map[string]time.Time // project dir path -> mtime at last read
	perProj    map[string]projSets  // project dir path -> its contribution
	// merged views, rebuilt only on a tick that re-read some project dir
	transcripts map[string]string
	sessionDirs map[string][]string
	forcedReads int // test seam: forced full passes (each is O(corpus))
}

// projSets is one project dir's contribution to the merged views.
type projSets struct {
	transcripts map[string]string // session id -> transcript path
	sessionDirs map[string]string // session id -> session dir path
}

// resetLocked clears the index without touching its mutex. Caller holds mu.
func (x *projIndex) resetLocked(root string) {
	x.root = root
	x.rootMtime = time.Time{}
	x.projMtimes = map[string]time.Time{}
	x.perProj = map[string]projSets{}
	x.transcripts, x.sessionDirs = nil, nil
}

// lookup refreshes the index (root/project-dir mtime gated; force runs
// the full pass) and returns the merged views. A missing root is empty,
// not an error, matching the old discover.
func (x *projIndex) lookup(root string, force bool) (map[string]string, map[string][]string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.root != root || x.projMtimes == nil {
		// param change (tests) or first use: start over. Field-wise reset --
		// a struct overwrite would zero the held mutex.
		x.resetLocked(root)
	}
	if force {
		x.forcedReads++
	}
	rootInfo, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			x.resetLocked(root)
			return nil, nil, nil
		}
		return nil, nil, err
	}
	changed := false
	if force || !rootInfo.ModTime().Equal(x.rootMtime) {
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, nil, err
		}
		seen := map[string]bool{}
		for _, p := range entries {
			if !p.IsDir() {
				continue
			}
			pdir := filepath.Join(root, p.Name())
			seen[pdir] = true
			if _, ok := x.projMtimes[pdir]; !ok {
				x.projMtimes[pdir] = time.Time{} // new project dir: force its read below
				changed = true
			}
		}
		for pdir := range x.projMtimes {
			if !seen[pdir] {
				delete(x.projMtimes, pdir)
				delete(x.perProj, pdir)
				changed = true
			}
		}
		x.rootMtime = rootInfo.ModTime()
	}
	for pdir, prev := range x.projMtimes {
		info, err := os.Stat(pdir)
		if err != nil {
			delete(x.projMtimes, pdir)
			delete(x.perProj, pdir)
			changed = true
			continue
		}
		if !force && info.ModTime().Equal(prev) && !prev.IsZero() {
			continue
		}
		sets := projSets{transcripts: map[string]string{}, sessionDirs: map[string]string{}}
		entries, err := os.ReadDir(pdir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					if sessionDirRe.MatchString(e.Name()) {
						sets.sessionDirs[e.Name()] = filepath.Join(pdir, e.Name())
					}
					continue
				}
				if id, ok := strings.CutSuffix(e.Name(), ".jsonl"); ok {
					sets.transcripts[id] = filepath.Join(pdir, e.Name())
				}
			}
		}
		x.perProj[pdir] = sets
		x.projMtimes[pdir] = info.ModTime()
		changed = true
	}
	if changed || x.transcripts == nil {
		tx := map[string]string{}
		sd := map[string][]string{}
		for _, sets := range x.perProj {
			for id, p := range sets.transcripts {
				tx[id] = p
			}
			for id, d := range sets.sessionDirs {
				sd[id] = append(sd[id], d)
			}
		}
		x.transcripts, x.sessionDirs = tx, sd
	}
	return x.transcripts, x.sessionDirs, nil
}

// agentMeta is one parsed agent-<id>.meta.json identity. Meta files are
// write-once (created at spawn), so a successful parse never invalidates.
type agentMeta struct {
	agentType   string
	description string
}

// fileState is one watched regular file. meta is set for parsed
// meta.json identities; metaBad marks a failed parse retried only on
// resync (never per tick).
type fileState struct {
	mtime   time.Time
	meta    *agentMeta
	metaBad bool
}

// hot reports whether the file needs a per-tick stat: only a
// recently-written file can flip liveness or grow without a membership
// change.
func (f *fileState) hot(now time.Time) bool { return now.Sub(f.mtime) <= hotFor }

// dirState caches one directory: file mtimes, parsed metas, and wf_
// subdir names. A zero mtime forces the next ReadDir.
type dirState struct {
	mtime time.Time
	files map[string]*fileState
	subs  []string // wf_* subdir names (workflows roots only)
}

// fleetCache serves fleet counts and panel agent rows from dir states.
type fleetCache struct {
	mu         sync.Mutex
	dirs       map[string]*dirState
	nextResync time.Time
	metaReads  int // test seam: meta files actually read+parsed
}

// resyncDue reports (and arms) the periodic full pass. One call per Poll:
// the caller threads the returned force through every sync this tick.
func (c *fleetCache) resyncDue(now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Before(c.nextResync) {
		return false
	}
	c.nextResync = now.Add(resyncEvery)
	return true
}

// sync brings one directory's state current: one stat always; ReadDir
// only when the dir mtime moved (or force); per-file stats for hot files,
// new files, and every file under force. A missing dir drops the state.
func (c *fleetCache) sync(dir string, now time.Time, force bool) *dirState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirs == nil {
		c.dirs = map[string]*dirState{}
	}
	info, err := os.Stat(dir)
	if err != nil {
		delete(c.dirs, dir)
		return nil
	}
	st := c.dirs[dir]
	if st == nil {
		st = &dirState{files: map[string]*fileState{}}
		c.dirs[dir] = st
		force = true
	}
	if force || !info.ModTime().Equal(st.mtime) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			delete(c.dirs, dir)
			return nil
		}
		seen := map[string]bool{}
		st.subs = st.subs[:0]
		for _, e := range entries {
			if e.IsDir() {
				if strings.HasPrefix(e.Name(), "wf_") {
					st.subs = append(st.subs, e.Name())
				}
				continue
			}
			seen[e.Name()] = true
			fst := st.files[e.Name()]
			if fst == nil {
				fst = &fileState{}
				st.files[e.Name()] = fst
			}
			if fi, err := e.Info(); err == nil {
				fst.mtime = fi.ModTime()
			}
			c.parseMetaLocked(dir, e.Name(), fst, force)
		}
		for name := range st.files {
			if !seen[name] {
				delete(st.files, name)
			}
		}
		st.mtime = info.ModTime()
		return st
	}
	for name, fst := range st.files {
		if !fst.hot(now) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
			fst.mtime = fi.ModTime()
		}
	}
	return st
}

// parseMetaLocked parses an agent meta identity once per file: write-once
// files never re-read on success; failures retry only under force.
func (c *fleetCache) parseMetaLocked(dir, name string, fst *fileState, force bool) {
	if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".meta.json") {
		return
	}
	if fst.meta != nil || (fst.metaBad && !force) {
		return
	}
	c.metaReads++
	m, ok := readAgentMeta(filepath.Join(dir, name))
	if !ok {
		fst.metaBad = true
		return
	}
	fst.meta, fst.metaBad = &m, false
}

// prune drops cached dirs not under any live session dir, so the cache
// tracks the rendered set, never the corpus.
func (c *fleetCache) prune(keepRoots []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for dir := range c.dirs {
		keep := false
		for _, root := range keepRoots {
			if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
				keep = true
				break
			}
		}
		if !keep {
			delete(c.dirs, dir)
		}
	}
}

// fleetCached is fleet() through the cache: live agent / workflow counts
// and the newest mtime under one session dir. Per tick: one stat per
// watched dir plus the hot files, instead of a full stat-walk.
func (c *fleetCache) fleetCached(sessionDir string, now time.Time, force bool) (agents, workflows int, newest time.Time) {
	subDir := filepath.Join(sessionDir, "subagents")
	sub := c.sync(subDir, now, force)
	if sub == nil {
		return 0, 0, time.Time{}
	}
	for name, fst := range sub.files {
		if !strings.HasPrefix(name, "agent-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if fst.mtime.After(newest) {
			newest = fst.mtime
		}
		if isLive(fst.mtime, now) {
			agents++
		}
	}
	wfRoot := filepath.Join(subDir, "workflows")
	if wroot := c.sync(wfRoot, now, force); wroot != nil {
		for _, name := range wroot.subs {
			wdir := filepath.Join(wfRoot, name)
			mt := c.wfMtimeCached(wdir, now, force)
			if mt.After(newest) {
				newest = mt
			}
			if isLive(mt, now) {
				workflows++
			}
		}
	}
	return agents, workflows, newest
}

// wfMtimeCached is wfMtime through the cache: newest file mtime inside a
// wf dir, the dir's own mtime standing in when it holds no files.
func (c *fleetCache) wfMtimeCached(wdir string, now time.Time, force bool) time.Time {
	st := c.sync(wdir, now, force)
	if st == nil {
		return time.Time{}
	}
	var newest time.Time
	for _, fst := range st.files {
		if fst.mtime.After(newest) {
			newest = fst.mtime
		}
	}
	if newest.IsZero() {
		newest = st.mtime
	}
	return newest
}

// scanAgentDirCached is scanAgentDir through the cache: rows from parsed
// metas and cached transcript mtimes; the per-tick I/O is the shared
// sync's, not a re-read of every meta.
func (c *fleetCache) scanAgentDirCached(dir string, now time.Time, force bool) []agentRow {
	st := c.sync(dir, now, force)
	if st == nil {
		return nil
	}
	var rows []agentRow
	for name, fst := range st.files {
		if fst.meta == nil && !fst.metaBad {
			continue // not a meta file
		}
		if fst.meta == nil {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "agent-"), ".meta.json")
		r := agentRow{id: id, typ: fst.meta.agentType, desc: fst.meta.description}
		transcript := filepath.Join(dir, "agent-"+id+".jsonl")
		if tst, ok := st.files["agent-"+id+".jsonl"]; ok {
			r.ts = tst.mtime
			r.running = isLive(tst.mtime, now)
		} else {
			r.ts = fst.mtime
		}
		// workflow agents ship no meta description; the prompt name-plate
		// is the leg identity the definitions publish for observers
		if r.desc == "" {
			r.wfName, r.desc = promptPlate(transcript)
		}
		rows = append(rows, r)
	}
	return rows
}

// readAgentMeta parses one agent meta identity file.
func readAgentMeta(path string) (agentMeta, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return agentMeta{}, false
	}
	var raw struct {
		AgentType   string `json:"agentType"`
		Description string `json:"description"`
	}
	if json.Unmarshal(b, &raw) != nil {
		return agentMeta{}, false
	}
	return agentMeta{agentType: raw.AgentType, description: raw.Description}, true
}
