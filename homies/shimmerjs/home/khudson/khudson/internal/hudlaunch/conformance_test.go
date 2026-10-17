package hudlaunch

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/cgdisplay"
)

// These scenarios exercise monitorChild itself. The child, event source,
// query and clock are substitutes; no display APIs or subprocesses run.
func TestConformancePendingChange(t *testing.T) {
	for _, failure := range []struct {
		name string
		err  error
		kind string
	}{
		{"query_timeout", context.DeadlineExceeded, "timeout"},
		{"connection_lost", &cgdisplay.APIError{Boundary: "snapshot", Code: 1002}, "invalid_connection"},
		{"unknown_code", &cgdisplay.APIError{Boundary: "snapshot", Code: 12345}, "unknown"},
		{"executable_missing", fs.ErrNotExist, "executable_missing"},
	} {
		t.Run(failure.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				changed := make(chan struct{}, 1)
				var records []Diagnostic
				var recordMu sync.Mutex
				trace := func() []Diagnostic {
					recordMu.Lock()
					defer recordMu.Unlock()
					return append([]Diagnostic(nil), records...)
				}
				var queries atomic.Int32
				stopped := 0
				opts := normalize(Options{DisplayName: "XENEON EDGE", RetryDelay: 15 * time.Second,
					Logf: t.Logf, Record: func(d Diagnostic) { recordMu.Lock(); defer recordMu.Unlock(); records = append(records, d) },
					Query: func(context.Context) ([]Screen, error) {
						if queries.Add(1) <= 2 {
							return nil, failure.err
						}
						return liveScreens, nil
					},
				})
				done := make(chan childOutcome, 1)
				go func() { done <- monitorChild(ctx, opts, changed, nil, func() { stopped++ }, 342, 2160) }()
				synctest.Wait()
				if queries.Load() != 0 {
					t.Fatal("queried before a change")
				}
				changed <- struct{}{}
				synctest.Wait()
				if queries.Load() != 1 || len(trace()) != 1 || trace()[0].Kind != failure.kind {
					t.Fatalf("first failure: queries=%d records=%+v", queries.Load(), trace())
				}
				// More notifications must neither hammer the query nor defer
				// the already scheduled retry.
				for range 100 {
					changed <- struct{}{}
					synctest.Wait()
				}
				if queries.Load() != 1 {
					t.Fatalf("notification burst bypassed backoff: %d", queries.Load())
				}
				time.Sleep(15 * time.Second)
				synctest.Wait()
				if queries.Load() != 2 {
					t.Fatalf("first retry: %d", queries.Load())
				}
				time.Sleep(15 * time.Second)
				synctest.Wait()
				if queries.Load() != 3 || trace()[len(trace())-1].Outcome != "recovered" {
					t.Fatalf("recovery: queries=%d records=%+v", queries.Load(), trace())
				}
				time.Sleep(24 * time.Hour)
				synctest.Wait()
				if queries.Load() != 3 {
					t.Fatalf("successful reconciliation left a retry armed: %d", queries.Load())
				}
				cancel()
				synctest.Wait()
				if <-done != childExited || stopped != 1 {
					t.Fatalf("shutdown stopped child %d times", stopped)
				}
			})
		})
	}
}

func TestConformanceEventDuringQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		changed := make(chan struct{}, 1)
		entered, release := make(chan struct{}), make(chan struct{})
		queries, stopped := 0, 0
		opts := normalize(Options{DisplayName: "XENEON EDGE", Logf: t.Logf,
			Query: func(context.Context) ([]Screen, error) {
				queries++
				if queries == 1 {
					close(entered)
					<-release
					return liveScreens, nil
				}
				return nil, nil
			},
		})
		done := make(chan childOutcome, 1)
		go func() { done <- monitorChild(ctx, opts, changed, nil, func() { stopped++ }, 342, 2160) }()
		changed <- struct{}{}
		<-entered
		changed <- struct{}{} // new state arrived while the old snapshot was in flight
		close(release)
		synctest.Wait()
		if <-done != childDisplayLost || queries != 2 || stopped != 1 {
			t.Fatalf("queries=%d stops=%d", queries, stopped)
		}
	})
}

func TestConformanceCancelAndSourceLoss(t *testing.T) {
	for _, closed := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			changed := make(chan struct{}, 1)
			queries, stopped := 0, 0
			var records []Diagnostic
			opts := normalize(Options{Logf: t.Logf, Record: func(d Diagnostic) { records = append(records, d) },
				Query: func(context.Context) ([]Screen, error) { queries++; return nil, errors.New("injected") },
			})
			done := make(chan childOutcome, 1)
			go func() { done <- monitorChild(ctx, opts, changed, nil, func() { stopped++ }, 0, 0) }()
			changed <- struct{}{}
			synctest.Wait()
			if closed {
				close(changed)
			} else {
				cancel()
			}
			synctest.Wait()
			got := <-done
			want := childExited
			if closed {
				want = childWatchLost
			}
			if got != want || stopped != 1 {
				t.Fatalf("outcome=%v stops=%d", got, stopped)
			}
			time.Sleep(time.Hour)
			synctest.Wait()
			if queries != 1 {
				t.Fatalf("query after teardown: %d", queries)
			}
			if closed && records[len(records)-1].Kind != "source_closed" {
				t.Fatalf("records: %+v", records)
			}
		})
	}
}

