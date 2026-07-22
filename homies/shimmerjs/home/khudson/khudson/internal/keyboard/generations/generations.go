// Package generations is the append-only record of deployed firmware: one
// JSON per flash the orchestrator drove and verified off the board's USB
// serial, plus the flashed .bin archived next to it. Oryx cannot enumerate
// a layout's revision history (no list query; unrecorded hashes are
// undiscoverable), so this store is the only place the timeline exists.
// History starts empty by decision -- no import of keymapp's synced rows.
//
// Layout under the state root (paths.Resolve):
//
//	generations/<yyyymmddThhmmssZ>-<revision>.json  one Record per flash
//	generations/firmware/<revision>.bin             archived firmware
package generations

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

// Record is one deployed generation. RevisionID is serial-verified: the
// record is written only after the re-enumerated board reported it.
type Record struct {
	FlashedAt      time.Time    `json:"flashedAt"`
	LayoutID       string       `json:"layoutId"`
	RevisionID     string       `json:"revisionId"`
	PrevRevisionID string       `json:"prevRevisionId,omitempty"`
	QmkVersion     string       `json:"qmkVersion,omitempty"`
	MD5            string       `json:"md5,omitempty"`
	Title          string       `json:"title,omitempty"`
	Layout         *oryx.Layout `json:"layout,omitempty"`
}

// DefaultDir is the store under khudson's state root.
func DefaultDir() (string, error) {
	p, err := paths.Resolve()
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Dir, "generations"), nil
}

// hashOK rejects anything but the alphanumeric slugs Oryx issues: revision
// hashes land in filenames under the state dir.
func hashOK(rev string) bool {
	if rev == "" {
		return false
	}
	for _, r := range rev {
		alnum := r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		if !alnum {
			return false
		}
	}
	return true
}

// FirmwarePath is where a revision's flashed binary is archived. The caller
// writes it (download step) and re-flashes from it (rollback); an archived
// bin keeps rollback working if Oryx ever drops the revision.
func FirmwarePath(dir, revisionID string) (string, error) {
	if !hashOK(revisionID) {
		return "", fmt.Errorf("generations: invalid revision hash %q", revisionID)
	}
	return filepath.Join(dir, "firmware", revisionID+".bin"), nil
}

// stampFormat orders records lexically == chronologically in a filename.
const stampFormat = "20060102T150405Z"

// Append writes one record. The filename carries the UTC stamp and revision
// so the store lists in flash order with no index file.
func Append(dir string, r Record) (string, error) {
	if !hashOK(r.RevisionID) {
		return "", fmt.Errorf("generations: invalid revision hash %q", r.RevisionID)
	}
	if r.FlashedAt.IsZero() {
		return "", fmt.Errorf("generations: record has no FlashedAt")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("generations: %w", err)
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("generations: encode: %w", err)
	}
	name := r.FlashedAt.UTC().Format(stampFormat) + "-" + r.RevisionID + ".json"
	file := filepath.Join(dir, name)
	// .part + rename: List fails loudly on any undecodable record, so one
	// torn write must not poison the whole store. .part carries no .json
	// suffix, so a crashed leftover is invisible to List.
	part := file + ".part"
	if err := os.WriteFile(part, raw, 0o600); err != nil {
		return "", fmt.Errorf("generations: %w", err)
	}
	if err := os.Rename(part, file); err != nil {
		return "", fmt.Errorf("generations: %w", err)
	}
	return file, nil
}

// List returns every record in flash order (oldest first). A missing store
// is an empty history, not an error; an unreadable record fails loudly
// rather than silently shortening the timeline.
func List(dir string) ([]Record, error) {
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("generations: %w", err)
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	recs := make([]Record, 0, len(names))
	for _, n := range names {
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, fmt.Errorf("generations: %w", err)
		}
		var r Record
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("generations: decode %s: %w", n, err)
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// Latest returns the newest record, or nil with no error on an empty store.
func Latest(dir string) (*Record, error) {
	recs, err := List(dir)
	if err != nil || len(recs) == 0 {
		return nil, err
	}
	return &recs[len(recs)-1], nil
}

// Find returns the newest record deploying revisionID, or nil when the
// revision was never captured (flashed before this store existed, or by
// other means).
func Find(dir, revisionID string) (*Record, error) {
	recs, err := List(dir)
	if err != nil {
		return nil, err
	}
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].RevisionID == revisionID {
			return &recs[i], nil
		}
	}
	return nil, nil
}
