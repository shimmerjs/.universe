package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
)

// fakeSup is a Supervisor that materializes instantly and records calls.
type fakeSup struct {
	mu         sync.Mutex
	nextID     int
	ensures    int
	releases   int
	resizes    int
	tree       []rc.OSWindow
	lsErr      error
	closed     []string
	ensureGate chan struct{} // non-nil: Ensure blocks until it closes
	lsGate     chan struct{} // non-nil: LS blocks until it closes
	// ensureAwaitCtx: Ensure parks until ctx is done, THEN binds -- the
	// shutdown-races-a-launched-Ensure shape (the Launch already happened;
	// the binding lands after cancellation)
	ensureAwaitCtx bool
}

func (f *fakeSup) Ensure(ctx context.Context, st *WidgetState) error {
	f.mu.Lock()
	gate := f.ensureGate
	awaitCtx := f.ensureAwaitCtx
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if awaitCtx {
		<-ctx.Done()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures++
	f.nextID++
	st.setWindowID(f.nextID)
	return nil
}

func (f *fakeSup) Resize(_ context.Context, st *WidgetState, cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes++
	st.setSize(cols, rows)
	return nil
}

func (f *fakeSup) Release(_ context.Context, st *WidgetState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases++
	st.setWindowID(0)
	return nil
}

func (f *fakeSup) Adopt(tree []rc.OSWindow, reg *Registry) int {
	return adoptTree(tree, reg, func(match string) error {
		f.mu.Lock()
		f.closed = append(f.closed, match)
		f.mu.Unlock()
		return nil
	})
}

func (f *fakeSup) LS() ([]rc.OSWindow, error) {
	f.mu.Lock()
	gate := f.lsGate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tree, f.lsErr
}

func (f *fakeSup) setLS(tree []rc.OSWindow, err error) {
	f.mu.Lock()
	f.tree, f.lsErr = tree, err
	f.mu.Unlock()
}

func (f *fakeSup) closedMatches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

func (f *fakeSup) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ensures, f.resizes, f.releases
}

// lsTree wraps windows in a one-os-window, one-tab ls tree.
func lsTree(wins ...rc.Window) []rc.OSWindow {
	return []rc.OSWindow{{ID: 1, Tabs: []rc.Tab{{ID: 1, Windows: wins}}}}
}

// fakeScraper answers every poll synchronously; capture mode parks the sink
// instead, modeling a get-text still in flight.
type fakeScraper struct {
	mu      sync.Mutex
	polls   int
	capture bool
	sinks   []func(snapshot []byte, err error)
}

func (f *fakeScraper) Poll(st *WidgetState, sink func(id string, snapshot []byte, err error)) {
	id := st.Widget.ID
	f.mu.Lock()
	f.polls++
	if f.capture {
		f.sinks = append(f.sinks, func(snapshot []byte, err error) { sink(id, snapshot, err) })
		f.mu.Unlock()
		return
	}
	f.mu.Unlock()
	sink(id, []byte("\x1b[38:2:1:2:3mfake\x1b[m"), nil)
}

func (f *fakeScraper) setCapture(on bool) {
	f.mu.Lock()
	f.capture = on
	f.mu.Unlock()
}

// resolve fires captured sink i, delivering a scrape that was in flight.
func (f *fakeScraper) resolve(t *testing.T, i int, snapshot []byte, err error) {
	t.Helper()
	f.mu.Lock()
	if i >= len(f.sinks) {
		f.mu.Unlock()
		t.Fatalf("no captured sink %d (have %d)", i, len(f.sinks))
	}
	sink := f.sinks[i]
	f.mu.Unlock()
	sink(snapshot, err)
}

func (f *fakeScraper) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}

func schedTestBus(t *testing.T) (*Bus, *fakeSup, *fakeScraper) {
	t.Helper()
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"w": {ID: "w", Render: config.Render{
				Kind: "exec", Argv: []string{"true"}, Poll: "300ms", IdleKill: "1s",
			}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"w"}}},
		Layout:  "main",
	}
	sup := &fakeSup{}
	scr := &fakeScraper{}
	b := &Bus{
		cfg:         cfg,
		reg:         NewRegistry(cfg),
		docks:       make(map[net.Conn]*json.Encoder),
		substrateRC: rc.New("/nonexistent/kitty.sock"),
		sup:         sup,
		scrape:      scr,
		snapshots:   make(chan snapshotResult, 16),
	}
	return b, sup, scr
}

// addFakeDock registers a dock connection whose reads are drained, so
// broadcast never blocks.
func addFakeDock(t *testing.T, b *Bus) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, err := client.Read(buf); err != nil {
				return
			}
		}
	}()
	b.mu.Lock()
	b.docks[server] = json.NewEncoder(server)
	b.panelCols, b.panelRows = 80, 24
	b.mu.Unlock()
}

