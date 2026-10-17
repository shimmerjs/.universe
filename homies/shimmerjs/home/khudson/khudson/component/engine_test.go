package component

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/renoservice"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
	module "github.com/shimmerjs/khudson/khudson/provider"
)

type provider struct {
	poll   func(context.Context) (Data, error)
	closes atomic.Int32
}

func (*provider) Name() string                                               { return "fake" }
func (p *provider) Poll(ctx context.Context, _ map[string]any) (Data, error) { return p.poll(ctx) }
func (p *provider) Close() error                                             { p.closes.Add(1); return nil }

func options(p Provider) Options {
	return Options{Provider: p, Widget: Widget{ID: "w", Render: Render{Kind: "native", Module: p.Name()}}, Interval: time.Hour}
}
func await(t *testing.T, e *engine, predicate func(update) bool) update {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case u, ok := <-e.updates:
			if !ok {
				t.Fatal("engine closed before expected update")
			}
			if predicate(u) {
				return u
			}
		case <-deadline:
			t.Fatal("engine did not publish expected update")
		}
	}
}

func TestSlowProviderNeverOverlapsAndCloseJoins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var active, calls atomic.Int32
		p := &provider{poll: func(ctx context.Context) (Data, error) {
			if active.Add(1) != 1 {
				t.Error("overlapping provider polls")
			}
			defer active.Add(-1)
			calls.Add(1)
			<-ctx.Done()
			return Data{}, ctx.Err()
		}}
		opts := options(p)
		opts.Interval = time.Second
		opts.Timeout = 5 * time.Second
		e := startEngine(context.Background(), opts, nil)
		synctest.Wait()
		time.Sleep(4 * time.Second)
		if calls.Load() != 1 {
			t.Fatal("slow poll spawned another poll")
		}
		e.close()
		if active.Load() != 0 || p.closes.Load() != 1 {
			t.Fatal("close failed to join provider")
		}
		time.Sleep(time.Hour)
		if calls.Load() != 1 {
			t.Fatal("closed provider polled again")
		}
	})
}

func TestActionsCommitBeforeExecutionAndRejectUnpublished(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	argv := []string{"fake-tool", "focus"}
	p := &provider{poll: func(context.Context) (Data, error) {
		return Data{Rows: []Row{{Kind: "text", Text: "session", Act: argv}}}, nil
	}}
	opts := options(p)
	var executions atomic.Int32
	opts.Execute = func(ctx context.Context, got []string) error {
		records, err := store.List(ctx, actionjournal.Running)
		if err != nil || len(records) != 1 {
			t.Errorf("execution preceded durable running state: %v %v", records, err)
		}
		executions.Add(1)
		return nil
	}
	e := startEngine(context.Background(), opts, store)
	defer e.close()
	await(t, e, func(update) bool { return true })
	id, err := e.submit(Action{Widget: "w", Argv: argv})
	if err != nil {
		t.Fatal(err)
	}
	await(t, e, func(u update) bool { return u.status == "action succeeded " })
	if _, err = e.submit(Action{Widget: "w", Argv: []string{"unpublished"}}); err != nil {
		t.Fatal(err)
	}
	await(t, e, func(u update) bool { return u.status == "action discarded action no longer published" })
	if executions.Load() != 1 {
		t.Fatal("unpublished action executed")
	}
	records, err := store.List(context.Background(), actionjournal.Succeeded)
	if err != nil || len(records) != 1 || records[0].ID != id {
		t.Fatalf("completion not retained: %v %v", records, err)
	}
}

func TestShutdownCancelsAndReapsAction(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	argv := []string{"long-action"}
	p := &provider{poll: func(context.Context) (Data, error) { return Data{Rows: []Row{{Act: argv}}}, nil }}
	started := make(chan struct{})
	opts := options(p)
	opts.Execute = func(ctx context.Context, _ []string) error { close(started); <-ctx.Done(); return ctx.Err() }
	e := startEngine(context.Background(), opts, store)
	await(t, e, func(update) bool { return true })
	if _, err = e.submit(Action{Widget: "w", Argv: argv}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("action not started")
	}
	e.close()
	if _, err = e.submit(Action{Widget: "w", Argv: argv}); !errors.Is(err, context.Canceled) {
		t.Fatalf("post-close submission: %v", err)
	}
	records, err := store.List(context.Background(), actionjournal.Uncertain)
	if err != nil || len(records) != 1 {
		t.Fatalf("cancellation outcome not saved: %v %v", records, err)
	}
}

