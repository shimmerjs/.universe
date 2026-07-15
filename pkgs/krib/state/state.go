// Package state tracks binding values over time, entirely outside nix: an
// on-disk statefile per sheet under $XDG_STATE_HOME/krib/ records, per entry
// id, a hash of the current value, when it last changed (since), when it was
// first seen, and palette usage. Observation happens when krib reads an
// envelope -- no daemon, no polling. A corrupt or missing statefile degrades
// to empty (no markers), never a crash.
package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shimmerjs/krib/envelope"
)

// Version is the statefile schema version this package writes.
const Version = 1

type Entry struct {
	// Hash is the value-hash of the entry's current binding value.
	Hash string `json:"hash"`
	// Since is when the value last CHANGED (not first-seen).
	Since     time.Time `json:"since"`
	FirstSeen time.Time `json:"firstSeen"`
	// usage, recorded on exec accept
	Accepts  int       `json:"accepts,omitempty"`
	LastUsed time.Time `json:"lastUsed,omitzero"`
}

type File struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

// New returns an empty statefile.
func New() *File {
	return &File{Version: Version, Entries: map[string]Entry{}}
}

// Path returns the statefile path for one sheet name:
// $XDG_STATE_HOME/krib/<sheet>.json (XDG default ~/.local/state).
func Path(sheet string) (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		root = filepath.Join(home, ".local", "state")
	}
	if sheet == "" {
		sheet = "default"
	}
	// the sheet name comes from envelope data; a separator or dot segment
	// would escape the krib state dir, so resolution fails instead (callers
	// degrade to in-memory state)
	if strings.ContainsAny(sheet, `/\`) || sheet == "." || sheet == ".." {
		return "", fmt.Errorf("invalid sheet name %q", sheet)
	}
	return filepath.Join(root, "krib", sheet+".json"), nil
}

// Load reads the statefile at path. Missing, corrupt, or version-skewed
// files degrade to an empty File (observation rebuilds it).
func Load(path string) *File {
	fresh := New()
	b, err := os.ReadFile(path)
	if err != nil {
		return fresh
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil || f.Version != Version || f.Entries == nil {
		return fresh
	}
	return &f
}

// hashValue is the value-hash of one entry's binding value: the raw Cmd
// (bindings: the action string), the Body for cards, and the structured
// flag columns (folded in now, while no producer exists, so their arrival
// never reads as an everything-changed blip).
func hashValue(e envelope.Entry) string {
	var flag string
	if e.Flag != nil {
		flag = strings.Join([]string{e.Flag.Short, e.Flag.Type, e.Flag.Default, e.Flag.Range, e.Flag.Help}, "\x00")
	}
	h := sha256.Sum256([]byte(e.Cmd + "\x00" + e.Body + "\x00" + flag))
	return hex.EncodeToString(h[:8])
}

// Observe folds the envelope's current values in: an unseen id gets
// firstSeen = since = now; a changed value-hash moves since (firstSeen
// stays); an unchanged value is untouched. Ids absent from the envelope are
// swept: a lingering entry would resurrect a stale firstSeen (and read as a
// value change) if the id ever came back. An empty envelope is a degenerate
// scrape, not an emptied sheet -- it sweeps nothing. Reports whether anything
// changed (callers skip the rewrite otherwise).
func (f *File) Observe(env *envelope.Envelope, now time.Time) bool {
	dirty := false
	live := make(map[string]bool, len(env.Entries))
	for _, e := range env.Entries {
		id := e.ID(env.Kind)
		live[id] = true
		h := hashValue(e)
		cur, ok := f.Entries[id]
		switch {
		case !ok:
			f.Entries[id] = Entry{Hash: h, Since: now, FirstSeen: now}
			dirty = true
		case cur.Hash != h:
			cur.Hash = h
			cur.Since = now
			f.Entries[id] = cur
			dirty = true
		}
	}
	if len(live) == 0 {
		return dirty
	}
	for id := range f.Entries {
		if !live[id] {
			delete(f.Entries, id)
			dirty = true
		}
	}
	return dirty
}

// RecordUse bumps the accept count and last-used time for id.
func (f *File) RecordUse(id string, now time.Time) {
	e := f.Entries[id]
	e.Accepts++
	e.LastUsed = now
	if e.FirstSeen.IsZero() {
		e.FirstSeen = now
		e.Since = now
	}
	f.Entries[id] = e
}

// Save atomically rewrites path: temp file in the same directory, then
// rename.
func (f *File) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".krib-state-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("rewrite statefile: %w", err)
	}
	return nil
}

// Lock takes a blocking exclusive flock on path's sidecar lock file
// (path+".lock"), creating it and the state dir as needed. The sidecar,
// not the statefile, holds the lock because Save replaces the statefile
// inode on every rewrite. The returned func releases the lock.
func Lock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err == nil {
			break
		}
		if err != syscall.EINTR {
			f.Close()
			return nil, fmt.Errorf("lock statefile: %w", err)
		}
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// Update runs one locked load-modify-save cycle: fn mutates the loaded file
// and reports whether it changed; a dirty file is rewritten before the lock
// releases. Concurrent krib instances (several kitty windows) serialize here
// instead of losing each other's read-modify-write. The loaded file is
// returned for read-side reuse.
func Update(path string, fn func(*File) bool) (*File, error) {
	unlock, err := Lock(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	f := Load(path)
	if fn(f) {
		if err := f.Save(path); err != nil {
			return f, err
		}
	}
	return f, nil
}
