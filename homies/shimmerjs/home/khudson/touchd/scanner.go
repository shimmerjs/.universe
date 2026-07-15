package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/sstallion/go-hid"
)

// Match selects one HID collection by the same tuple findCollection keys on.
type Match struct {
	VID, PID, UsagePage, Usage uint16
}

// hidEnumerate is the enumeration seam: tests swap it to count scans and
// inject device sets without hardware.
var hidEnumerate = hid.Enumerate

// enumerateLogInterval bounds enumerate-failure logging: once on the
// transition into the failing state, then once per interval while it lasts.
const enumerateLogInterval = time.Minute

// scanner is the central arrival scanner behind every Env.AwaitDevice: ONE
// hid.Enumerate per tick services all waiting modules, run under openMu so a
// scan can never race an open flipping the process-global exclusive flag.
// Per-waiter backoff is reopenBackoff: not enumerable at a scheduled tick is
// the absent class; a failed open reported back through Env.OpenShared /
// OpenExclusive is the seized class; a class flip resets the ramp. The tick
// source is the wake channel, so a future IOKit matching-callback can feed
// arrivals with zero module changes.
type scanner struct {
	mu     sync.Mutex
	states map[Match]*matchState
	wake   chan struct{}

	// enumerate-failure log rate limit; touched only from tick (single
	// caller: run)
	enumFailing    bool
	lastEnumErrLog time.Time
}

// matchState is the per-waiter ledger, persistent across await calls so the
// backoff ramp survives the open attempt between them.
type matchState struct {
	bo       reopenBackoff
	due      time.Time   // next scheduled attempt; zero = immediate
	ch       chan string // non-nil while a waiter is parked
	lastPath string      // handed-out path with no open outcome reported yet
	handedAt time.Time
	armed    bool // last open succeeded; ramp reset pending the re-entry check
}

func newScanner() *scanner {
	return &scanner{states: map[Match]*matchState{}, wake: make(chan struct{}, 1)}
}

func (s *scanner) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// await parks until m is enumerable and the waiter's schedule allows another
// attempt, then returns the collection path. Episode classification happens
// on re-entry: bouncing back faster than reconnectMin after a successful
// open means the module never got a session out of the device (a failed
// pairing init), which is the seized class; a slower return means a real
// session ran, so the ramp resets like the old loops' reset-on-open.
func (s *scanner) await(ctx context.Context, m Match) (string, error) {
	s.mu.Lock()
	st := s.states[m]
	if st == nil {
		st = &matchState{}
		s.states[m] = st
	}
	if st.ch != nil {
		s.mu.Unlock()
		return "", fmt.Errorf("duplicate waiter for %04X:%04X usage_page=0x%02X usage=0x%02X", m.VID, m.PID, m.UsagePage, m.Usage)
	}
	now := time.Now()
	if st.armed {
		st.armed = false
		if now.Sub(st.handedAt) < reconnectMin {
			st.due = now.Add(st.bo.fail(false))
		} else {
			st.bo.reset()
			st.due = now
		}
	}
	st.lastPath = ""
	if st.due.IsZero() {
		st.due = now
	}
	ch := make(chan string, 1)
	st.ch = ch
	s.mu.Unlock()
	s.poke()

	select {
	case <-ctx.Done():
		s.mu.Lock()
		if st.ch == ch {
			st.ch = nil
		}
		s.mu.Unlock()
		return "", ctx.Err()
	case path := <-ch:
		return path, nil
	}
}

// reportOpen records an Env open outcome against the waiter whose handed-out
// path matches. Failure is the seized/present-but-unopenable class; success
// arms the ramp reset, committed on the next await (where a rapid bounce
// demotes it, see await).
func (s *scanner) reportOpen(path string, err error) {
	s.reportOpenAt(path, err, time.Now())
}

func (s *scanner) reportOpenAt(path string, err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.states {
		if st.lastPath != path {
			continue
		}
		st.lastPath = ""
		if err != nil {
			st.due = now.Add(st.bo.fail(false))
			st.armed = false
		} else {
			st.armed = true
		}
		return
	}
}

// tick runs one shared enumerate and services every parked waiter: a found
// device is delivered when the waiter is due -- or early for the absent
// class, whose cap is a poll bound, not an open-rate limit -- and a missed
// scheduled attempt advances the absent ramp.
func (s *scanner) tick(now time.Time) {
	s.mu.Lock()
	found := map[Match]string{}
	for m, st := range s.states {
		if st.ch != nil {
			found[m] = ""
		}
	}
	s.mu.Unlock()
	if len(found) == 0 {
		return
	}

	openMu.Lock()
	err := hidEnumerate(hid.VendorIDAny, hid.ProductIDAny, func(info *hid.DeviceInfo) error {
		m := Match{VID: info.VendorID, PID: info.ProductID, UsagePage: info.UsagePage, Usage: info.Usage}
		if path, ok := found[m]; ok && path == "" {
			found[m] = info.Path
		}
		return nil
	})
	openMu.Unlock()
	// enumerate failure keeps the retry-forever posture (waiters stay
	// parked); the log is rate-limited so a chronic failure cannot spam
	if err != nil {
		if !s.enumFailing || now.Sub(s.lastEnumErrLog) >= enumerateLogInterval {
			fmt.Fprintf(os.Stderr, "hid enumerate: %v (retrying, logging per %s while it persists)\n", err, enumerateLogInterval)
			s.lastEnumErrLog = now
		}
		s.enumFailing = true
	} else {
		s.enumFailing = false
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for m, path := range found {
		st := s.states[m]
		if st == nil || st.ch == nil {
			continue
		}
		if path != "" {
			if st.due.After(now) && !st.bo.absent && st.bo.next != 0 {
				continue // seized class backs off between open attempts
			}
			st.lastPath = path
			st.handedAt = now
			st.ch <- path
			st.ch = nil
		} else if !st.due.After(now) {
			st.due = now.Add(st.bo.fail(true))
		}
	}
}

// run drives ticks until ctx ends: sleep to the earliest scheduled attempt
// (or a wake -- waiter registration, or a future IOKit arrival), then scan
// once for everyone. No parked waiter means no timer and no enumerate.
func (s *scanner) run(ctx context.Context) {
	for {
		next, ok := s.nextDue()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
			continue
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-s.wake:
			timer.Stop()
		case <-timer.C:
		}
		s.tick(time.Now())
	}
}

// nextDue reports the earliest scheduled attempt among parked waiters.
func (s *scanner) nextDue() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var next time.Time
	ok := false
	for _, st := range s.states {
		if st.ch == nil {
			continue
		}
		if !ok || st.due.Before(next) {
			next = st.due
			ok = true
		}
	}
	return next, ok
}
