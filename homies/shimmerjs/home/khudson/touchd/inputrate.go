package main

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// inputRateEvery paces the liveness line. 60s is a forensic cadence, not a
// sampling one: the line exists to be read after the fact, against a log the
// system rolls in about a day.
const inputRateEvery = time.Minute

// inputRate counts HID input reports per source and drains the counts to the
// log on a slow tick.
//
// It earns its place because magicbusd reads these devices directly, below
// the macOS event pipeline. When the machine stops registering input, this
// line separates "the hardware went quiet" from "the hardware kept sending
// and something above us stopped delivering it" -- a distinction nothing else
// on the box records, and one the unified log cannot answer once it has
// rolled.
//
// Reports add atomically; source arrival, totals, and interval reads share a mutex.
type inputRate struct {
	mu       sync.Mutex
	counters map[string]*atomic.Int64
	last     map[string]int64
}

func newInputRate() *inputRate {
	return &inputRate{counters: map[string]*atomic.Int64{}, last: map[string]int64{}}
}

// source returns name's counter, creating it on first use. Called when a
// collection opens, never per report.
func (r *inputRate) source(name string) *atomic.Int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.counters[name]
	if !ok {
		c = &atomic.Int64{}
		r.counters[name] = c
	}
	return c
}

// total sums every source's live counter WITHOUT draining it. The wedge
// watch polls this far faster than the log line drains, so it must not
// disturb the counts the forensic line reports.
func (r *inputRate) total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var n int64
	for _, c := range r.counters {
		n += c.Load()
	}
	return n
}

// drain renders deltas without resetting the cumulative liveness counters. Sources that have
// opened stay in the line at zero: a source reporting nothing is the
// observation, so it must not vanish from the record.
func (r *inputRate) drain() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.counters))
	for name := range r.counters {
		names = append(names, name)
	}
	if len(names) == 0 {
		return ""
	}
	slices.Sort(names)
	line := ""
	for _, name := range names {
		n := r.counters[name].Load()
		line += fmt.Sprintf(" %s=%d", name, n-r.last[name])
		r.last[name] = n
	}
	return line
}

// run logs one liveness line per interval until ctx ends.
func (r *inputRate) run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if line := r.drain(); line != "" {
				fmt.Printf("input reports/%s:%s\n", every, line)
			}
		}
	}
}
