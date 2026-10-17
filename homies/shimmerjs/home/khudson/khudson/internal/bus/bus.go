// Package bus is the khudson daemon: it owns the config, the widget registry,
// the gesture recognizer, and the khudson.sock ndjson server that dock and
// ctl clients connect to.
// RC supervision and the scrape loop sit behind interfaces (see registry.go)
// so tests can fake them.
package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/atomicfile"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/effectrun"
	"github.com/shimmerjs/khudson/khudson/internal/gesture"
	"github.com/shimmerjs/khudson/khudson/internal/hookspool"
	"github.com/shimmerjs/khudson/khudson/internal/module/registry"
	"github.com/shimmerjs/khudson/khudson/internal/ownerlock"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
	"github.com/shimmerjs/khudson/khudson/internal/sockclaim"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
	"github.com/shimmerjs/khudson/khudson/provider"
)

// Options configures Run.
type Options struct {
	ConfigPath  string // empty = embedded example
	RendererBin string // owned, demand-driven kitty; empty supports externally managed test/dev instances
	Paths       paths.Paths
	Ready       func() // called once after the bus owns its socket; nil ok
}

// Bus is the daemon state.
type Bus struct {
	runCtx          context.Context
	actionWaiters   sync.WaitGroup
	paletteWaiters  sync.WaitGroup
	journal         *actionjournal.Store
	journalErr      error
	journalRequired bool
	opts            Options
	started         time.Time

	desiredMu   sync.Mutex         // serialize desired-state acceptance through outcome
	layoutMu    sync.Mutex         // serialize layout persistence without holding mu
	layoutWrite func(string) error // persistence seam; nil uses writeLayoutState
	mu          sync.Mutex
	cfg         *config.Config
	reg         *Registry
	docks       map[net.Conn]*socketio.Writer
	dockCaps    map[net.Conn]proto.ViewCapabilities
	views       map[net.Conn]*viewSession
	relayWake   chan struct{}
	// activeDock owns the bus singletons: the cell grid, the panel region,
	// and the recognizer's output. Exactly one dock holds the slot -- the
	// first to claim it -- and it frees on that dock's disconnect. Before
	// this, any dock's hello repointed the recognizer at its own grid, so a
	// second or reconnecting dock stole the live surface's taps and sized
	// scraped windows to a panel nobody was looking at.
	activeDock net.Conn
	needAdopt  bool
	// pending is a reloaded config awaiting a quiesced scheduler tick; the
	// scheduler swaps it in once no single-flight RC call is in flight.
	pending *config.Config
	theme   string // "" = day
	// palette is the HUD kitty's effective colors (get-colors), fetched at
	// dock adopt and re-fetched on every theme switch; nil until the first
	// fetch lands. fetchingPalette keeps the adopt fetch single-flight.
	palette         map[string]string
	fetchingPalette bool
	themePending    bool
	// the dock's active-panel content region; scraped windows get sized
	// to it (zero until a dock reports in)
	panelCols, panelRows int
	// lastLogi is the most recent MX-device battery frame (logiLoop); the
	// greeting replays it so a reconnecting dock renders the readout at once.
	// nil until the first frame lands (or while logiretch is absent).
	lastLogi *proto.LogiState
	// lastActFail is the latest failed act/verb exec (input.go actFailed):
	// ONE slot, overwritten per failure, so failure volume never grows state.
	// The greeting replays it so a dock connecting after the failure still
	// renders the warn cell.
	lastActFail *proto.ActFail

	// themeMu serializes whole theme switches (set-colors + m1ddc +
	// re-fetch) so concurrent ctl calls cannot interleave RC ops; never
	// taken while holding mu.
	themeMu sync.Mutex
	// colors is the HUD kitty color seam (get-colors/set-colors ONLY --
	// see hudColors in theme.go for why injection never rides it); lum is
	// the m1ddc luminance pairing.
	colors ThemeColors
	lum    Luminance

	renderer          *rendererLifecycle
	rendererQuiescing bool       // scheduler-owned; stops new work while retiring a generation
	rendererSocket    string     // protected by mu
	substrateRC       *rc.Client // scrape substrate: window lifecycle, get-text, injection
	sup               Supervisor
	scrape            Scraper
	inj               Injector
	mods              map[string]module.Module
	snapshots         chan snapshotResult
	natives           chan nativeResult
	input             chan proto.Msg
	inputEpoch        atomic.Uint64
	// repoll asks the scheduler to poll a native widget on its next tick
	// (module-handled row acts must land on glass now, not at the poll
	// cadence). Nil on bare test buses; pokes are best-effort drops.
	repoll chan string
	// execStart is the row-act/action exec seam: start argv, return its
	// waiter. nil runs the real exec.Command; tests stub it to observe (or
	// suppress) process starts.
	execStart func(argv []string) (wait func() error, err error)
	// lastRowAct records the last dispatch per (widget, argv) for the retap
	// debounce (see handleRowAct); touched only on the inputWorker
	// goroutine, so no lock. actNow is its clock seam -- nil means time.Now
	// -- so tests cross the window without sleeping.
	lastRowAct map[string]time.Time
	actNow     func() time.Time
	// effects owns module-effect children (effects.go): keyed debounce,
	// process-group tracking, cancel-on-shutdown. Nil on bare test buses
	// (dispatch drops loudly).
	effects *effectrun.Host
	// readGrace overrides dockReadGrace (tests); zero uses the constant.
	readGrace time.Duration

	recMu   sync.Mutex
	rec     *gesture.Recognizer
	touchOK bool

	// mainKitty tracks the daily kitty RC socket's health (mainkitty.go);
	// own leaf mutex, read by ctl status.
	mainKitty mainKittyHealth

	// caff supervises the background caffeinate (caffeinate.go); own leaf
	// mutex, nil-safe accessors for bare test buses.
	caff *caffeinator
}

