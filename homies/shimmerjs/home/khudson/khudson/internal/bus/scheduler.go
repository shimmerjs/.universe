package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// schedTick is the scheduler's pass cadence; the schema floors widget poll
// intervals at 250ms so one tick per floor is enough resolution.
const schedTick = 250 * time.Millisecond

// idleGraceDefault covers exec widgets with keepAlive=false and no usable
// idleKill: don't churn windows on a quick layout flip.
const idleGraceDefault = time.Minute

// schedEntry is scheduler-private per-widget state; shared runtime state
// (window id, size, snapshot) lives on WidgetState. All WidgetState
// mutation happens either on the scheduler goroutine or inside the
// single-flight goroutine guarded by busy -- never both at once.
type schedEntry struct {
	nextPoll    time.Time
	lastVisible time.Time
	busy        bool // an Ensure/Resize/Release/verify RC call is in flight
	errPending  bool // last scrape errored; window binding needs a verify
	staleSent   bool // stale mark broadcast; a fresh snapshot clears it
	// retry backoff: any failed single-flight call counts, and backoffUntil
	// gates materialize and native polls so a widget with a dead argv or a
	// down kitty does not relaunch or repoll every tick
	failures     int
	backoffUntil time.Time
}

// busyDone reports a finished single-flight RC call back to the scheduler.
type busyDone struct {
	id string
	ok bool
}

// backoffFor caps the retry delay at 30s.
func backoffFor(failures int) time.Duration {
	return min(time.Second<<min(failures, 5), 30*time.Second)
}

// snapshotResult carries one scrape off the poller goroutine back into the
// scheduler for fan-out. win is the window the scrape targeted; applySnapshot
// drops results whose binding has moved (released, rebound) since the poll.
type snapshotResult struct {
	id   string
	win  int
	data []byte
	err  error
}

// nativeResult carries one module poll back into the scheduler.
type nativeResult struct {
	id   string
	data module.Data
	err  error
}

// scheduler drives the exec-widget lifecycle: adopt leftover
// windows, materialize widgets visible in the active layout while a dock is
// connected, poll each at its cadence, retire idle ones. Runs until ctx is
// done, then releases everything it materialized.
func (b *Bus) scheduler(ctx context.Context) {
	entries := make(map[string]*schedEntry)
	busyCh := make(chan busyDone, 16) // finished single-flight RC calls
	// adopt is single-flight off the tick goroutine: LS rides rc.Client's 5s
	// deadlines, and a hung substrate must not freeze polls/fan-out per tick
	adoptBusy := false
	adoptDone := make(chan struct{}, 1)
	var shed loadShedder

	tick := time.NewTicker(schedTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			// an in-flight adopt can rebind a window after releaseAll walked
			// past it; wait it out like the busy drain below
			if adoptBusy {
				<-adoptDone
			}
			b.drainInFlight(entries, busyCh)
			if b.adoptPending() {
				// a swap re-armed adopt but it has not run: rebind leftover
				// windows onto the current registry so releaseAll can close them
				b.tryAdopt()
			}
			b.releaseAll()
			return
		case <-adoptDone:
			adoptBusy = false
		case d := <-busyCh:
			if e, ok := entries[d.id]; ok {
				e.busy = false
				if d.ok {
					e.failures = 0
				} else {
					e.failures++
					e.backoffUntil = time.Now().Add(backoffFor(e.failures))
				}
			}
		case r := <-b.snapshots:
			b.applySnapshot(r, entries)
		case r := <-b.natives:
			b.applyNative(r)
		case id := <-b.repoll:
			repollEntry(entries, id)
		case now := <-tick.C:
			b.trySwapPending(entries)
			if !adoptBusy && b.adoptPending() {
				adoptBusy = true
				go func() {
					b.tryAdopt()
					adoptDone <- struct{}{}
				}()
			}
			b.schedulerPass(ctx, now, entries, busyCh, adoptBusy, shed.active(now))
		}
	}
}

// repollEntry dues a widget's next poll now: a module-handled row act wants
// its state on glass, so the poll fires on the next tick (the pass still
// respects busy single-flight and visibility). The backoff clears too -- a
// user tap must not wait out a stale failure window, and taps bound the
// retry rate on their own.
func repollEntry(entries map[string]*schedEntry, id string) {
	if e, ok := entries[id]; ok {
		e.nextPoll = time.Time{}
		e.backoffUntil = time.Time{}
	}
}