// drainBusy waits for the single-flight goroutine spawned by a pass.
func drainBusy(t *testing.T, entries map[string]*schedEntry, busyCh chan busyDone) {
	t.Helper()
	select {
	case d := <-busyCh:
		entries[d.id].busy = false
		if !d.ok {
			entries[d.id].failures++
			entries[d.id].backoffUntil = time.Now().Add(backoffFor(entries[d.id].failures))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no busy completion within 2s")
	}
}

// TestServeDockCtlLayout: a dock-sent ctl layout (tray nav, brand tap) moves
// the bus config layout -- the scheduler's visibility source -- and comes
// back as a TypeLayout broadcast, with no resp on the dock role.
func TestServeDockCtlLayout(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.cfg.Layouts["other"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}

	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
	}()

	enc := json.NewEncoder(client)
	dec := json.NewDecoder(client)
	// drain the whole greeting before touching b.mu: serveDock holds it
	// across the synchronous pipe writes
	var reloadMsg proto.Msg
	if err := dec.Decode(&reloadMsg); err != nil || reloadMsg.Type != proto.TypeReload || reloadMsg.Config == nil {
		t.Fatalf("greeting config = %+v (%v), want reload with config", reloadMsg, err)
	}
	var greet proto.Msg
	if err := dec.Decode(&greet); err != nil || greet.Type != proto.TypeLayout || greet.Layout != "main" {
		t.Fatalf("greeting = %+v (%v), want layout/main", greet, err)
	}
	var themeMsg proto.Msg
	if err := dec.Decode(&themeMsg); err != nil || themeMsg.Type != proto.TypeTheme || themeMsg.Theme != "day" {
		t.Fatalf("greeting theme = %+v (%v), want theme/day", themeMsg, err)
	}
	var caffMsg proto.Msg
	if err := dec.Decode(&caffMsg); err != nil || caffMsg.Type != proto.TypeCaffeinate {
		t.Fatalf("greeting caffeinate = %+v (%v), want caffeinate", caffMsg, err)
	}
	b.mu.Lock()
	cur := b.cfg.Layout
	b.mu.Unlock()
	if greet.Layout != cur {
		t.Fatalf("greeting layout %q != bus layout %q", greet.Layout, cur)
	}

	if err := enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "other"}); err != nil {
		t.Fatal(err)
	}
	var bcast proto.Msg
	if err := dec.Decode(&bcast); err != nil {
		t.Fatal(err)
	}
	if bcast.Type != proto.TypeLayout || bcast.Layout != "other" {
		t.Fatalf("broadcast = %s/%s, want layout/other", bcast.Type, bcast.Layout)
	}
	b.mu.Lock()
	got := b.cfg.Layout
	b.mu.Unlock()
	if got != "other" {
		t.Fatalf("bus layout = %q, want other", got)
	}

	// unknown target: no resp, but the bus re-asserts the current layout
	// so a desynced dock snaps back
	if err := enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "nope"}); err != nil {
		t.Fatal(err)
	}
	var reassert proto.Msg
	if err := dec.Decode(&reassert); err != nil {
		t.Fatal(err)
	}
	if reassert.Type != proto.TypeLayout || reassert.Layout != "other" {
		t.Fatalf("after unknown target got %s/%s, want re-asserted layout/other", reassert.Type, reassert.Layout)
	}
	if err := enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "main"}); err != nil {
		t.Fatal(err)
	}
	var next proto.Msg
	if err := dec.Decode(&next); err != nil {
		t.Fatal(err)
	}
	if next.Type != proto.TypeLayout || next.Layout != "main" {
		t.Fatalf("after re-assert got %s/%s, want layout/main", next.Type, next.Layout)
	}
	b.mu.Lock()
	got = b.cfg.Layout
	b.mu.Unlock()
	if got != "main" {
		t.Fatalf("bus layout = %q, want main", got)
	}

	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}
}

// TestHandleCtlLayoutResp: the ctl role keeps its resp on layout switches.
func TestHandleCtlLayoutResp(t *testing.T) {
	b, _, _ := schedTestBus(t)
	resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "main"})
	if resp.Type != proto.TypeResp || !resp.OK {
		t.Fatalf("resp = %+v, want ok", resp)
	}
	resp = b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "nope"})
	if resp.Type != proto.TypeResp || resp.OK || resp.Error == "" {
		t.Fatalf("resp = %+v, want error", resp)
	}
}

