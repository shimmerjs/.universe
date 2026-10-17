package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

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
	publish         func(v any)
	publishRetained func(key string, v any)
	recordTap       func(t int64, raw []byte)
	scan            *scanner
	rate            *inputRate
}

// InputSource returns the report counter a module's read loop bumps, or nil
// when the daemon runs without the liveness record (spike, tests).
func (e Env) InputSource(name string) *atomic.Int64 {
	if e.rate == nil {
		return nil
	}
	return e.rate.source(name)
}

// Publish queues one ndjson line on the module's socket.
func (e Env) Publish(v any) { e.publish(v) }

// PublishRetained publishes v and retains it under key: a client connecting
// later receives the last line per key at accept (battery, mapping state).
func (e Env) PublishRetained(key string, v any) {
	if e.publishRetained != nil {
		e.publishRetained(key, v)
		return
	}
	e.publish(v)
}

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
	boundary := "hid.open.shared"
	if exclusive {
		boundary = "hid.open.exclusive"
	}
	hidBoundaries.Observe(boundary, err, "backoff_if_open_failed")
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

// moduleTerminal is a known local contract violation. Restarting other HID
// owners cannot repair it. Unknown errors retain process-wide fail-stop behavior.
type moduleTerminal struct{ cause error }

func (e moduleTerminal) Error() string { return e.cause.Error() }
func (e moduleTerminal) Unwrap() error { return e.cause }

// runModules joins all modules. Known local contract failures disable their
// module; unclassified failures stop the shared runtime. The scanner is
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
			err := e.mod.Run(ctx, e.env)
			if ctx.Err() != nil {
				return
			}
			if err == nil || errors.Is(err, context.Canceled) {
				err = moduleTerminal{errors.New("module exited without owner cancellation")}
			}
			var local moduleTerminal
			if errors.As(err, &local) {
				hidBoundaries.Observe("module."+e.mod.Name(), err, "disabled; inspect module contract; siblings remain running")
				<-ctx.Done()
				return
			}
			if err != nil {
				hidBoundaries.Observe("module."+e.mod.Name(), err, "unclassified shared-runtime risk; stop daemon")
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
