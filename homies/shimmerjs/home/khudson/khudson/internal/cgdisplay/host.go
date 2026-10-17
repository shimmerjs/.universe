package cgdisplay

import (
	"context"
	"errors"

	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
)

// WithNotifications runs on the initial locked OS thread; worker owns its
// services on a goroutine. Callback removal precedes process exit.
func WithNotifications(ctx context.Context, worker func(context.Context, <-chan struct{}) error) error {
	return withNotifications(ctx, applicationHost{init: ApplicationInit, subscribe: func() (func() error, <-chan struct{}, error) {
		s, err := Subscribe()
		if err != nil {
			return nil, nil, err
		}
		return s.Close, s.Changes, nil
	}, run: ApplicationRun, stop: ApplicationStop}, worker)
}

type applicationHost struct {
	init      func() error
	subscribe func() (func() error, <-chan struct{}, error)
	run, stop func()
}

func withNotifications(ctx context.Context, api applicationHost, worker func(context.Context, <-chan struct{}) error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := api.init(); err != nil {
		diagnostic.Default.Observe("application.initialize", err, "restart owner")
		return err
	}
	closeSub, changes, err := api.subscribe()
	diagnostic.Default.Observe("display.callback.register", err, "use bounded demand-driven fallback on failure")
	if closeSub != nil {
		defer func() {
			for range 3 {
				err := closeSub()
				diagnostic.Default.Observe("display.callback.remove", err, "retry removal then process exit")
				if err == nil {
					return
				}
			}
		}()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		// Publish the result before stop can release run. Join the stop call
		// separately so a normal stop cannot look like premature loop exit.
		done <- worker(ctx, changes)
		api.stop()
	}()
	api.run()
	select {
	case err := <-done:
		<-stopped
		return err
	default:
	}
	unexpected := ctx.Err() == nil
	cancel()
	err = <-done
	<-stopped
	if unexpected {
		err = errors.Join(err, errors.New("application event loop exited before service"))
	}
	return err
}
