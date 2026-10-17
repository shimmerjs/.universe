package component

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	module "github.com/shimmerjs/khudson/khudson/provider"
)

type update struct {
	generation uint64
	data       Data
	err        error
	status     string
}
type request struct {
	id         string
	generation uint64
	action     Action
}
type engine struct {
	visible           atomic.Bool
	visibilityVersion atomic.Uint64
	visibility        chan struct{}
	pollCancel        context.CancelFunc
	ctx               context.Context
	cancel            context.CancelFunc
	opts              Options
	journal           *actionjournal.Store
	commands          chan request
	updates           chan update
	done              chan struct{}
	life              sync.Mutex
	closed            bool
	submits           sync.WaitGroup
}

func startEngine(ctx context.Context, opts Options, journal *actionjournal.Store) *engine {
	ctx, cancel := context.WithCancel(ctx)
	if opts.Interval < 100*time.Millisecond {
		opts.Interval = time.Second
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 3 * time.Second
	}
	if opts.Execute == nil {
		opts.Execute = execute
	}
	opts.Widget.Render.Params = maps.Clone(opts.Widget.Render.Params)
	if opts.Widget.Render.Params == nil {
		opts.Widget.Render.Params = make(map[string]any)
	}
	opts.Widget.Render.Params["poll-interval"] = opts.Interval
	e := &engine{ctx: ctx, cancel: cancel, opts: opts, journal: journal, commands: make(chan request, 16), updates: make(chan update, 1), done: make(chan struct{})}
	e.visible.Store(true)
	e.visibility = make(chan struct{}, 1)
	go e.run()
	return e
}

func (e *engine) setVisible(visible bool) {
	e.life.Lock()
	if e.closed || e.visible.Swap(visible) == visible {
		e.life.Unlock()
		return
	}
	e.visibilityVersion.Add(1)
	if !visible && e.pollCancel != nil {
		e.pollCancel()
	}
	e.life.Unlock()
	select {
	case e.visibility <- struct{}{}:
	default:
	}
}

func (e *engine) close() {
	e.life.Lock()
	e.closed = true
	e.cancel()
	e.life.Unlock()
	e.submits.Wait()
	<-e.done
}

// submit commits before handing an operation to the worker. A crash between
// these steps leaves pending work for startup recovery, never silent replay.
func (e *engine) submit(action Action) (string, error) {
	e.life.Lock()
	if e.closed {
		e.life.Unlock()
		return "", context.Canceled
	}
	e.submits.Add(1)
	e.life.Unlock()
	defer e.submits.Done()
	if e.journal == nil {
		return "", errors.New("component actions disabled")
	}
	generation := e.visibilityVersion.Load()
	action.Argv = slices.Clone(action.Argv)
	id := rand.Text()
	payload, err := json.Marshal(action)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	_, _, err = e.journal.Accept(ctx, actionjournal.Request{ID: id, Kind: "component-action", Source: e.opts.Provider.Name(), Policy: actionjournal.DiscardOnRestart, Payload: payload})
	diagnostic.Default.Observe("component.action.accept", err, "refuse unless committed")
	if err != nil {
		return id, err
	}
	diagnostic.Operation("component.action", id, "pending", "committed")
	select {
	case <-e.ctx.Done():
		return id, e.ctx.Err()
	case e.commands <- request{id: id, generation: generation, action: action}:
		return id, nil
	default:
		err = e.journal.Transition(ctx, id, actionjournal.Pending, actionjournal.Discarded, "action queue full")
		return id, errors.Join(errors.New("action queue full"), err)
	}
}

func (e *engine) publish(u update) {
	select {
	case <-e.updates:
	default:
	}
	e.updates <- u
}