func TestSchedulerLifecycle(t *testing.T) {
	b, sup, scr := schedTestBus(t)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	// no dock connected: nothing materializes
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if e, _, _ := sup.counts(); e != 0 {
		t.Fatalf("ensured with no dock connected")
	}

	// dock arrives: widget materializes at the panel region
	addFakeDock(t, b)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	st, _ := b.reg.Get("w")
	if e, _, _ := sup.counts(); e != 1 || st.WindowID == 0 {
		t.Fatalf("ensure: calls=%d windowID=%d", e, st.WindowID)
	}
	if st.Cols != 80 || st.Rows != 24 {
		t.Fatalf("materialized at %dx%d, want 80x24", st.Cols, st.Rows)
	}

	// next pass: poll fires and the snapshot lands on the registry
	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if scr.count() != 1 {
		t.Fatalf("polls = %d, want 1", scr.count())
	}
	b.applySnapshot(<-b.snapshots, entries)
	if len(st.Snapshot) == 0 {
		t.Fatal("snapshot not recorded on registry")
	}

	// cadence respected: within the poll interval no second poll fires
	now = now.Add(100 * time.Millisecond)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if scr.count() != 1 {
		t.Fatalf("poll fired inside the interval")
	}
	now = now.Add(300 * time.Millisecond)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if scr.count() != 2 {
		t.Fatalf("poll did not fire after the interval")
	}
	b.applySnapshot(<-b.snapshots, entries)

	// grid change: window chases the new panel region
	b.mu.Lock()
	b.panelCols, b.panelRows = 100, 30
	b.mu.Unlock()
	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	if _, r, _ := sup.counts(); r != 1 {
		t.Fatalf("resizes = %d, want 1", r)
	}
	if st.Cols != 100 || st.Rows != 30 {
		t.Fatalf("resized to %dx%d, want 100x30", st.Cols, st.Rows)
	}

	// scrape error: binding survives a failed verify (kitty unreachable)
	sup.setLS(nil, context.DeadlineExceeded)
	b.applySnapshot(snapshotResult{id: "w", win: st.WindowID, err: context.DeadlineExceeded}, entries)
	if !entries["w"].errPending {
		t.Fatal("scrape error did not mark errPending")
	}
	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	if st.WindowID == 0 {
		t.Fatal("binding dropped on unverifiable error")
	}

	// dock leaves: widget is invisible, idleKill=1s retires it
	b.mu.Lock()
	for c := range b.docks {
		delete(b.docks, c)
	}
	b.mu.Unlock()
	lastVisible := entries["w"].lastVisible
	now = lastVisible.Add(500 * time.Millisecond)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if _, _, r := sup.counts(); r != 0 {
		t.Fatalf("released before idleKill elapsed")
	}
	now = lastVisible.Add(1100 * time.Millisecond)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	if _, _, r := sup.counts(); r != 1 || st.WindowID != 0 {
		t.Fatalf("idle release did not happen: releases=%d windowID=%d", r, st.WindowID)
	}
}

// TestVerifyUnbindRace: the errPending verify goroutine unbinds through
// bindMu while the input worker reads via Binding on another goroutine;
// run under -race.
func TestVerifyUnbindRace(t *testing.T) {
	b, sup, _ := schedTestBus(t)
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	st, _ := b.reg.Get("w")
	st.setWindowID(7)
	entries["w"] = &schedEntry{lastVisible: now, errPending: true}
	sup.setLS(nil, nil) // tree without window 7: verify unbinds

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				st.Binding()
			}
		}
	}()

	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	close(stop)
	wg.Wait()

	if id, _, _ := st.Binding(); id != 0 {
		t.Fatalf("verify did not unbind: windowID=%d", id)
	}
}

// TestReloadQuiesce: a queued reload waits for in-flight single-flight RC
// calls, then swaps registry + config atomically and re-arms adoption; the
// late-bound window whose widget left the config is GC'd on adopt.
func TestReloadQuiesce(t *testing.T) {
	b, sup, _ := schedTestBus(t)
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	gate := make(chan struct{})
	sup.mu.Lock()
	sup.ensureGate = gate
	sup.mu.Unlock()

	b.schedulerPass(ctx, now, entries, busyCh, false)
	if !entries["w"].busy {
		t.Fatal("ensure not in flight")
	}

	newCfg := &config.Config{
		Widgets: map[string]config.Widget{
			"v": {ID: "v", Render: config.Render{Kind: "exec", Argv: []string{"true"}, Poll: "300ms"}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"v"}}},
		Layout:  "main",
	}
	oldReg := b.reg
	b.mu.Lock()
	b.pending = newCfg
	b.mu.Unlock()

	b.trySwapPending(entries)
	b.mu.Lock()
	sameReg := b.reg == oldReg
	stillPending := b.pending != nil
	b.mu.Unlock()
	if !sameReg || !stillPending {
		t.Fatal("swap ran while an Ensure was in flight")
	}

	close(gate)
	drainBusy(t, entries, busyCh)
	b.trySwapPending(entries)
	b.mu.Lock()
	swapped := b.reg != oldReg
	needAdopt := b.needAdopt
	pending := b.pending
	b.mu.Unlock()
	if !swapped || pending != nil || !needAdopt {
		t.Fatalf("swap did not run after drain: swapped=%v pending=%v needAdopt=%v", swapped, pending != nil, needAdopt)
	}
	if len(entries) != 0 {
		t.Fatalf("entries not cleared: %d left", len(entries))
	}

	// the Ensure that finished during the drain bound a window on the old
	// registry; adoption closes it because "w" left the config
	oldSt, _ := oldReg.Get("w")
	winID, _, _ := oldSt.Binding()
	if winID == 0 {
		t.Fatal("blocked ensure did not bind")
	}
	sup.setLS(lsTree(rc.Window{ID: winID, UserVars: map[string]string{UserVarWidget: "w"}}), nil)
	b.tryAdopt()
	want := fmt.Sprintf("id:%d", winID)
	if got := sup.closedMatches(); len(got) != 1 || got[0] != want {
		t.Fatalf("orphan GC closed %v, want [%s]", got, want)
	}
}

