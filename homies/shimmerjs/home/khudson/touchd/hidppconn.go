// hidppconn.go: the HID++ 2.0 reader/demux, extracted from the logiretch-0
// probe so the probe and the persistent logiretch module share one transport
// core. It owns Device.Read for the handle's whole life: the per-handle
// hidapi queue is bounded at 32 reports with silent oldest-drop (~250ms of
// 125Hz motion), and mouse reports interleave with HID++ frames on the one
// handle, so the reader drains continuously, routing swId-matched replies to
// the pending request and handing everything else to onEvent.
package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sstallion/go-hid"
)

// hidppReplyTimeout bounds one request round trip; a var so tests shrink it.
// hidppReadTimeout is the reader's per-Read wake so stop is observed promptly.
var (
	hidppReplyTimeout = 4 * time.Second
	hidppReadTimeout  = 250 * time.Millisecond
)

// hidDevice is the device I/O seam: exactly the two methods the conn needs,
// so *hid.Device (which exposes both) and a byte-fixture fake both satisfy it
// and request/reply/onEvent stay hermetically testable without hardware.
type hidDevice interface {
	Write(p []byte) (int, error)
	ReadWithTimeout(p []byte, timeout time.Duration) (int, error)
}

type pendingReq struct {
	devIdx, featIdx, fnID, swID byte
	ch                          chan hidppResult
}

type hidppResult struct {
	raw    []byte
	params []byte
	code   byte
	isErr  bool
}

// hidppConn is the single reader goroutine plus the request correlator over
// one HID++ handle: one request in flight, swId cycled per request, replies
// matched by classifyReply and everything else fed to onEvent.
type hidppConn struct {
	dev  hidDevice
	stop chan struct{}
	done chan struct{}

	mu      sync.Mutex
	pending *pendingReq
	swSeq   byte               // cycles the per-request swId nibble
	onEvent func(frame []byte) // fired under mu for every unrouted frame
	readErr error
	total   int
	byID    map[byte]int          // frame counts by report id
	lens    map[byte]map[int]bool // observed frame lengths by report id
	events  int                   // HID++ frames with swId 0 (unsolicited)
	foreign int                   // HID++ frames not matched to the pending request
}

// nextSwID cycles the software id across 0x08..0x0F; call under mu.
func (c *hidppConn) nextSwID() byte {
	c.swSeq++
	return hidppSwIDLo | (c.swSeq & (hidppSwIDLen - 1))
}

func newHidppConn(dev hidDevice) *hidppConn {
	c := &hidppConn{
		dev:  dev,
		stop: make(chan struct{}),
		done: make(chan struct{}),
		byID: map[byte]int{},
		lens: map[byte]map[int]bool{},
	}
	go c.loop()
	return c
}

// close stops the reader and joins it; the caller closes the device after
// (and hid.Exit comes later still).
func (c *hidppConn) close() {
	close(c.stop)
	<-c.done
}

func (c *hidppConn) loop() {
	defer close(c.done)
	buf := make([]byte, 64)
	for {
		select {
		case <-c.stop:
			return
		default:
		}
		n, err := c.dev.ReadWithTimeout(buf, hidppReadTimeout)
		if errors.Is(err, hid.ErrTimeout) {
			continue
		}
		if err != nil {
			c.mu.Lock()
			c.readErr = err
			c.mu.Unlock()
			return
		}
		if n <= 0 {
			continue
		}
		frame := make([]byte, n)
		copy(frame, buf[:n])
		c.route(frame)
	}
}

func (c *hidppConn) route(frame []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.recordFrame(frame)
	if p := c.pending; p != nil {
		kind, params, code := classifyReply(frame, p.devIdx, p.featIdx, p.fnID, p.swID)
		if kind != replyForeign {
			c.pending = nil
			p.ch <- hidppResult{raw: frame, params: params, code: code, isErr: kind == replyErr}
			return
		}
	}
	// not the reply we are waiting for: an unsolicited event or another
	// master's (or a late reply whose request already timed out)
	c.tallyUnrouted(frame)
	if c.onEvent != nil {
		// runs under mu: onEvent MUST NOT block or call back into the conn
		// (the probe does a non-blocking spool send; the module signals a
		// buffered channel and re-applies config off the reader goroutine)
		c.onEvent(frame)
	}
}