func TestConformanceRegistrationFailure(t *testing.T) {
	var sourceContext context.Context
	var records []Diagnostic
	queries := 0
	injected := &cgdisplay.APIError{Boundary: "register", Code: 1001}
	err := Run(context.Background(), Options{
		Socket: hudSockPath(t), Logf: t.Logf,
		Record: func(d Diagnostic) { records = append(records, d) },
		Watch:  func(ctx context.Context) (<-chan struct{}, error) { sourceContext = ctx; return nil, injected },
		Query:  func(context.Context) ([]Screen, error) { queries++; return nil, nil },
	})
	if !errors.Is(err, injected) || queries != 0 {
		t.Fatalf("err=%v queries=%d", err, queries)
	}
	if sourceContext.Err() != context.Canceled {
		t.Fatal("failed startup retained its source context")
	}
	if len(records) != 1 || records[0].Kind != "illegal_argument" || records[0].Code == nil || *records[0].Code != 1001 {
		t.Fatalf("records: %+v", records)
	}
}

func TestConformanceClosedSourceWhileAbsent(t *testing.T) {
	changed := make(chan struct{})
	close(changed)
	queries := 0
	var records []Diagnostic
	err := Run(context.Background(), Options{
		Socket: hudSockPath(t), Watch: stubWatch(changed), Logf: t.Logf,
		Record: func(d Diagnostic) { records = append(records, d) },
		Query:  func(context.Context) ([]Screen, error) { queries++; return nil, nil },
	})
	if err == nil || queries != 1 {
		t.Fatalf("closed source: err=%v queries=%d", err, queries)
	}
	if records[len(records)-1].Kind != "source_closed" {
		t.Fatalf("records: %+v", records)
	}
}

func TestConformanceScanFailureIsNotDisconnection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var mode, calls atomic.Int32
		records := make(chan Diagnostic, 16)
		changed, err := watchByScan(ctx, 2*time.Second, func() (string, error) {
			calls.Add(1)
			switch mode.Load() {
			case 1:
				return "", &cgdisplay.APIError{Boundary: "enumerate", Code: 1002}
			case 2:
				return "", nil
			default:
				return "edge:0,0,2560x720", nil
			}
		}, func(d Diagnostic) { records <- d })
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 1 {
			t.Fatal("baseline was deferred until after Watch returned")
		}
		mode.Store(1)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case <-changed:
			t.Fatal("enumeration error became a disconnection")
		default:
		}
		d := <-records
		if d.Kind != "invalid_connection" || d.Code == nil || *d.Code != 1002 {
			t.Fatalf("diagnostic: %+v", d)
		}
		mode.Store(0)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case <-changed:
			t.Fatal("unchanged recovery invented a topology change")
		default:
		}
		if d := <-records; d.Outcome != "recovered" {
			t.Fatalf("recovery: %+v", d)
		}
		mode.Store(2)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case <-changed:
		default:
			t.Fatal("valid empty display set did not signal disconnection")
		}
		cancel()
		synctest.Wait()
		before := calls.Load()
		time.Sleep(time.Hour)
		synctest.Wait()
		if calls.Load() != before {
			t.Fatal("scan continued after cancellation")
		}
	})
}

func TestConformanceInvalidSnapshotIsNotDisconnection(t *testing.T) {
	for _, payload := range []string{"null", "{}", "broken", `[{"name":"XENEON EDGE"}]`} {
		_, err := parseScreens([]byte(payload))
		if err == nil {
			t.Fatalf("%q became a successful empty/invalid snapshot", payload)
		}
		var records []Diagnostic
		recordFailure(Options{Record: func(d Diagnostic) { records = append(records, d) }}, "query.changed", err, "retry_pending_change", 1)
		if len(records) != 1 || (records[0].Kind != "invalid_shape" && records[0].Kind != "malformed_json") {
			t.Fatalf("%q: %+v", payload, records)
		}
	}
	if screens, err := parseScreens([]byte("[]")); err != nil || len(screens) != 0 {
		t.Fatalf("valid absence: %v %v", screens, err)
	}
}

func TestConformanceProcessErrorRetainsStderr(t *testing.T) {
	var record Diagnostic
	err := &exec.ExitError{Stderr: []byte("injected OS boundary detail")}
	// No process is created: a missing ProcessState has exit code -1.
	recordFailure(Options{Record: func(d Diagnostic) { record = d }}, "query.changed", err, "retry_pending_change", 1)
	if record.Kind != "process_exit" || record.Code == nil || *record.Code != -1 || record.Detail != "injected OS boundary detail" {
		t.Fatalf("diagnostic: %+v", record)
	}
}

func TestInitialRetryCannotBeAcceleratedByNotificationStorm(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		changed := make(chan struct{}, 1)
		done := make(chan bool, 1)
		go func() { done <- waitChange(ctx, changed, 15*time.Second) }()
		for range 14 {
			changed <- struct{}{}
			time.Sleep(time.Second)
			select {
			case <-done:
				t.Fatal("notification bypassed failure retry")
			default:
			}
		}
		time.Sleep(time.Second)
		if !<-done {
			t.Fatal("retry did not fire")
		}
	})
}
