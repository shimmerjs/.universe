package main

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// The wedge watch joins the two halves of the signal that identify this
// machine's recurring input hang, and writes the one line reconstructing it
// afterwards used to cost an afternoon.
//
// magicbusd reads HID reports below the macOS event pipeline; osIdle asks
// the pipeline how long it believes it has been since any input reached it.
// Reports arriving while the pipeline insists it has been idle for far
// longer IS the wedge: the hardware is sending and something above us has
// stopped delivering. Neither half means anything alone -- a quiet keyboard
// and an idle pipeline is just nobody typing.
//
// It exists because every episode so far destroyed its own evidence. The
// machine needed a hard reset, and pinning the onset after the fact meant
// triangulating powerd's assertion age against per-minute HID counts. The
// unified log keeps about a month, so a timestamped onset is the difference
// between a grep and an afternoon (2026-08-23: onset 17:01:02, 194 keyboard
// reports delivered to nothing, resolved instantly on unplugging the Edge).
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

// secureInputPIDRE pulls the holder out of IORegistry's IOConsoleUsers
// dictionary. macOS adds this key to the console session ONLY while some
// process has secure event input enabled, so its presence is the fact and
// its absence is the other fact.
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
	out, err := exec.Command("ioreg", "-l", "-w", "0", "-d", "1").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

// secureInput names the process holding secure event input, which is the
// first thing to know about an input stall that followed a permission
// prompt. Secure input is how a password field or an authorization dialog
// takes the keyboard, and a holder that never gives it back is a documented
// way to lose input system-wide while the hardware keeps sending -- exactly
// this wedge's shape. macOS publishes the holder only while it is held, so
// this has to be sampled AT onset; it cannot be recovered afterwards, which
// is why every previous episode could not answer the question.
func (w *wedgeWatch) secureInput() string {
	if w.console == nil {
		return "secure input: unknown"
	}
	m := secureInputPIDRE.FindStringSubmatch(w.console())
	if m == nil {
		return "secure input: not held"
	}
	name := "?"
	if out, err := exec.Command("ps", "-o", "comm=", "-p", m[1]).Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			name = s
		}
	}
	return fmt.Sprintf("secure input HELD by pid %s (%s)", m[1], name)
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
	// a drain zeroes the counters mid-interval; that is bookkeeping, not
	// the hardware going quiet, so resync rather than read it as either
	w.prev = total

	idle := w.idle()
	sending := !w.lastSeen.IsZero() && now.Sub(w.lastSeen) <= wedgeFresh

	if w.onset.IsZero() {
		if sending && idle >= wedgeIdle {
			w.onset = now
			return fmt.Sprintf("WEDGE: hid reports arriving but the event pipeline reports %s idle -- input delivery is stalled; %s",
				idle.Round(time.Second), w.secureInput())
		}
		return ""
	}
	if idle < wedgeIdle {
		d := now.Sub(w.onset)
		w.onset = time.Time{}
		return fmt.Sprintf("WEDGE CLEARED after %s -- the event pipeline is delivering again", d.Round(time.Second))
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
