package actionjournal

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/atomicfile"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
)

func TestInterruptedExecutionRequiresReview(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, exec.ErrWaitDelay, os.ErrDeadlineExceeded, socketio.ErrUncertainDelivery, &atomicfile.PublishedError{Err: errors.New("sync failed")}} {
		if got := ExecutionState(err); got != Uncertain {
			t.Fatalf("%v => %s", err, got)
		}
	}
	if ExecutionState(nil) != Succeeded || ExecutionState(errors.New("refused")) != Failed {
		t.Fatal("completion classification")
	}
	cmd := exec.Command("sh", "-c", "kill -TERM $$")
	if got := ExecutionState(cmd.Run()); got != Uncertain {
		t.Fatalf("signal outcome=%s", got)
	}
}
