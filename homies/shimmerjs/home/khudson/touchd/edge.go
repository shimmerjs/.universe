package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"
)

// The two Edge input collections (findCollection tuple order).
var (
	edgeDigitizerMatch = Match{VID: edgeVID, PID: edgePID, UsagePage: usagePageDigitizer, Usage: usageTouchScreen}
	edgeMouseMatch     = Match{VID: edgeVID, PID: edgePID, UsagePage: usagePageDesktop, Usage: usageMouse}
)

// edgeModule serves parsed frames on the touch socket from BOTH Edge input
// collections concurrently: the digitizer (tier 1 -- silent until the vendor
// mode switch is cracked, kept open so a future unlock streams immediately)
// and the mouse collection (tier 2 -- the proven single-touch path). Each
// collection reopens via the shared scanner on device loss; the digitizer
// reasserts device mode on every reopen and de-asserts on shutdown.
type edgeModule struct {
	noMode bool
}

func (m *edgeModule) Name() string { return "edge" }

func (m *edgeModule) Run(ctx context.Context, env Env) error {
	// one emit hook for both collections, recorder tap before parse so the
	// capture and the fanout see reports in the same order
	var mu sync.Mutex
	emit := func(t int64, raw []byte) {
		mu.Lock()
		defer mu.Unlock()
		env.RecordTap(t, raw)
		if f, ok := parseReport(t, raw); ok {
			env.Publish(f)
		}
	}
	var wg sync.WaitGroup
	for _, mouse := range []bool{false, true} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.collectionLoop(ctx, env, mouse, emit)
		}()
	}
	wg.Wait()
	return nil
}

// collectionLoop pumps one collection into emit, reopening via AwaitDevice
// on device loss or open failure (a missing Input Monitoring grant or a
// seize-holding driver surfaces here as repeated open errors; the scanner
// owns the backoff schedule). Logs only when the error changes or hourly,
// not per attempt.
func (m *edgeModule) collectionLoop(ctx context.Context, env Env, mouse bool, emit func(int64, []byte)) {
	name, match := "digitizer", edgeDigitizerMatch
	if mouse {
		name, match = "mouse", edgeMouseMatch
	}
	var lastErr string
	var lastLog time.Time
	for {
		path, err := env.AwaitDevice(ctx, match)
		if err != nil {
			return
		}
		dev, err := env.OpenExclusive(path)
		if err != nil {
			err = fmt.Errorf("open (Input Monitoring granted?): %w", err)
			if err.Error() != lastErr || time.Since(lastLog) >= time.Hour {
				fmt.Fprintf(os.Stderr, "%s open: %v (retrying, backoff caps at %s, logging on change or hourly)\n", name, err, reconnectCap)
				lastErr = err.Error()
				lastLog = time.Now()
			}
			continue
		}
		asserted := assertEdgeMode(dev, mouse, m.noMode, false)
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
