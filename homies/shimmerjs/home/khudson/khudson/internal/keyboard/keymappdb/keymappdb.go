// Package keymappdb reads a Moonlander layout out of Keymapp's local SQLite
// store, offline. The reader execs the host sqlite3 CLI (macOS ships
// /usr/bin/sqlite3) instead of linking a driver, keeping the sqlite
// dependency cone out of the module. The DB is opened
// read-only and never write-locked (Keymapp may hold it open): queries run
// -readonly against a file: URI with mode=ro&immutable=1, and blobs come
// back hex()-encoded so no CLI quoting mode can mangle them.
//
// Storage shape (verified empirically against a real store):
//
//   - revision(revisionId TEXT UNIQUE, data BLOB): one row per synced
//     revision. data is plain JSON text, the exact Oryx getLayout payload
//     ({"layout":{"revision":{layers,...}}}), NOT gzip or msgpack.
//   - metadata(data BLOB): one JSON row, {"keys":[...],"categories":[...]}.
//     keys is the code dictionary (code -> label / glyph).
//   - config(key TEXT, value TEXT): app settings; carries no active-revision
//     pointer, so the newest-synced revision (highest rowid) is taken as
//     active.
package keymappdb

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
)

// DefaultPath is Keymapp's store under the user's Application Support.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("keymappdb: resolve home: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", ".keymapp", "keymapp.sqlite3"), nil
}

// ErrNoRevision means the store exists but holds no synced revision (Keymapp
// installed but never connected to a board); callers render the sync hint.
var ErrNoRevision = errors.New("keymappdb: no synced revision")

// Revision is one stored layout revision plus the key dictionary that
// resolves its codes to legends.
type Revision struct {
	// ID is the revisionId column (the Oryx revision hash).
	ID string
	// Layout is the decoded getLayout payload; Layout.Layers carry the
	// positional per-layer key legends.
	Layout *oryx.Layout
	// Dict maps a QMK code to its metadata entry; nil-safe via Legend.
	Dict Dict
}

// revisionBlob is the top-level {"layout":{...}} wrapper each revision.data
// row holds. It mirrors the Oryx GraphQL layout node, so the inner revision
// unmarshals into the same shapes oryx.FetchLayout produces.
type revisionBlob struct {
	Layout struct {
		HashID   string `json:"hashId"`
		Title    string `json:"title"`
		Geometry string `json:"geometry"`
		Revision struct {
			HashID     string          `json:"hashId"`
			QmkVersion string          `json:"qmkVersion"`
			Title      string          `json:"title"`
			CreatedAt  string          `json:"createdAt"`
			Model      string          `json:"model"`
			MD5        string          `json:"md5"`
			Layers     []oryx.Layer    `json:"layers"`
			Combos     []oryx.Combo    `json:"combos"`
			Config     json.RawMessage `json:"config"`
		} `json:"revision"`
	} `json:"layout"`
}

func (b revisionBlob) toLayout() *oryx.Layout {
	r := b.Layout.Revision
	return &oryx.Layout{
		HashID:     b.Layout.HashID,
		Title:      b.Layout.Title,
		Geometry:   b.Layout.Geometry,
		RevisionID: r.HashID,
		QmkVersion: r.QmkVersion,
		CreatedAt:  r.CreatedAt,
		Model:      r.Model,
		MD5:        r.MD5,
		Layers:     r.Layers,
		Combos:     r.Combos,
		Config:     r.Config,
	}
}

// Sqlite3Bin resolves the sqlite3 CLI: the macOS system binary first, PATH
// as the fallback. Tests gate on this same resolution (skip-on-missing).
func Sqlite3Bin() (string, error) {
	const sys = "/usr/bin/sqlite3"
	if _, err := exec.LookPath(sys); err == nil {
		return sys, nil
	}
	return exec.LookPath("sqlite3")
}

// queryTimeout bounds one sqlite3 exec; the store is a small local file, so
// a slow call means a wedged binary, not a big read.
const queryTimeout = 10 * time.Second

// query runs one SQL statement against path via sqlite3 -readonly -json and
// returns stdout. The file: URI (percent-escaped; the default path has a
// space) carries mode=ro&immutable=1 so no lock is ever taken even while
// Keymapp holds the DB open.
func query(path, sql string) ([]byte, error) {
	bin, err := Sqlite3Bin()
	if err != nil {
		return nil, fmt.Errorf("keymappdb: sqlite3 unavailable: %w", err)
	}
	// -readonly never creates a store, but stat first so a missing file
	// fails uniformly across CLI versions
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("keymappdb: open: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("keymappdb: open: %w", err)
	}
	uri := url.URL{Scheme: "file", Path: abs, RawQuery: "mode=ro&immutable=1"}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-readonly", "-json", uri.String(), sql)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("keymappdb: sqlite3: %v: %s", err, bytes.TrimSpace(errb.Bytes()))
	}
	return out.Bytes(), nil
}

// Active returns the newest-synced revision (highest rowid) with its key
// dictionary. A missing file, an empty store, or an unreadable blob is
// reported to the caller; nothing panics and no write lock is taken.
func Active(path string) (*Revision, error) {
	out, err := query(path, `SELECT revisionId, hex(data) AS data FROM revision ORDER BY rowid DESC LIMIT 1;`)
	if err != nil {
		return nil, err
	}
	// -json prints nothing at all for zero rows
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, ErrNoRevision
	}
	var rows []struct {
		RevisionID string `json:"revisionId"`
		Data       string `json:"data"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("keymappdb: read revision: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrNoRevision
	}
	id := rows[0].RevisionID
	blob, err := hex.DecodeString(rows[0].Data)
	if err != nil {
		return nil, fmt.Errorf("keymappdb: read revision %s: %w", id, err)
	}

	var rb revisionBlob
	if err := json.Unmarshal(blob, &rb); err != nil {
		return nil, fmt.Errorf("keymappdb: decode revision %s: %w", id, err)
	}
	layout := rb.toLayout()
	if len(layout.Layers) == 0 {
		return nil, ErrNoRevision
	}

	dict, err := readDict(path)
	if err != nil {
		// the dictionary only enriches legends; a missing one degrades to
		// humanized codes rather than failing the whole read
		dict = nil
	}
	return &Revision{ID: id, Layout: layout, Dict: dict}, nil
}

func readDict(path string) (Dict, error) {
	out, err := query(path, `SELECT hex(data) AS data FROM metadata LIMIT 1;`)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("keymappdb: no metadata row")
	}
	blob, err := hex.DecodeString(rows[0].Data)
	if err != nil {
		return nil, err
	}
	var md struct {
		Keys []dictEntry `json:"keys"`
	}
	if err := json.Unmarshal(blob, &md); err != nil {
		return nil, err
	}
	d := make(Dict, len(md.Keys))
	for _, e := range md.Keys {
		if e.Deleted {
			continue
		}
		// first entry per code wins; the dictionary is priority-ordered but a
		// present entry beats a later duplicate
		if _, ok := d[e.Code]; !ok {
			d[e.Code] = e
		}
	}
	return d, nil
}