// drainInFlight blocks until every busy-marked single-flight goroutine has
// reported back: releasing at shutdown while an Ensure that already
// Launch()ed is in flight would miss its late setWindowID and leak the
// window. Each busy entry owes exactly one busyDone (native pollers send it
// before the natives buffer, so the drain cannot deadlock on them), so the
// busy set counts down to empty.
// INVARIANT (accepted): the drain carries no deadline of
// its own -- it is bounded only by rc.Client's 5s dial/conn deadlines
// (~30s worst case per in-flight Ensure) and by modules honoring pctx.
// A ctx-ignoring module with a long PollInterval would stall SIGTERM
// exit until launchd's SIGKILL; cap here if such a module ever ships.
func (b *Bus) drainInFlight(entries map[string]*schedEntry, busyCh <-chan busyDone) {
	for {
		busy := false
		for _, e := range entries {
			if e.busy {
				busy = true
				break
			}
		}
		if !busy {
			return
		}
		d := <-busyCh
		if e, ok := entries[d.id]; ok {
			e.busy = false
		}
	}
}

// trySwapPending installs a queued config reload once every single-flight RC
// call has drained; swapping mid-flight would orphan windows an Ensure is
// still binding. Leftover windows rebind or get GC'd by the adopt pass the
// swap re-arms. The runtime layout selection survives the swap when the new
// config still defines it; only then does the file default win.
func (b *Bus) trySwapPending(entries map[string]*schedEntry) {
	b.mu.Lock()
	if b.pending == nil {
		b.mu.Unlock()
		return
	}
	for _, e := range entries {
		if e.busy {
			b.mu.Unlock()
			return
		}
	}
	cfg := b.pending
	if cur := b.cfg.Layout; cur != "" {
		if _, ok := cfg.Layouts[cur]; ok {
			cfg.Layout = cur
		}
	}
	b.cfg = cfg
	b.reg = NewRegistry(cfg)
	b.needAdopt = true
	b.pending = nil
	clear(entries)
	b.mu.Unlock()
	// the full config rides the broadcast: a layout-only ack would leave the
	// docks' startup config copies stale (widgets/strip/gestures desync); the
	// dock derives the effective layout from Config
	b.broadcast(proto.Msg{Type: proto.TypeReload, Config: cfg})
	log.Printf("khudson bus: config reloaded")
}

func (b *Bus) adoptPending() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.needAdopt
}

// tryAdopt rebinds windows left over from a previous bus (crash model:
// topology from CUE + @ ls + user vars). Retried every tick until the
// substrate kitty answers; the bus must tolerate kitty starting after it.
// Also re-run after a config reload swaps the registry. Runs off the tick
// goroutine (adopt single-flight); needAdopt clears only after LS succeeds,
// so the materialize gate stays closed while the adopt is in flight and no
// Ensure races the rebind for unbound widgets.
func (b *Bus) tryAdopt() {
	tree, err := b.sup.LS()
	if err != nil {
		return
	}
	b.mu.Lock()
	reg := b.reg
	b.needAdopt = false
	b.mu.Unlock()
	if n := b.sup.Adopt(tree, reg); n > 0 {
		log.Printf("khudson bus: adopted %d scrape windows", n)
	}
}