// TestShutdownDrainsInFlightEnsure: an Ensure that already Launch()ed when
// shutdown lands binds its window AFTER cancellation; the drain waits for it
// so releaseAll sees the late binding instead of leaking the window. Run
// under -race: the busyDone receive is the synchronization edge between the
// goroutine's setWindowID and releaseAll's read.
func TestShutdownDrainsInFlightEnsure(t *testing.T) {
	b, sup, _ := schedTestBus(t)
	addFakeDock(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	sup.mu.Lock()
	sup.ensureAwaitCtx = true
	sup.mu.Unlock()

	b.schedulerPass(ctx, now, entries, busyCh, false)
	if !entries["w"].busy {
		t.Fatal("ensure not in flight")
	}

	// the shutdown path: cancel, drain the busy set, then release
	cancel()
	b.drainInFlight(entries, busyCh)
	b.releaseAll()

	st, _ := b.reg.Get("w")
	if id, _, _ := st.Binding(); id != 0 {
		t.Fatalf("late-bound window %d survived shutdown", id)
	}
	if _, _, rel := sup.counts(); rel != 1 {
		t.Fatalf("releases = %d, want 1 (the late-bound window)", rel)
	}
}

// TestShutdownAfterSwapReleasesLeftovers: after a config-reload swap the
// bindings live only in the substrate's user vars; shutdown before the
// re-armed adopt has run must rebind them (one bounded adopt) so releaseAll
// closes the windows instead of leaking every materialized one.
func TestShutdownAfterSwapReleasesLeftovers(t *testing.T) {
	b, sup, _ := schedTestBus(t)
	addFakeDock(t, b)

	// a previous life bound window 7 to widget w; the substrate user var is
	// the only surviving record
	sup.setLS(lsTree(rc.Window{ID: 7, UserVars: map[string]string{UserVarWidget: "w"}}), nil)
	newCfg := &config.Config{
		Widgets: map[string]config.Widget{
			"w": {ID: "w", Render: config.Render{Kind: "exec", Argv: []string{"true"}, Poll: "300ms"}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"w"}}},
		Layout:  "main",
	}
	b.mu.Lock()
	b.pending = newCfg
	b.mu.Unlock()
	entries := make(map[string]*schedEntry)
	b.trySwapPending(entries) // installs the new registry, re-arms adopt

	// SIGTERM lands before the re-armed adopt ran: the shutdown branch is
	// immediately ready, no tick fires first
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { b.scheduler(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not exit")
	}
	if _, _, rel := sup.counts(); rel < 1 {
		t.Fatalf("releases = %d, want > 0 (post-swap leftover window leaked)", rel)
	}
}

// TestReloadRebindSameWidget: a reload that keeps a widget rebinds its
// leftover window to the new WidgetState instead of Ensuring a duplicate.
func TestReloadRebindSameWidget(t *testing.T) {
	b, sup, _ := schedTestBus(t)
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	oldSt, _ := b.reg.Get("w")
	winID, _, _ := oldSt.Binding()
	if winID == 0 {
		t.Fatal("ensure did not bind")
	}

	newCfg := &config.Config{
		Widgets: map[string]config.Widget{
			"w": {ID: "w", Render: config.Render{Kind: "exec", Argv: []string{"true"}, Poll: "300ms"}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"w"}}},
		Layout:  "main",
	}
	b.mu.Lock()
	b.pending = newCfg
	b.mu.Unlock()
	b.trySwapPending(entries)
	newSt, _ := b.reg.Get("w")
	if newSt == oldSt {
		t.Fatal("registry not swapped")
	}

	sup.setLS(lsTree(rc.Window{ID: winID, UserVars: map[string]string{UserVarWidget: "w"}}), nil)
	b.tryAdopt()
	if id, _, _ := newSt.Binding(); id != winID {
		t.Fatalf("rebound to %d, want %d", id, winID)
	}
	if got := sup.closedMatches(); len(got) != 0 {
		t.Fatalf("rebind closed windows: %v", got)
	}

	ensuresBefore, _, _ := sup.counts()
	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if e, _, _ := sup.counts(); e != ensuresBefore {
		t.Fatalf("duplicate ensure after rebind: %d -> %d", ensuresBefore, e)
	}
}

