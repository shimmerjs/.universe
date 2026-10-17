// Package diagnostic records boundary failures without guessing their cause.
package diagnostic

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/mattn/go-sqlite3"
)

type Event struct {
	ViewID            string    `json:"view_id,omitempty"`
	ParentOperationID string    `json:"parent_operation_id,omitempty"`
	Owner             string    `json:"owner,omitempty"`
	Session           string    `json:"session,omitempty"`
	OperationID       string    `json:"operation_id,omitempty"`
	Generation        uint64    `json:"generation,omitempty"`
	Time              time.Time `json:"time"`
	Boundary          string    `json:"boundary"`
	Outcome           string    `json:"outcome"`
	Kind              string    `json:"kind,omitempty"`
	Code              *int      `json:"code,omitempty"`
	Action            string    `json:"action"`
	Count             uint64    `json:"count,omitempty"`
	Error             string    `json:"error,omitempty"`
}

func classify(err error) (string, *int) {
	var errno syscall.Errno
	var exit *exec.ExitError
	var ne net.Error
	var se sqlite3.Error
	var typed interface{ Kind() string }
	switch {
	case errors.As(err, &typed):
		var coded interface{ NativeCode() int }
		if errors.As(err, &coded) {
			code := coded.NativeCode()
			return typed.Kind(), &code
		}
		return typed.Kind(), nil
	case errors.As(err, &se):
		kind := "sqlite_error"
		switch se.Code {
		case sqlite3.ErrBusy, sqlite3.ErrLocked:
			kind = "sqlite_busy"
		case sqlite3.ErrReadonly, sqlite3.ErrPerm:
			kind = "sqlite_permission"
		case sqlite3.ErrFull:
			kind = "storage_full"
		case sqlite3.ErrCorrupt, sqlite3.ErrNotADB:
			kind = "sqlite_corrupt"
		case sqlite3.ErrIoErr:
			kind = "sqlite_io"
		}
		code := int(se.ExtendedCode)
		return kind, &code
	case errors.Is(err, context.Canceled):
		return "cancelled", nil
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout", nil
	case errors.As(err, &exit):
		n := exit.ExitCode()
		return "process_exit", &n
	case errors.As(err, &errno):
		kind := "os_error"
		switch errno {
		case syscall.ENOENT:
			kind = "not_found"
		case syscall.EACCES, syscall.EPERM:
			kind = "permission"
		case syscall.ECONNREFUSED:
			kind = "connection_refused"
		case syscall.EPIPE, syscall.ECONNRESET:
			kind = "connection_lost"
		case syscall.ENOSPC:
			kind = "storage_full"
		}
		n := int(errno)
		return kind, &n
	case errors.As(err, &ne) && ne.Timeout():
		return "timeout", nil
	case errors.Is(err, fs.ErrPermission):
		return "permission", nil
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return "connection_eof", nil
	default:
		return fmt.Sprintf("%T", err), nil
	}
}

type Recorder struct {
	mu      sync.Mutex
	counts  map[string]uint64
	failing map[string]bool
	emit    func(Event)
}

func New(emit func(Event)) *Recorder {
	return &Recorder{counts: make(map[string]uint64), failing: make(map[string]bool), emit: emit}
}

// First and power-of-two repeats are retained per distinct error. The bounded
// ledger resets when full; unfamiliar errors are always recorded immediately.
func (r *Recorder) Observe(boundary string, err error, action string) {
	if joined := errorLeaves(err); len(joined) > 1 {
		for _, leaf := range joined {
			r.observe(boundary, fmt.Errorf("%s: %w", err.Error(), leaf), action)
		}
		return
	}
	r.observe(boundary, err, action)
}

// errors.As selects only the first matching child of errors.Join. Emit each
// leaf so a partial operation preserves every distinct native failure code.
func errorLeaves(err error) []error {
	if err == nil {
		return nil
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		var leaves []error
		for _, child := range multi.Unwrap() {
			leaves = append(leaves, errorLeaves(child)...)
		}
		return leaves
	}
	if child := errors.Unwrap(err); child != nil {
		return errorLeaves(child)
	}
	return []error{err}
}

func (r *Recorder) observe(boundary string, err error, action string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		if r.failing[boundary] {
			r.emit(Event{Time: time.Now().UTC(), Boundary: boundary, Outcome: "recovered", Action: action})
			delete(r.failing, boundary)
		}
		return
	}
	kind, code := classify(err)
	message := err.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	key := boundary + "\x00" + kind + "\x00" + message
	if code != nil {
		key += fmt.Sprintf("\x00%d", *code)
	}
	if len(r.counts) >= 128 {
		clear(r.counts)
		clear(r.failing)
	}
	newEpisode := !r.failing[boundary]
	r.counts[key]++
	r.failing[boundary] = true
	n := r.counts[key]
	if n&(n-1) != 0 && !newEpisode {
		return
	}
	r.emit(Event{Time: time.Now().UTC(), Boundary: boundary, Outcome: "error", Kind: kind, Code: code, Action: action, Count: n, Error: message})
}

var processSession = rand.Text()

func emitDefault(e Event) {
	e.Owner = os.Args[0]
	e.Session = processSession
	data, _ := json.Marshal(e)
	log.Printf("boundary-event %s", data)
}

// Operation records committed state transitions; failure coalescing must not
// hide an individual operation's recovery decision.
func Operation(boundary, id, outcome, action string) {
	OperationGeneration(boundary, id, 0, outcome, action)
}

func OperationGeneration(boundary, id string, generation uint64, outcome, action string) {
	emitDefault(Event{Time: time.Now().UTC(), Boundary: boundary, OperationID: id, Generation: generation, Outcome: outcome, Action: action})
}

var Default = New(emitDefault)

func Action(boundary, id, parent, view string, generation uint64, outcome, action string) {
	emitDefault(Event{Time: time.Now().UTC(), Boundary: boundary, OperationID: id, ParentOperationID: parent, ViewID: view, Generation: generation, Outcome: outcome, Action: action})
}