// Run starts the bus and blocks until ctx is done or the listener fails.
func Run(ctx context.Context, opts Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cfg, err := loadConfig(opts.ConfigPath)
	if err != nil {
		return err
	}
	// restore the runtime layout selection before the Bus is constructed, so
	// no greeting or broadcast can see the file default first
	adoptLayoutState(cfg, opts.Paths.Dir)

	// every RC target is a scrape window in the substrate instance
	// (forwarded widget taps, config send-key, window lifecycle, get-text).
	// The HUD kitty (kitty-panel.sock) is never dialed here: window ids are
	// per-instance, so sending a substrate id through a HUD client would
	// poke the dock's own PTY -- bubbletea would parse the raw SGR as real
	// clicks.
	substrateRC := rc.New(opts.Paths.KittySocket())
	b := &Bus{
		runCtx: ctx, opts: opts,
		started:     time.Now(),
		cfg:         cfg,
		reg:         NewRegistry(cfg),
		docks:       make(map[net.Conn]*socketio.Writer),
		relayWake:   make(chan struct{}, 1),
		needAdopt:   true,
		substrateRC: substrateRC,
		sup:         NewSupervisor(substrateRC),
		scrape:      NewScraper(substrateRC),
		inj:         NewInjector(substrateRC),
		colors:      newHudColors(opts.Paths.HudKittySocket()),
		lum:         m1ddcLuminance{},
		snapshots:   make(chan snapshotResult, 16),
		natives:     make(chan nativeResult, 16),
		input:       make(chan proto.Msg, 32),
		repoll:      make(chan string, 16),
	}
	b.effects = b.newEffectRunner()
	b.caff = newCaffeinator(cfg.CaffeinateOn(), opts.Paths)
	if opts.RendererBin != "" {
		ownerPath := filepath.Join(opts.Paths.Dir, "renderer-owner.json")
		b.needAdopt = false
		b.renderer = &rendererLifecycle{dir: opts.Paths.Dir, launch: func(socket string) (rendererProcess, error) {
			if err := reapRenderer(ownerPath, opts.Paths.Dir); err != nil {
				return nil, err
			}
			return launchRenderer(opts.RendererBin, socket, ownerPath)
		}, cleanup: func() {
			err := os.Remove(ownerPath)
			if os.IsNotExist(err) {
				err = nil
			}
			diagnostic.Default.Observe("renderer.owner.remove", err, "reverify_before_next_spawn")
		}}
	}
	b.setGrid(0, 0)

	sock := opts.Paths.BusSocket()
	// Keep ownership through worker shutdown, even after the listener closes.
	owner, err := ownerlock.Acquire(sock + ".lock")
	if err != nil {
		return err
	}
	defer owner.Close()
	ln, err := sockclaim.ClaimSocket(sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}
	defer os.Remove(sock)
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("tighten socket: %w", err)
	}
	// Constructors can recover and prune job state; only the socket owner may run them.
	b.mods = registry.All()
	b.openActions()
	if b.journal != nil {
		defer b.journal.Close()
	}
	b.restoreTheme()
	b.recoverActions()
	// boot-time spool sweep: the hookspool reaper otherwise rides only
	// session-end events, so dead or foreign-version spools linger until one
	// fires. One-shot after the socket claim; never per-tick.
	if opts.Paths.Dir != "" {
		hookspool.Sweep(opts.Paths.ClaudeSpool(), time.Now())
	}
	if b.renderer != nil {
		ownerPath := filepath.Join(opts.Paths.Dir, "renderer-owner.json")
		if err := reapRenderer(ownerPath, opts.Paths.Dir); err != nil {
			diagnostic.Default.Observe("renderer.orphan", err, "block_renderer_until_owner_verified")
		}
	}
	if opts.Ready != nil {
		opts.Ready()
	}
	var workers, connections sync.WaitGroup
	workers.Go(func() { b.runRelays(ctx, [3]func(context.Context){b.touchLoop, b.keyLoop, b.logiLoop}) })
	workers.Go(func() { b.mainKittyLoop(ctx, mainKittyProbeInterval) })
	workers.Go(func() { b.inputWorkerContext(ctx) })
	go b.runEffects()
	caffDone := make(chan struct{})
	go func() {
		defer close(caffDone)
		b.caff.run(ctx)
	}()
	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		b.scheduler(ctx)
	}()
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	defer func() {
		cancel()
		ln.Close()
		connections.Wait()
		b.paletteWaiters.Wait()
		workers.Wait()
		b.actionWaiters.Wait()
		<-schedDone
		<-caffDone
		b.effects.Shutdown()
		for name, m := range b.mods {
			if closer, ok := m.(module.Closer); ok {
				if err := closer.Close(); err != nil {
					log.Printf("khudson bus: close module %s: %v", name, err)
				}
			}
		}
	}()

	log.Printf("khudson bus: listening on %s (config: %s)", sock, describeConfig(opts.ConfigPath))
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		connections.Go(func() { b.serve(ctx, conn) })
	}
}

