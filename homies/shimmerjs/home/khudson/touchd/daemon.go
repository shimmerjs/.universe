package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sstallion/go-hid"
)

const (
	readTimeout  = 500 * time.Millisecond
	reconnectMin = 500 * time.Millisecond
	// seized/present-but-unopenable converges here: every attempt costs an
	// IOKit enumeration plus runningboardd churn, and a seize holds for
	// hours (the incident's 5s retries were this class).
	reconnectCap = 5 * time.Minute
	// absent converges lower: a replug is only observed when the current
	// timer expires, so this cap IS the worst-case reattach latency, and
	// absent enumerations are the cheap failure class.
	absentCap = 30 * time.Second
)

// socket bind retry: a just-killed prior instance can hold a module socket
// while its listener unwinds; the dial guard in newBroadcaster makes a
// genuinely live daemon fail every attempt fast. Vars so squatter tests
// shrink the wait.
var (
	bindAttempts  = 3
	bindRetryWait = time.Second
)

// reopenBackoff schedules the wait between open attempts for a source that
// will not open. Every attempt costs an IOKit enumeration (runningboardd
// resolves the client each time), so a chronic failure -- a seize-holding
// driver, a parked absence -- must converge to its class cap, not poll at
// seconds all day. The wait doubles from reconnectMin to the class cap and
// resets on success or on a failure-class flip (absent <-> present but
// unopenable), the cheap device-set-change signal: replugging or freeing the
// device flips the class, so reattach latency stays on the fast ramp, while
// error-text churn inside one class cannot pin the schedule at the floor.
type reopenBackoff struct {
	next   time.Duration // 0 = fresh episode
	absent bool
}

// fail records one failed attempt and returns how long to wait before the
// next; absent is the failure class (true = collection not enumerable).
func (b *reopenBackoff) fail(absent bool) time.Duration {
	ceil := reconnectCap
	if absent {
		ceil = absentCap
	}
	if b.next == 0 || absent != b.absent {
		b.next = reconnectMin
	}
	b.absent = absent
	wait := min(b.next, ceil)
	b.next = min(b.next*2, ceil)
	return wait
}

// reset marks a successful open; the next failure starts a fresh episode.
func (b *reopenBackoff) reset() { b.next = 0 }

// runDaemon assembles the enabled modules and runs them under the registry:
// the edge module serves parsed frames on the touch socket, the moonlander
// module serves decoded key events on the keys socket, the logiretch module
// serves MX Master state on the logi socket, and every module reopens its
// device via the shared arrival scanner. A module whose socket cannot bind is
// disabled loudly while the others run (config problems fail fast in run
// instead). -record captures Edge reports only.
func runDaemon(ctx context.Context, opts options, enabled map[string]bool, logiCfg *logiConfig, rec *recorder) error {
	return runDaemonScanner(ctx, newScanner(), opts, enabled, logiCfg, rec)
}

func runDaemonScanner(ctx context.Context, sc *scanner, opts options, enabled map[string]bool, logiCfg *logiConfig, rec *recorder) error {
	var tap func(int64, []byte)
	if rec != nil {
		tap = rec.write
	}
	var lc logiConfig
	if logiCfg != nil {
		lc = *logiCfg
	}
	specs := []struct {
		mod    Module
		socket string
		tap    func(int64, []byte)
	}{
		{mod: &edgeModule{noMode: opts.noMode}, socket: opts.socket, tap: tap},
		{mod: moonModule{}, socket: opts.keysSocket},
		{mod: &logiretchModule{cfg: lc}, socket: opts.logiSocket},
	}
	var entries []moduleEntry
	for _, s := range specs {
		if !enabled[s.mod.Name()] {
			continue
		}
		// a module socket failing to bind must not take down the others;
		// that module just stays off (loud once, review posture)
		b, err := bindBroadcaster(ctx, s.socket)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s socket unavailable, %s module disabled: %v\n", s.socket, s.mod.Name(), err)
			continue
		}
		defer b.close()
		entries = append(entries, moduleEntry{mod: s.mod, env: Env{publish: b.publishJSON, recordTap: s.tap, scan: sc}})
	}
	if len(entries) == 0 {
		return errors.New("no modules running")
	}
	return runModules(ctx, sc, entries)
}

// bindBroadcaster wraps newBroadcaster in a bounded retry over the
// transient-conflict window; the wait honors ctx so shutdown never hangs
// inside a retry.
func bindBroadcaster(ctx context.Context, path string) (*broadcaster, error) {
	for attempt := 1; ; attempt++ {
		b, err := newBroadcaster(path)
		if err == nil || attempt >= bindAttempts {
			return b, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(bindRetryWait):
		}
	}
}

// runStream is spike mode: open the collection and print parsed frames (and
// any unrecognized reports) to stdout; exits when the device disappears.
func runStream(ctx context.Context, rec *recorder, mouse, noMode bool) error {
	dev, asserted, err := openCollection(mouse, noMode, true)
	if err != nil {
		return err
	}
	defer dev.Close()

	fmt.Println("streaming reports -- touch the glass (ctrl-c to quit)")
	err = readLoop(ctx, dev, func(t int64, raw []byte) {
		if rec != nil {
			rec.write(t, raw)
		}
		if f, ok := parseReport(t, raw); ok {
			printFrame(f)
		} else {
			fmt.Printf("report id=0x%02X len=%d raw=%X\n", raw[0], len(raw), raw)
		}
	})
	if asserted {
		deassertMode(dev)
	}
	if ctx.Err() != nil {
		fmt.Println("bye")
		return nil
	}
	return err
}

// readLoop pumps raw input reports into emit until ctx is canceled or the
// device disappears. hidapi surfaces timeouts as ErrTimeout; any other read
// error is treated as device loss.
func readLoop(ctx context.Context, dev *hid.Device, emit func(t int64, raw []byte)) error {
	buf := make([]byte, 64)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := dev.ReadWithTimeout(buf, readTimeout)
		if errors.Is(err, hid.ErrTimeout) {
			continue
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			continue
		}
		raw := make([]byte, n)
		copy(raw, buf[:n])
		emit(time.Now().UnixNano(), raw)
	}
}