func (e *engine) run() {
	defer close(e.done)
	defer close(e.updates)
	if closer, ok := e.opts.Provider.(module.Closer); ok {
		defer func() { diagnostic.Default.Observe("component.provider.close", closer.Close(), "closed") }()
	}
	if demand, ok := e.opts.Provider.(module.DemandAware); ok {
		demand.SetDemand(true)
		defer demand.SetDemand(false)
	}
	var current update
	var failures uint
	poll := func() {
		e.life.Lock()
		if !e.visible.Load() || e.closed {
			e.life.Unlock()
			return
		}
		ctx, cancel := context.WithTimeout(e.ctx, e.opts.Timeout)
		e.pollCancel = cancel
		version := e.visibilityVersion.Load()
		e.life.Unlock()
		data, err := e.opts.Provider.Poll(ctx, e.opts.Widget.Render.Params)
		cancel()
		e.life.Lock()
		e.pollCancel = nil
		e.life.Unlock()
		if !e.visible.Load() || version != e.visibilityVersion.Load() {
			return
		}
		diagnostic.Default.Observe("component.provider.poll", err, "retain last successful data; retry with bounded backoff")
		if err == nil {
			failures = 0
			current.data = data
			current.generation = version
		} else {
			failures = min(failures+1, 5)
		}
		current.err = err
		e.publish(current)
	}
	appliedVisibility := e.visibilityVersion.Load()
	poll()
	timer := time.NewTimer(pollDelay(e.opts.Interval, failures))
	defer timer.Stop()
	for {
		var tick <-chan time.Time
		if e.visible.Load() {
			tick = timer.C
		}
		select {
		case <-e.ctx.Done():
			return
		case req := <-e.commands:
			if e.ctx.Err() != nil {
				return
			}
			current.status = e.act(req, current)
			poll()
			timer.Reset(pollDelay(e.opts.Interval, failures))
		case <-e.visibility:
			version := e.visibilityVersion.Load()
			if version == appliedVisibility {
				continue
			}
			appliedVisibility = version
			visible := e.visible.Load()
			if demand, ok := e.opts.Provider.(module.DemandAware); ok {
				demand.SetDemand(visible)
			}
			if visible {
				poll()
				timer.Reset(pollDelay(e.opts.Interval, failures))
			} else {
				timer.Stop()
			}
			diagnostic.OperationGeneration("component.visibility", "", version, fmt.Sprintf("visible=%t", visible), "provider demand updated")
		case <-tick:
			poll()
			timer.Reset(pollDelay(e.opts.Interval, failures))
		}
	}
}

func allows(data Data, argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	for _, row := range data.Rows {
		if slices.Equal(row.Act, argv) {
			return true
		}
		for _, act := range row.Menu {
			if slices.Equal(act.Argv, argv) {
				return true
			}
		}
		for _, end := range row.Ends {
			if !end.Dim && slices.Equal(end.Argv, argv) {
				return true
			}
		}
	}
	return false
}

func (e *engine) act(req request, current update) string {
	ctx, cancel := context.WithTimeout(e.ctx, e.opts.Timeout)
	defer cancel()
	state, reason := actionjournal.Discarded, "action no longer published"
	if e.visible.Load() && req.generation == e.visibilityVersion.Load() && current.generation == req.generation && current.err == nil && req.action.Widget == e.opts.Widget.ID && req.action.URL == "" && allows(current.data, req.action.Argv) {
		if err := e.journal.Transition(ctx, req.id, actionjournal.Pending, actionjournal.Running, ""); err != nil {
			diagnostic.Default.Observe("component.action.running", err, "refuse execution")
			return "action refused: " + err.Error()
		}
		diagnostic.Operation("component.action", req.id, "running", "validated against current data")
		state, reason = actionjournal.Succeeded, ""
		handled := false
		if owner, ok := e.opts.Provider.(module.ActionHandler); ok {
			handled = true
			err := owner.Act(ctx, req.action.Argv, module.Operation{ID: req.id, Generation: req.generation})
			state, reason = actionjournal.Dispatched, "shared owner accepted operation"
			if err != nil {
				state, reason = actionjournal.ExecutionState(err), err.Error()
			}
		}
		if h, ok := e.opts.Provider.(module.ActHandler); ok && !handled {
			handled = h.HandleAct(req.action.Argv)
		}
		if !handled {
			err := e.opts.Execute(ctx, req.action.Argv)
			diagnostic.Default.Observe("component.action.execute", err, "record outcome; never automatically retry")
			if err != nil {
				state, reason = actionjournal.ExecutionState(err), err.Error()
			}
		}
		// Preserve the outcome even when shutdown or the action deadline fired.
		ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel2()
		if err := e.journal.Transition(ctx2, req.id, actionjournal.Running, state, reason); err != nil {
			diagnostic.Default.Observe("component.action.finish", err, "retain uncertain outcome for restart review")
			return "action outcome could not be saved: " + err.Error()
		}
	} else {
		if err := e.journal.Transition(ctx, req.id, actionjournal.Pending, state, reason); err != nil {
			return "action discard failed: " + err.Error()
		}
	}
	diagnostic.Operation("component.action", req.id, string(state), reason)
	return fmt.Sprintf("action %s %s", state, reason)
}

func pollDelay(interval time.Duration, failures uint) time.Duration {
	if failures == 0 || interval >= 30*time.Second {
		return interval
	}
	return min(interval*time.Duration(1<<min(failures, 5)), 30*time.Second)
}