func loadConfig(path string) (*config.Config, error) {
	if path == "" {
		return config.LoadExample()
	}
	return config.LoadFile(path)
}

// layoutStateFileName holds the active layout NAME under the state root
// (the caffeinate.pid pattern), so the runtime selection survives bus
// restarts and config reloads instead of the file default stomping it.
const layoutStateFileName = "layout.state"

// adoptLayoutState restores the persisted layout selection into cfg when the
// config still defines it. An unknown name is ignored and the file LEFT in
// place (a later config may define it again); no state root means no-op.
func adoptLayoutState(cfg *config.Config, dir string) {
	if dir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, layoutStateFileName))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("khudson bus: layout state: %v", err)
		}
		return
	}
	name := strings.TrimSpace(string(data))
	if _, ok := cfg.Layouts[name]; ok {
		cfg.Layout = name
	}
}

// writeLayoutState commits the desired default before publishing it.
func (b *Bus) writeLayoutState(name string) error {
	if b.opts.Paths.Dir == "" {
		return nil
	}
	path := filepath.Join(b.opts.Paths.Dir, layoutStateFileName)
	err := atomicfile.Write(path, []byte(name), 0o600)
	diagnostic.Default.Observe("layout.persist", err, "refuse publication unless desired layout is durable")
	return err
}

func describeConfig(path string) string {
	if path == "" {
		return "embedded example"
	}
	return path
}

// serve handles one khudson.sock connection: hello first, then role-specific
// traffic.
func (b *Bus) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	// shutdown must unpark the Decode below (and the role loops); closing
	// the conn on ctx cancel is the consumeKeys precedent
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	dec := socketio.NewDecoder(conn)
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		diagnostic.Default.Observe("bus.hello.deadline", err, "close connection")
		return
	}

	var hello proto.Msg
	if err := dec.Decode(&hello); err != nil {
		if !errors.Is(err, io.EOF) {
			diagnostic.Default.Observe("bus.hello.read", err, "close connection")
		}
		return
	}
	if hello.Type != proto.TypeHello {
		diagnostic.Default.Observe("bus.hello.protocol", errors.New("expected hello"), "close connection")
		return
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		diagnostic.Default.Observe("bus.hello.deadline", err, "close connection")
		return
	}
	switch hello.Role {
	case proto.RoleDock:
		b.serveDock(conn, dec, hello)
	case proto.RoleCtl:
		b.serveCtl(conn, dec)
	default:
		diagnostic.Default.Observe("bus.hello.role", errors.New("unknown client role"), "close connection")
	}
}

