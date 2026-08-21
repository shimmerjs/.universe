package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
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
	// open counts collections currently held open; the heartbeat rides it.
	open atomic.Int64
}

// heartbeatEvery paces the liveness line. The bus flips touch dead after
// three missed beats, so this sets the worst-case lag between the Edge going
// away and `ctl status` admitting it.
const heartbeatEvery = 5 * time.Second

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
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.heartbeat(ctx, env)
	}()
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

// heartbeat publishes a liveness line while at least one collection is open.
// It is gated on an OPEN COLLECTION, not on the daemon merely running: the
// claim is "the Edge is present and being read", which is what a consumer
// needs to distinguish idle glass from dead glass. Unplugged, seized, or
// ungranted, the collections are shut and the line stops.
func (m *edgeModule) heartbeat(ctx context.Context, env Env) {
	t := time.NewTicker(heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if m.open.Load() > 0 {
				env.Publish(Frame{T: time.Now().UnixNano(), HB: true})
			}
		}
	}
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

		m.open.Add(1)
		err = readLoop(ctx, dev, emit)
		m.open.Add(-1)
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
