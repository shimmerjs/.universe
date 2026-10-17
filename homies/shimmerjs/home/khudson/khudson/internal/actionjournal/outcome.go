package actionjournal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/shimmerjs/khudson/khudson/internal/socketio"
)

// ExecutionState classifies an attempted operation. Interruption, lost replies,
// and partial publication cannot establish a definite failure.
func ExecutionState(err error) State {
	if err == nil {
		return Succeeded
	}
	if errors.Is(err, socketio.ErrUncertainDelivery) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, exec.ErrWaitDelay) {
		return Uncertain
	}
	var outcome interface{ OutcomeUncertain() bool }
	if errors.As(err, &outcome) && outcome.OutcomeUncertain() {
		return Uncertain
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return Uncertain
		}
	}
	return Failed
}