func (b *Bus) serveDock(conn net.Conn, dec interface{ Decode(any) error }, hello proto.Msg) {
	caps, err := negotiateView(hello)
	if err != nil {
		diagnostic.Default.Observe("view.negotiate", err, "reject unsupported protocol")
		_ = conn.SetWriteDeadline(time.Now().Add(dockWriteGrace))
		_ = json.NewEncoder(conn).Encode(proto.Msg{Type: proto.TypeNotice, Error: err.Error()})
		return
	}
	b.mu.Lock()
	if b.dockCaps == nil {
		b.dockCaps = make(map[net.Conn]proto.ViewCapabilities)
	}
	b.dockCaps[conn] = caps
	b.addViewLocked(conn, hello, caps)
	b.mu.Unlock()
	writer := socketio.NewWriter(conn, 128, dockWriteGrace, func(err error) {
		diagnostic.Default.Observe("bus.dock.write", err, "disconnect; resync on new hello; do not replay actions")
	})
	defer func() { writer.Close(); writer.Wait() }()
	if b.claimActive(conn) {
		b.setGrid(hello.Cols, hello.Rows)
		b.setPanel(hello.PanelCols, hello.PanelRows)
	} else {
		// it still renders and still gets state; it just does not get to move
		// the recognizer under the surface the user is touching
		log.Printf("khudson bus: second dock connected (%dx%d cells); singletons stay with the active dock",
			hello.Cols, hello.Rows)
	}

	// registration + greeting share one critical section so a concurrent
	// setLayout cannot land between them and get overwritten by a stale
	// greeting. Greeting replays config, layout, theme, and cached widget
	// state so a reconnecting dock renders immediately (dock re-reports, bus
	// re-asserts). Enqueue under the lock preserves ordering without socket I/O.
	b.mu.Lock()
	b.docks[conn] = writer
	view := b.views[conn]
	b.notifyRelaysLocked()
	b.caff.SetAvailable(b.edgeAvailableLocked())
	// config first: a reconnecting dock's startup config copy is replaced
	// before the TypeLayout below validates against it
	b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeReload, Config: b.cfg, Protocol: proto.Version, Capabilities: &caps})
	b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeLayout, Layout: view.layout})
	theme := b.themeLocked()
	b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeTheme, Theme: theme, Palette: b.palette, ResetPalette: b.themePending && len(b.palette) == 0})
	b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeCaffeinate, Caffeinate: b.caff.wire()})
	if b.lastLogi != nil {
		b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeLogiState, Logi: b.lastLogi})
	}
	if b.lastActFail != nil {
		// replayed regardless of age (the lastLogi convention): the dock's
		// decay window decides whether it still renders
		b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeActFail, ActFail: b.lastActFail})
	}
	for _, id := range b.reg.IDs() {
		st, _ := b.reg.Get(id)
		snap, native, cols, rows, polledAt := st.cached()
		if len(snap) > 0 {
			// an aged frame replays with the stale mark so a reconnecting
			// dock cannot trust a dead widget's screen
			stale := !polledAt.IsZero() && time.Since(polledAt) > 3*st.Widget.Render.PollInterval()
			b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeSnapshot, Widget: id, Cols: cols, Rows: rows, ANSI: string(snap), Stale: stale})
		}
		if len(native) > 0 {
			b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeWidgetData, Widget: id, Data: native})
		}
	}
	b.mu.Unlock()
	log.Printf("khudson bus: dock connected (%dx%d cells)", hello.Cols, hello.Rows)
	// dock adopt is the kitty liveness gate: the dock runs inside the HUD
	// kitty, so its palette is fetchable now (broadcast follows the fetch)
	if caps.Edge {
		b.ensurePalette()
	}

	grace := b.readGrace
	if grace <= 0 {
		grace = dockReadGrace
	}
	why := "disconnected"
	for {
		// read-side liveness: any dock frame (heartbeat included) re-arms
		// the deadline; a dock silent past the grace is reaped instead of
		// lingering connected until a blocked write trips dockWriteGrace
		_ = conn.SetReadDeadline(time.Now().Add(grace))
		var m proto.Msg
		if err := dec.Decode(&m); err != nil {
			diagnostic.Default.Observe("bus.dock.read", err, "remove client; await reconnect")
			if errors.Is(err, os.ErrDeadlineExceeded) {
				why = fmt.Sprintf("silent for %s, reaped", grace)
			}
			break
		}
		diagnostic.Default.Observe("bus.dock.read", nil, "receiving")
		switch m.Type {
		case proto.TypePing:
			// keepalive only; the deadline re-arm above is the effect
		case proto.TypeGrid:
			// a resize claims the free slot: the owner disconnecting must not
			// leave a lone survivor unable to point the recognizer at itself
			if b.claimActive(conn) {
				b.setGrid(m.Cols, m.Rows)
				b.setPanel(m.PanelCols, m.PanelRows)
			}
		case proto.TypeViewState:
			b.updateView(conn, m)
		case proto.TypeCtl:
			if !b.bindViewAction(conn, &m) {
				continue
			}
			if m.Cmd != "layout" && m.Cmd != "caffeinate" {
				b.discardInput(m, "control not available to a view")
				continue
			}
			if m.Cmd == "caffeinate" && !caps.Edge {
				b.discardInput(m, "sleep assertions belong to the Edge surface")
				_ = writer.Send(proto.Msg{Type: proto.TypeNotice, Error: "sleep assertions belong to the Edge surface"})
				continue
			}
			resp := b.executeCtl(m)
			if !resp.OK {
				if m.Cmd == "layout" {
					b.mu.Lock()
					current := ""
					if v := b.views[conn]; v != nil {
						current = v.layout
					}
					b.mu.Unlock()
					b.mu.Lock()
					b.writeDockLocked(conn, writer, proto.Msg{Type: proto.TypeLayout, Layout: current})
					b.mu.Unlock()
				} else {
					b.broadcast(proto.Msg{Type: proto.TypeNotice, Error: resp.Error})
				}
				log.Printf("khudson bus: control %s failed: %s", m.Cmd, resp.Error)
			}

		case proto.TypeForward, proto.TypeAction, proto.TypeRowAct:
			if b.bindViewAction(conn, &m) {
				b.enqueueInput(m)
			}
		}
	}
	b.mu.Lock()
	b.removeDockLocked(conn)
	b.mu.Unlock()
	log.Printf("khudson bus: dock %s", why)
}

// claimActive gives conn the singleton slot when it is free, reporting
// whether conn holds it now. Claiming is FIRST-come, not last: last-hello-wins
// is what let a reconnecting dock repoint the recognizer mid-touch.
func (b *Bus) claimActive(conn net.Conn) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dockCaps != nil && !b.dockCaps[conn].Edge {
		return false
	}
	if b.activeDock == nil {
		b.activeDock = conn
	}
	return b.activeDock == conn
}

