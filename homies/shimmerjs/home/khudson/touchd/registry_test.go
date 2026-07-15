package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func parkedMatches(sc *scanner) []Match {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	var ms []Match
	for m, st := range sc.states {
		if st.ch != nil {
			ms = append(ms, m)
		}
	}
	return ms
}

// startDaemon runs runDaemonScanner against a fresh scanner and temp socket
// paths; the fake enumerate has no devices, so enabled modules park in
// AwaitDevice without ever touching hidapi.
func startDaemon(t *testing.T, enabled map[string]bool, opts options) (*scanner, func() error) {
	t.Helper()
	sc := newScanner()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runDaemonScanner(ctx, sc, opts, enabled, nil) }()
	return sc, func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("daemon did not stop")
			return nil
		}
	}
}

// The registry runs exactly the config-enabled modules; a disabled module
// has zero footprint: no socket file, no scanner waiter, no goroutine
// parked on the bus.
func TestRegistryRunsExactlyEnabledModules(t *testing.T) {
	installFakeHID(t)
	dir := t.TempDir()
	opts := options{
		socket:     filepath.Join(dir, "touch.sock"),
		keysSocket: filepath.Join(dir, "keys.sock"),
	}

	sc, stop := startDaemon(t, map[string]bool{"moonlander": true}, opts)
	waitParked(t, sc, 1)
	if _, err := os.Stat(opts.keysSocket); err != nil {
		t.Fatalf("enabled moonlander did not bind keys.sock: %v", err)
	}
	if _, err := os.Stat(opts.socket); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("disabled edge module bound touch.sock (stat err %v)", err)
	}
	if got := parkedMatches(sc); len(got) != 1 || got[0] != moonMatch {
		t.Fatalf("scanner waiters = %v, want only the moonlander match", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	// inverse cut: edge only -- both collections wait, no keys.sock
	dir = t.TempDir()
	opts = options{
		socket:     filepath.Join(dir, "touch.sock"),
		keysSocket: filepath.Join(dir, "keys.sock"),
	}
	sc, stop = startDaemon(t, map[string]bool{"edge": true}, opts)
	waitParked(t, sc, 2)
	if _, err := os.Stat(opts.socket); err != nil {
		t.Fatalf("enabled edge did not bind touch.sock: %v", err)
	}
	if _, err := os.Stat(opts.keysSocket); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("disabled moonlander bound keys.sock (stat err %v)", err)
	}
	got := parkedMatches(sc)
	if len(got) != 2 || !slices.Contains(got, edgeDigitizerMatch) || !slices.Contains(got, edgeMouseMatch) {
		t.Fatalf("scanner waiters = %v, want the two edge collections", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// A module whose socket cannot bind is disabled loudly while the others
// run -- the old keys.sock posture, generalized per module.
func TestRegistryBindFailureDisablesModule(t *testing.T) {
	installFakeHID(t)
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys.sock")
	squatter, err := newBroadcaster(keys)
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.close()

	opts := options{socket: filepath.Join(dir, "touch.sock"), keysSocket: keys}
	sc, stop := startDaemon(t, defaultModules(), opts)
	waitParked(t, sc, 2)
	got := parkedMatches(sc)
	if slices.Contains(got, moonMatch) {
		t.Fatalf("moonlander ran despite its socket bind failing: %v", got)
	}
	if _, err := os.Stat(opts.socket); err != nil {
		t.Fatalf("edge did not run past the moonlander bind failure: %v", err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// With every enabled module failing to bind, the daemon errors instead of
// idling under KeepAlive with nothing to serve.
func TestRegistryAllBindsFailedErrors(t *testing.T) {
	installFakeHID(t)
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys.sock")
	squatter, err := newBroadcaster(keys)
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.close()

	opts := options{socket: filepath.Join(dir, "touch.sock"), keysSocket: keys}
	if err := runDaemonScanner(context.Background(), newScanner(), opts, map[string]bool{"moonlander": true}, nil); err == nil {
		t.Fatal("daemon with no running modules returned nil")
	}
}