func TestPollFailureRetainsDataButRefusesActions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls int
		p := &provider{poll: func(context.Context) (Data, error) {
			calls++
			if calls > 1 {
				return Data{}, errors.New("offline")
			}
			return Data{Title: "retained"}, nil
		}}
		opts := options(p)
		opts.Interval = time.Second
		e := startEngine(context.Background(), opts, nil)
		defer e.close()
		synctest.Wait()
		<-e.updates
		time.Sleep(time.Second)
		synctest.Wait()
		u := <-e.updates
		if u.err == nil || u.data.Title != "retained" {
			t.Fatal("failure erased retained data")
		}
		if _, err := e.submit(Action{Widget: "w", Argv: []string{"anything"}}); err == nil {
			t.Fatal("read-only component accepted action")
		}
	})
}

// No display or terminal is involved: fake time spans a full hidden day.
func TestHiddenProviderCancelsAndResumesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls, active atomic.Int32
		p := &provider{poll: func(ctx context.Context) (Data, error) {
			n := calls.Add(1)
			active.Add(1)
			defer active.Add(-1)
			if n == 1 {
				<-ctx.Done()
				return Data{}, ctx.Err()
			}
			return Data{Title: "fresh"}, nil
		}}
		opts := options(p)
		opts.Timeout = 48 * time.Hour
		opts.Interval = time.Second
		e := startEngine(context.Background(), opts, nil)
		defer e.close()
		synctest.Wait()
		e.setVisible(false)
		synctest.Wait()
		if active.Load() != 0 {
			t.Fatal("hide failed to join in-flight poll")
		}
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if calls.Load() != 1 {
			t.Fatal("hidden provider kept polling")
		}
		select {
		case u := <-e.updates:
			t.Fatalf("hidden result published: %+v", u)
		default:
		}
		e.setVisible(true)
		e.setVisible(true)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("resume started %d polls", calls.Load()-1)
		}
		if u := <-e.updates; u.data.Title != "fresh" || u.err != nil {
			t.Fatal("resume did not refresh data")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if calls.Load() != 3 {
			t.Fatal("resume did not retain one poll cadence")
		}
	})
}

func TestRapidHideShowRejectsCanceledGeneration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		release := make(chan struct{})
		p := &provider{poll: func(ctx context.Context) (Data, error) {
			if calls.Add(1) == 1 {
				<-ctx.Done()
				<-release
				return Data{Title: "stale"}, nil
			}
			return Data{Title: "current"}, nil
		}}
		opts := options(p)
		opts.Timeout = time.Hour
		e := startEngine(context.Background(), opts, nil)
		defer e.close()
		synctest.Wait()
		e.setVisible(false)
		e.setVisible(true)
		close(release)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatalf("coalesced transitions produced %d polls", calls.Load())
		}
		if u := <-e.updates; u.data.Title != "current" {
			t.Fatalf("stale generation delivered: %+v", u)
		}
	})
}

