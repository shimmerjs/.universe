package cgdisplay

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestTopologyCacheRetainsFailedInvalidation(t *testing.T) {
	events := make(chan struct{}, 1)
	now := time.Unix(0, 0)
	calls := 0
	fail := false
	cache := Cache{Changes: events, Now: func() time.Time { return now }, Query: func() ([]Display, error) {
		calls++
		if fail {
			return nil, errors.New("unavailable")
		}
		return []Display{{ID: uint32(calls)}}, nil
	}}
	first, err := cache.Read()
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	for range 100 {
		cache.Read()
	}
	if calls != 1 {
		t.Fatal("queried unchanged topology")
	}
	fail = true
	events <- struct{}{}
	if _, err = cache.Read(); err == nil {
		t.Fatal("lost error")
	}
	for range 100 {
		cache.Read()
	}
	if calls != 2 {
		t.Fatal("failed query was hammered")
	}
	now = now.Add(time.Second)
	fail = false
	last, err := cache.Read()
	if err != nil || len(last) != 1 || last[0].ID != 3 {
		t.Fatalf("failed event not retained: %v %v", last, err)
	}
	last[0].ID = 100
	copied, _ := cache.Read()
	if copied[0].ID != 3 {
		t.Fatal("caller mutated shared topology")
	}
	now = now.Add(time.Minute)
	cache.Read()
	if calls != 4 {
		t.Fatal("fallback omitted")
	}
}
func TestApplicationHostOwnsCallbackAndWorker(t *testing.T) {
	stopped := make(chan struct{})
	removed := 0
	err := withNotifications(context.Background(), applicationHost{init: func() error { return nil }, subscribe: func() (func() error, <-chan struct{}, error) {
		return func() error { removed++; return nil }, make(chan struct{}), nil
	}, run: func() { <-stopped }, stop: func() { close(stopped) }}, func(context.Context, <-chan struct{}) error { return errors.New("worker failed") })
	if err == nil || removed != 1 {
		t.Fatalf("ownership %v removals=%d", err, removed)
	}
}
func TestApplicationHostEarlyLoopExitCancelsWorker(t *testing.T) {
	joined := false
	err := withNotifications(context.Background(), applicationHost{init: func() error { return nil }, subscribe: func() (func() error, <-chan struct{}, error) { return nil, nil, errors.New("register failed") }, run: func() {}, stop: func() {}}, func(ctx context.Context, events <-chan struct{}) error {
		if events != nil {
			t.Error("failed registration provided events")
		}
		<-ctx.Done()
		joined = true
		return ctx.Err()
	})
	if err == nil || !joined {
		t.Fatal("worker survived loop exit")
	}
}

func TestClosedTopologySourceSwitchesToDemandFallback(t *testing.T) {
	events := make(chan struct{})
	now := time.Unix(0, 0)
	calls := 0
	cache := Cache{Changes: events, Now: func() time.Time { return now }, Query: func() ([]Display, error) { calls++; return nil, nil }}
	cache.Read()
	close(events)
	cache.Read()
	if calls != 2 {
		t.Fatal("source loss left minute-long cached snapshot")
	}
	for range 100 {
		cache.Read()
	}
	if calls != 2 {
		t.Fatal("closed channel caused query loop")
	}
	now = now.Add(2 * time.Second)
	cache.Read()
	if calls != 3 {
		t.Fatal("source loss did not install bounded fallback")
	}
}

func TestApplicationHostJoinsStopBeforeRemovingCallback(t *testing.T) {
	stopping, release, removed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- withNotifications(context.Background(), applicationHost{
			init: func() error { return nil },
			subscribe: func() (func() error, <-chan struct{}, error) {
				return func() error { close(removed); return nil }, make(chan struct{}), nil
			},
			run:  func() {},
			stop: func() { close(stopping); <-release },
		}, func(ctx context.Context, _ <-chan struct{}) error { <-ctx.Done(); return ctx.Err() })
	}()
	<-stopping
	select {
	case <-removed:
		t.Error("callback removed before stop completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("unexpected event loop exit reported success")
	}
	select {
	case <-removed:
	default:
		t.Fatal("callback not removed")
	}
}

func TestApplicationNormalStopPublishesResultBeforeLoopExit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stopping, release := make(chan struct{}), make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- withNotifications(context.Background(), applicationHost{
				init:      func() error { return nil },
				subscribe: func() (func() error, <-chan struct{}, error) { return nil, nil, nil },
				run:       func() { <-stopping },
				stop:      func() { close(stopping); <-release },
			}, func(context.Context, <-chan struct{}) error { return nil })
		}()
		<-stopping
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("returned before stop completed: %v", err)
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("normal stop classified as early loop exit: %v", err)
		}
	})
}