// schedulerPass runs one tick: reconcile every exec widget against
// visibility, then fire due polls. RC calls happen off this goroutine;
// entry.busy keeps them single-flight per widget.
// adoptInFlight keeps the materialize gate closed for the WHOLE adopt:
// needAdopt clears after LS but sup.Adopt is still rebinding windows in
// the adopt goroutine, and an Ensure racing it can have its fresh
// binding overwritten; the launched window then leaks hidden until
// the next reload adopt.
// shed (load shedding, loadshed.go) skips non-essential polls this tick;
// due polls stay due, so they fire on the first non-shed tick. Lifecycle
// work (materialize/resize/release/verify) never sheds.
func (b *Bus) schedulerPass(ctx context.Context, now time.Time, entries map[string]*schedEntry, busyCh chan<- busyDone, adoptInFlight, shed bool) {
	// runRC is the one-busyDone-per-busy single-flight protocol shared by the
	// Ensure/Resize/Release branches below.
	runRC := func(e *schedEntry, st *WidgetState, call func() error) {
		e.busy = true
		go func() {
			ok := true
			defer func() { busyCh <- busyDone{id: st.Widget.ID, ok: ok} }()
			if err := call(); err != nil {
				log.Printf("khudson bus: %v", err)
				ok = false
			}
		}()
	}

	b.mu.Lock()
	reg := b.reg
	layout, hasLayout := b.cfg.Layouts[b.cfg.Layout]
	docks := len(b.docks)
	panelCols, panelRows := b.panelCols, b.panelRows
	adoptPending := b.needAdopt || adoptInFlight
	b.mu.Unlock()

	visible := make(map[string]bool)
	if hasLayout && docks > 0 {
		for _, id := range layout.WidgetIDs() {
			visible[id] = true
		}
	}

	for _, id := range reg.IDs() {
		st, _ := reg.Get(id)
		kind := st.Widget.Render.Kind
		if kind != "exec" && kind != "native" {
			continue
		}
		e, ok := entries[id]
		if !ok {
			e = &schedEntry{lastVisible: now}
			entries[id] = e
		}
		vis := visible[id]
		if vis {
			e.lastVisible = now
		}
		if e.busy {
			continue
		}

		if kind == "native" {
			if vis && !now.Before(e.nextPoll) && now.After(e.backoffUntil) {
				if shed && b.sheddable(st.Widget.Render.Module) {
					continue
				}
				e.nextPoll = now.Add(st.Widget.Render.PollInterval())
				e.busy = true
				go func(st *WidgetState, id string) {
					r := b.pollNative(ctx, st)
					busyCh <- busyDone{id: id, ok: r.err == nil}
					b.natives <- r
				}(st, id)
			}
			continue
		}

		// the async adopt goroutine writes bindings under bindMu while this
		// pass runs: every read below goes through one Binding() snapshot,
		// never the bare fields
		win, cols, rows := st.Binding()

		// a widget whose scrapes stopped keeps its last frame; once it ages
		// past 3x the poll cadence, mark it stale on the wire so the dock
		// dims the tile instead of trusting a dead screen
		if !e.staleSent && vis && win != 0 {
			if at := st.polled(); !at.IsZero() && now.Sub(at) > 3*st.Widget.Render.PollInterval() {
				e.staleSent = true
				b.broadcast(proto.Msg{Type: proto.TypeSnapshot, Widget: id, Stale: true})
			}
		}

		switch {
		case e.errPending && win != 0:
			// a scrape failed: confirm the window still exists before
			// trusting the binding again; a dead one unbinds so the next
			// pass re-materializes
			e.errPending = false
			e.busy = true
			go func(st *WidgetState, win int) {
				ok := true
				defer func() { busyCh <- busyDone{id: st.Widget.ID, ok: ok} }()
				if tree, err := b.sup.LS(); err == nil && !windowExists(tree, win) {
					log.Printf("khudson bus: widget %s window %d is gone; rebinding", st.Widget.ID, win)
					st.setWindowID(0)
					ok = false
				}
			}(st, win)

		case vis && win == 0 && !adoptPending && now.After(e.backoffUntil) &&
			panelCols > 0 && panelRows > 0:
			// materialize sized to the dock's panel region
			st.setSize(panelCols, panelRows)
			runRC(e, st, func() error { return b.sup.Ensure(ctx, st) })

		case vis && win != 0 &&
			panelCols > 0 && panelRows > 0 && (cols != panelCols || rows != panelRows):
			// dock grid changed; chase it
			runRC(e, st, func() error { return b.sup.Resize(ctx, st, panelCols, panelRows) })

		case vis && win != 0 && !now.Before(e.nextPoll):
			if shed {
				break
			}
			e.nextPoll = now.Add(st.Widget.Render.PollInterval())
			b.scrape.Poll(st, func(_ string, snapshot []byte, err error) {
				b.snapshots <- snapshotResult{id: id, win: win, data: snapshot, err: err}
			})

		case !vis && win != 0 && !st.Widget.Render.KeepAlive:
			grace := st.Widget.Render.IdleKillAfter()
			if grace <= 0 {
				grace = idleGraceDefault
			}
			if now.Sub(e.lastVisible) < grace {
				break
			}
			runRC(e, st, func() error {
				err := b.sup.Release(ctx, st)
				if err == nil {
					log.Printf("khudson bus: released idle widget %s", st.Widget.ID)
				}
				return err
			})
		}
	}
}

