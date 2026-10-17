package bus

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// Journal I/O runs on action/control workers, never under b.mu or UI Update.
func actionContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}

func (b *Bus) openActions() {
	b.journalRequired = true
	b.journal, b.journalErr = actionjournal.Open(filepath.Join(b.opts.Paths.Dir, "actions.sqlite"))
	if b.journalErr == nil {
		ctx, cancel := actionContext()
		b.journalErr = b.journal.Recover(ctx)
		cancel()
	}
	diagnostic.Default.Observe("actions.open", b.journalErr, "disable actions on failure; preserve database")
}

func (b *Bus) actionStatus(id string, state actionjournal.State, reason string) {
	if id == "" {
		return
	}
	diagnostic.Operation("actions.state", id, string(state), reason)
	b.broadcast(proto.Msg{Type: proto.TypeActionStatus, OperationID: id, OperationState: string(state), Error: reason})
}

func (b *Bus) acceptAction(m proto.Msg, policy actionjournal.Policy) (proto.Msg, bool, error) {
	if b.journalErr != nil {
		return m, false, b.journalErr
	}
	if b.journal == nil {
		if b.journalRequired {
			return m, false, errors.New("action journal unavailable")
		}
		return m, true, nil // buses constructed directly by unit tests
	}
	if m.OperationID == "" {
		m.OperationID = rand.Text()
	}
	payload := m
	payload.OperationID = ""
	data, err := json.Marshal(payload)
	if err != nil {
		return m, false, err
	}
	source := m.Widget
	if source == "" {
		source = m.Cmd
	}
	ctx, cancel := actionContext()
	defer cancel()
	record, fresh, err := b.journal.Accept(ctx, actionjournal.Request{ID: m.OperationID, Kind: m.Type, Source: source, Policy: policy, Payload: data})
	diagnostic.Default.Observe("actions.accept", err, "refuse execution unless committed")
	if err == nil && fresh {
		diagnostic.Action("actions.accept", m.OperationID, m.ParentOperationID, m.ViewID, m.ViewGeneration, "pending", "committed before execution")
	}
	if err == nil {
		m.OperationState = string(record.State)
		m.Error = record.Reason
		b.actionStatus(record.ID, record.State, record.Reason)
	}
	return m, fresh || err == nil && record.State == actionjournal.Pending, err
}

func (b *Bus) transitionAction(m proto.Msg, from, to actionjournal.State, reason string) error {
	if b.journal == nil || m.OperationID == "" {
		return nil
	}
	ctx, cancel := actionContext()
	defer cancel()
	err := b.journal.Transition(ctx, m.OperationID, from, to, reason)
	diagnostic.Default.Observe("actions.transition", err, "retain unresolved operation; never replay unknown outcome")
	if err == nil {
		b.actionStatus(m.OperationID, to, reason)
		diagnostic.Action("actions.transition", m.OperationID, m.ParentOperationID, m.ViewID, m.ViewGeneration, string(to), reason)
	}
	return err
}

func (b *Bus) finishAction(m proto.Msg, state actionjournal.State, reason string) {
	_ = b.transitionAction(m, actionjournal.Running, state, reason)
}

func (b *Bus) rejectAction(m proto.Msg, err error) {
	diagnostic.Default.Observe("actions.dispatch", err, "refuse action")
	b.actionStatus(m.OperationID, actionjournal.Failed, err.Error())
	b.broadcast(proto.Msg{Type: proto.TypeNotice, Error: "action refused: " + err.Error()})
}

func (b *Bus) reapAction(m proto.Msg, kind string, argv []string, wait func() error) {
	err := wait()
	diagnostic.Default.Observe("actions.exec.wait", err, "record outcome; never automatically retry")
	if err != nil {
		b.reapCmd(kind, argv, func() error { return err })
		b.finishAction(m, actionjournal.ExecutionState(err), err.Error())
	} else {
		b.finishAction(m, actionjournal.Succeeded, "")
	}
}

func durableControl(m proto.Msg) (bool, actionjournal.Policy) {
	switch m.Cmd {
	case "reno":
		if len(m.Args) > 0 && m.Args[0] == "status" {
			return false, ""
		}
		return true, actionjournal.Review
	case "layout", "theme":
		if m.ViewID != "" {
			return true, actionjournal.DiscardOnRestart
		}
		return true, actionjournal.Reconcile
	case "caffeinate":
		if m.ViewID != "" {
			return true, actionjournal.DiscardOnRestart
		}
		if m.Arg == "on" || m.Arg == "off" {
			return true, actionjournal.Reconcile
		}
		return true, actionjournal.DiscardOnRestart
	}
	return false, ""
}