// recordFrame tallies report-id and length stats for every inbound frame;
// runs under mu.
func (c *hidppConn) recordFrame(frame []byte) {
	id := frame[0]
	c.total++
	c.byID[id]++
	if c.lens[id] == nil {
		c.lens[id] = map[int]bool{}
	}
	c.lens[id][len(frame)] = true
}

// tallyUnrouted classifies a HID++ frame not matched to the pending request:
// swId 0 is an unsolicited event, any other nibble is foreign. Runs under mu.
func (c *hidppConn) tallyUnrouted(frame []byte) {
	id := frame[0]
	if (id != hidppReportLong && id != hidppReportShort) || len(frame) < 5 {
		return
	}
	sw := frame[3] & 0x0F
	if frame[2] == hidppErrLong || frame[2] == hidppErrShort {
		sw = frame[4] & 0x0F
	}
	if sw == 0 {
		c.events++
	} else {
		c.foreign++
	}
}

func (c *hidppConn) setOnEvent(fn func(frame []byte)) {
	c.mu.Lock()
	c.onEvent = fn
	c.mu.Unlock()
}

// readError reports the reader goroutine's terminal error, if any. It lets a
// caller tell a dead reader (device loss -- reopen) apart from a request
// timeout on a live reader (sleeping mouse -- keep the handle).
func (c *hidppConn) readError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.readErr
}

// request performs one HID++ round trip: a numbered long-report Write (the
// report number 0x11 leads the buffer), then the swId-matched reply or its
// error frame within the reply deadline. res.raw is valid whenever err wraps
// hidppError.
func (c *hidppConn) request(ctx context.Context, devIdx, featIdx, fnID byte, params ...byte) (hidppResult, error) {
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return hidppResult{}, fmt.Errorf("reader dead: %w", err)
	}
	if c.pending != nil {
		c.mu.Unlock()
		return hidppResult{}, errors.New("request already in flight")
	}
	sw := c.nextSwID()
	req := hidppRequest(devIdx, featIdx, fnID, sw, params...)
	p := &pendingReq{devIdx: devIdx, featIdx: featIdx, fnID: fnID, swID: sw, ch: make(chan hidppResult, 1)}
	c.pending = p
	c.mu.Unlock()

	if _, err := c.dev.Write(req); err != nil {
		c.clearPending(p)
		return hidppResult{}, fmt.Errorf("write: %w", err)
	}
	select {
	case res := <-p.ch:
		if res.isErr {
			return res, hidppError(res.code)
		}
		return res, nil
	case <-time.After(hidppReplyTimeout):
		c.clearPending(p)
		return hidppResult{}, fmt.Errorf("no reply within %s", hidppReplyTimeout)
	case <-ctx.Done():
		c.clearPending(p)
		return hidppResult{}, ctx.Err()
	}
}

func (c *hidppConn) clearPending(p *pendingReq) {
	c.mu.Lock()
	if c.pending == p {
		c.pending = nil
	}
	c.mu.Unlock()
}

type readerStats struct {
	total   int
	byID    map[byte]int
	lens    map[byte][]int
	events  int
	foreign int
	readErr error
}

func (c *hidppConn) snapshot() readerStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := readerStats{
		total:   c.total,
		events:  c.events,
		foreign: c.foreign,
		readErr: c.readErr,
		byID:    maps.Clone(c.byID),
		lens:    map[byte][]int{},
	}
	for id, set := range c.lens {
		st.lens[id] = slices.Sorted(maps.Keys(set))
	}
	return st
}