// TestReloadReachesDock: a swapped reload reaches connected docks as a
// TypeReload carrying the full new config, so widgets/strip/layout state
// cannot silently desync when the layout name is unchanged.
func TestReloadReachesDock(t *testing.T) {
	b, _, _ := schedTestBus(t)
	entries := make(map[string]*schedEntry)

	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
	}()
	msgs := make(chan proto.Msg, 16)
	go func() {
		dec := json.NewDecoder(client)
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()
	read := func() proto.Msg {
		t.Helper()
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatal("dock connection closed")
			}
			return m
		case <-time.After(2 * time.Second):
			t.Fatal("no message within 2s")
			return proto.Msg{}
		}
	}

	// greeting: config first (the dock's startup copy is replaced on
	// connect), then layout/theme/caffeinate
	if m := read(); m.Type != proto.TypeReload || m.Config == nil || m.Config.Layout != "main" {
		t.Fatalf("greeting[0] = %+v, want reload with the current config", m)
	}
	for _, want := range []string{proto.TypeLayout, proto.TypeTheme, proto.TypeCaffeinate} {
		if m := read(); m.Type != want {
			t.Fatalf("greeting = %s, want %s", m.Type, want)
		}
	}

	newCfg := &config.Config{
		Widgets: map[string]config.Widget{
			"v": {ID: "v", Render: config.Render{Kind: "exec", Argv: []string{"true"}, Poll: "300ms"}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"v"}}},
		Layout:  "main",
		Strip:   &config.Strip{Entries: []config.StripEntry{{Label: "vv", Target: "main"}}},
	}
	b.mu.Lock()
	b.pending = newCfg
	b.mu.Unlock()
	b.trySwapPending(entries)

	m := read()
	if m.Type != proto.TypeReload || m.Config == nil {
		t.Fatalf("reload broadcast = %+v, want reload with config", m)
	}
	if _, ok := m.Config.Widgets["v"]; !ok {
		t.Fatalf("reload config widgets = %v, want v", m.Config.Widgets)
	}
	if m.Config.Strip == nil || len(m.Config.Strip.Entries) != 1 || m.Config.Strip.Entries[0].Label != "vv" {
		t.Fatalf("reload config strip = %+v, want the new entries", m.Config.Strip)
	}
	if m.Config.Layout != "main" {
		t.Fatalf("reload config layout = %q, want main", m.Config.Layout)
	}

	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}
}

// TestNativeBackoff: a failing native module respects backoffUntil instead
// of repolling at full cadence.
func TestNativeBackoff(t *testing.T) {
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"n": {ID: "n", Render: config.Render{Kind: "native", Module: "absent", Poll: "1s"}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"n"}}},
		Layout:  "main",
	}
	sup := &fakeSup{}
	b := &Bus{
		cfg:       cfg,
		reg:       NewRegistry(cfg),
		docks:     make(map[net.Conn]*json.Encoder),
		sup:       sup,
		snapshots: make(chan snapshotResult, 16),
		natives:   make(chan nativeResult, 16),
	}
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	r := <-b.natives
	if r.err == nil {
		t.Fatal("absent module polled without error")
	}
	if entries["n"].failures == 0 {
		t.Fatal("failure not recorded")
	}

	// past nextPoll but inside the backoff window: no second poll
	now = now.Add(time.Second + schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if entries["n"].busy {
		t.Fatal("native repolled inside backoff")
	}
	select {
	case r := <-b.natives:
		t.Fatalf("unexpected native result inside backoff: %+v", r)
	default:
	}
}

// TestNoMaterializeWhileAdoptPending: launch waits until adoption resolves
// so a transient LS failure cannot duplicate leftover keepAlive windows.
func TestNoMaterializeWhileAdoptPending(t *testing.T) {
	b, sup, _ := schedTestBus(t)
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	b.mu.Lock()
	b.needAdopt = true
	b.mu.Unlock()
	sup.setLS(nil, fmt.Errorf("kitty not up yet"))

	for range 2 {
		if b.adoptPending() {
			b.tryAdopt()
		}
		b.schedulerPass(ctx, now, entries, busyCh, false)
		now = now.Add(schedTick)
	}
	if e, _, _ := sup.counts(); e != 0 {
		t.Fatalf("materialized while adopt pending: %d ensures", e)
	}

	sup.setLS(lsTree(rc.Window{ID: 42, UserVars: map[string]string{UserVarWidget: "w"}}), nil)
	if b.adoptPending() {
		b.tryAdopt()
	}
	st, _ := b.reg.Get("w")
	if id, _, _ := st.Binding(); id != 42 {
		t.Fatalf("adopt bound %d, want 42", id)
	}
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if e, _, _ := sup.counts(); e != 0 {
		t.Fatalf("ensured after adoption: %d", e)
	}
}