// pollNative runs one module poll with a bounded deadline.
func (b *Bus) pollNative(ctx context.Context, st *WidgetState) nativeResult {
	id := st.Widget.ID
	mod, ok := b.mods[st.Widget.Render.Module]
	if !ok {
		return nativeResult{id: id, err: fmt.Errorf("module %q not compiled in", st.Widget.Render.Module)}
	}
	timeout := max(st.Widget.Render.PollInterval(), 5*time.Second)
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	data, err := mod.Poll(pctx, pollParams(st.Widget.Render))
	return nativeResult{id: id, data: data, err: err}
}

// pollParams copies the widget's config params and injects the runtime
// "poll-interval" cadence so modules size history rings by the real poll
// rather than an assumed one (module.HistCadence). Mirrors the resources
// composite's "cpu-util": runtime-injected, never schema'd config; the
// config's own map stays untouched.
func pollParams(r config.Render) map[string]any {
	out := make(map[string]any, len(r.Params)+1)
	maps.Copy(out, r.Params)
	out["poll-interval"] = r.PollInterval()
	return out
}

// applyNative records a module poll and fans it out to docks. Runs on the
// scheduler goroutine.
func (b *Bus) applyNative(r nativeResult) {
	msg := proto.Msg{Type: proto.TypeWidgetData, Widget: r.id}
	if r.err != nil {
		msg.Error = r.err.Error()
	} else if data, err := json.Marshal(r.data); err != nil {
		msg.Error = err.Error()
	} else {
		msg.Data = data
		// cache so a reconnecting dock's greeting can replay it
		b.mu.Lock()
		reg := b.reg
		b.mu.Unlock()
		if st, ok := reg.Get(r.id); ok {
			st.setNative(data)
			// publish the poll's row acts for handleRowAct's allowlist;
			// the error branches above keep the previous set
			var acts [][]string
			for _, row := range r.data.Rows {
				if len(row.Act) > 0 {
					acts = append(acts, row.Act)
				}
			}
			st.setActs(acts)
		}
	}
	b.broadcast(msg)
}

// applySnapshot records a scrape on the registry and fans it out to docks.
// Runs on the scheduler goroutine.
func (b *Bus) applySnapshot(r snapshotResult, entries map[string]*schedEntry) {
	b.mu.Lock()
	reg := b.reg
	b.mu.Unlock()
	st, ok := reg.Get(r.id)
	if !ok {
		return
	}
	cur, cols, rows := st.Binding()
	if cur != r.win {
		// the scrape's window was released (or rebound) while the get-text
		// was in flight; recording it would repopulate a torn-down window's
		// snapshot and the dock greeting would replay the stale screen
		return
	}
	msg := proto.Msg{Type: proto.TypeSnapshot, Widget: r.id, Cols: cols, Rows: rows}
	if r.err != nil {
		// loud, not silent: the dock shows the failure on the tile; the
		// scheduler verifies the window binding before reusing it
		msg.Error = r.err.Error()
		if e, ok := entries[r.id]; ok {
			e.errPending = true
		}
	} else {
		st.setSnapshot(r.data)
		msg.ANSI = string(r.data)
		if e, ok := entries[r.id]; ok {
			e.staleSent = false
		}
	}
	b.broadcast(msg)
}

// releaseAll tears down every materialized window at shutdown.
func (b *Bus) releaseAll() {
	b.mu.Lock()
	reg := b.reg
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, id := range reg.IDs() {
		st, _ := reg.Get(id)
		if winID, _, _ := st.Binding(); winID != 0 {
			if err := b.sup.Release(ctx, st); err != nil {
				log.Printf("khudson bus: shutdown: %v", err)
			}
		}
	}
}
