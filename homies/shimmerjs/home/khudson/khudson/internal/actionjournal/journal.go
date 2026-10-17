// Package actionjournal records durable acceptance and recovery decisions.
package actionjournal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mattn/go-sqlite3"
)

type State string
type Policy string

const (
	Pending          State  = "pending"
	Running          State  = "running"
	Succeeded        State  = "succeeded"
	Dispatched       State  = "dispatched"
	Failed           State  = "failed"
	Discarded        State  = "discarded"
	Uncertain        State  = "uncertain"
	DiscardOnRestart Policy = "discard-on-restart"
	Reconcile        Policy = "reconcile"
	Review           Policy = "review"
)

const maxRecords = 2048
const maxPayload = 64 * 1024

var ErrCapacity = errors.New("action journal unresolved-record capacity reached")
var ErrConflict = errors.New("operation id already used for different action")
var ErrTransition = errors.New("action journal state transition refused")

type Request struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Source  string          `json:"source"`
	Policy  Policy          `json:"policy"`
	Payload json.RawMessage `json:"payload"`
}
type Record struct {
	Request
	Sequence  int64  `json:"sequence"`
	State     State  `json:"state"`
	Reason    string `json:"reason,omitempty"`
	CreatedNS int64  `json:"created_ns"`
	UpdatedNS int64  `json:"updated_ns"`
}

type Store struct{ db *sql.DB }