// TestLayoutBroadcastConsistency: setLayout's mutate + broadcast share one
// critical section, so the last TypeLayout a dock receives matches the
// final config layout even while switches race the greeting; run under
// -race.
func TestLayoutBroadcastConsistency(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.cfg.Layouts["other"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
	}()

	var mu sync.Mutex
	last := ""
	greeted := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		dec := json.NewDecoder(client)
		seen := false
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				return
			}
			if m.Type == proto.TypeLayout {
				mu.Lock()
				last = m.Layout
				mu.Unlock()
				if !seen {
					seen = true
					close(greeted)
				}
			}
		}
	}()

	// the greeting TypeLayout proves registration (they share one critical
	// section), so every setLayout after this point reaches the dock
	select {
	case <-greeted:
	case <-time.After(2 * time.Second):
		t.Fatal("no greeting layout before switch storm")
	}

	swDone := make(chan struct{})
	go func() {
		defer close(swDone)
		for i := range 100 {
			name := "main"
			if i%2 == 1 {
				name = "other"
			}
			if err := b.setLayout(name); err != nil {
				t.Errorf("setLayout %s: %v", name, err)
				return
			}
		}
	}()
	<-swDone

	b.mu.Lock()
	final := b.cfg.Layout
	b.mu.Unlock()
	client.Close()
	<-readerDone
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}
	mu.Lock()
	got := last
	mu.Unlock()
	if got != final {
		t.Fatalf("last layout on the wire %q, want final %q", got, final)
	}
}

// TestGreetingReplaysCachedState: a reconnecting dock gets layout, theme
// (defaulted to day), and every cached snapshot / native payload instead of
// blanks until the next poll.
func TestGreetingReplaysCachedState(t *testing.T) {
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"w": {ID: "w", Render: config.Render{Kind: "exec", Argv: []string{"true"}, Poll: "300ms"}},
			"n": {ID: "n", Render: config.Render{Kind: "native", Module: "m", Poll: "1s"}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"w", "n"}}},
		Layout:  "main",
	}
	b := &Bus{
		cfg:   cfg,
		reg:   NewRegistry(cfg),
		docks: make(map[net.Conn]*json.Encoder),
	}
	stW, _ := b.reg.Get("w")
	stW.setSize(80, 24)
	stW.setSnapshot([]byte("snap-w"))
	stN, _ := b.reg.Get("n")
	stN.setNative([]byte(`{"title":"cpu"}`))

	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
	}()

	dec := json.NewDecoder(client)
	read := func() proto.Msg {
		t.Helper()
		var m proto.Msg
		if err := dec.Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	if m := read(); m.Type != proto.TypeReload || m.Config == nil {
		t.Fatalf("greeting[0] = %+v, want reload with config", m)
	}
	if m := read(); m.Type != proto.TypeLayout || m.Layout != "main" {
		t.Fatalf("greeting[1] = %+v, want layout/main", m)
	}
	if m := read(); m.Type != proto.TypeTheme || m.Theme != "day" {
		t.Fatalf("greeting[2] = %+v, want theme/day", m)
	}
	if m := read(); m.Type != proto.TypeCaffeinate {
		t.Fatalf("greeting[3] = %+v, want caffeinate", m)
	}
	var gotSnap, gotData bool
	for range 2 {
		switch m := read(); m.Type {
		case proto.TypeSnapshot:
			if m.Widget != "w" || m.Cols != 80 || m.Rows != 24 || m.ANSI != "snap-w" {
				t.Fatalf("snapshot replay = %+v", m)
			}
			gotSnap = true
		case proto.TypeWidgetData:
			if m.Widget != "n" || string(m.Data) != `{"title":"cpu"}` {
				t.Fatalf("widget-data replay = %+v", m)
			}
			gotData = true
		default:
			t.Fatalf("unexpected greeting message %s", m.Type)
		}
	}
	if !gotSnap || !gotData {
		t.Fatalf("greeting missed cached state: snapshot=%v data=%v", gotSnap, gotData)
	}

	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}
}

