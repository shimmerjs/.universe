// Package renoservice owns the shared Reno state and ordered effects.
package renoservice

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/effectrun"
	"github.com/shimmerjs/khudson/khudson/internal/ownerlock"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/sockclaim"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
	module "github.com/shimmerjs/khudson/khudson/provider"
)

type Backend interface {
	module.Module
	module.EffectHandler
	HandleCtl([]string) (any, []module.Effect, error)
}
type owner struct {
	backend  Backend
	journal  *actionjournal.Store
	effects  *effectrun.Host
	pollGate chan struct{}
}

func Socket() (string, error) {
	p, err := paths.Resolve()
	return filepath.Join(p.Dir, "reno", "owner.sock"), err
}

// Construct only after exclusive socket ownership and recovery. No view owns
// this lifetime, and no polling runs while clients are absent.
func Run(ctx context.Context, dir string, construct func() Backend) (runErr error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := ownerlock.Acquire(filepath.Join(dir, "owner.lock"))
	diagnostic.Default.Observe("reno.owner.lock.claim", err, "refuse startup unless owner lock is available")
	if err != nil {
		return err
	}
	defer lock.Close()
	socket := filepath.Join(dir, "owner.sock")
	listener, err := sockclaim.ClaimSocket(socket)
	diagnostic.Default.Observe("reno.owner.socket", err, "refuse startup; preserve existing listener")
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	store, err := actionjournal.Open(filepath.Join(dir, "actions.sqlite"))
	diagnostic.Default.Observe("reno.journal.open", err, "refuse mutation until storage repaired")
	if err != nil {
		return err
	}
	defer store.Close()
	err = store.Recover(ctx)
	diagnostic.Default.Observe("reno.journal.recover", err, "refuse startup; do not replay uncertain work")
	if err != nil {
		return err
	}
	o := &owner{backend: construct(), journal: store, pollGate: make(chan struct{}, 1)}
	o.effects = effectrun.New(effectrun.Hooks{Accept: o.accept, Transition: o.transition, Finish: func(m proto.Msg, s actionjournal.State, r string) { _ = o.transition(m, actionjournal.Running, s, r) }, Reject: func(m proto.Msg, e error) {
		diagnostic.Default.Observe("reno.effect.reject", e, "retain operation for review")
	}, Broadcast: func(m proto.Msg) {
		if m.Error != "" {
			diagnostic.Operation("reno.notice", m.OperationID, "notice", m.Error)
		}
	}, Failed: func(a []string, e error) {
		diagnostic.Default.Observe("reno.effect.exec", e, "record outcome; do not replay")
	}})
	go o.effects.Run()
	diagnostic.Operation("reno.owner", "", "ready", "journal recovered; socket claimed")
	var clients sync.WaitGroup
	defer func() {
		cancel()
		listener.Close()
		clients.Wait()
		o.effects.Shutdown()
		if closer, ok := o.backend.(module.Closer); ok {
			err := closer.Close()
			diagnostic.Default.Observe("reno.provider.close", err, "owner shutdown; preserve close failure")
			runErr = errors.Join(runErr, err)
		}
	}()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		clients.Go(func() {
			defer conn.Close()
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			defer stop()
			o.serve(ctx, conn)
		})
	}
}
func (o *owner) accept(m proto.Msg, policy actionjournal.Policy) (proto.Msg, bool, error) {
	if m.OperationID == "" {
		m.OperationID = rand.Text()
	}
	payload := m
	payload.OperationID = ""
	raw, err := json.Marshal(payload)
	if err != nil {
		return m, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r, fresh, err := o.journal.Accept(ctx, actionjournal.Request{ID: m.OperationID, Kind: m.Type, Source: "reno", Policy: policy, Payload: raw})
	m.OperationState, m.Error = string(r.State), r.Reason
	diagnostic.Default.Observe("reno.action.accept", err, "refuse unless committed")
	if err == nil {
		diagnostic.Action("reno.action.accept", m.OperationID, m.ParentOperationID, m.ViewID, m.ViewGeneration, m.OperationState, "shared owner accepted")
	}
	return m, fresh || err == nil && r.State == actionjournal.Pending, err
}
func (o *owner) transition(m proto.Msg, from, to actionjournal.State, reason string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := o.journal.Transition(ctx, m.OperationID, from, to, reason)
	diagnostic.Default.Observe("reno.action.transition", err, "retain unresolved outcome")
	if err == nil {
		diagnostic.Action("reno.action.transition", m.OperationID, m.ParentOperationID, m.ViewID, m.ViewGeneration, string(to), reason)
	}
	return err
}
func (o *owner) serve(ctx context.Context, conn net.Conn) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		diagnostic.Default.Observe("reno.request.deadline", err, "close connection")
		return
	}
	dec := json.NewDecoder(io.LimitReader(conn, 256<<10))
	var hello, m proto.Msg
	if err := dec.Decode(&hello); err != nil {
		diagnostic.Default.Observe("reno.request.hello", err, "close connection")
		return
	}
	if hello.Type != proto.TypeHello || hello.Role != proto.RoleCtl {
		diagnostic.Default.Observe("reno.request.protocol", errors.New("expected control hello"), "close connection")
		return
	}
	if err := dec.Decode(&m); err != nil {
		diagnostic.Default.Observe("reno.request.decode", err, "close connection; no intent accepted")
		return
	}
	diagnostic.Default.Observe("reno.request.decode", nil, "request decoded")
	if m.Type == "poll" {
		// A poll belongs to this connection. Mutations instead keep their
		// durable ownership when a caller disappears after submission.
		peerDone := make(chan struct{})
		go func() {
			defer close(peerDone)
			var extra [1]byte
			conn.Read(extra[:])
			cancel()
		}()
		defer func() { conn.Close(); <-peerDone }()
	}
	response := o.handle(ctx, m)
	diagnostic.Default.Observe("reno.response.write", json.NewEncoder(conn).Encode(response), "outcome is durable; do not resubmit")
}
func (o *owner) handle(ctx context.Context, m proto.Msg) proto.Msg {
	response := proto.Msg{Type: proto.TypeResp, OperationID: m.OperationID}
	fail := func(err error) proto.Msg { response.Error = err.Error(); return response }
	encode := func(data any) proto.Msg {
		var err error
		response.Data, err = json.Marshal(data)
		diagnostic.Default.Observe("reno.response.encode", err, "retain operation state; report encoding failure")
		if err != nil {
			return fail(err)
		}
		response.OK = true
		return response
	}
	if m.Type == "poll" {
		var params map[string]any
		if err := json.Unmarshal(m.Data, &params); err != nil {
			return fail(err)
		}
		select {
		case o.pollGate <- struct{}{}:
		case <-ctx.Done():
			return fail(ctx.Err())
		}
		defer func() { <-o.pollGate }()
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		data, err := o.backend.Poll(ctx, params)
		diagnostic.Default.Observe("reno.provider.poll", err, "return error; caller retains snapshot")
		if err != nil {
			return fail(err)
		}
		return encode(data)
	}
	if m.Type != proto.TypeCtl && m.Type != proto.TypeRowAct {
		return fail(errors.New("unsupported Reno request"))
	}
	if m.Type == proto.TypeCtl && m.Cmd != "reno" {
		return fail(errors.New("unknown control family"))
	}
	if m.Type == proto.TypeCtl && len(m.Args) > 0 {
		switch m.Args[0] {
		case "jobs":
			if len(m.Args) > 2 {
				return fail(errors.New("jobs accepts an optional sequence cursor"))
			}
			cursor := ""
			if len(m.Args) == 2 {
				cursor = m.Args[1]
			}
			after, err := actionjournal.ParseCursor(cursor)
			if err != nil {
				return fail(err)
			}
			records, err := o.journal.Page(ctx, after)
			diagnostic.Default.Observe("reno.journal.inspect", err, "preserve journal; retry read after storage recovery")
			if err != nil {
				return fail(err)
			}
			return encode(records)
		case "discard-job":
			if len(m.Args) != 2 {
				return fail(errors.New("discard-job requires operation ID"))
			}
			err := o.journal.Transition(ctx, m.Args[1], actionjournal.Uncertain, actionjournal.Discarded, "reviewed and discarded by user")
			diagnostic.Default.Observe("reno.journal.discard", err, "preserve unresolved operation on failure")
			if err != nil {
				return fail(err)
			}
			diagnostic.Operation("reno.action.discard", m.Args[1], "discarded", "reviewed and discarded by user")
			response.OK = true
			return response
		}
	}
	// Status is read-only and intentionally absent from the action journal.
	if m.Type == proto.TypeCtl && len(m.Args) == 1 && m.Args[0] == "status" {
		data, _, err := o.backend.HandleCtl(m.Args)
		diagnostic.Default.Observe("reno.provider.status", err, "return read failure without retrying mutations")
		if err != nil {
			return fail(err)
		}
		return encode(data)
	}
	m, fresh, err := o.accept(m, actionjournal.Review)
	response.OperationID = m.OperationID
	if err != nil {
		return fail(err)
	}
	if !fresh {
		response.OperationState = m.OperationState
		response.OK = m.OperationState == string(actionjournal.Dispatched) || m.OperationState == string(actionjournal.Succeeded)
		response.Error = m.Error
		return response
	}
	if err = o.transition(m, actionjournal.Pending, actionjournal.Running, ""); err != nil {
		// Another request may have claimed this operation after accept.
		response.OperationState = string(actionjournal.Uncertain)
		return fail(err)
	}
	var data any
	var semanticErr error
	started := false
	dispatchErr := o.effects.Transition(m, "reno", func() []module.Effect {
		if semanticErr = ctx.Err(); semanticErr != nil {
			return nil
		}
		started = true
		if m.Type == proto.TypeCtl {
			var effects []module.Effect
			data, effects, semanticErr = o.backend.HandleCtl(m.Args)
			return effects
		}
		handled, effects := o.backend.HandleActEffects(m.Argv)
		if !handled {
			semanticErr = errors.New("Reno action not recognized")
		}
		return effects
	})
	diagnostic.Default.Observe("reno.provider.transition", semanticErr, "record outcome; do not replay transition")
	diagnostic.Default.Observe("reno.effect.dispatch", dispatchErr, "retain partial transition for review")
	err = errors.Join(semanticErr, dispatchErr)
	state, reason := actionjournal.Dispatched, "module transition applied; child effects tracked separately"
	if err != nil {
		state, reason = actionjournal.ExecutionState(err), err.Error()
		if !started {
			// Expiry or shutdown before the provider ran leaves no unknown outcome.
			state = actionjournal.Failed
		}
	}
	response.OperationState = string(state)
	if commitErr := o.transition(m, actionjournal.Running, state, reason); commitErr != nil {
		response.OperationState = string(actionjournal.Uncertain)
		err = errors.Join(err, commitErr)
	}
	if err != nil {
		return fail(err)
	}
	return encode(data)
}