// removeDockLocked drops a dock and frees the slot if it held it. Caller
// holds b.mu.
func (b *Bus) removeDockLocked(conn net.Conn) {
	delete(b.docks, conn)
	delete(b.dockCaps, conn)
	delete(b.views, conn)
	if !b.edgeAvailableLocked() {
		b.themePending = b.theme != ""
		if !b.themePending {
			b.palette = nil
		}
	}
	b.notifyRelaysLocked()
	b.caff.SetAvailable(b.edgeAvailableLocked())
	if b.activeDock == conn {
		b.activeDock = nil
	}
}

func (b *Bus) serveCtl(conn net.Conn, dec interface{ Decode(any) error }) {
	enc := json.NewEncoder(conn)
	for {
		if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
			diagnostic.Default.Observe("bus.ctl.deadline", err, "close connection")
			return
		}
		var m proto.Msg
		if err := dec.Decode(&m); err != nil {
			if !errors.Is(err, io.EOF) {
				diagnostic.Default.Observe("bus.ctl.read", err, "close connection")
			}
			return
		}
		if m.Type != proto.TypeCtl {
			continue
		}
		resp := b.executeCtl(m)
		if err := conn.SetWriteDeadline(time.Now().Add(dockWriteGrace)); err != nil {
			diagnostic.Default.Observe("bus.ctl.deadline", err, "close connection")
			return
		}
		if err := enc.Encode(resp); err != nil {
			diagnostic.Default.Observe("bus.ctl.write", err, "close connection; inspect committed outcome")
			return
		}
	}
}

func (b *Bus) handleCtl(m proto.Msg) proto.Msg {
	if m.ViewID != "" {
		if m.Cmd == "layout" {
			err := b.setViewLayout(m)
			if err != nil {
				return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
			}
			return proto.Msg{Type: proto.TypeResp, OK: true}
		}
		b.mu.Lock()
		current := b.viewActionCurrentLocked(m)
		b.mu.Unlock()
		if !current {
			return proto.Msg{Type: proto.TypeResp, Error: "view lease expired"}
		}
	}
	switch m.Cmd {
	case "actions":
		return b.listActions(m.Arg)
	case "discard-action":
		if b.journal == nil {
			return proto.Msg{Type: proto.TypeResp, Error: "action journal unavailable"}
		}
		ctx, cancel := actionContext()
		defer cancel()
		err := b.journal.Transition(ctx, m.Arg, actionjournal.Uncertain, actionjournal.Discarded, "discarded explicitly after review")
		diagnostic.Default.Observe("actions.discard.review", err, "preserve unresolved operation on failure")
		if err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
		}
		b.actionStatus(m.Arg, actionjournal.Discarded, "discarded explicitly after review")
		return proto.Msg{Type: proto.TypeResp, OK: true}
	case "status":
		b.mu.Lock()
		reg := b.reg
		st := proto.Status{
			RendererSocket: b.rendererSocket,
			ConfigPath:     b.opts.ConfigPath,
			Layout:         b.cfg.Layout,
			Widgets:        b.reg.IDs(),
			Docks:          len(b.docks),
			Touch:          "absent",
			Uptime:         time.Since(b.started).Round(time.Second).String(),
		}
		b.mu.Unlock()
		ages := make(map[string]string)
		for _, id := range reg.IDs() {
			ws, _ := reg.Get(id)
			if ws.Widget.Render.Kind != "exec" {
				continue
			}
			if at := ws.polled(); at.IsZero() {
				ages[id] = "never"
			} else {
				ages[id] = time.Since(at).Round(time.Second).String()
			}
		}
		st.SnapshotAges = ages
		b.recMu.Lock()
		if b.touchOK {
			st.Touch = "connected"
		}
		b.recMu.Unlock()
		st.MainKitty = b.mainKitty.State()
		st.Caffeinate = b.caff.State()
		data, err := json.Marshal(st)
		if err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
		}
		return proto.Msg{Type: proto.TypeResp, OK: true, Data: data}

	case "repoll":
		// advisory nudge (the hook poke): due every widget whose module --
		// or exact id -- matches the arg, on the scheduler's next tick.
		if m.Arg == "" {
			return proto.Msg{Type: proto.TypeResp, Error: "repoll: need a widget id or module name"}
		}
		n := b.repollMatching(m.Arg)
		data, err := json.Marshal(map[string]int{"repolled": n})
		if err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
		}
		return proto.Msg{Type: proto.TypeResp, OK: true, Data: data}

	case "reno":
		// The shared Reno owner handles production controls; local providers
		// use the same transition/effect ordering contract.
		h, ok := b.mods["reno"].(renoCtl)
		if !ok {
			return proto.Msg{Type: proto.TypeResp, Error: "reno module not registered"}
		}
		var data any
		var err error
		// Preserve cleanup effects even if the provider also returns an error.
		dispatchErr := b.dispatchTransitionFor(m, "reno", func() []module.Effect {
			var effects []module.Effect
			if owner, ok := h.(interface {
				HandleCtlOperation([]string, module.Operation) (any, []module.Effect, error)
			}); ok {
				data, effects, err = owner.HandleCtlOperation(m.Args, module.Operation{ID: m.OperationID, ViewID: m.ViewID, Generation: m.ViewGeneration})
			} else {
				data, effects, err = h.HandleCtl(m.Args)
			}
			return effects
		})
		err = errors.Join(err, dispatchErr)
		b.repollMatching("reno")
		if err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error(), OperationState: string(actionjournal.ExecutionState(err))}
		}
		resp := proto.Msg{Type: proto.TypeResp, OK: true}
		if data != nil {
			raw, err := json.Marshal(data)
			if err != nil {
				return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
			}
			resp.Data = raw
		}
		return resp

	case "reload":
		cfg, err := loadConfig(b.opts.ConfigPath)
		if err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
		}
		// the scheduler installs it after the in-flight RC calls drain;
		// swapping here would orphan windows an Ensure is still binding
		b.mu.Lock()
		b.pending = cfg
		b.mu.Unlock()
		return proto.Msg{Type: proto.TypeResp, OK: true}

	case "layout":
		if err := b.setLayout(m.Arg); err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error(), OperationState: string(actionjournal.ExecutionState(err))}
		}
		return proto.Msg{Type: proto.TypeResp, OK: true}

	case "theme":
		if m.Arg != "day" && m.Arg != "night" {
			return proto.Msg{Type: proto.TypeResp, Error: fmt.Sprintf("theme %q is not day|night", m.Arg)}
		}
		if err := b.switchTheme(m.Arg); err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error(), OperationState: string(actionjournal.ExecutionState(err))}
		}
		return proto.Msg{Type: proto.TypeResp, OK: true}

	case "caffeinate":
		state, err := b.setCaffeinate(m.Arg)
		if err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
		}
		data, err := json.Marshal(map[string]string{"caffeinate": state})
		if err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
		}
		return proto.Msg{Type: proto.TypeResp, OK: true, Data: data}

	default:
		return proto.Msg{Type: proto.TypeResp, Error: fmt.Sprintf("unknown ctl command %q", m.Cmd)}
	}
}

