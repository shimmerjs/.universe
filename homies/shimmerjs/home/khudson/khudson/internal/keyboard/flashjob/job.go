// Package flashjob owns firmware operations independently of terminal clients.
package flashjob

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/atomicfile"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/flash"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
	"github.com/shimmerjs/khudson/khudson/internal/ownerlock"
)

var ErrOwned = errors.New("firmware worker is active")
var ErrReview = errors.New("previous firmware operation requires review")

type Service struct{ Dir string }
type Status struct {
	ID     string              `json:"id"`
	Target string              `json:"target"`
	State  actionjournal.State `json:"state"`
	Reason string              `json:"reason,omitempty"`
	Step   flash.Step          `json:"step"`
	Info   string              `json:"info,omitempty"`
}
type request struct {
	Target string `json:"target"`
}
type progress struct {
	ID   string
	Step flash.Step
	Info string
}

func (s Service) lock() (*os.File, error) {
	if err := os.MkdirAll(s.Dir, 0700); err != nil {
		return nil, err
	}
	f, err := ownerlock.Acquire(filepath.Join(s.Dir, "owner.lock"))
	if errors.Is(err, ownerlock.ErrHeld) {
		return nil, ErrOwned
	}
	return f, err
}
func unlock(f *os.File) { f.Close() }
func (s Service) open() (*actionjournal.Store, error) {
	return actionjournal.Open(filepath.Join(s.Dir, "jobs.sqlite"))
}

// Submit persists intent before spawning the detached owner. Pending/running
// jobs are never resubmitted: an interrupted flash is not safely replayable.
func (s Service) Submit(ctx context.Context, target string, spawn func(string) error) (string, error) {
	if target == "" {
		return "", errors.New("firmware target revision required")
	}
	lock, err := s.lock()
	if err != nil {
		return "", err
	}
	store, err := s.open()
	if err != nil {
		unlock(lock)
		return "", err
	}
	defer store.Close()
	records, err := store.List(ctx, "")
	if err != nil {
		unlock(lock)
		return "", err
	}
	for _, r := range records {
		if r.State == actionjournal.Pending || r.State == actionjournal.Running || r.State == actionjournal.Uncertain {
			unlock(lock)
			return "", ErrReview
		}
	}
	id := rand.Text()
	payload, _ := json.Marshal(request{target})
	_, _, err = store.Accept(ctx, actionjournal.Request{ID: id, Kind: "firmware", Source: "keyboard", Policy: actionjournal.Review, Payload: payload})
	unlock(lock)
	diagnostic.Default.Observe("firmware.accept", err, "refuse spawn unless intent committed")
	if err != nil {
		return "", err
	}
	if err = spawn(id); err != nil {
		transition := store.Transition(ctx, id, actionjournal.Pending, actionjournal.Discarded, "worker failed to start: "+err.Error())
		diagnostic.Default.Observe("firmware.spawn.record", transition, "preserve unresolved intent")
	}
	diagnostic.Default.Observe("firmware.spawn", err, "inspect recorded operation; never automatically retry")
	return id, err
}

