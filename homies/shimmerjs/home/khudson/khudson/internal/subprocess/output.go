// Package subprocess bounds provider command output and lifetime.
package subprocess

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

const maxOutput = 8 << 20

var ErrOutputLimit = errors.New("subprocess output exceeds limit")

type boundedOutput struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if n > b.limit-b.buffer.Len() {
		b.buffer.Write(p[:b.limit-b.buffer.Len()])
		b.exceeded = true
		b.cancel()
		return 0, ErrOutputLimit
	}
	return b.buffer.Write(p)
}

// Output caps a command at five seconds (or the caller's earlier deadline),
// kills its process group on cancellation, and bounds pipe-drain time. Stdout
// is capped at 8 MiB and stderr at 64 KiB. Exit errors retain captured stderr.
func Output(parent context.Context, name string, args ...string) ([]byte, error) {
	return OutputWithTimeout(parent, 5*time.Second, name, args...)
}

// OutputWithTimeout preserves an integration's explicit deadline while sharing
// the output and pipe-drain bounds. An earlier parent deadline still wins.
func OutputWithTimeout(parent context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	stdout := &boundedOutput{limit: maxOutput, cancel: cancel}
	stderr := &boundedOutput{limit: 64 << 10, cancel: cancel}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if stdout.exceeded || stderr.exceeded {
		return stdout.buffer.Bytes(), ErrOutputLimit
	}
	if ctx.Err() != nil {
		return stdout.buffer.Bytes(), ctx.Err()
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		exit.Stderr = stderr.buffer.Bytes()
	}
	return stdout.buffer.Bytes(), err
}
