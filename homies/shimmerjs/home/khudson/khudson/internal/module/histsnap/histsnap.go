// Package histsnap persists module history rings across bus restarts:
// every Persistent module's series lands in one snapshot file under the
// state root (paths.HistSnap). Format: one version byte, then a
// gob-encoded entry list sorted by name so identical state encodes to
// identical bytes. Saves are tmp+rename atomic (the claude spool hook's
// pattern) and 0600 under the 0700 state root; loads never crash the bus
// -- a corrupt or missing snapshot errors out to a fresh start.
package histsnap

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
)

// version is the snapshot format byte; a mismatch fails the load (fresh
// start) rather than guessing at a stale gob shape.
const version = 1

// entry is one series on disk; this gob shape is the compatibility
// surface -- changing it means bumping version.
type entry struct {
	Name     string
	Cadence  time.Duration
	LastUnix int64
	Samples  []float32
}

// encode renders series as snapshot file bytes, entries sorted by name.
func encode(series map[string]module.HistState) ([]byte, error) {
	names := make([]string, 0, len(series))
	for name := range series {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]entry, 0, len(names))
	for _, name := range names {
		st := series[name]
		entries = append(entries, entry{Name: name, Cadence: st.Cadence, LastUnix: st.LastUnix, Samples: st.Samples})
	}
	var buf bytes.Buffer
	buf.WriteByte(version)
	if err := gob.NewEncoder(&buf).Encode(entries); err != nil {
		return nil, fmt.Errorf("encode hist snapshot: %w", err)
	}
	return buf.Bytes(), nil
}

func decode(raw []byte) (map[string]module.HistState, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty hist snapshot")
	}
	if raw[0] != version {
		return nil, fmt.Errorf("hist snapshot version %d, want %d", raw[0], version)
	}
	var entries []entry
	if err := gob.NewDecoder(bytes.NewReader(raw[1:])).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode hist snapshot: %w", err)
	}
	out := make(map[string]module.HistState, len(entries))
	for _, e := range entries {
		out[e.Name] = module.HistState{Cadence: e.Cadence, LastUnix: e.LastUnix, Samples: e.Samples}
	}
	return out, nil
}

// Save writes series to path atomically: tmp + rename, 0600.
func Save(path string, series map[string]module.HistState) error {
	data, err := encode(series)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write hist snapshot: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("commit hist snapshot: %w", err)
	}
	return nil
}

// Load reads and decodes path. A missing file surfaces os.ErrNotExist so
// the caller can tell first boot from corruption.
func Load(path string) (map[string]module.HistState, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(raw)
}

// Prepare filters loaded series for restore at now. An entry whose gap
// since LastUnix reaches the span its samples cover is dropped: nothing in
// the ring would still be inside the window it draws. A shorter gap gets
// gap/cadence zero fillers appended at cadence, so the restored ring shows
// the outage instead of splicing pre-restart samples onto post-restart
// ones.
func Prepare(series map[string]module.HistState, now time.Time) map[string]module.HistState {
	out := make(map[string]module.HistState, len(series))
	for name, st := range series {
		if st.Cadence <= 0 || len(st.Samples) == 0 {
			continue
		}
		gap := time.Duration(now.Unix()-st.LastUnix) * time.Second
		if gap < 0 {
			gap = 0 // clock skew: trust the samples over the stamp
		}
		window := st.Cadence * time.Duration(len(st.Samples))
		if gap >= window {
			continue
		}
		if fill := int(gap / st.Cadence); fill > 0 {
			padded := make([]float32, len(st.Samples), len(st.Samples)+fill)
			copy(padded, st.Samples)
			padded = append(padded, make([]float32, fill)...)
			st.Samples = padded
			st.LastUnix += int64((time.Duration(fill) * st.Cadence) / time.Second)
		}
		out[name] = st
	}
	return out
}

// Age is now minus the newest entry's last sample: the restart gap the
// restore log reports. Empty or future-stamped series age zero.
func Age(series map[string]module.HistState, now time.Time) time.Duration {
	var newest int64
	for _, st := range series {
		if st.LastUnix > newest {
			newest = st.LastUnix
		}
	}
	if newest == 0 {
		return 0
	}
	d := now.Sub(time.Unix(newest, 0))
	if d < 0 {
		return 0
	}
	return d
}

// Flush merges every module's HistSnapshot into one Save. Nothing sampled
// yet means nothing written: an early flush must not clobber a good
// snapshot with an empty one.
func Flush(path string, mods []module.Persistent) error {
	merged := map[string]module.HistState{}
	for _, m := range mods {
		for name, st := range m.HistSnapshot() {
			merged[name] = st
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return Save(path, merged)
}

// FlushLoop persists mods' histories on every tick and once more when ctx
// is cancelled. The shutdown flush is best-effort -- launchctl kickstart -k
// may SIGKILL before it runs -- which is why the ticker is the load-bearing
// mechanism. Flush failures are loud, never fatal.
func FlushLoop(ctx context.Context, path string, mods []module.Persistent, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			if err := Flush(path, mods); err != nil {
				log.Printf("khudson bus: hist snapshot (shutdown): %v", err)
			}
			return
		case <-tick:
			if err := Flush(path, mods); err != nil {
				log.Printf("khudson bus: hist snapshot: %v", err)
			}
		}
	}
}
