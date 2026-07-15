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
	"io/fs"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/gesture"
	"github.com/shimmerjs/khudson/khudson/internal/hookspool"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/module/registry"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
	"github.com/shimmerjs/khudson/khudson/internal/sockclaim"
)

// Options configures Run.
type Options struct {
	ConfigPath string // empty = embedded example
	Paths      paths.Paths
	Ready      func() // called once after the bus owns its socket; nil ok
}

// Bus is the daemon state.
type Bus struct {
	opts    Options
	started time.Time

	mu        sync.Mutex
	cfg       *config.Config
	reg       *Registry
	docks     map[net.Conn]*json.Encoder
	needAdopt bool
	// pending is a reloaded config awaiting a quiesced scheduler tick; the
	// scheduler swaps it in once no single-flight RC call is in flight.
	pending *config.Config
	theme   string // "" = day
	// palette is the HUD kitty's effective colors (get-colors), fetched at
	// dock adopt and re-fetched on every theme switch; nil until the first
	// fetch lands. fetchingPalette keeps the adopt fetch single-flight.
	palette         map[string]string
	fetchingPalette bool
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

	substrateRC *rc.Client // scrape substrate: window lifecycle, get-text, injection
	sup         Supervisor
	scrape      Scraper
	inj         Injector
	mods        map[string]module.Module
	snapshots   chan snapshotResult
	natives     chan nativeResult
	input       chan proto.Msg
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
		opts:        opts,
		started:     time.Now(),
		cfg:         cfg,
		reg:         NewRegistry(cfg),
		docks:       make(map[net.Conn]*json.Encoder),
		needAdopt:   true,
		substrateRC: substrateRC,
		sup:         NewSupervisor(substrateRC),
		scrape:      NewScraper(substrateRC),
		inj:         NewInjector(substrateRC),
		colors:      newHudColors(opts.Paths.HudKittySocket()),
		lum:         m1ddcLuminance{},
		mods:        registry.All(),
		snapshots:   make(chan snapshotResult, 16),
		natives:     make(chan nativeResult, 16),
		input:       make(chan proto.Msg, 32),
		repoll:      make(chan string, 16),
	}
	b.caff = newCaffeinator(cfg.CaffeinateOn(), opts.Paths)
	b.setGrid(0, 0)

	sock := opts.Paths.BusSocket()
	// probe-then-claim: a second bus must refuse a live socket instead of
	// stealing it; only a dead file is removed
	ln, err := sockclaim.ClaimSocket(sock)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sock, err)
	}
	defer os.Remove(sock)
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("tighten socket: %w", err)
	}
	// boot-time spool sweep: the hookspool reaper otherwise rides only
	// session-end events, so dead or foreign-version spools linger until one
	// fires. One-shot after the socket claim; never per-tick.
	if opts.Paths.Dir != "" {
		hookspool.Sweep(opts.Paths.ClaudeSpool(), time.Now())
	}
	if opts.Ready != nil {
		opts.Ready()
	}

	go b.touchLoop(ctx)
	go b.keyLoop(ctx)
	go b.logiLoop(ctx)
	go b.mainKittyLoop(ctx, mainKittyProbeInterval)
	go b.inputWorker()
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

	log.Printf("khudson bus: listening on %s (config: %s)", sock, describeConfig(opts.ConfigPath))
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				// clean shutdown: wait for the scheduler to release the
				// scrape windows and the caffeinate supervisor to kill its
				// child before reporting done
				<-schedDone
				<-caffDone
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		go b.serve(ctx, conn)
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

// writeLayoutState persists the active layout name, best-effort like the
// caffeinate pidfile: a write failure logs and never fails the switch. No-op
// without a state root (bare test buses).
func (b *Bus) writeLayoutState(name string) {
	if b.opts.Paths.Dir == "" {
		return
	}
	path := filepath.Join(b.opts.Paths.Dir, layoutStateFileName)
	if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
		log.Printf("khudson bus: layout state: %v", err)
	}
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
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var hello proto.Msg
	if err := dec.Decode(&hello); err != nil || hello.Type != proto.TypeHello {
		return
	}
	switch hello.Role {
	case proto.RoleDock:
		b.serveDock(conn, enc, dec, hello)
	case proto.RoleCtl:
		b.serveCtl(enc, dec)
	}
}