// renoCtl is the reno module's control seam, matched structurally (the
// essential-marker pattern): `khudson reno load|start|...` lands here
// via handleCtl.
type renoCtl interface {
	HandleCtl(args []string) (data any, effects []module.Effect, err error)
}

// repollMatching pokes every widget whose exact id -- or module name --
// matches arg onto the scheduler's repoll lane. Non-blocking sends: a
// full channel means repolls are already queued, so dropping is correct.
func (b *Bus) repollMatching(arg string) int {
	b.mu.Lock()
	reg := b.reg
	b.mu.Unlock()
	n := 0
	for _, id := range reg.IDs() {
		ws, ok := reg.Get(id)
		if !ok || (id != arg && ws.Widget.Render.Module != arg) {
			continue
		}
		select {
		case b.repoll <- id:
			n++
		default:
		}
	}
	return n
}

// setLayout switches the active layout and broadcasts TypeLayout to every
// dock; the config keeps the scheduler's widget visibility in sync. Unknown
// layouts leave the config untouched. Mutate + broadcast share one critical
// section so interleaved switches cannot reorder on the wire. Every
// successful switch persists the name so a bus restart or
// config reload keeps the runtime selection.
func (b *Bus) setLayout(name string) error {
	b.layoutMu.Lock()
	defer b.layoutMu.Unlock()
	validate := func() error {
		if _, ok := b.cfg.Layouts[name]; !ok {
			return fmt.Errorf("layout %q is not defined", name)
		}
		for conn := range b.docks {
			if b.dockCaps != nil && !supportsLayout(b.cfg, name, b.dockCaps[conn]) {
				return fmt.Errorf("layout %q requires rendering unsupported by a connected view", name)
			}
		}
		return nil
	}
	b.mu.Lock()
	err := validate()
	b.mu.Unlock()
	if err != nil {
		return err
	}
	write := b.layoutWrite
	if write == nil {
		write = b.writeLayoutState
	}
	if err := write(name); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Config or view capabilities may change while storage is busy.
	if err := validate(); err != nil {
		return &reconciliationError{err: err}
	}
	b.cfg.Layout = name
	for _, v := range b.views {
		v.layout = name
		v.generation++
	}
	b.notifyRelaysLocked()
	b.broadcastLocked(proto.Msg{Type: proto.TypeLayout, Layout: name})
	return nil
}

// dockWriteGrace bounds a writer operation. Timeout closes the connection;
// reconnect replays state because a timed-out frame can be incomplete.
const dockWriteGrace = 2 * time.Second