// TestScrapeDropsPostReleaseSnapshot: a get-text still in flight when its
// widget's window is released resolves against a dead binding; applySnapshot
// drops it so the released widget's snapshot stays empty and no greeting
// replays a torn-down window's screen.
func TestScrapeDropsPostReleaseSnapshot(t *testing.T) {
	b, _, scr := schedTestBus(t)
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	scr.setCapture(true)

	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	st, _ := b.reg.Get("w")
	if st.WindowID == 0 {
		t.Fatal("ensure did not bind")
	}

	// fire the poll: the sink parks, the get-text stays in flight
	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	if scr.count() != 1 {
		t.Fatalf("polls = %d, want 1", scr.count())
	}

	// dock leaves, idleKill=1s elapses: the widget releases mid-scrape
	b.mu.Lock()
	for c := range b.docks {
		delete(b.docks, c)
	}
	b.mu.Unlock()
	now = entries["w"].lastVisible.Add(1100 * time.Millisecond)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	if st.WindowID != 0 {
		t.Fatal("release did not unbind")
	}

	scr.resolve(t, 0, []byte("late frame"), nil)
	b.applySnapshot(<-b.snapshots, entries)
	if snap, _, _, _, _ := st.cached(); len(snap) != 0 {
		t.Fatalf("late snapshot repopulated a released widget: %q", snap)
	}

	// a fresh dock's greeting replays no snapshot for w; everything up to
	// the second TypeLayout (greeting layout, then the ctl layout ack the
	// read loop broadcasts) is the whole greeting
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock, Cols: 320, Rows: 18})
	}()
	msgs := make(chan proto.Msg, 16)
	go func() {
		dec := json.NewDecoder(client)
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()
	if err := json.NewEncoder(client).Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: "main"}); err != nil {
		t.Fatal(err)
	}
	layouts := 0
	for layouts < 2 {
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatal("dock connection closed")
			}
			if m.Type == proto.TypeSnapshot {
				t.Fatalf("greeting replayed a snapshot: %+v", m)
			}
			if m.Type == proto.TypeLayout {
				layouts++
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no message within 2s")
		}
	}
	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}
}

// TestAdoptDoesNotBlockTick: adoption runs single-flight off the tick
// goroutine, so a substrate whose LS hangs cannot stall polls; needAdopt
// stays armed (materialize gate closed) until LS answers, then clears.
func TestAdoptDoesNotBlockTick(t *testing.T) {
	b, sup, scr := schedTestBus(t)
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	st, _ := b.reg.Get("w")
	st.setWindowID(7)
	st.setSize(80, 24) // match the panel region so no resize fires

	gate := make(chan struct{})
	sup.mu.Lock()
	sup.lsGate = gate
	sup.mu.Unlock()
	b.mu.Lock()
	b.needAdopt = true
	b.mu.Unlock()

	// the tick branch's adopt trigger: single-flight, off the tick goroutine
	adoptDone := make(chan struct{}, 1)
	go func() {
		b.tryAdopt()
		adoptDone <- struct{}{}
	}()

	for range 4 {
		b.schedulerPass(ctx, now, entries, busyCh, false)
		now = now.Add(schedTick + 100*time.Millisecond)
	}
	if scr.count() < 2 {
		t.Fatalf("polls stalled during a hung adopt: %d", scr.count())
	}
	if !b.adoptPending() {
		t.Fatal("needAdopt cleared while LS was still blocked")
	}

	close(gate)
	select {
	case <-adoptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("adopt did not complete after ungate")
	}
	if b.adoptPending() {
		t.Fatal("adopt did not clear needAdopt")
	}
}

// TestSnapshotStaleFlag: a widget whose scrapes stop gets one ANSI-less
// TypeSnapshot{Stale:true} pulse once its frame ages past 3x the poll
// interval, and ctl status reports the snapshot age.
func TestSnapshotStaleFlag(t *testing.T) {
	b, _, scr := schedTestBus(t)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)

	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serveDock(server, json.NewEncoder(server), json.NewDecoder(server),
			proto.Msg{Type: proto.TypeHello, Role: proto.RoleDock,
				Cols: 320, Rows: 18, PanelCols: 80, PanelRows: 24})
	}()
	msgs := make(chan proto.Msg, 16)
	go func() {
		dec := json.NewDecoder(client)
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()
	read := func() proto.Msg {
		t.Helper()
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatal("dock connection closed")
			}
			return m
		case <-time.After(2 * time.Second):
			t.Fatal("no message within 2s")
			return proto.Msg{}
		}
	}
	for range 4 { // greeting: reload, layout, theme, caffeinate
		read()
	}

	// materialize, then one fresh snapshot; PolledAt stamps wall-clock now
	now := time.Now()
	b.schedulerPass(ctx, now, entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	now = now.Add(schedTick)
	b.schedulerPass(ctx, now, entries, busyCh, false)
	b.applySnapshot(<-b.snapshots, entries)
	if m := read(); m.Type != proto.TypeSnapshot || m.Stale || m.ANSI == "" {
		t.Fatalf("fresh snapshot = %+v, want live TypeSnapshot", m)
	}

	// scrapes freeze; the frame ages past 3x Poll (300ms)
	scr.setCapture(true)
	time.Sleep(1100 * time.Millisecond)
	b.schedulerPass(ctx, time.Now(), entries, busyCh, false)
	if m := read(); m.Type != proto.TypeSnapshot || m.Widget != "w" || !m.Stale || m.ANSI != "" {
		t.Fatalf("stale pulse = %+v, want ANSI-less TypeSnapshot{Stale:true}", m)
	}
	if !entries["w"].staleSent {
		t.Fatal("staleSent not latched after the pulse")
	}

	resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "status"})
	if !resp.OK {
		t.Fatalf("status resp = %+v", resp)
	}
	var status proto.Status
	if err := json.Unmarshal(resp.Data, &status); err != nil {
		t.Fatal(err)
	}
	age, err := time.ParseDuration(status.SnapshotAges["w"])
	if err != nil || age <= 0 {
		t.Fatalf("snapshotAges[w] = %q, want a nonzero age", status.SnapshotAges["w"])
	}

	client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveDock did not exit on close")
	}
}

