package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sstallion/go-hid"
)

// shortBindRetry shrinks the bind-retry wait so squatter tests stay fast.
func shortBindRetry(t *testing.T, wait time.Duration) {
	t.Helper()
	old := bindRetryWait
	bindRetryWait = wait
	t.Cleanup(func() { bindRetryWait = old })
}

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
	go func() { done <- runDaemonScanner(ctx, sc, opts, enabled, nil, nil) }()
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

// A module whose socket stays unbindable -- a persistent squatter failing
// every retry attempt -- is disabled loudly while the others run: the old
// keys.sock posture, generalized per module.
func TestRegistryBindFailureDisablesModule(t *testing.T) {
	installFakeHID(t)
	shortBindRetry(t, 10*time.Millisecond)
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
	shortBindRetry(t, 10*time.Millisecond)
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys.sock")
	squatter, err := newBroadcaster(keys)
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.close()

	opts := options{socket: filepath.Join(dir, "touch.sock"), keysSocket: keys}
	if err := runDaemonScanner(context.Background(), newScanner(), opts, map[string]bool{"moonlander": true}, nil, nil); err == nil {
		t.Fatal("daemon with no running modules returned nil")
	}
}

// logiretch is a first-class registry module: enabling only it binds
// logiretch.sock, parks exactly the vendor-collection waiter, and leaves the
// HUD sockets untouched.
func TestRegistryRunsLogiretch(t *testing.T) {
	installFakeHID(t)
	dir := t.TempDir()
	opts := options{
		socket:     filepath.Join(dir, "touch.sock"),
		keysSocket: filepath.Join(dir, "keys.sock"),
		logiSocket: filepath.Join(dir, "logiretch.sock"),
	}
	sc, stop := startDaemon(t, map[string]bool{"logiretch": true}, opts)
	waitParked(t, sc, 1)
	if _, err := os.Stat(opts.logiSocket); err != nil {
		t.Fatalf("enabled logiretch did not bind logiretch.sock: %v", err)
	}
	if _, err := os.Stat(opts.socket); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("disabled edge module bound touch.sock (stat err %v)", err)
	}
	if got := parkedMatches(sc); len(got) != 1 || got[0] != logiMatch {
		t.Fatalf("scanner waiters = %v, want only the logiretch match", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// A transient bind conflict -- a just-killed prior instance still holding
// the socket -- recovers within the retry window instead of disabling the
// module for the process life. The squatter is released only after its
// dial guard connection proves the first attempt failed, so the recovery
// is provably a retry. (Short test name: t.TempDir embeds it in the socket
// path, which must stay under the 104-byte sun_path cap.)
func TestRegistryBindRetry(t *testing.T) {
	installFakeHID(t)
	shortBindRetry(t, 200*time.Millisecond)
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys.sock")
	squatter, err := newBroadcaster(keys)
	if err != nil {
		t.Fatal(err)
	}
	// the first failed attempt shows up as newBroadcaster's dial guard
	// connecting to the squatter; release the socket before the next attempt
	go func() {
		for squatter.clientCount() == 0 {
			time.Sleep(time.Millisecond)
		}
		squatter.close()
	}()

	opts := options{socket: filepath.Join(dir, "touch.sock"), keysSocket: keys}
	sc, stop := startDaemon(t, defaultModules(), opts)
	waitParked(t, sc, 3)
	if got := parkedMatches(sc); !slices.Contains(got, moonMatch) {
		t.Fatalf("moonlander did not recover from the transient bind conflict: %v", got)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}

// stubModule drives the registry runner without hardware.
type stubModule struct {
	name string
	run  func(ctx context.Context, env Env) error
}

func (m stubModule) Name() string                           { return m.name }
func (m stubModule) Run(ctx context.Context, env Env) error { return m.run(ctx, env) }

// runModules must not return until the scanner goroutine has: main frees
// hidapi right after the daemon returns, and an in-flight enumerate would
// be a use-after-free.
func TestRunModulesJoinsScanner(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	old := hidEnumerate
	hidEnumerate = func(vid, pid uint16, fn hid.EnumFunc) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil
	}
	t.Cleanup(func() { hidEnumerate = old })

	sc := newScanner()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mod := stubModule{name: "stub", run: func(ctx context.Context, env Env) error {
		env.AwaitDevice(ctx, moonMatch) // park a waiter so the scanner ticks
		return nil
	}}
	done := make(chan error, 1)
	go func() {
		done <- runModules(ctx, sc, []moduleEntry{{mod: mod, env: Env{scan: sc}}})
	}()

	<-entered // the scanner is inside the blocked enumerate
	cancel()  // the module returns; the scanner is still pinned
	select {
	case <-done:
		t.Fatal("runModules returned with the scanner inside enumerate")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runModules did not return after the enumerate was released")
	}
}

// A module Run error surfaces as a non-nil runModules return naming the
// module (daemon exits nonzero), while nil and context.Canceled returns
// keep the clean-shutdown exit 0.
func TestRunModulesSurfacesModuleError(t *testing.T) {
	installFakeHID(t)
	sentinel := errors.New("duplicate waiter")
	sc := newScanner()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runModules(ctx, sc, []moduleEntry{
			{mod: stubModule{name: "failer", run: func(context.Context, Env) error { return sentinel }}, env: Env{scan: sc}},
			{mod: stubModule{name: "parker", run: func(ctx context.Context, env Env) error {
				<-ctx.Done()
				return nil
			}}, env: Env{scan: sc}},
		})
	}()
	// no parent cancel: the failer's error must tear down the parked sibling
	// on its own, or the daemon idles instead of exiting under KeepAlive
	err := <-done
	cancel()
	if err == nil || !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "failer") {
		t.Fatalf("module error not surfaced: %v", err)
	}

	sc = newScanner()
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		done <- runModules(ctx, sc, []moduleEntry{
			{mod: stubModule{name: "nilmod", run: func(context.Context, Env) error { return nil }}, env: Env{scan: sc}},
			{mod: stubModule{name: "cancelmod", run: func(ctx context.Context, env Env) error {
				<-ctx.Done()
				return ctx.Err()
			}}, env: Env{scan: sc}},
		})
	}()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}
}