func (b *Bus) serveDock(conn net.Conn, enc *json.Encoder, dec *json.Decoder, hello proto.Msg) {
	b.setGrid(hello.Cols, hello.Rows)
	b.setPanel(hello.PanelCols, hello.PanelRows)

	// registration + greeting share one critical section so a concurrent
	// setLayout cannot land between them and get overwritten by a stale
	// greeting. Greeting replays config, layout, theme, and cached widget
	// state so a reconnecting dock renders immediately (dock re-reports, bus
	// re-asserts); deadline-bounded like broadcast.
	b.mu.Lock()
	b.docks[conn] = enc
	_ = conn.SetWriteDeadline(time.Now().Add(dockWriteGrace))
	// config first: a reconnecting dock's startup config copy is replaced
	// before the TypeLayout below validates against it
	_ = enc.Encode(proto.Msg{Type: proto.TypeReload, Config: b.cfg})
	_ = enc.Encode(proto.Msg{Type: proto.TypeLayout, Layout: b.cfg.Layout})
	theme := b.themeLocked()
	_ = enc.Encode(proto.Msg{Type: proto.TypeTheme, Theme: theme, Palette: b.palette})
	_ = enc.Encode(proto.Msg{Type: proto.TypeCaffeinate, Caffeinate: b.caff.wire()})
	if b.lastLogi != nil {
		_ = enc.Encode(proto.Msg{Type: proto.TypeLogiState, Logi: b.lastLogi})
	}
	if b.lastActFail != nil {
		// replayed regardless of age (the lastLogi convention): the dock's
		// decay window decides whether it still renders
		_ = enc.Encode(proto.Msg{Type: proto.TypeActFail, ActFail: b.lastActFail})
	}
	for _, id := range b.reg.IDs() {
		st, _ := b.reg.Get(id)
		snap, native, cols, rows, polledAt := st.cached()
		if len(snap) > 0 {
			// an aged frame replays with the stale mark so a reconnecting
			// dock cannot trust a dead widget's screen
			stale := !polledAt.IsZero() && time.Since(polledAt) > 3*st.Widget.Render.PollInterval()
			_ = enc.Encode(proto.Msg{Type: proto.TypeSnapshot, Widget: id, Cols: cols, Rows: rows, ANSI: string(snap), Stale: stale})
		}
		if len(native) > 0 {
			_ = enc.Encode(proto.Msg{Type: proto.TypeWidgetData, Widget: id, Data: native})
		}
	}
	_ = conn.SetWriteDeadline(time.Time{})
	b.mu.Unlock()
	log.Printf("khudson bus: dock connected (%dx%d cells)", hello.Cols, hello.Rows)
	// dock adopt is the kitty liveness gate: the dock runs inside the HUD
	// kitty, so its palette is fetchable now (broadcast follows the fetch)
	b.ensurePalette()

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
			if errors.Is(err, os.ErrDeadlineExceeded) {
				why = fmt.Sprintf("silent for %s, reaped", grace)
			}
			break
		}
		switch m.Type {
		case proto.TypePing:
			// keepalive only; the deadline re-arm above is the effect
		case proto.TypeGrid:
			b.setGrid(m.Cols, m.Rows)
			b.setPanel(m.PanelCols, m.PanelRows)
		case proto.TypeCtl:
			// dock-initiated verbs get no resp; the state broadcast is the ack
			switch m.Cmd {
			case "layout":
				// nav (tray, brand): TypeLayout is the ack
				if err := b.setLayout(m.Arg); err != nil {
					// config skew is loud -- re-assert the current
					// layout so a desynced dock snaps back
					log.Printf("khudson bus: dock layout nav: %v", err)
					b.mu.Lock()
					cur := b.cfg.Layout
					b.mu.Unlock()
					b.broadcast(proto.Msg{Type: proto.TypeLayout, Layout: cur})
				}
			case "caffeinate":
				// cup tap: TypeCaffeinate is the ack
				if _, err := b.setCaffeinate(m.Arg); err != nil {
					log.Printf("khudson bus: dock caffeinate: %v", err)
				}
			}
		case proto.TypeForward, proto.TypeAction, proto.TypeRowAct:
			b.enqueueInput(m)
		}
	}
	b.mu.Lock()
	delete(b.docks, conn)
	b.mu.Unlock()
	log.Printf("khudson bus: dock %s", why)
}

