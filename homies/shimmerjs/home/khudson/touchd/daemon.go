package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
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

// runDaemon serves parsed frames on the touch socket from BOTH Edge input
// collections concurrently: the digitizer (tier 1 -- silent until the vendor
// mode switch is cracked, kept open so a future unlock streams immediately)
// and the mouse collection (tier 2 -- the proven single-touch path).
// A third source, the Moonlander raw-HID vendor channel, serves decoded key
// events on the keys socket (kb; nil when the keys broadcaster could not
// start). Each source reopens with backoff on device loss; the digitizer
// reasserts device mode on every reopen and de-asserts on shutdown.
// -record captures Edge reports only.
func runDaemon(ctx context.Context, b, kb *broadcaster, rec *recorder, noMode bool) error {
	var mu sync.Mutex
	emit := func(t int64, raw []byte) {
		mu.Lock()
		defer mu.Unlock()
		if rec != nil {
			rec.write(t, raw)
		}
		if f, ok := parseReport(t, raw); ok {
			b.publishJSON(f)
		}
	}
	var wg sync.WaitGroup
	for _, mouse := range []bool{false, true} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			collectionLoop(ctx, mouse, noMode, emit)
		}()
	}
	if kb != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			moonLoop(ctx, func(ev KeyEvent) { kb.publishJSON(ev) })
		}()
	}
	wg.Wait()
	return nil
}

// collectionLoop opens one collection and pumps it into emit, reopening with
// backoff on device loss or open failure (a missing Input Monitoring grant or
// a seize-holding driver surfaces here as repeated open errors).
func collectionLoop(ctx context.Context, mouse, noMode bool, emit func(int64, []byte)) {
	name := "digitizer"
	if mouse {
		name = "mouse"
	}
	// a permanently seized collection (gestures driver holding the
	// digitizer, a user-parked state) retries forever -- the wait backs
	// off to reconnectCap so an all-day failure goes quiet, and logs only
	// when the error changes or hourly, not per attempt
	var bo reopenBackoff
	var lastErr string
	var lastLog time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		dev, asserted, err := openCollection(mouse, noMode, false)
		if err != nil {
			wait := bo.fail(errors.Is(err, errAbsent))
			if err.Error() != lastErr || time.Since(lastLog) >= time.Hour {
				fmt.Fprintf(os.Stderr, "%s open: %v (retrying in %s, backoff caps at %s, logging on change or hourly)\n", name, err, wait, reconnectCap)
				lastErr = err.Error()
				lastLog = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		bo.reset()
		lastErr = ""
		fmt.Printf("%s open (mode asserted: %v)\n", name, asserted)

		err = readLoop(ctx, dev, emit)
		if ctx.Err() != nil {
			if asserted {
				deassertMode(dev)
			}
			dev.Close()
			return
		}
		dev.Close()
		fmt.Fprintf(os.Stderr, "%s gone, reopening: %v\n", name, err)
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