func Call(ctx context.Context, socket string, m proto.Msg) (proto.Msg, error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		diagnostic.Default.Observe("reno.owner.dial", err, "start or repair Reno owner service")
		return proto.Msg{}, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline := time.Now().Add(12 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		diagnostic.Default.Observe("reno.owner.deadline", err, "refuse request without bounded transport")
		return proto.Msg{}, err
	}
	if m.OperationID == "" && m.Type != "poll" {
		m.OperationID = rand.Text()
	}
	enc := json.NewEncoder(conn)
	err = enc.Encode(proto.Msg{Type: proto.TypeHello, Role: proto.RoleCtl})
	submitted := err == nil
	if submitted {
		err = enc.Encode(m)
	}
	var response proto.Msg
	if err == nil {
		err = socketio.NewDecoder(conn).Decode(&response)
	}
	if err == nil && (response.Type != proto.TypeResp || response.OperationID != m.OperationID) {
		err = errors.New("Reno response does not match request")
	}
	diagnostic.Default.Observe("reno.owner.exchange", err, "inspect recorded operation; never automatically replay")
	if err != nil {
		if submitted && mutating(m) {
			response.OperationID = m.OperationID
			response.OperationState = string(actionjournal.Uncertain)
			err = errors.Join(socketio.ErrUncertainDelivery, err)
			diagnostic.Action("reno.owner.exchange", m.OperationID, m.ParentOperationID, m.ViewID, m.ViewGeneration, "uncertain", "inspect owner journal; do not retry")
			err = fmt.Errorf("Reno operation %s: %w", m.OperationID, err)
		}
		return response, err
	}
	if !response.OK {
		err = fmt.Errorf("Reno owner: %s (%s)", response.Error, response.OperationState)
		if mutating(m) && (response.OperationState == string(actionjournal.Uncertain) || response.OperationState == string(actionjournal.Running) || response.OperationState == string(actionjournal.Pending) || response.OperationState == string(actionjournal.Dispatched) || response.OperationState == string(actionjournal.Succeeded)) {
			err = errors.Join(socketio.ErrUncertainDelivery, err)
		}
		return response, err
	}
	return response, nil
}

func mutating(m proto.Msg) bool {
	if m.Type == "poll" {
		return false
	}
	if m.Type == proto.TypeCtl && len(m.Args) > 0 && (m.Args[0] == "status" || m.Args[0] == "jobs") {
		return false
	}
	return true
}
