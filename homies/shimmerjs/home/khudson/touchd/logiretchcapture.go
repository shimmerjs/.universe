// logiretch-capture: a one-shot raw listen on the MX Master 4 vendor
// collection that dumps the device's STANDARD mouse input reports
// (report id 0x02) so their byte layout can be pinned from observation.
// The logiretch viewer's live-activation decode is built on that layout;
// nothing here writes to the device. Run it, then click every button and
// roll both wheels inside the window.
package main

import (
	"context"
	"io"
	"time"
)

// captureWindow is how long the listen runs: long enough to click five
// buttons and roll two wheels deliberately.
const captureWindow = 25 * time.Second

func runLogiretchCapture(ctx context.Context, w io.Writer) error {
	p := &logiretchProbe{w: w}
	p.printf("magicbusd logiretch-capture -- raw 0x02 mouse-report dump (%s window)\n", captureWindow)
	p.printf("read-only. During the window: click LEFT, RIGHT, MIDDLE, BACK, FORWARD,\n")
	p.printf("roll the wheel BOTH ways, roll the THUMBWHEEL both ways, press the\n")
	p.printf("GESTURE and FORCE buttons, and move the mouse.\n\n")

	vendor, err := p.step1Enumerate()
	if err != nil || vendor == nil {
		return err
	}
	dev, ok := p.step2Open(vendor.Path)
	if !ok {
		return nil
	}
	defer dev.Close()

	deadline := time.Now().Add(captureWindow)
	buf := make([]byte, 64)
	var prev []byte
	frames := 0
	distinct := map[string]int{}
	// per-byte change tally across consecutive 0x02 frames: motion bytes
	// churn constantly, button/wheel bytes step -- the histogram separates
	// them even when the eye misses a frame
	changed := map[int]int{}
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return nil
		}
		n, err := dev.ReadWithTimeout(buf, 250*time.Millisecond)
		if err != nil || n <= 0 {
			continue
		}
		frame := make([]byte, n)
		copy(frame, buf[:n])
		if frame[0] != 0x02 {
			continue
		}
		frames++
		if prev != nil && len(prev) == len(frame) {
			for i := range frame {
				if frame[i] != prev[i] {
					changed[i]++
				}
			}
		}
		key := hexFrame(frame)
		distinct[key]++
		if distinct[key] == 1 && len(distinct) <= 80 {
			p.printf("  %s\n", key)
		}
		prev = frame
	}
	p.printf("\n%d 0x02 frames, %d distinct\n", frames, len(distinct))
	p.printf("byte-change histogram (index: changes):\n")
	for i := 0; i < 20; i++ {
		if changed[i] > 0 {
			p.printf("  [%2d] %d\n", i, changed[i])
		}
	}
	return nil
}
