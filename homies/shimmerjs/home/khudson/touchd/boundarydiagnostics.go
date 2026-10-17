// Boundary diagnostics record boundary failures without guessing their cause.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"syscall"
	"time"
)

type boundaryEvent struct {
	Time     time.Time `json:"time"`
	Boundary string    `json:"boundary"`
	Outcome  string    `json:"outcome"`
	Kind     string    `json:"kind,omitempty"`
	Code     *int      `json:"code,omitempty"`
	Action   string    `json:"action"`
	Count    uint64    `json:"count,omitempty"`
	Error    string    `json:"error,omitempty"`
}

var ioReturnPattern = regexp.MustCompile(`\(0x([0-9A-Fa-f]{8})\)`)

func classify(err error) (string, *int) {
	var local moduleTerminal
	if errors.As(err, &local) {
		return "module_contract", nil
	}
	var deviceError hidppError
	if errors.As(err, &deviceError) {
		n := int(deviceError)
		return "hidpp_device_error", &n
	}
	if match := ioReturnPattern.FindStringSubmatch(err.Error()); match != nil {
		n, _ := strconv.ParseUint(match[1], 16, 32)
		code := int(n)
		return "native_code_in_message", &code
	}
	var errno syscall.Errno
	var exit *exec.ExitError
	var ne net.Error
	switch {
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

type boundaryRecorder struct {
	mu      sync.Mutex
	counts  map[string]uint64
	failing map[string]bool
	emit    func(boundaryEvent)
}

func newBoundaryRecorder(emit func(boundaryEvent)) *boundaryRecorder {
	return &boundaryRecorder{counts: make(map[string]uint64), failing: make(map[string]bool), emit: emit}
}

// First and power-of-two repeats are retained per distinct error. The bounded
// ledger resets when full; unfamiliar errors are always recorded immediately.
func (r *boundaryRecorder) Observe(boundary string, err error, action string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil {
		if r.failing[boundary] {
			r.emit(boundaryEvent{Time: time.Now().UTC(), Boundary: boundary, Outcome: "recovered", Action: action})
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
	r.emit(boundaryEvent{Time: time.Now().UTC(), Boundary: boundary, Outcome: "error", Kind: kind, Code: code, Action: action, Count: n, Error: message})
}

var hidBoundaries = newBoundaryRecorder(func(e boundaryEvent) { data, _ := json.Marshal(e); log.Printf("boundary-event %s", data) })