// Run holds the exclusive device-job lock until verification and final outcome
// commit. Its caller is a detached worker, not a terminal's lifetime context.
func (s Service) Run(ctx context.Context, id string, run func(context.Context, string, func(flash.Event)) (*generations.Record, error)) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock(lock)
	store, err := s.open()
	if err != nil {
		return err
	}
	defer store.Close()
	records, err := store.List(ctx, "")
	if err != nil {
		return err
	}
	var req request
	found := false
	for _, r := range records {
		if r.ID == id && r.State == actionjournal.Pending {
			err = json.Unmarshal(r.Payload, &req)
			found = true
			break
		}
	}
	if !found || err != nil {
		return errors.New("firmware request missing or already claimed")
	}
	if err = store.Transition(ctx, id, actionjournal.Pending, actionjournal.Running, "worker owns firmware operation"); err != nil {
		return err
	}
	diagnostic.Operation("firmware.state", id, "running", "independent worker owns device job")
	var last time.Time
	var step flash.Step = -1
	emit := func(e flash.Event) {
		if e.Step == step && time.Since(last) < time.Second {
			return
		}
		last = time.Now()
		step = e.Step
		p := progress{id, e.Step, e.Info}
		if len(p.Info) > 2048 {
			p.Info = p.Info[:2048]
		}
		data, _ := json.Marshal(p)
		err := atomicfile.Write(filepath.Join(s.Dir, "progress.json"), data, 0600)
		diagnostic.Default.Observe("firmware.progress", err, "continue owned flash; preserve journal outcome")
	}
	_, runErr := run(ctx, req.Target, emit)
	state, reason := actionjournal.Succeeded, "verified deployed revision"
	if errors.Is(runErr, flash.ErrAlreadyDeployed) {
		reason = "target already deployed"
	} else if runErr != nil {
		state, reason = actionjournal.Uncertain, runErr.Error()
	}
	// Verification or record failure can follow a successful write. It requires
	// inspection, never an automatic retry or a claim that nothing changed.
	bounded, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = store.Transition(bounded, id, actionjournal.Running, state, reason)
	diagnostic.Default.Observe("firmware.complete", err, "retain uncertain operation on commit failure")
	diagnostic.Operation("firmware.state", id, string(state), reason)
	return errors.Join(runErr, err)
}

// Inspect recovers only when no worker owns the lock. A pending launch gets a
// startup grace; running without an owner is uncertain regardless of progress.
func (s Service) Inspect(ctx context.Context) (Status, error) {
	if _, err := os.Stat(filepath.Join(s.Dir, "jobs.sqlite")); os.IsNotExist(err) {
		return Status{}, nil
	}
	store, err := s.open()
	if err != nil {
		return Status{}, err
	}
	defer store.Close()
	lock, lockErr := s.lock()
	if lockErr != nil && !errors.Is(lockErr, ErrOwned) {
		return Status{}, lockErr
	}
	if lock != nil {
		defer unlock(lock)
	}
	records, err := store.List(ctx, "")
	if err != nil || len(records) == 0 {
		return Status{}, err
	}
	r := records[len(records)-1]
	if lock != nil && (r.State == actionjournal.Running || r.State == actionjournal.Pending && time.Since(time.Unix(0, r.CreatedNS)) > 10*time.Second) {
		err = store.Transition(ctx, r.ID, r.State, actionjournal.Uncertain, "firmware owner exited; inspect board before another flash")
		diagnostic.Default.Observe("firmware.recovery", err, "require explicit review; never replay")
		if err != nil {
			return Status{}, err
		}
		r.State = actionjournal.Uncertain
		r.Reason = "firmware owner exited; inspect board before another flash"
	}
	var req request
	if err = json.Unmarshal(r.Payload, &req); err != nil {
		return Status{}, err
	}
	status := Status{ID: r.ID, Target: req.Target, State: r.State, Reason: r.Reason}
	data, err := os.ReadFile(filepath.Join(s.Dir, "progress.json"))
	if err == nil {
		var p progress
		if err = json.Unmarshal(data, &p); err == nil && p.ID == r.ID {
			status.Step, status.Info = p.Step, p.Info
		}
	}
	if err != nil && !os.IsNotExist(err) {
		diagnostic.Default.Observe("firmware.progress.read", err, "show journal state")
	}
	return status, nil
}
func (s Service) Discard(ctx context.Context, id string) error {
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock(lock)
	store, err := s.open()
	if err != nil {
		return err
	}
	defer store.Close()
	if err = store.Transition(ctx, id, actionjournal.Uncertain, actionjournal.Discarded, "reviewed and discarded by user"); err != nil {
		return fmt.Errorf("discard firmware operation: %w", err)
	}
	return nil
}