func init() {
	// These pragmas are connection-local. database/sql can replace a connection
	// after an I/O failure, so setting them once at Open is insufficient.
	sql.Register("khudson-actionjournal", &sqlite3.SQLiteDriver{ConnectHook: func(conn *sqlite3.SQLiteConn) error {
		_, err := conn.Exec("PRAGMA fullfsync=ON; PRAGMA checkpoint_fullfsync=ON;", nil)
		return err
	}})
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	q := u.Query()
	q.Set("_busy_timeout", "250")
	q.Set("_journal_mode", "WAL")
	q.Set("_synchronous", "FULL")
	q.Set("_txlock", "immediate")
	u.RawQuery = q.Encode()
	db, err := sql.Open("khudson-actionjournal", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fail := func(err error) (*Store, error) { db.Close(); return nil, err }
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != 0 && version != 1 {
		return fail(fmt.Errorf("unsupported action journal version %d", version))
	}
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS operations (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, source TEXT NOT NULL,
 policy TEXT NOT NULL, payload BLOB NOT NULL,
 state TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
 created_ns INTEGER NOT NULL, updated_ns INTEGER NOT NULL);
 PRAGMA user_version=1;`)
	if err != nil {
		return fail(err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func validRequest(r Request) bool {
	if len(r.ID) == 0 || len(r.ID) > 128 || len(r.Kind) == 0 || len(r.Kind) > 64 || len(r.Source) > 256 || len(r.Payload) > maxPayload || !json.Valid(r.Payload) {
		return false
	}
	for _, c := range r.ID {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return r.Policy == DiscardOnRestart || r.Policy == Reconcile || r.Policy == Review
}

// Accept commits before returning fresh=true. Duplicate IDs are never accepted
// as new work while retained, including when the original acknowledgement was lost.
// Clients must not retry IDs beyond the retained history window.
func (s *Store) Accept(ctx context.Context, request Request) (record Record, fresh bool, err error) {
	if !validRequest(request) {
		return record, false, errors.New("invalid action journal request")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return record, false, err
	}
	defer tx.Rollback()
	record, err = scan(tx.QueryRowContext(ctx, "SELECT "+columns+" FROM operations WHERE id=?", request.ID))
	if err == nil {
		if record.Kind != request.Kind || record.Source != request.Source || record.Policy != request.Policy || string(record.Payload) != string(request.Payload) {
			return record, false, ErrConflict
		}
		return record, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return record, false, err
	}
	// Keep recent terminal outcomes for inspection and deduplication; unresolved
	// records are never pruned to make room for new work. Retain the newest
	// successful outcome per kind/source/policy while an older reconciliation
	// remains unresolved, so recovery can prove the old intent was superseded.
	if _, err = tx.ExecContext(ctx, `DELETE FROM operations
 WHERE state IN ('succeeded','dispatched','failed','discarded')
 AND sequence NOT IN (SELECT sequence FROM operations ORDER BY sequence DESC LIMIT 1024)
 AND NOT (
   state IN ('succeeded','dispatched')
   AND EXISTS (SELECT 1 FROM operations older
     WHERE older.policy='reconcile' AND older.state IN ('pending','running')
     AND older.kind=operations.kind AND older.source=operations.source
     AND older.sequence<operations.sequence)
   AND NOT EXISTS (SELECT 1 FROM operations newer
     WHERE newer.kind=operations.kind AND newer.source=operations.source
     AND newer.policy=operations.policy AND newer.state IN ('succeeded','dispatched')
     AND newer.sequence>operations.sequence))`); err != nil {
		return record, false, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM operations").Scan(&count); err != nil {
		return record, false, err
	}
	if count >= maxRecords {
		return record, false, ErrCapacity
	}
	now := time.Now().UnixNano()
	result, err := tx.ExecContext(ctx, `INSERT INTO operations(id,kind,source,policy,payload,state,created_ns,updated_ns) VALUES(?,?,?,?,?,?,?,?)`, request.ID, request.Kind, request.Source, request.Policy, []byte(request.Payload), Pending, now, now)
	if err != nil {
		return record, false, err
	}
	sequence, err := result.LastInsertId()
	if err != nil {
		return record, false, err
	}
	if err = tx.Commit(); err != nil {
		return record, false, err
	}
	return Record{Request: request, Sequence: sequence, State: Pending, CreatedNS: now, UpdatedNS: now}, true, nil
}

func (s *Store) Transition(ctx context.Context, id string, from, to State, reason string) error {
	valid := from == Pending && (to == Running || to == Discarded || to == Uncertain) || from == Running && (to == Succeeded || to == Failed || to == Discarded || to == Uncertain || to == Dispatched) || from == Uncertain && to == Discarded
	if !valid {
		return ErrTransition
	}
	if len(reason) > 2048 {
		reason = reason[:2048]
	}
	res, err := s.db.ExecContext(ctx, "UPDATE operations SET state=?,reason=?,updated_ns=? WHERE id=? AND state=?", to, reason, time.Now().UnixNano(), id, from)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrTransition
	}
	return err
}

// Recover runs only after the bus acquires exclusive socket ownership. A
// running operation may have completed its external effect before the crash.
// It is never replayed automatically. Only explicit desired-state requests
// remain pending for reconciliation against current configuration.
func (s *Store) Recover(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixNano()
	for _, change := range []struct {
		from   State
		policy string
		to     State
		reason string
	}{
		{Running, string(Reconcile), Pending, "reconcile explicit desired state after owner restart"},
		{Running, "", Uncertain, "owner restarted during execution; external outcome unknown"},
		{Pending, string(DiscardOnRestart), Discarded, "owner restarted; transient action expired"},
		{Pending, string(Review), Uncertain, "owner restarted; action requires reconciliation"},
	} {
		query := "UPDATE operations SET state=?,reason=?,updated_ns=? WHERE state=?"
		args := []any{change.to, change.reason, now, change.from}
		if change.policy != "" {
			query += " AND policy=?"
			args = append(args, change.policy)
		}
		if _, err = tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const columns = "sequence,id,kind,source,policy,payload,state,reason,created_ns,updated_ns"

type scanner interface{ Scan(...any) error }

func scan(row scanner) (r Record, err error) {
	err = row.Scan(&r.Sequence, &r.ID, &r.Kind, &r.Source, &r.Policy, &r.Payload, &r.State, &r.Reason, &r.CreatedNS, &r.UpdatedNS)
	return
}
func (s *Store) List(ctx context.Context, state State) ([]Record, error) {
	query := "SELECT " + columns + " FROM operations"
	args := []any{}
	if state != "" {
		query += " WHERE state=?"
		args = append(args, state)
	}
	query += " ORDER BY sequence ASC LIMIT 2048"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Record, 0)
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

// Page bounds inspection responses independently of the total journal capacity.
// Next is the last returned sequence when more records were present. Pass it
// back as the next cursor; concurrent inserts may appear on later pages.
type Page struct {
	Records []Record `json:"records"`
	Next    int64    `json:"next,omitempty"`
}

const pageByteLimit = 1 << 20
const pageRecordLimit = 128

func ParseCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return 0, errors.New("journal cursor must be a nonnegative sequence number")
	}
	return cursor, nil
}

func (s *Store) Page(ctx context.Context, after int64) (Page, error) {
	page := Page{Records: make([]Record, 0)}
	if after < 0 {
		return page, errors.New("negative journal cursor")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+columns+" FROM operations WHERE sequence>? ORDER BY sequence ASC LIMIT ?", after, pageRecordLimit+1)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	size := 256 // page envelope and cursor
	for rows.Next() {
		r, err := scan(rows)
		if err != nil {
			return page, err
		}
		encoded, err := json.Marshal(r)
		if err != nil {
			return page, err
		}
		if len(page.Records) >= pageRecordLimit || size+len(encoded)+1 > pageByteLimit {
			if len(page.Records) == 0 {
				return page, errors.New("journal record exceeds inspection page limit")
			}
			page.Next = page.Records[len(page.Records)-1].Sequence
			break
		}
		page.Records = append(page.Records, r)
		size += len(encoded) + 1
	}
	return page, rows.Err()
}