func TestQueuedActionCannotSurviveHideShow(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	argv := []string{"fake-tool"}
	p := &provider{poll: func(context.Context) (Data, error) { return Data{}, nil }}
	opts := options(p)
	opts.Timeout = time.Second
	opts.Execute = func(context.Context, []string) error { t.Fatal("retired action executed"); return nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := &engine{ctx: ctx, opts: opts, journal: store, visibility: make(chan struct{}, 1)}
	e.visible.Store(true)
	_, _, err = store.Accept(ctx, actionjournal.Request{ID: "retired", Kind: "component-action", Source: "fake", Policy: actionjournal.DiscardOnRestart, Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	req := request{id: "retired", generation: e.visibilityVersion.Load(), action: Action{Widget: "w", Argv: argv}}
	e.setVisible(false)
	e.setVisible(true)
	e.act(req, update{data: Data{Rows: []Row{{Act: argv}}}})
	records, err := store.List(ctx, actionjournal.Discarded)
	if err != nil || len(records) != 1 {
		t.Fatalf("retired intent not discarded: %v %v", records, err)
	}
}

func TestNewVisibilityRequiresFreshSnapshot(t *testing.T) {
	store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	argv := []string{"fake-tool"}
	p := &provider{poll: func(context.Context) (Data, error) { return Data{}, nil }}
	opts := options(p)
	opts.Timeout = time.Second
	executions := 0
	opts.Execute = func(context.Context, []string) error { executions++; return nil }
	e := &engine{ctx: context.Background(), opts: opts, journal: store, visibility: make(chan struct{}, 1)}
	e.visible.Store(true)
	e.setVisible(false)
	e.setVisible(true)
	for _, fresh := range []bool{false, true} {
		id := fmt.Sprintf("fresh-%t", fresh)
		_, _, err := store.Accept(e.ctx, actionjournal.Request{ID: id, Kind: "component-action", Source: "fake", Policy: actionjournal.DiscardOnRestart, Payload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		snapshot := update{data: Data{Rows: []Row{{Act: argv}}}}
		if fresh {
			snapshot.generation = e.visibilityVersion.Load()
		}
		e.act(request{id: id, generation: e.visibilityVersion.Load(), action: Action{Widget: "w", Argv: argv}}, snapshot)
		want := actionjournal.Discarded
		if fresh {
			want = actionjournal.Succeeded
		}
		records, err := store.List(e.ctx, want)
		if err != nil || len(records) != 1 {
			t.Fatalf("fresh=%t records=%v err=%v", fresh, records, err)
		}
	}
	if executions != 1 {
		t.Fatalf("executions=%d", executions)
	}
}

type ownerProvider struct {
	provider
	err error
}

func (p *ownerProvider) Act(context.Context, []string, module.Operation) error { return p.err }

// A shared owner's definite refusal is a failure; only a lost reply needs review.
func TestSharedOwnerOutcomeKeepsDefiniteFailure(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "component-owner-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	argv := []string{"reno:next"}
	poll := func(context.Context) (Data, error) { return Data{Rows: []Row{{Act: argv}}}, nil }
	for _, tc := range []struct {
		name     string
		provider Provider
		want     actionjournal.State
	}{
		{"accepted", &ownerProvider{provider: provider{poll: poll}}, actionjournal.Dispatched},
		{"refused", &ownerProvider{provider: provider{poll: poll}, err: errors.New("Reno owner: Reno action not recognized (failed)")}, actionjournal.Failed},
		{"reply-lost", &ownerProvider{provider: provider{poll: poll}, err: fmt.Errorf("Reno operation x: %w", errors.Join(socketio.ErrUncertainDelivery, io.EOF))}, actionjournal.Uncertain},
		{"owner-absent", &renoservice.Client{Socket: filepath.Join(dir, "absent.sock")}, actionjournal.Failed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := actionjournal.Open(filepath.Join(t.TempDir(), "actions.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			opts := Options{Provider: tc.provider, Widget: Widget{ID: "w"}, Timeout: time.Second}
			e := &engine{ctx: context.Background(), opts: opts, journal: store, visibility: make(chan struct{}, 1)}
			e.visible.Store(true)
			if _, _, err := store.Accept(e.ctx, actionjournal.Request{ID: tc.name, Kind: "component-action", Source: "fake", Policy: actionjournal.DiscardOnRestart, Payload: []byte(`{}`)}); err != nil {
				t.Fatal(err)
			}
			e.act(request{id: tc.name, action: Action{Widget: "w", Argv: argv}}, update{data: Data{Rows: []Row{{Act: argv}}}})
			records, err := store.List(e.ctx, "")
			if err != nil || len(records) != 1 || records[0].State != tc.want {
				t.Fatalf("outcome = %+v %v, want %s", records, err, tc.want)
			}
		})
	}
}

func TestProviderErrorsBackOffAndSuccessRestoresCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		p := &provider{poll: func(context.Context) (Data, error) {
			if calls.Add(1) <= 2 {
				return Data{}, errors.New("offline")
			}
			return Data{}, nil
		}}
		opts := options(p)
		opts.Interval = time.Second
		e := startEngine(context.Background(), opts, nil)
		defer e.close()
		synctest.Wait()
		for _, step := range []struct {
			wait  time.Duration
			calls int
		}{{time.Second, 1}, {time.Second, 2}, {3 * time.Second, 2}, {time.Second, 3}, {time.Second, 4}} {
			time.Sleep(step.wait)
			synctest.Wait()
			if int(calls.Load()) != step.calls {
				t.Fatalf("calls=%d want %d", calls.Load(), step.calls)
			}
		}
	})
}
