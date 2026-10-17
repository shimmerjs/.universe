package hudlaunch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
)

type fakeSubscription struct{ removals int }

func (s *fakeSubscription) Close() error {
	s.removals++
	if s.removals < 2 {
		return &cgdisplay.APIError{Boundary: "remove", Code: 1002}
	}
	return nil
}
func (*fakeSubscription) Stats() cgdisplay.NotificationStats { return cgdisplay.NotificationStats{} }
func TestApplicationOwnsRegistrationWorkerLoopAndRemoval(t *testing.T) {
	for _, registrationFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback", true: "fallback"}[registrationFails], func(t *testing.T) {
			sub := &fakeSubscription{}
			stopped := make(chan struct{})
			initialized, registered := false, false
			err := runApplication(context.Background(), Options{Record: func(Diagnostic) {}}, applicationAPI{
				initialize: func() error { initialized = true; return nil },
				subscribe: func() (displaySubscription, <-chan struct{}, error) {
					if !initialized {
						t.Error("registration before app init")
					}
					registered = true
					if registrationFails {
						return nil, nil, errors.New("register failed")
					}
					return sub, make(chan struct{}), nil
				},
				run: func() { <-stopped }, stop: func() { close(stopped) },
			}, func(ctx context.Context, opts Options) error {
				if !registered {
					t.Error("worker before registration")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !registrationFails && sub.removals != 2 {
				t.Fatalf("removals=%d", sub.removals)
			}
		})
	}
}
func TestApplicationInitializationFailureDoesNotRunWorker(t *testing.T) {
	failure := errors.New("no application")
	err := runApplication(context.Background(), Options{Record: func(Diagnostic) {}}, applicationAPI{initialize: func() error { return failure }}, func(context.Context, Options) error { t.Fatal("worker ran"); return nil })
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
func TestEventWatcherRetainsFailedSnapshotAndReconcilesWithoutEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan struct{}, 1)
		var mu sync.Mutex
		calls := 0
		scan := func() (string, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 2 {
				return "", errors.New("transient")
			}
			if calls >= 3 {
				return "changed", nil
			}
			return "initial", nil
		}
		records := make(chan Diagnostic, 32)
		ch, err := watchEvents(ctx, time.Minute, events, scan, func(d Diagnostic) { records <- d }, nil)
		if err != nil {
			t.Fatal(err)
		}
		events <- struct{}{}
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		select {
		case <-ch:
		default:
			t.Fatal("event consumed by failed snapshot")
		}
		if d := <-records; d.Boundary != "callback.delivery" {
			t.Fatalf("callback receipt not recorded before failed work: %+v", d)
		}
		if d := <-records; d.Boundary != "watch.snapshot" || d.Outcome != "error" {
			t.Fatalf("snapshot failure not recorded: %+v", d)
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		select {
		case <-ch:
		default:
			t.Fatal("fallback did not reconcile")
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		select {
		case <-ch:
			t.Fatal("unchanged fallback signaled")
		default:
		}
		cancel()
		synctest.Wait()
		if _, ok := <-ch; ok {
			t.Fatal("watch did not close")
		}
	})
}

func TestApplicationRegistrationFailureUsesObservableScanFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stopped := make(chan struct{})
		var scans int
		var mu sync.Mutex
		err := runApplication(context.Background(), Options{Record: func(Diagnostic) {}}, applicationAPI{
			initialize: func() error { return nil },
			subscribe: func() (displaySubscription, <-chan struct{}, error) {
				return nil, nil, errors.New("registration failed")
			},
			snapshot: func() (string, error) { mu.Lock(); defer mu.Unlock(); scans++; return "same", nil },
			run:      func() { <-stopped }, stop: func() { close(stopped) },
		}, func(ctx context.Context, opts Options) error {
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			ch, err := opts.Watch(ctx)
			if err != nil {
				return err
			}
			time.Sleep(5 * time.Second)
			synctest.Wait()
			mu.Lock()
			got := scans
			mu.Unlock()
			if got != 3 {
				t.Errorf("fallback scans=%d, want initial plus two", got)
			}
			select {
			case <-ch:
				t.Error("unchanged fallback emitted invalidation")
			default:
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
func TestUnexpectedApplicationLoopExitCancelsWorkerAndRemovesCallback(t *testing.T) {
	sub := &fakeSubscription{}
	err := runApplication(context.Background(), Options{Record: func(Diagnostic) {}}, applicationAPI{
		initialize: func() error { return nil }, subscribe: func() (displaySubscription, <-chan struct{}, error) { return sub, make(chan struct{}), nil },
		run: func() {}, stop: func() {},
	}, func(ctx context.Context, _ Options) error { <-ctx.Done(); return nil })
	if err == nil || sub.removals != 2 {
		t.Fatalf("err=%v removals=%d", err, sub.removals)
	}
}
