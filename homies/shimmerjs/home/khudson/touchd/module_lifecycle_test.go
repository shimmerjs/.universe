package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLocalModuleFailureDoesNotCancelSibling(t *testing.T) {
	installFakeHID(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := make(chan struct{})
	sibling := make(chan struct{})
	done := make(chan error, 1)
	sc := newScanner()
	go func() {
		done <- runModules(ctx, sc, []moduleEntry{
			{mod: stubModule{name: "local", run: func(context.Context, Env) error {
				close(failed)
				return moduleTerminal{errors.New("invalid local ownership")}
			}}},
			{mod: stubModule{name: "sibling", run: func(ctx context.Context, _ Env) error { <-ctx.Done(); close(sibling); return nil }}},
		})
	}()
	<-failed
	select {
	case <-sibling:
		t.Fatal("local failure stopped sibling")
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown failed to join modules")
	}
}

func TestDuplicateWaiterIsLocalTerminalFailure(t *testing.T) {
	sc := newScanner()
	sc.states[moonMatch] = &matchState{ch: make(chan string, 1)}
	_, err := sc.await(context.Background(), moonMatch)
	var local moduleTerminal
	if !errors.As(err, &local) {
		t.Fatalf("duplicate waiter lost local classification: %v", err)
	}
}
