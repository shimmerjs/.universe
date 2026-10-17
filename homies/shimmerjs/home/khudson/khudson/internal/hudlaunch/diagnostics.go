package hudlaunch

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
)

// Diagnostic records a boundary outcome and the recovery actually selected.
// Unknown errors retain their message/code; they are not guessed to be TCC
// denial, display absence, or a terminal WindowServer failure.
type Diagnostic struct {
	Time          time.Time                    `json:"time"`
	Notifications *cgdisplay.NotificationStats `json:"notifications,omitempty"`
	Heartbeat     *uint64                      `json:"application_heartbeat,omitempty"`
	SecureInput   *bool                        `json:"secure_input,omitempty"`
	Boundary      string                       `json:"boundary"`
	Outcome       string                       `json:"outcome"`
	Kind          string                       `json:"kind,omitempty"`
	Code          *int                         `json:"code,omitempty"`
	Action        string                       `json:"action"`
	Attempt       int                          `json:"attempt,omitempty"`
	Error         string                       `json:"error,omitempty"`
	Detail        string                       `json:"detail,omitempty"`
}

func recordFailure(opts Options, boundary string, err error, action string, attempt int) {
	d := Diagnostic{Boundary: boundary, Outcome: "error", Kind: "unknown", Action: action, Attempt: attempt, Error: err.Error()}
	var api *cgdisplay.APIError
	var application *cgdisplay.ApplicationError
	var exit *exec.ExitError
	var syntax *json.SyntaxError
	var shape *json.UnmarshalTypeError
	switch {
	case errors.As(err, &application):
		d.Kind = application.Kind()
		code := application.Code
		d.Code = &code
	case errors.As(err, &api):
		d.Kind = api.Kind()
		code := int(api.Code)
		d.Code = &code
	case errors.Is(err, context.Canceled):
		d.Kind = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		d.Kind = "timeout"
	case errors.Is(err, fs.ErrNotExist):
		d.Kind = "executable_missing"
		if strings.HasPrefix(boundary, "state.") {
			d.Kind = "filesystem_not_found"
		}
	case errors.Is(err, fs.ErrPermission):
		d.Kind = "filesystem_permission"
	case errors.As(err, &exit):
		d.Kind = "process_exit"
		code := exit.ExitCode()
		d.Code = &code
		d.Detail = strings.TrimSpace(string(exit.Stderr))
	case errors.As(err, &syntax):
		d.Kind = "malformed_json"
	case errors.As(err, &shape):
		d.Kind = "invalid_shape"
	case errors.Is(err, errInvalidSnapshot):
		d.Kind = "invalid_shape"
	case strings.HasPrefix(boundary, "layout."):
		d.Kind = "unresolved_layout"
	case boundary == "watch.receive":
		d.Kind = "source_closed"
	}
	if len(d.Error) > 2048 {
		d.Error = d.Error[:2048]
	}
	if len(d.Detail) > 2048 {
		d.Detail = d.Detail[:2048]
	}
	if opts.Record != nil {
		opts.Record(d)
	}
}
