package subprocess

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func TestOutputBoundsAndPreservesExit(t *testing.T) {
	out, err := Output(context.Background(), "sh", "-c", "printf ok; printf failed >&2; exit 7")
	var exit *exec.ExitError
	if string(out) != "ok" || !errors.As(err, &exit) || exit.ExitCode() != 7 || string(exit.Stderr) != "failed" {
		t.Fatalf("out=%q err=%v", out, err)
	}
}
func TestOutputDeadlineReapsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := Output(ctx, "sh", "-c", "sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
}
func TestOutputLimitCancelsProducer(t *testing.T) {
	_, err := Output(context.Background(), "sh", "-c", "while :; do printf '0123456789012345678901234567890123456789012345678901234567890123456789' >&2; done")
	if !errors.Is(err, ErrOutputLimit) {
		t.Fatalf("limit=%v", err)
	}
}
func TestBoundedOutputRetainsOnlyLimit(t *testing.T) {
	cancelled := false
	b := boundedOutput{limit: 3, cancel: func() { cancelled = true }}
	if _, err := b.Write(bytes.Repeat([]byte("x"), 10)); !errors.Is(err, ErrOutputLimit) || b.buffer.Len() != 3 || !cancelled {
		t.Fatalf("len=%d cancel=%t err=%v", b.buffer.Len(), cancelled, err)
	}
}

func TestOutputExplicitDeadline(t *testing.T) {
	_, err := OutputWithTimeout(context.Background(), 30*time.Millisecond, "sh", "-c", "sleep 30 & wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("explicit timeout lost: %v", err)
	}
}
