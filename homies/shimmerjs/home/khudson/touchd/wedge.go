package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

// The watch reports disagreement between HID activity and aggregate session idle.
// Pointer motion can end the disagreement while keyboard delivery remains broken.
const (
	// wedgePoll is fast enough to catch the onset within a second and cheap
	// enough to run forever: one map walk over a handful of atomics plus one
	// CoreGraphics call.
	wedgePoll = time.Second
	// wedgeIdle is how far the pipeline's idle clock must run past our own
	// evidence of input before it counts. Generous on purpose -- someone who
	// genuinely has not touched anything for this long is producing no
	// reports either, so it is the AND of the two that makes this specific
	// rather than the threshold.
	wedgeIdle = 5 * time.Second
	// wedgeFresh is how recently reports must have arrived to count as "the
	// hardware is sending". Two polls, so a single missed tick does not read
	// as the hardware going quiet.
	wedgeFresh = 2 * wedgePoll
)

// This IORegistry field is diagnostic evidence, not a documented global lock API.
var secureInputPIDRE = regexp.MustCompile(`"kCGSSessionSecureInputPID"\s*=\s*(\d+)`)

// wedgeWatch reports input-delivery stalls. The clock, the idle probe and
// the console read are injected so the policy tests without any of them.
type wedgeWatch struct {
	rate *inputRate
	idle func() time.Duration
	// console reads the IOConsoleUsers dictionary; injected for tests.
	console func() string

	prev     int64     // last total seen, to spot new reports
	lastSeen time.Time // last tick that saw the total move
	onset    time.Time // zero when not wedged
}

func newWedgeWatch(rate *inputRate) *wedgeWatch {
	return &wedgeWatch{rate: rate, idle: osIdle, console: readConsoleUsers}
}

// readConsoleUsers returns the IOConsoleUsers dictionary, or "" if it
// cannot be read. Shelling out is fine HERE and nowhere near the poll: this
// runs once, at onset, on an event that happens a few times a month.
func readConsoleUsers() string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := probeOutput(ctx, "/usr/sbin/ioreg", "-l", "-w", "0", "-d", "1")
	hidBoundaries.Observe("input.console.query", err, "retain unknown diagnostic state; do not infer secure-input ownership")
	if err != nil {
		return ""
	}
	return string(out)
}

func (w *wedgeWatch) secureInput() string {
	if w.console == nil {
		return "secure input: unknown"
	}
	console := w.console()
	if console == "" {
		return "secure input: unknown (console read failed)"
	}
	m := secureInputPIDRE.FindStringSubmatch(console)
	if m == nil {
		return "secure input: holder not observed in console snapshot"
	}
	name := "?"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := probeOutput(ctx, "/bin/ps", "-o", "comm=", "-p", m[1])
	hidBoundaries.Observe("input.console.process", err, "keep PID evidence; process name remains unknown")
	if err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			name = s
		}
	}
	return fmt.Sprintf("secure input field names pid %s (%s)", m[1], name)
}

// tick advances the watch one poll and returns the line to log, or "".
//
// Entering needs both halves. LEAVING deliberately does not: it waits for
// the pipeline's idle clock to drop, meaning it actually delivered
// something. Clearing when the reports stop instead would call the wedge
// over the moment you gave up typing at a dead machine, which is exactly
// when it is still happening.
func (w *wedgeWatch) tick(now time.Time) string {
	total := w.rate.total()
	if total > w.prev {
		w.lastSeen = now
	}
	// The counters are cumulative across log intervals.
	w.prev = total

	idle := w.idle()
	sending := !w.lastSeen.IsZero() && now.Sub(w.lastSeen) <= wedgeFresh

	if w.onset.IsZero() {
		if sending && idle >= wedgeIdle {
			w.onset = now
			return fmt.Sprintf("SUSPECTED WEDGE: HID reports arriving with %s session idle -- possible stalled input; %s",
				idle.Round(time.Second), w.secureInput())
		}
		return ""
	}
	if idle < wedgeIdle {
		d := now.Sub(w.onset)
		w.onset = time.Time{}
		return fmt.Sprintf("IDLE DISAGREEMENT ENDED after %s -- a session event was observed; keyboard/click recovery unverified", d.Round(time.Second))
	}
	return ""
}

// run polls until ctx ends.
func (w *wedgeWatch) run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if line := w.tick(now); line != "" {
				fmt.Println(line)
			}
		}
	}
}

var errProbeOutputLimit = errors.New("diagnostic command output exceeds 1 MiB")

type probeCapture struct {
	buffer   bytes.Buffer
	cancel   context.CancelFunc
	exceeded bool
}

func (b *probeCapture) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-b.buffer.Len() {
		b.exceeded = true
		b.cancel()
		return 0, errProbeOutputLimit
	}
	return b.buffer.Write(p)
}

// Diagnostic sampling must not strand the input monitor on a stuck pipe.
func probeOutput(parent context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	output := &probeCapture{cancel: cancel}
	cmd.Stdout = output
	err := cmd.Run()
	if output.exceeded {
		return nil, errProbeOutputLimit
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return output.buffer.Bytes(), err
}
