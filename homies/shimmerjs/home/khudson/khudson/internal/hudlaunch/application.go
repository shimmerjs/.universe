package hudlaunch

import (
	"context"
	"errors"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
)

type displaySubscription interface {
	Close() error
	Stats() cgdisplay.NotificationStats
}
type applicationAPI struct {
	initialize func() error
	snapshot   func() (string, error)
	subscribe  func() (displaySubscription, <-chan struct{}, error)
	run        func()
	stop       func()
}

// RunApplication owns the AppKit event loop on the process main thread.
// The caller must lock that thread in init, before main can migrate.
func RunApplication(ctx context.Context, opts Options) error {
	opts = normalize(opts)
	record := opts.Record
	opts.Record = func(d Diagnostic) {
		if d.Boundary == "watch.health" {
			heartbeat := cgdisplay.ApplicationHeartbeat()
			d.Heartbeat = &heartbeat
			if state := cgdisplay.ApplicationSecureInput(); state >= 0 {
				enabled := state != 0
				d.SecureInput = &enabled
			}
		}
		record(d)
	}
	return runApplication(ctx, opts, applicationAPI{
		initialize: cgdisplay.ApplicationInit,
		subscribe: func() (displaySubscription, <-chan struct{}, error) {
			s, err := cgdisplay.Subscribe()
			if err != nil {
				return nil, nil, err
			}
			return s, s.Changes, nil
		},
		run: cgdisplay.ApplicationRun, stop: cgdisplay.ApplicationStop,
	}, Run)
}

func runApplication(ctx context.Context, opts Options, api applicationAPI, worker func(context.Context, Options) error) error {
	opts = normalize(opts)
	if ctx.Err() != nil {
		return nil
	}
	if err := api.initialize(); err != nil {
		recordFailure(opts, "application.initialize", err, "restart_application", 1)
		return err
	}
	sub, changes, err := api.subscribe()
	cadence := time.Minute
	if err != nil {
		recordFailure(opts, "callback.register", err, "scan_fallback", 1)
		cadence = 2 * time.Second
	} else {
		opts.Record(Diagnostic{Boundary: "callback.register", Outcome: "ready", Action: "run_application_loop"})
		defer func() {
			for attempt := 1; attempt <= 3; attempt++ {
				if err := sub.Close(); err != nil {
					recordFailure(opts, "callback.remove", err, "retry_removal_then_process_exit", attempt)
				} else {
					return
				}
			}
		}()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	snapshot := api.snapshot
	if snapshot == nil {
		snapshot = scanDisplays
	}
	opts.Watch = func(ctx context.Context) (<-chan struct{}, error) {
		return watchEvents(ctx, cadence, changes, snapshot, opts.Record, sub)
	}
	done := make(chan error, 1)
	go func() { done <- worker(ctx, opts); api.stop() }()
	api.run()
	select {
	case err = <-done:
	default:
		unexpected := ctx.Err() == nil
		cancel()
		err = <-done
		if unexpected {
			err = errors.Join(err, errors.New("AppKit event loop exited before worker completion"))
			recordFailure(opts, "application.run", err, "restart_application", 1)
		}
	}
	cancel()
	if err != nil {
		return err
	}
	return nil
}

// Notifications invalidate the snapshot; they are not a queue of topology
// deltas. Reconciliation retains failed work in the launcher. The slow scan
// is an observable fallback while native delivery gains operational evidence.
func watchEvents(ctx context.Context, cadence time.Duration, events <-chan struct{}, scan func() (string, error), record func(Diagnostic), sub displaySubscription) (<-chan struct{}, error) {
	ch := make(chan struct{}, 1)
	last, err := scan()
	known := err == nil
	if err != nil {
		recordFailure(Options{Record: record}, "watch.snapshot", err, "keep_last_state", 1)
	}
	go func() {
		defer close(ch)
		tick := time.NewTicker(cadence)
		defer tick.Stop()
		health := time.NewTicker(time.Minute)
		defer health.Stop()
		lastError := ""
		failures := 0
		if err != nil {
			lastError, failures = err.Error(), 1
		}
		signal := func() {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		for {
			event := false
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					recordFailure(Options{Record: record}, "callback.channel", errors.New("closed notification source"), "scan_fallback", 1)
					events = nil
					tick.Reset(2 * time.Second)
					continue
				}
				event = true
			case <-health.C:
				d := Diagnostic{Boundary: "watch.health", Outcome: "observed", Action: "continue"}
				if sub != nil {
					stats := sub.Stats()
					d.Notifications = &stats
				}
				record(d)
				continue
			case <-tick.C:
			}
			if event {
				d := Diagnostic{Boundary: "callback.delivery", Outcome: "observed", Action: "coalesce_then_snapshot"}
				if sub != nil {
					stats := sub.Stats()
					d.Notifications = &stats
				}
				record(d)
				if !sleepCtx(ctx, 250*time.Millisecond) {
					return
				}
				// Drain only already-delivered callbacks; a callback arriving during
				// the following snapshot remains pending for the next pass.
			drain:
				for {
					select {
					case _, ok := <-events:
						if !ok {
							recordFailure(Options{Record: record}, "callback.channel", errors.New("closed notification source"), "scan_fallback", 1)
							events = nil
							tick.Reset(2 * time.Second)
							break drain
						}
					default:
						break drain
					}
				}
			}
			current, err := scan()
			if err != nil {
				failures++
				if failures&(failures-1) == 0 || err.Error() != lastError {
					recordFailure(Options{Record: record}, "watch.snapshot", err, "keep_last_state", failures)
				}
				lastError = err.Error()
				if event {
					signal()
				} // snapshot failure must not consume the invalidation
				continue
			}
			if failures > 0 {
				record(Diagnostic{Boundary: "watch.snapshot", Outcome: "recovered", Action: "reconcile", Attempt: failures})
			}
			failures = 0
			different := !known || current != last
			last, known = current, true
			if event || different {
				boundary := "watch.snapshot"
				if !event {
					boundary = "watch.reconciliation"
				}
				d := Diagnostic{Boundary: boundary, Outcome: "changed", Action: "reconcile", Detail: current}
				if sub != nil {
					stats := sub.Stats()
					d.Notifications = &stats
				}
				record(d)
				signal()
			}
		}
	}()
	return ch, nil
}