// TestBusShutdownClosesClients: serve honors ctx -- cancellation closes the
// conn, so a parked client Decode unblocks instead of outliving Run.
func TestBusShutdownClosesClients(t *testing.T) {
	b, _, _ := schedTestBus(t)
	ctx, cancel := context.WithCancel(context.Background())
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.serve(ctx, server)
	}()

	if err := json.NewEncoder(client).Encode(proto.Msg{Type: proto.TypeHello, Role: proto.RoleCtl}); err != nil {
		t.Fatal(err)
	}
	cancel()

	errCh := make(chan error, 1)
	go func() {
		var m proto.Msg
		errCh <- json.NewDecoder(client).Decode(&m)
	}()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected EOF after shutdown, got a message")
		}
	case <-time.After(time.Second):
		t.Fatal("client read did not unblock within 1s of shutdown")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("serve did not exit on shutdown")
	}
}

// TestChromeNeverScheduled: a visible chrome-kind widget is never
// polled, never materialized, and emits no widget traffic.
func TestChromeNeverScheduled(t *testing.T) {
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"tray": {ID: "tray", Chrome: true, Render: config.Render{
				Kind: "chrome", Module: "nav-tray",
				Params: map[string]any{"entries": []any{}},
			}},
		},
		Layouts: map[string]config.Layout{"home": {Kind: "home", Regions: []config.Region{
			{Widget: "tray", Edge: "top", Size: 3},
		}}},
		Layout: "home",
	}
	sup := &fakeSup{}
	scr := &fakeScraper{}
	b := &Bus{
		cfg:       cfg,
		reg:       NewRegistry(cfg),
		docks:     make(map[net.Conn]*json.Encoder),
		sup:       sup,
		scrape:    scr,
		snapshots: make(chan snapshotResult, 16),
		natives:   make(chan nativeResult, 16),
	}
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	for range 4 {
		b.schedulerPass(ctx, now, entries, busyCh, false)
		now = now.Add(schedTick)
	}
	if e, r, rel := sup.counts(); e != 0 || r != 0 || rel != 0 {
		t.Fatalf("chrome widget hit the supervisor: ensures=%d resizes=%d releases=%d", e, r, rel)
	}
	if scr.count() != 0 {
		t.Fatalf("chrome widget scraped %d times", scr.count())
	}
	select {
	case r := <-b.natives:
		t.Fatalf("chrome widget polled natively: %+v", r)
	default:
	}
	if _, ok := entries["tray"]; ok {
		t.Fatal("chrome widget got a sched entry")
	}
}

// The materialize gate stays closed for the WHOLE adopt, not just the LS
// leg: tryAdopt clears needAdopt after LS while sup.Adopt is still
// rebinding, and an Ensure racing the rebind gets its fresh window
// overwritten and leaked. adoptInFlight=true must suppress
// materialize even with needAdopt clear.
func TestAdoptInFlightClosesMaterializeGate(t *testing.T) {
	b, sup, _ := schedTestBus(t)
	addFakeDock(t, b)
	ctx := context.Background()
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16)
	now := time.Unix(1000, 0)

	// visible, unbound, no needAdopt: only the in-flight flag gates
	b.mu.Lock()
	b.needAdopt = false
	b.mu.Unlock()

	b.schedulerPass(ctx, now, entries, busyCh, true)
	// nothing may be busy: the gate suppressed the materialize entirely
	for id, e := range entries {
		if e.busy {
			t.Fatalf("entry %s went busy while an adopt is in flight", id)
		}
	}
	if e, _, _ := sup.counts(); e != 0 {
		t.Fatalf("ensures = %d, want 0 while an adopt is in flight", e)
	}
	b.schedulerPass(ctx, now.Add(time.Second), entries, busyCh, false)
	drainBusy(t, entries, busyCh)
	if e, _, _ := sup.counts(); e != 1 {
		t.Fatalf("ensures = %d, want 1 once the adopt completes", e)
	}
}