// dockReadGrace bounds dock-side silence: the dock heartbeats every
// proto.HeartbeatEvery, so 3x lets two lost or late beats slide and reaps
// on the third. Write-side eviction alone only fires when a blocked write
// trips dockWriteGrace -- a connected-but-mute dock that kept reading held
// its fan-out slot forever.
const dockReadGrace = 3 * proto.HeartbeatEvery

func (b *Bus) broadcast(m proto.Msg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.broadcastLocked(m)
}

// broadcastLocked fans m out to every dock; the caller holds b.mu.
func (b *Bus) broadcastLocked(m proto.Msg) {
	for conn, enc := range b.docks {
		b.writeDockLocked(conn, enc, m)
	}
}

// sendActive delivers m to the dock holding the singleton slot, and to no
// other. Gestures resolve against the ACTIVE dock's grid, so a cell that
// means one thing there means something else on a dock with another layout
// -- fanning them out fires phantom taps on every surface but the one under
// the finger.
func (b *Bus) sendActive(m proto.Msg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	conn := b.activeDock
	if v := b.views[conn]; v != nil && v.hidden {
		return
	}
	if conn == nil {
		return
	}
	if enc, ok := b.docks[conn]; ok {
		b.writeDockLocked(conn, enc, m)
	}
}

// writeDockLocked enqueues one immutable message. Caller holds b.mu.
func (b *Bus) writeDockLocked(conn net.Conn, writer *socketio.Writer, m proto.Msg) {
	if v := b.views[conn]; v != nil {
		m.ViewID, m.ViewGeneration = v.id, v.generation
		if m.Type == proto.TypeViewState {
			m.Hidden = v.hidden
		}
		if m.Type == proto.TypeReload {
			m.Layout = v.layout
		}
	}
	if err := writer.Send(m); err != nil {
		b.removeDockLocked(conn)
	}
}

// setPanel records the dock's active-panel content region; the scheduler
// sizes scraped windows to it. Zero values (older docks, startup) keep the
// last known region.
func (b *Bus) setPanel(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		return
	}
	b.mu.Lock()
	b.panelCols, b.panelRows = cols, rows
	b.mu.Unlock()
}

// setGrid rebuilds the recognizer for a new dock cell grid; zero cols/rows
// installs a placeholder grid until the dock reports in.
func (b *Bus) setGrid(cols, rows int) {
	if cols <= 0 || rows <= 0 {
		cols, rows = 320, 18
	}
	cal := gesture.DefaultCalibration
	cells := gesture.CellMetrics{Cols: cols, Rows: rows, PanelW: cal.PanelW, PanelH: cal.PanelH}
	b.recMu.Lock()
	b.rec = gesture.New(cal, cells, gesture.Config{})
	b.recMu.Unlock()
}

// touchLoop dials touchd's socket, feeds frames through the recognizer,
// and broadcasts gestures to docks. Reconnects forever; touch-dead glass
// is loud, not silent (status reports touch: absent).
func (b *Bus) touchLoop(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", b.opts.Paths.TouchSocket())
		diagnostic.Default.Observe("touch.dial", err, "retry after backoff while Edge view exists")
		if err != nil {
			b.setTouchOK(false)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		b.setTouchOK(true)
		log.Printf("khudson bus: touchd connected")
		b.consumeFrames(ctx, conn)
		conn.Close()
		b.setTouchOK(false)
		log.Printf("khudson bus: touchd connection lost")
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (b *Bus) setTouchOK(ok bool) {
	b.recMu.Lock()
	b.touchOK = ok
	b.recMu.Unlock()
}

// touch liveness. touchd beats every 5s while it holds an Edge collection
// open, so three missed beats means dead glass, not an idle finger -- the
// distinction a connected-but-silent socket cannot make on its own, and the
// reason `ctl status` used to report working touch on a frozen panel. Vars:
// tests shrink them.
var (
	touchStaleAfter = 16 * time.Second
	touchCheckEvery = 2 * time.Second
)

// consumeFrames decodes ndjson TouchFrames and runs the recognizer loop.
// Frames pass through a bounded drop-oldest channel so a burst
// never blocks the socket reader; drops cost flick precision, not
// correctness.
func (b *Bus) consumeFrames(ctx context.Context, conn net.Conn) {
	frames := make(chan gesture.Frame, 64)
	done := make(chan struct{})
	// last line of ANY kind: a heartbeat proves the source as well as a
	// report does, and the reader is the only writer
	var lastLine atomic.Int64
	lastLine.Store(time.Now().UnixNano())

	go func() {
		defer close(done)
		dec := socketio.NewDecoder(conn)
		for {
			var tf proto.TouchFrame
			if err := dec.Decode(&tf); err != nil {
				if ctx.Err() == nil {
					diagnostic.Default.Observe("touch.read", err, "discard incomplete gesture; reconnect after backoff")
				}
				return
			}
			diagnostic.Default.Observe("touch.read", nil, "receiving")
			lastLine.Store(time.Now().UnixNano())
			if tf.HB {
				continue // liveness only: never a recognizer input
			}
			f := toFrame(tf)
			select {
			case frames <- f:
			default:
				// full: drop the oldest, then try once more
				select {
				case <-frames:
				default:
				}
				select {
				case frames <- f:
				default:
				}
			}
		}
	}()

	defer func() { conn.Close(); <-done; b.recMu.Lock(); b.rec.Cancel(); b.recMu.Unlock() }()

	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
	}
	defer stopTimer()

	// the connection stays up across a stale spell: touchd may be holding a
	// device that went quiet, and the beat resuming is what un-flips the
	// status. Only a decode error tears the connection down.
	live := time.NewTicker(touchCheckEvery)
	defer live.Stop()

	for {
		var tick <-chan time.Time
		b.recMu.Lock()
		deadline, armed := b.rec.Deadline()
		b.recMu.Unlock()
		if armed {
			stopTimer()
			timer = time.NewTimer(time.Until(deadline))
			tick = timer.C
		}

		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case f := <-frames:
			stopTimer()
			b.setTouchOK(true)
			b.recMu.Lock()
			evs := b.rec.Frame(f)
			b.recMu.Unlock()
			b.emit(evs)
		case now := <-tick:
			timer = nil
			b.recMu.Lock()
			evs := b.rec.Tick(now)
			b.recMu.Unlock()
			b.emit(evs)
		case now := <-live.C:
			b.setTouchOK(!touchStale(time.Unix(0, lastLine.Load()), now))
		}
	}
}