func (b *Bus) serveCtl(enc *json.Encoder, dec *json.Decoder) {
	for {
		var m proto.Msg
		if err := dec.Decode(&m); err != nil {
			return
		}
		if m.Type != proto.TypeCtl {
			continue
		}
		resp := b.handleCtl(m)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (b *Bus) handleCtl(m proto.Msg) proto.Msg {
	switch m.Cmd {
	case "status":
		b.mu.Lock()
		reg := b.reg
		st := proto.Status{
			ConfigPath: b.opts.ConfigPath,
			Layout:     b.cfg.Layout,
			Widgets:    b.reg.IDs(),
			Docks:      len(b.docks),
			Touch:      "absent",
			Uptime:     time.Since(b.started).Round(time.Second).String(),
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
			return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
		}
		return proto.Msg{Type: proto.TypeResp, OK: true}

	case "theme":
		if m.Arg != "day" && m.Arg != "night" {
			return proto.Msg{Type: proto.TypeResp, Error: fmt.Sprintf("theme %q is not day|night", m.Arg)}
		}
		if err := b.switchTheme(m.Arg); err != nil {
			return proto.Msg{Type: proto.TypeResp, Error: err.Error()}
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

// setLayout switches the active layout and broadcasts TypeLayout to every
// dock; the config keeps the scheduler's widget visibility in sync. Unknown
// layouts leave the config untouched. Mutate + broadcast share one critical
// section so interleaved switches cannot reorder on the wire. Every
// successful switch persists the name (best-effort) so a bus restart or
// config reload keeps the runtime selection.
func (b *Bus) setLayout(name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.cfg.Layouts[name]; !ok {
		return fmt.Errorf("layout %q is not defined", name)
	}
	b.cfg.Layout = name
	b.writeLayoutState(name)
	b.broadcastLocked(proto.Msg{Type: proto.TypeLayout, Layout: name})
	return nil
}

// dockWriteGrace bounds one dock write. Writes happen under b.mu, and
// snapshots are whole ANSI screens: a peer that stops reading fills the
// socket buffer and would otherwise wedge every b.mu holder (scheduler, ctl,
// touch fan-out) behind one blocked Encode. On deadline the dock is evicted;
// its stream is mid-message garbage anyway.
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
		_ = conn.SetWriteDeadline(time.Now().Add(dockWriteGrace))
		if err := enc.Encode(m); err != nil {
			log.Printf("khudson bus: dock write failed, evicting: %v", err)
			conn.Close()
			delete(b.docks, conn)
			continue
		}
		_ = conn.SetWriteDeadline(time.Time{})
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
		conn, err := net.Dial("unix", b.opts.Paths.TouchSocket())
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
	}
}

func (b *Bus) setTouchOK(ok bool) {
	b.recMu.Lock()
	b.touchOK = ok
	b.recMu.Unlock()
}

// consumeFrames decodes ndjson TouchFrames and runs the recognizer loop.
// Frames pass through a bounded drop-oldest channel so a burst
// never blocks the socket reader; drops cost flick precision, not
// correctness.
func (b *Bus) consumeFrames(ctx context.Context, conn net.Conn) {
	frames := make(chan gesture.Frame, 64)
	done := make(chan struct{})

	go func() {
		defer close(done)
		dec := json.NewDecoder(conn)
		for {
			var tf proto.TouchFrame
			if err := dec.Decode(&tf); err != nil {
				return
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

	var timer *time.Timer
	stopTimer := func() {
		if timer != nil {
			timer.Stop()
			timer = nil
		}
	}
	defer stopTimer()

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
		}
	}
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
		b.broadcast(proto.Msg{Type: proto.TypeGesture, Gesture: g})
	}
}

func toGesture(ev gesture.Event) *proto.Gesture {
	switch e := ev.(type) {
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
