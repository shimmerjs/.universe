package effectrun

import (
	"errors"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/provider"
)

func TestStartChecksGenerationAtAdmission(t *testing.T) {
	starts := 0
	h := New(Hooks{Start: func() func([]string) (func() error, error) {
		return func([]string) (func() error, error) { starts++; return func() error { return nil }, nil }
	}})
	epoch := h.effects.epoch
	h.effects.boundary("test")
	if _, err := h.startEffectAt([]string{"fake"}, false, &epoch); err == nil || starts != 0 {
		t.Fatalf("retired effect started: %v %d", err, starts)
	}
	if _, err := h.startEffectAt([]string{"cleanup"}, true, &epoch); err != nil || starts != 1 {
		t.Fatalf("cleanup refused: %v %d", err, starts)
	}
	h.effects.closed = true
	if _, err := h.startEffectAt([]string{"cleanup"}, true, &epoch); err == nil || starts != 1 {
		t.Fatalf("closed owner admitted cleanup: %v %d", err, starts)
	}
}

func TestTransitionAfterShutdownCannotMutateProvider(t *testing.T) {
	h := New(Hooks{})
	go h.Run()
	h.Shutdown()
	called := false
	err := h.DispatchTransition("reno", func() []module.Effect { called = true; return nil })
	if err == nil || called {
		t.Fatalf("closed host mutated provider: called=%v err=%v", called, err)
	}
}

func TestPartialAdmissionRetainsRequiredCleanup(t *testing.T) {
	states := map[string]actionjournal.State{}
	h := New(Hooks{
		Accept: func(m proto.Msg, _ actionjournal.Policy) (proto.Msg, bool, error) {
			m.OperationID = m.Arg
			if m.Arg == "later" {
				return m, false, errors.New("journal full")
			}
			return m, true, nil
		},
		Transition: func(m proto.Msg, _, to actionjournal.State, _ string) error { states[m.OperationID] = to; return nil },
		Reject:     func(proto.Msg, error) {},
	})
	go h.Run()
	defer h.Shutdown()
	err := h.DispatchTransition("reno", func() []module.Effect {
		return []module.Effect{
			{Key: "cleanup", Final: true, Argv: []string{"renoman", "reno", "obs", "stop"}},
			{Key: "later", Argv: []string{"renoman", "reno", "next"}},
		}
	})
	if actionjournal.ExecutionState(err) != actionjournal.Uncertain || states["cleanup"] != actionjournal.Uncertain {
		t.Fatalf("required cleanup discarded: err=%v states=%v", err, states)
	}
}
