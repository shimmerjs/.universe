package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/sstallion/go-hid"
)

// Module is one HID source inside the daemon: it owns a device lifecycle
// (await, open, pump, reopen) and publishes ndjson lines on its own socket.
type Module interface {
	Name() string
	Run(ctx context.Context, env Env) error
}

// Env is the capability surface a module runs against: its own broadcaster,
// opens that inherit the process-global exclusive-flag serialization, the
// edge-only recorder tap, and arrival waits backed by the shared scanner --
// modules never own their own backoff select.
type Env struct {
	publish   func(v any)
	recordTap func(t int64, raw []byte)
	scan      *scanner
}

// Publish queues one ndjson line on the module's socket.
func (e Env) Publish(v any) { e.publish(v) }

// RecordTap feeds the raw-report recorder; a nop unless wired (edge only).
func (e Env) RecordTap(t int64, raw []byte) {
	if e.recordTap != nil {
		e.recordTap(t, raw)
	}
}

// OpenShared opens path without seizing it (Keymapp-style coexistence);
// OpenExclusive seizes. Both report the outcome back to the scanner, which
// is how a failed open lands in the seized backoff class.
func (e Env) OpenShared(path string) (*hid.Device, error) { return e.open(path, false) }

func (e Env) OpenExclusive(path string) (*hid.Device, error) { return e.open(path, true) }

func (e Env) open(path string, exclusive bool) (*hid.Device, error) {
	dev, err := openPath(path, exclusive)
	if e.scan != nil {
		e.scan.reportOpen(path, err)
	}
	return dev, err
}

// AwaitDevice parks until the matched collection is enumerable and this
// waiter's backoff schedule allows another attempt.
func (e Env) AwaitDevice(ctx context.Context, m Match) (string, error) {
	return e.scan.await(ctx, m)
}

// moduleEntry pairs a module with its wired Env for the registry runner.
type moduleEntry struct {
	mod Module
	env Env
}

// runModules is the registry runner: one goroutine per enabled module plus
// the shared scanner, fan-in-waited until every module returns. A non-nil,
// non-cancel module error is logged AND returned so the daemon exits nonzero
// instead of idling under KeepAlive with a dead module. The scanner is
// joined before returning: it may be inside hidEnumerate under openMu, and
// main's deferred hid.Exit must not free the manager under an in-flight
// enumerate.
func runModules(ctx context.Context, sc *scanner, entries []moduleEntry) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		sc.run(ctx)
	}()
	errs := make(chan error, len(entries))
	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.mod.Run(ctx, e.env); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "%s module failed: %v\n", e.mod.Name(), err)
				errs <- fmt.Errorf("%s module: %w", e.mod.Name(), err)
				// tear the siblings down so the daemon exits (and launchd
				// relaunches) instead of idling on a dead module -- the
				// others park on ctx and wg.Wait would never return
				cancel()
			}
		}()
	}
	wg.Wait()
	cancel()
	<-scanDone
	close(errs)
	var failed error
	for err := range errs {
		failed = errors.Join(failed, err)
	}
	return failed
}