// touchStale is the pure liveness decision: no line from touchd within the
// window. It is time-since-last-LINE, not last gesture -- an hour of nobody
// touching the glass is healthy, a minute of no heartbeat is not.
func touchStale(last, now time.Time) bool {
	return now.Sub(last) > touchStaleAfter
}

func toFrame(tf proto.TouchFrame) gesture.Frame {
	f := gesture.Frame{
		Time: time.Unix(0, tf.TimeNS),
	}
	if tf.TimeNS == 0 {
		f.Time = time.Now()
	}
	for _, c := range tf.Contacts {
		f.Contacts = append(f.Contacts, gesture.Contact{ID: c.ID, Tip: c.Tip, X: c.X, Y: c.Y})
	}
	return f
}

func (b *Bus) emit(evs []gesture.Event) {
	for _, ev := range evs {
		g := toGesture(ev)
		if g == nil {
			continue
		}
		b.sendActive(proto.Msg{Type: proto.TypeGesture, Gesture: g})
	}
}

func toGesture(ev gesture.Event) *proto.Gesture {
	switch e := ev.(type) {
	case gesture.Press:
		return &proto.Gesture{Kind: proto.GesturePress, Col: e.Pos.Col, Row: e.Pos.Row, PX: e.Pos.PX, PY: e.Pos.PY}
	case gesture.Tap:
		return &proto.Gesture{Kind: proto.GestureTap, Col: e.Pos.Col, Row: e.Pos.Row, PX: e.Pos.PX, PY: e.Pos.PY}
	case gesture.LongPress:
		return &proto.Gesture{Kind: proto.GestureLongPress, Col: e.Pos.Col, Row: e.Pos.Row, PX: e.Pos.PX, PY: e.Pos.PY}
	case gesture.DragStart:
		return &proto.Gesture{Kind: proto.GestureDragStart, Col: e.Start.Col, Row: e.Start.Row, PX: e.Start.PX, PY: e.Start.PY}
	case gesture.DragMove:
		return &proto.Gesture{
			Kind: proto.GestureDragMove,
			Col:  e.Pos.Col, Row: e.Pos.Row, PX: e.Pos.PX, PY: e.Pos.PY,
			StartCol: e.Start.Col, StartRow: e.Start.Row,
			DX: e.DX, DY: e.DY,
		}
	case gesture.DragEnd:
		return &proto.Gesture{
			Kind: proto.GestureDragEnd,
			Col:  e.Pos.Col, Row: e.Pos.Row, PX: e.Pos.PX, PY: e.Pos.PY,
			StartCol: e.Start.Col, StartRow: e.Start.Row,
		}
	case gesture.Swipe:
		return &proto.Gesture{
			Kind: proto.GestureSwipe,
			Col:  e.End.Col, Row: e.End.Row, PX: e.End.PX, PY: e.End.PY,
			StartCol: e.Start.Col, StartRow: e.Start.Row,
			Dir: string(e.Dir), Cells: e.Cells,
		}
	case gesture.TwoFingerSwipe:
		return &proto.Gesture{
			Kind: proto.GestureTwoFingerSwipe,
			Col:  e.End.Col, Row: e.End.Row, PX: e.End.PX, PY: e.End.PY,
			StartCol: e.Start.Col, StartRow: e.Start.Row,
			Dir: string(e.Dir), Cells: e.Cells,
		}
	case gesture.Wheel:
		return &proto.Gesture{
			Kind: proto.GestureWheel,
			Col:  e.Pos.Col, Row: e.Pos.Row, PX: e.Pos.PX, PY: e.Pos.PY,
			DX: e.DeltaCols, DY: e.DeltaRows,
		}
	}
	return nil
}