func (b *Bus) executeCtl(m proto.Msg) proto.Msg {
	if desiredStateKey(m) != "" {
		b.desiredMu.Lock()
		defer b.desiredMu.Unlock()
	}
	durable, policy := durableControl(m)
	if !durable {
		return b.handleCtl(m)
	}
	m, fresh, err := b.acceptAction(m, policy)
	if err != nil {
		return proto.Msg{Type: proto.TypeResp, OperationID: m.OperationID, Error: err.Error()}
	}
	if !fresh {
		ok := m.OperationState == string(actionjournal.Succeeded) || m.OperationState == string(actionjournal.Dispatched)
		reason := m.Error
		if !ok && reason == "" {
			reason = "operation is " + m.OperationState
		}
		return proto.Msg{Type: proto.TypeResp, OperationID: m.OperationID, OperationState: m.OperationState, OK: ok, Error: reason}
	}
	if err = b.transitionAction(m, actionjournal.Pending, actionjournal.Running, ""); err != nil {
		return proto.Msg{Type: proto.TypeResp, OperationID: m.OperationID, Error: err.Error()}
	}
	resp := b.handleCtl(m)
	state := actionjournal.Succeeded
	if m.Cmd == "reno" {
		state = actionjournal.Dispatched
	}
	if !resp.OK {
		state = actionjournal.Failed
		if resp.OperationState == string(actionjournal.Uncertain) {
			state = actionjournal.Uncertain
		}
	}
	if err := b.transitionAction(m, actionjournal.Running, state, resp.Error); err != nil {
		resp.OK = false
		resp.Error = fmt.Sprintf("operation outcome could not be saved; inspect %s before retrying: %v", m.OperationID, err)
		resp.OperationState = string(actionjournal.Uncertain)
	} else {
		resp.OperationState = string(state)
	}
	resp.OperationID = m.OperationID
	return resp
}

// Layouts scoped to views do not supersede the persistent default layout.
func desiredStateKey(m proto.Msg) string {
	switch m.Cmd {
	case "layout":
		if m.ViewID == "" {
			return "layout"
		}
	case "theme", "caffeinate":
		return m.Cmd
	}
	return ""
}

func (b *Bus) recoverActions() {
	if b.journal == nil || b.journalErr != nil {
		return
	}
	ctx, cancel := actionContext()
	records, err := b.journal.List(ctx, "")
	cancel()
	diagnostic.Default.Observe("actions.recovery", err, "retain pending records on error")
	if err != nil {
		return
	}
	// Acceptance order is the desired-state order. An unresolved older
	// request must not overwrite a newer completed or still-pending choice.
	latest := make(map[string]int64)
	for _, record := range records {
		if record.State == actionjournal.Failed || record.State == actionjournal.Discarded {
			continue
		}
		var m proto.Msg
		if record.Kind == proto.TypeCtl && json.Unmarshal(record.Payload, &m) == nil {
			if key := desiredStateKey(m); key != "" {
				latest[key] = record.Sequence
			}
		}
	}
	for _, record := range records {
		if record.State != actionjournal.Pending {
			continue
		}
		var m proto.Msg
		err = json.Unmarshal(record.Payload, &m)
		durable, policy := durableControl(m)
		if err != nil || record.Kind != proto.TypeCtl || !durable || policy != actionjournal.Reconcile || record.Policy != actionjournal.Reconcile {
			ctx, cancel := actionContext()
			err = b.journal.Transition(ctx, record.ID, actionjournal.Pending, actionjournal.Uncertain, "no eligible recovery handler")
			cancel()
			diagnostic.Default.Observe("actions.recovery", err, "retain unsupported operation for review")
			continue
		}
		m.OperationID = record.ID
		if latest[desiredStateKey(m)] > record.Sequence {
			_ = b.transitionAction(m, actionjournal.Pending, actionjournal.Discarded, "superseded by newer desired-state operation")
			continue
		}
		if err = b.transitionAction(m, actionjournal.Pending, actionjournal.Running, "reconcile desired state after restart"); err != nil {
			continue
		}
		resp := b.handleCtl(m) // current config and normal semantic validation
		state := actionjournal.Succeeded
		if !resp.OK {
			state = actionjournal.Failed
			if resp.OperationState == string(actionjournal.Uncertain) {
				state = actionjournal.Uncertain
			}
		}
		b.finishAction(m, state, resp.Error)
	}
}

func (b *Bus) listActions(cursor string) proto.Msg {
	if b.journal == nil {
		return proto.Msg{Type: proto.TypeResp, Error: fmt.Sprintf("action journal unavailable: %v", b.journalErr)}
	}
	ctx, cancel := actionContext()
	defer cancel()
	after, err := actionjournal.ParseCursor(cursor)
	if err != nil {
		return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
	}
	records, err := b.journal.Page(ctx, after)
	diagnostic.Default.Observe("actions.inspect", err, "preserve journal; retry read after storage recovery")
	if err != nil {
		return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
	}
	data, err := json.Marshal(records)
	if err != nil {
		return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
	}
	return proto.Msg{Type: proto.TypeResp, OK: true, Data: data}
}

func (b *Bus) discardInput(m proto.Msg, reason string) {
	if m.OperationID == "" || m.Type == proto.TypeForward {
		return
	}
	m, fresh, err := b.acceptAction(m, actionjournal.DiscardOnRestart)
	if err != nil {
		diagnostic.Default.Observe("actions.discard", err, "retain unresolved intent")
		return
	}
	if fresh {
		_ = b.transitionAction(m, actionjournal.Pending, actionjournal.Discarded, reason)
	}
}

// Desired state is durable, but applying it to the current host is incomplete.
type reconciliationError struct{ err error }

func (e *reconciliationError) Error() string {
	return "desired state saved; application pending: " + e.err.Error()
}
func (e *reconciliationError) Unwrap() error        { return e.err }
func (*reconciliationError) OutcomeUncertain() bool { return true }
