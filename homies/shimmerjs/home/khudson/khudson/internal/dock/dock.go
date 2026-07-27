// Package dock is the pane of glass: one fullscreen bubbletea program that
// owns every pixel on the Edge. Tiles come from the vetted config; gestures
// arrive as ndjson from the bus, with kitty mouse events as the fallback
// when the bus is absent.
//
// Launch on the Edge:
//
//	kitten panel --detach --edge=center --output-name "XENEON EDGE" khudson dock
package dock

import (
	"encoding/json"
	"fmt"
	"image/color"
	"maps"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/kbview"
	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// Options configures Run.
type Options struct {
	ConfigPath string // empty = embedded example
	BusSocket  string
}

// Run loads the config and runs the dock program.
func Run(opts Options) error {
	var cfg *config.Config
	var err error
	if opts.ConfigPath == "" {
		cfg, err = config.LoadExample()
	} else {
		cfg, err = config.LoadFile(opts.ConfigPath)
	}
	if err != nil {
		return err
	}
	m := &model{
		opts:        opts,
		cfg:         cfg,
		layout:      cfg.Layout,
		now:         time.Now(),
		widgetData:  make(map[string]module.Data),
		widgetErr:   make(map[string]string),
		widgetStale: make(map[string]bool),
		sty:         buildStyles(day),
		openURL:     openWithMacOS,
	}
	_, err = tea.NewProgram(m).Run()
	return err
}

type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// grow expands a rect by dx/dy on every side, clamped at the glass origin.
// Used for near-miss gesture matching, never for painting.
func (r rect) grow(dx, dy int) rect {
	x, y := max(r.x-dx, 0), max(r.y-dy, 0)
	return rect{x, y, r.w + dx + (r.x - x), r.h + dy + (r.y - y)}
}

// hitRegion is one tappable rect; do runs with the tap's absolute cell
// coords. The first containing rect in the table wins, and a match consumes
// the tap even when its action no-ops.
type hitRegion struct {
	area rect
	do   func(x, y int)
	// longPress, when non-nil, opens the region's context menu anchored at
	// the press cell (the hitRegion bridge until the gesture keystone lands;
	// resolveLongPress skips regions without one).
	longPress func(x, y int)
	// pressKey, when set, is the flash key this element lights while a touch
	// press is held on it: flashLive ORs the live press in, so any renderer
	// with tap-flash treatment acknowledges contact for free.
	pressKey string
	// weldTile marks a rail-tile hit whose menu welds to the tile's border;
	// unmarked regions (whole-widget rows) keep plain press-cell anchoring
	// even when their rect happens to be tile-shaped.
	weldTile bool
}

// regionSwipe is one home region's vertical-swipe binding at its rect on
// glass: the swipe table's entry, the hitRegion sibling for gestures that
// resolve by where the stroke STARTED rather than where it landed.
type regionSwipe struct {
	area     rect
	up, down string
}

// target is the layout this binding navigates to for dir, "" when the
// region leaves that direction to the layout.
func (rs regionSwipe) target(dir string) string {
	if dir == "down" {
		return rs.down
	}
	return rs.up
}

// pressState is the touch currently held on a press-keyed element; nil
// when no press is live. Cleared by the resolving gesture, backstopped by
// pressHoldMax (a menu-less held release is silent recognizer-side).
type pressState struct {
	key string
	at  time.Time
}

// pressHoldMax bounds a press light: the recognizer long-press threshold
// plus margin, so the light survives the whole hold window but never
// sticks past an unresolved release.
const pressHoldMax = 700 * time.Millisecond

// pressAt lights the press-keyed hit region containing the touch. The
// modal gate applies: while a menu is open the base layer must not light.
func (m *model) pressAt(x, y int) {
	if m.overlay != nil {
		return
	}
	for _, h := range m.hits {
		if h.pressKey != "" && h.area.contains(x, y) {
			m.now = time.Now()
			m.pressed = &pressState{key: h.pressKey, at: m.now}
			m.homeCache.ok = false
			return
		}
	}
}

// resolveTap runs the first hit region containing (x, y); reports whether it
// consumed the tap. An open overlay owns EVERY tap first -- the modal gate
// sits here so both the mouse-fallback and the gesture path route through
// it, and the base hits (still rebuilt each frame) stay unreachable until
// the overlay closes.
func (m *model) resolveTap(x, y int) bool {
	if m.overlay != nil {
		m.overlayTap(x, y)
		return true
	}
	for _, h := range m.hits {
		if h.area.contains(x, y) {
			h.do(x, y)
			return true
		}
	}
	return false
}

// resolveLongPress runs the first hit region containing (x, y) that carries
// a longPress opener; reports whether one fired. A press while a menu is
// already open closes it first (dropping any pending confirm), so a second
// long-press reopens anchored at the new press.
func (m *model) resolveLongPress(x, y int) bool {
	m.overlay = nil
	for _, h := range m.hits {
		if h.longPress != nil && h.area.contains(x, y) {
			// stash the element's key and rect first: the fired flash lands
			// on the menu's origin element when an item execs, and the
			// opener welds the box to a weldTile origin rect
			m.overlayOriginKey = h.pressKey
			m.overlayOriginTile = rect{}
			if h.weldTile {
				m.overlayOriginTile = h.area
			}
			h.longPress(x, y)
			return true
		}
	}
	return false
}

// resetHits starts fresh frame tables (taps and region swipes); every layout
// renderer calls it before appending its own targets, so the tables always
// match the frame on glass.
func (m *model) resetHits() {
	m.hits = m.hits[:0]
	m.regionSwipes = m.regionSwipes[:0]
}

// consumeTap is the no-op action: the region owns the tap, nothing fires.
func consumeTap(int, int) {}

type busState int

const (
	busAbsent busState = iota // mouse fallback active
	busConnected
)

// Bus dial messages carry the generation of the connectBus cmd that spawned
// them (model.busGen at dial time): a stale dial's connect must not displace
// a newer conn without closing, and a stale reader's busGoneMsg must not
// tear down the healthy replacement.
type (
	tickMsg         time.Time
	busConnectedMsg struct {
		gen  int
		conn net.Conn
		ch   chan proto.Msg
	}
	busGoneMsg struct {
		gen int
		err error
	}
	busEventMsg struct {
		gen int
		msg proto.Msg
	}
	retryMsg struct{}
	// flashTickMsg is the one-shot redraw at a tap flash's expiry; it
	// advances m.now so the expired flash drops off the next frame.
	flashTickMsg time.Time
	// bloomTickMsg is the deferred resources-bloom open at the double-tap
	// debounce deadline.
	bloomTickMsg time.Time
	// xitionTickMsg advances the swipe wash one frame; the chain re-arms
	// itself until the wash's last frame, then stops. gen is the wash it
	// belongs to -- a retargeted wash drops its predecessor's ticks.
	xitionTickMsg struct {
		gen int
		at  time.Time
	}
)

type model struct {
	opts   Options
	cfg    *config.Config
	layout string

	width, height int
	taps          int
	now           time.Time

	bus     busState
	busConn net.Conn
	busCh   chan proto.Msg
	// busGen is the live dial generation; connect/gone/event messages from
	// an older connectBus cmd (stale gen) are dropped, never acted on
	busGen  int
	lastGst string
	// lastPing is the previous keepalive's clock reading; sendHeartbeat
	// paces itself against it off the 1 s tick
	lastPing time.Time

	theme string // theme name as broadcast ("day"/"night"); labels only
	sty   styles
	// palette is the HUD kitty's effective colors as broadcast by the bus
	// (TypeTheme). sty and rows derive from it; nil = indexed-ANSI defaults.
	palette palette
	// rows is the derived row vocabulary, memoized until the next theme
	// broadcast (rowsOK=false forces a re-derive on the next rowStyles call)
	rows   rowStyles
	rowsOK bool

	// caffeinate is the bus supervisor's state as broadcast (TypeCaffeinate,
	// "on"|"off"); "" until the first broadcast (the greeting replays it, so
	// only a bus-absent dock stays unknown -- the cup renders that as off).
	caffeinate string

	// logi is the latest MX-device battery frame as broadcast (TypeLogiState);
	// nil until the first frame lands. The strip battery cell reads it at
	// render, dimming once it ages past the staleness horizon.
	logi *proto.LogiState

	// actFail is the bus's latest failed act/verb as broadcast (TypeActFail);
	// nil until one lands. The strip warn cell reads it at render and drops
	// off entirely once it ages past actFailFor.
	actFail *proto.ActFail

	// skew marks a bus TypeLayout naming a layout this config lacks; the
	// next successful switch clears it. Chrome (the strip cup) renders it
	// as a warn state.
	skew bool

	// last module view model / poll error per native widget id
	widgetData map[string]module.Data
	widgetErr  map[string]string
	// exec widgets the bus marked stale (TypeSnapshot Stale); the titled
	// region renders a dim stale suffix until a fresh snapshot clears it
	widgetStale map[string]bool

	// hits is the tap hit table: the active layout's targets, rebuilt from
	// scratch by the layout renderer (via resetHits); the first containing
	// rect wins
	hits []hitRegion
	// regionSwipes is the vertical-swipe table: one entry per placed region
	// that binds a direction, rebuilt beside hits. A swipe resolves against
	// the region its stroke STARTED in; an empty table (no bindings, or a
	// swipe landing before the first frame) falls back to the layout target
	regionSwipes []regionSwipe
	// pressed is the touch currently held on a press-keyed element (touch
	// acknowledgment); nil = none. overlayOriginKey is the pressKey of the
	// element whose long-press opened the current menu -- the fired flash
	// lands there when an item execs. overlayOriginTile is that element's
	// hit rect; openOverlay welds the box to tile-shaped origins and zeroes
	// it, so direct openOverlay callers keep plain touch-cell anchoring.
	pressed           *pressState
	overlayOriginKey  string
	overlayOriginTile rect
	// overlay is the modal long-press popover; nil = closed. While set,
	// resolveTap routes every tap through it (base hits unreachable) and
	// View composites overlay.box over the frame on the P0-pinned Canvas
	// path; the closed path never touches the canvas.
	overlay *overlayState
	// trayFlash holds "soon" stubs keyed by entry label; trayCache memoizes
	// parsed tray entries per widget until the config/layout changes
	trayFlash map[string]time.Time
	trayCache map[string][]trayEntry
	// flashArmed marks a tap flash recorded during the current Update
	// dispatch; Update drains it into a one-shot tapFlashFor tick so the
	// 250 ms flash clears without waiting for the 1 s clock
	flashArmed bool

	// homeCache memoizes the composed home body + hit table between frames;
	// resizes, layout switches, visible-widget updates, and tray flash
	// writes invalidate it, and unexpired flashes bypass it (the "soon"
	// label is clock-driven)
	homeCache homeCache

	// keyboard view state: the static Moonlander board, the shown layer
	// index, and the load error/empty message. kbLoader resolves the board
	// off the USB serial (TTL-bounded) with the local caches behind it, so
	// a flash is adopted without a dock restart.
	kbBoard  *keyboard.Board
	kbErr    string
	kbLayer  int
	kbLoader *keyboard.Loader

	// resPending is the resources card's armed-but-not-open bloom: the
	// first tap records it, bloomTick opens it after the debounce, a fast
	// second tap converts to the monitor layout instead (no bloom
	// flicker). resPendingArmed is drained by Update into the tick, the
	// flashArmed idiom.
	resPending      *pendingBloom
	resPendingArmed bool

	// xition is the live swipe wash (nil = none); xitionArmed is drained by
	// Update into the wash's tick chain, the flashArmed idiom. xitionGen is
	// the live chain's generation: a retargeting swipe bumps it and the
	// older chain's ticks are dropped (the busGen convention)
	xition      *xition
	xitionArmed bool
	xitionGen   int

	// openURL hands a URL to the OS (default /usr/bin/open); nil in bare
	// test models so a stray tap in a test never spawns a browser
	openURL func(string)
}

// rowStyles is the render-time row vocabulary: derived from the broadcast
// palette, memoized until the next theme broadcast. A model that never saw
// a broadcast (or a bare test model) derives the indexed-ANSI defaults.
func (m *model) rowStyles() rowStyles {
	if !m.rowsOK {
		m.rows = newRowStyles(m.palette)
		m.rowsOK = true
	}
	return m.rows
}

// resetLayout drops the per-layout caches on a layout switch: the tray memo,
// the composed home body, and the stale hit table (a tap before the new
// layout's renderer rebuilds it must fire nothing).
func (m *model) resetLayout() {
	m.trayCache = nil
	m.homeCache.ok = false
	m.hits = m.hits[:0]
	// an open menu (or an armed confirm) must not float over a layout it
	// was not anchored in; same for an armed-but-unopened resources bloom
	m.overlay = nil
	m.resPending = nil
	m.resPendingArmed = false
}

// stripH is the status strip under the body: ONE band row -- the body runs
// down to touch the band, and the active notch opens straight into the
// panel above (a bare spacer row read as a gap on glass). Touch rides the
// elements' width; a 30px-tall row is the accepted target height.
// Strip-chrome geometry, not a region size.
const stripH = 1

// stripIconW is one strip icon's width in cells: a nerd-font glyph with a
// space either side. Bigger-than-a-cell icons are a dead end twice over:
// OSC 66 scaled runs die at the compositor (ultraviolet forwards only SGR
// and OSC 8 -- TestStripSurvivesCompositor pins that class), and 4x2
// quadrant block art has ~8x4 pixel resolution, which read as blobs on
// glass. One crisp designed glyph beats both.
const stripIconW = 3

// homeGlyph is the chrome-owned home icon on the strip; not a config
// entry -- homeTap resolves its target by layout kind. md-home-variant:
// the plain md-home read as an up arrow at strip size.
const homeGlyph = "\U000F02DD"

// batteryCellW is the strip battery readout's fixed width in cells: the
// mouse marker, a nerd-font battery glyph, and the integer pct, budgeted so
// the largest reading ("100%") fits and the always-present cell never
// shifts the layout.
const batteryCellW = 10

// logiStale is how long a battery frame stays trusted; past it the readout
// dims to its last-known value. Computed against m.now, which the 1 s tick
// advances -- no new timer.
const logiStale = 5 * time.Minute

// actFailFor is how long the strip's act-fail warn cell stays on glass: long
// enough to glance at after a tap went nowhere, short enough not to nag.
// Past it the cell is absent entirely -- expiry is one clock compare against
// m.now (the logiStale pattern), no timer, no scan.
const actFailFor = 60 * time.Second

// panelRegion is the home content area in cells (the whole body -- the base
// HUD draws no outer frame); the bus receives it via hello/grid so the
// recognizer and any scraped windows size to the same grid.
func (m *model) panelRegion() (cols, rows int) {
	return m.width, m.height - stripH
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// flashTick arms the one-shot redraw at a tap flash's expiry.
func flashTick() tea.Cmd {
	return tea.Tick(tapFlashFor, func(t time.Time) tea.Msg { return flashTickMsg(t) })
}

// bloomTick arms the deferred-bloom open at the debounce deadline.
func bloomTick() tea.Cmd {
	return tea.Tick(bloomDelay, func(t time.Time) tea.Msg { return bloomTickMsg(t) })
}

// xitionTick arms the next frame of gen's swipe wash.
func xitionTick(gen int) tea.Cmd {
	return tea.Tick(xitionFrame, func(t time.Time) tea.Msg { return xitionTickMsg{gen: gen, at: t} })
}

// drainMark reports and clears a one-shot dispatch mark (the armed-tick
// idiom): a renderer or tap handler sets the mark, Update drains it into
// its one-shot tick.
func drainMark(mark *bool) bool {
	armed := *mark
	*mark = false
	return armed
}

// drainBloomArmed drains the deferred-bloom mark tapResources set during
// the current dispatch; Update turns it into bloomTick.
func (m *model) drainBloomArmed() bool { return drainMark(&m.resPendingArmed) }

// drainXitionArmed drains the wash mark startXition set during the current
// dispatch; Update turns it into the xitionTick chain.
func (m *model) drainXitionArmed() bool { return drainMark(&m.xitionArmed) }

// drainFlashArmed drains the tap-flash mark flash() set during the current
// dispatch; Update turns it into flashTick.
func (m *model) drainFlashArmed() bool { return drainMark(&m.flashArmed) }

func retryBus() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return retryMsg{} })
}

// connectBus dials the bus and sends the dock hello with the current cell
// grid + panel region; the reader goroutine feeds bus msgs through ch. gen
// is the dial generation stamped on every message this cmd produces.
func connectBus(gen int, socket string, cols, rows, pcols, prows int) tea.Cmd {
	return func() tea.Msg {
		conn, err := net.DialTimeout("unix", socket, time.Second)
		if err != nil {
			return busGoneMsg{gen: gen, err: err}
		}
		enc := json.NewEncoder(conn)
		if err := enc.Encode(proto.Msg{
			Type: proto.TypeHello, Role: proto.RoleDock,
			Cols: cols, Rows: rows, PanelCols: pcols, PanelRows: prows,
		}); err != nil {
			conn.Close()
			return busGoneMsg{gen: gen, err: err}
		}
		ch := make(chan proto.Msg, 16)
		go func() {
			dec := json.NewDecoder(conn)
			for {
				var msg proto.Msg
				if err := dec.Decode(&msg); err != nil {
					close(ch)
					return
				}
				ch <- msg
			}
		}()
		return busConnectedMsg{gen: gen, conn: conn, ch: ch}
	}
}

func waitBus(gen int, ch chan proto.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return busGoneMsg{gen: gen, err: fmt.Errorf("bus connection closed")}
		}
		return busEventMsg{gen: gen, msg: msg}
	}
}

// sendGrid pushes the current cell grid + panel region to the bus; the bus
// rebuilds its recognizer and resizes scrape windows to it. No-op until both
// the bus and a real size exist.
func (m *model) sendGrid() {
	if m.bus != busConnected || m.busConn == nil || m.width <= 0 {
		return
	}
	pcols, prows := m.panelRegion()
	enc := json.NewEncoder(m.busConn)
	_ = enc.Encode(proto.Msg{
		Type: proto.TypeGrid,
		Cols: m.width, Rows: m.height, PanelCols: pcols, PanelRows: prows,
	})
}

// sendHeartbeat pings the bus at proto.HeartbeatEvery, piggybacked on the
// 1 s clock tick: every other dock->bus message is event-driven, and the
// bus reaps a dock silent past its read grace. Write errors are the reader
// goroutine's to surface (the sendGrid convention).
func (m *model) sendHeartbeat() {
	if m.bus != busConnected || m.busConn == nil {
		return
	}
	if !m.lastPing.IsZero() && m.now.Sub(m.lastPing) < proto.HeartbeatEvery {
		return
	}
	m.lastPing = m.now
	_ = json.NewEncoder(m.busConn).Encode(proto.Msg{Type: proto.TypePing})
}

func (m *model) Init() tea.Cmd { return tick() }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		first := m.width == 0
		m.width, m.height = msg.Width, msg.Height
		m.homeCache.ok = false
		m.overlay = nil // anchor and item rects are stale at the new size
		if first {
			pcols, prows := m.panelRegion()
			m.busGen++
			return m, connectBus(m.busGen, m.opts.BusSocket, m.width, m.height, pcols, prows)
		}
		m.sendGrid()

	case tea.MouseClickMsg:
		// kitty mouse fallback: works with or without the bus. The gestures
		// driver delivers a touch long-press as a right click (its
		// hold-as-right-click vocabulary), so right routes to the context
		// menu exactly like a recognizer LongPress -- the menu tier stays
		// reachable while the driver owns the digitizer.
		if msg.Button == tea.MouseRight {
			m.lastGst = fmt.Sprintf("long-press @%d,%d (mouse)", msg.X, msg.Y)
			m.resolveLongPress(msg.X, msg.Y)
			if m.drainFlashArmed() {
				// openOverlay armed the bloom settle; without the tick the
				// box would stay accent-framed until an unrelated redraw
				return m, flashTick()
			}
			return m, nil
		}
		m.taps++
		m.resolveTap(msg.X, msg.Y)
		var cmds []tea.Cmd
		if m.drainFlashArmed() {
			cmds = append(cmds, flashTick())
		}
		if m.drainBloomArmed() {
			cmds = append(cmds, bloomTick())
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}

	case busConnectedMsg:
		if msg.gen != m.busGen {
			// a stale dial won its race after a newer one was armed: close
			// its conn, never adopt it or touch the current one
			msg.conn.Close()
			return m, nil
		}
		if m.bus != busConnected {
			// bus state is chrome (the strip cup): a real transition drops
			// the composed frame
			m.homeCache.ok = false
		}
		if m.busConn != nil {
			// a replaced conn closes; its reader's busGoneMsg arrives with
			// a stale gen and is dropped above
			m.busConn.Close()
		}
		m.bus = busConnected
		m.busConn = msg.conn
		m.busCh = msg.ch
		// hello carried the dims captured when the dial Cmd was created;
		// re-assert the current grid (a duplicate is idempotent bus-side)
		m.sendGrid()
		return m, waitBus(m.busGen, m.busCh)

	case busGoneMsg:
		if msg.gen != m.busGen {
			// a stale dial or reader: the current conn is healthy -- no
			// teardown, no retry
			return m, nil
		}
		if m.bus != busAbsent {
			// transition-guarded like the connect case: busGoneMsg refires
			// every retry while absent and must not rebuild the frame
			m.homeCache.ok = false
		}
		m.bus = busAbsent
		if m.busConn != nil {
			m.busConn.Close()
			m.busConn = nil
		}
		return m, retryBus()

	case retryMsg:
		if m.bus == busAbsent && m.width > 0 {
			pcols, prows := m.panelRegion()
			m.busGen++
			return m, connectBus(m.busGen, m.opts.BusSocket, m.width, m.height, pcols, prows)
		}

	case busEventMsg:
		if msg.gen != m.busGen {
			// stale reader chain: drop the event and let the chain die
			return m, nil
		}
		m.handleBusMsg(msg.msg)
		// one-shot ticks armed by the dispatch ride BESIDE the bus wait --
		// dropping the wait would wedge the reader
		cmds := []tea.Cmd{waitBus(m.busGen, m.busCh)}
		if m.drainFlashArmed() {
			cmds = append(cmds, flashTick())
		}
		if m.drainBloomArmed() {
			cmds = append(cmds, bloomTick())
		}
		if m.drainXitionArmed() {
			// a swipe opened the wash: gestures arrive on this path only
			cmds = append(cmds, xitionTick(m.xitionGen))
		}
		return m, tea.Batch(cmds...)

	case tickMsg:
		m.now = time.Time(msg)
		m.sendHeartbeat()
		// keyboard-bearing views adopt loader changes (an async fetch
		// landing, a flash's new revision) here: the keyboard layout has no
		// polled widgets, so nothing else invalidates its cached frame.
		// Bounded: a memo-hit Load is field checks (constant-cost).
		if m.layoutKind() == "keyboard" || m.kbLiveVisible() {
			m.ensureBoard()
		}
		return m, tick()

	case flashTickMsg:
		// advance the clock so the expired flash drops off the frame this
		// redraw composes; no re-arm -- the 1 s tick owns steady state
		m.now = time.Time(msg)
		if o := m.overlay; o != nil && len(o.items) > 0 && m.now.Sub(o.openedAt) >= tapFlashFor {
			// the bloom window closed: settle the box border to chrome.
			// Item-less overlays (the resources info bloom) are excluded:
			// their box is hand-built, and o.render would rebuild it as an
			// empty menu frame.
			o.box = o.render(m.overlayBloomStyle(o), m.overlayFillStyle())
		}

	case bloomTickMsg:
		m.now = time.Time(msg)
		m.openPendingBloom()

	case xitionTickMsg:
		// the wash owns its own chain: each frame re-arms until the last,
		// then the tick stops and the dock is back on the 1 s clock. A tick
		// from a retired chain (its wash retargeted or already ended) is
		// dropped -- two chains must not advance one wash at double speed.
		if m.xition == nil || msg.gen != m.xition.gen {
			break
		}
		m.now = msg.at
		if m.advanceXition() {
			return m, xitionTick(m.xition.gen)
		}
	}
	return m, nil
}

func (m *model) handleBusMsg(msg proto.Msg) {
	switch msg.Type {
	case proto.TypeReload:
		// the bus's config replaces the startup copy (greeting) or a reloaded
		// one (broadcast): re-derive cfg-dependent state exactly like startup.
		// Runtime-only state survives -- kbBoard/kbLayer, caffeinate, palette,
		// widget data, taps; strip/toggles and the kb-live gate read m.cfg at
		// render, so assigning it covers them.
		if msg.Config == nil {
			return
		}
		m.cfg = msg.Config
		m.layout = msg.Config.Layout
		m.skew = false
		m.resetLayout()
		m.trayFlash = nil
		// the bus resizes scrape windows to the (possibly re-regioned) grid
		m.sendGrid()
	case proto.TypeLayout:
		if _, ok := m.cfg.Layouts[msg.Layout]; ok {
			m.layout = msg.Layout
			m.skew = false // resetLayout below drops the composed frame
			m.resetLayout()
			// the bus resizes scrape windows to the new region
			m.sendGrid()
		} else {
			// config skew: keep the current layout, surface it on the strip
			// and its cup
			m.lastGst = fmt.Sprintf("layout %q unknown (config skew)", msg.Layout)
			if !m.skew {
				m.skew = true
				m.homeCache.ok = false
			}
		}
	case proto.TypeTheme:
		m.theme = msg.Theme
		// a palette-less broadcast (bus hasn't fetched yet) keeps the last
		// known palette rather than clearing it
		if msg.Palette != nil {
			m.palette = palette(msg.Palette)
		}
		// styles derive from the palette: rebuild them and drop the composed
		// frame so the new tones land immediately
		m.sty = buildStyles(m.palette)
		m.rowsOK = false
		m.homeCache.ok = false
	case proto.TypeCaffeinate:
		if msg.Caffeinate != m.caffeinate {
			m.caffeinate = msg.Caffeinate
			// the cup lives in the tray chrome: drop the composed frame so
			// the new state lands immediately
			m.homeCache.ok = false
		}
	case proto.TypeNotice:
		// transient bus-side warning (refused row act, nonzero exec exit):
		// surfaces on the strip like the skew path
		m.lastGst = msg.Error
		m.homeCache.ok = false
	case proto.TypeWidgetData:
		m.invalidateHome(msg.Widget)
		if msg.Error != "" {
			m.widgetErr[msg.Widget] = msg.Error
			break
		}
		var d module.Data
		if err := json.Unmarshal(msg.Data, &d); err != nil {
			m.widgetErr[msg.Widget] = "decode: " + err.Error()
			break
		}
		delete(m.widgetErr, msg.Widget)
		m.widgetData[msg.Widget] = d
	case proto.TypeSnapshot:
		// the dock has no scraped-frame renderer yet; the stale mark lands
		// on the widget's titled region
		was := m.widgetStale[msg.Widget]
		switch {
		case msg.Stale:
			if m.widgetStale == nil {
				m.widgetStale = make(map[string]bool)
			}
			m.widgetStale[msg.Widget] = true
		case msg.Error == "":
			// only a REAL frame clears the mark: error frames while the
			// window wedges must not resurrect a dead screen as live (the
			// scheduler's staleSent latch never re-pulses)
			delete(m.widgetStale, msg.Widget)
		}
		if was != m.widgetStale[msg.Widget] {
			// transitions only: fresh frames arrive at poll cadence and
			// must not defeat the home cache
			m.invalidateHome(msg.Widget)
		}
	case proto.TypeKey:
		m.handleKeyMsg(msg)
	case proto.TypeLogiState:
		// the battery readout lives in the strip chrome, which View rebuilds
		// every frame (never part of homeCache), so storing the frame is
		// enough -- the next View picks it up without dropping the body cache
		m.logi = msg.Logi
	case proto.TypeActFail:
		// strip chrome like the battery readout: storing the slot is enough
		m.actFail = msg.ActFail
	case proto.TypeGesture:
		if msg.Gesture == nil {
			return
		}
		g := msg.Gesture
		if g.Kind == proto.GesturePress {
			// touch acknowledgment: light the pressed element immediately;
			// the resolving tap/long-press/drag below clears it. lastGst
			// keeps the resolution, never the press.
			m.pressAt(g.Col, g.Row)
			return
		}
		if m.pressed != nil {
			m.pressed = nil
			m.homeCache.ok = false
		}
		m.lastGst = g.Kind
		switch g.Kind {
		case proto.GestureTap:
			m.taps++
			m.resolveTap(g.Col, g.Row)
		case proto.GestureLongPress:
			m.lastGst = fmt.Sprintf("long-press @%d,%d", g.Col, g.Row)
			m.resolveLongPress(g.Col, g.Row)
		case proto.GestureSwipe:
			m.lastGst = "swipe-" + g.Dir
			// single-finger swipe (multi-finger is hardware-refuted: the
			// panel firmware is single-contact). An open overlay owns the
			// glass -- its drags must not switch layouts under the menu.
			// Horizontal cycles strip tabs; vertical follows the active
			// layout's swipeUp/swipeDown nav target.
			if m.overlay == nil {
				switch g.Dir {
				case "left", "right":
					m.swipeTab(g.Dir)
				case "up", "down":
					m.swipeVertical(g)
				}
			}
		case proto.GestureTwoFingerSwipe:
			// tray gesture; tray engine is a later milestone
			m.lastGst = "two-finger-swipe-" + g.Dir + " (tray stub)"
		case proto.GestureWheel:
			m.lastGst = fmt.Sprintf("wheel %+d,%+d", g.DX, g.DY)
		}
	}
}

// swipeTab cycles the strip tabs on a horizontal swipe: left = next tab,
// right = prev, over the ring [home, entries...]. Alt pairs occupy one
// slot (active on either side, entered on the Target side); stub entries
// (undefined target layouts) are skipped -- a swipe must land somewhere
// real, never on a "soon" flash. A layout not on the strip (config
// default screens) swipes nowhere: jumping to an arbitrary slot mid-
// glass is worse than a no-op.
func (m *model) swipeTab(dir string) {
	if dir != "left" && dir != "right" {
		return
	}
	type slot struct{ target, label string }
	var ring []slot
	cur := -1
	if home, ok := m.homeLayout(); ok {
		if m.layout == home {
			cur = 0
		}
		ring = append(ring, slot{home, "home"})
	}
	if m.cfg != nil && m.cfg.Strip != nil {
		for _, e := range m.cfg.Strip.Entries {
			if _, ok := m.cfg.Layouts[e.Target]; !ok {
				continue // stub tab: nothing to land on
			}
			if m.layout == e.Target || (e.Alt != "" && m.layout == e.Alt) {
				cur = len(ring)
			}
			ring = append(ring, slot{e.Target, e.Label})
		}
	}
	if len(ring) < 2 || cur < 0 {
		return
	}
	step := 1
	if dir == "right" {
		step = -1
	}
	next := ring[(cur+step+len(ring))%len(ring)]
	m.flash("tab:" + next.label)
	// the whole body changes on a tab cycle, so the wash covers all of it
	cols, rows := m.panelRegion()
	m.startXition(rect{0, 0, cols, rows})
	m.trayActivate(next.target, next.label)
}

// swipeVertical follows a swipeUp/swipeDown target: the region the stroke
// STARTED in owns the direction first (swipe up on the kb column swaps that
// column to the MX viewer, swipe down on the viewer restores it), with the
// active layout's own target as the fallback (keyboard view swipe-up ->
// logiretch viewer). No target for the direction swipes nowhere -- silent,
// like an off-strip horizontal swipe. The wash runs over whatever changes:
// the region's rect, or the whole body for a layout-level target.
func (m *model) swipeVertical(g *proto.Gesture) {
	if m.cfg == nil {
		return
	}
	target, area := m.swipeTarget(g)
	if target == "" {
		return
	}
	m.lastGst = "swipe-" + g.Dir + ": " + target
	m.startXition(area)
	m.trayActivate(target, target)
}

// swipeSlopCols / swipeSlopRows widen a region's swipe rect for the START
// cell only. A thumb arriving at a column edge lands a cell or two off
// the region it meant, and the stroke then fell through to a layout with
// no vertical binding -- silently doing nothing, which reads as a broken
// gesture rather than a missed one.
const (
	swipeSlopCols = 6
	swipeSlopRows = 2
)

// swipeTarget resolves a vertical swipe to its nav target and the area that
// changes. Region bindings win by start cell (first containing rect, the hit
// table's convention); a start that landed in no bound region at all gets a
// second pass against widened rects, and the layout target covers the rest
// of the glass.
func (m *model) swipeTarget(g *proto.Gesture) (target string, area rect) {
	for _, rs := range m.regionSwipes {
		if !rs.area.contains(g.StartCol, g.StartRow) {
			continue
		}
		if t := rs.target(g.Dir); t != "" {
			return t, rs.area
		}
		// a region binding only the other direction leaves this one to the
		// layout: the stroke is not swallowed by the column it started in,
		// and the near-miss pass must not overrule that deliberate choice
		return m.layoutSwipe(g)
	}
	for _, rs := range m.regionSwipes {
		if rs.area.grow(swipeSlopCols, swipeSlopRows).contains(g.StartCol, g.StartRow) {
			if t := rs.target(g.Dir); t != "" {
				return t, rs.area
			}
		}
	}
	return m.layoutSwipe(g)
}

// layoutSwipe is the whole-glass fallback binding.
func (m *model) layoutSwipe(g *proto.Gesture) (string, rect) {
	l := m.cfg.Layouts[m.layout]
	target := l.SwipeUp
	if g.Dir == "down" {
		target = l.SwipeDown
	}
	cols, rows := m.panelRegion()
	return target, rect{0, 0, cols, rows}
}

// navigateTo switches to a layout the dock config knows. Bus-connected navs
// route through the bus exactly like `khudson ctl layout` -- the bus config
// (scheduler visibility) moves with the switch and the dock flips when the
// TypeLayout broadcast comes back. Dock-local switching is the bus-absent
// fallback only.
func (m *model) navigateTo(target string) {
	m.lastGst = "nav: " + target
	if m.bus == busConnected && m.busConn != nil {
		enc := json.NewEncoder(m.busConn)
		_ = enc.Encode(proto.Msg{Type: proto.TypeCtl, Cmd: "layout", Arg: target})
		return
	}
	m.layout = target
	m.resetLayout()
}

// homeTap navigates to the home-kind layout; no-op when already there or
// when none is configured. Reached from the status strip's home icon (the
// one persistent return affordance).
func (m *model) homeTap(int, int) {
	if t, ok := m.homeLayout(); ok && m.layout != t {
		m.navigateTo(t)
	}
}

// homeLayout is the home tap target: the layout NAMED "home" when it is
// home-kind, else the config default, else the first home-kind name in
// sorted order. Kind alone cannot resolve it: kind selects the render
// ENGINE, and other home-kind layouts exist (the fullscreen clod panel) --
// layout.state persists the runtime selection into cfg.Layout across bus
// restarts, so with clod active a kind-of-the-default pick resolved home
// to clod itself and the icon no-opped (glass-reported; the sorted scan
// is alphabetical and "claude" < "home", so it strands the same way).
func (m *model) homeLayout() (string, bool) {
	if m.cfg == nil {
		return "", false
	}
	if m.cfg.Layouts["home"].Kind == "home" {
		return "home", true
	}
	if m.cfg.Layouts[m.cfg.Layout].Kind == "home" {
		return m.cfg.Layout, true
	}
	for _, name := range slices.Sorted(maps.Keys(m.cfg.Layouts)) {
		if m.cfg.Layouts[name].Kind == "home" {
			return name, true
		}
	}
	return "", false
}

func (m *model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	if m.width == 0 {
		v.SetContent("...")
		return v
	}

	bodyH := m.height - stripH

	var body string
	// kind drives the ENGINE (which renderer runs); the strip's persistent
	// home icon is the way back from every layout, so no per-view return
	// affordance wraps the body
	switch m.layoutKind() {
	case "home":
		if m.trayFlashLive() || m.attentionLive() {
			// clock-driven frame: render fresh until the flash expires or
			// the attention border stops marching
			m.homeCache.ok = false
			body = m.renderHome(bodyH)
		} else if m.homeCache.ok {
			body = m.loadHomeCache()
		} else {
			body = m.renderHome(bodyH)
			m.storeHomeCache(body)
		}
	case "keyboard":
		if m.trayFlashLive() {
			m.homeCache.ok = false
			body = m.renderKeyboard(bodyH)
		} else if m.homeCache.ok {
			body = m.loadHomeCache()
		} else {
			body = m.renderKeyboard(bodyH)
			m.storeHomeCache(body)
		}
	default:
		body = m.renderSkewStub(bodyH)
	}

	// renderStrip appends the strip hits onto whichever body table is live;
	// cap the slice first so the append reallocates instead of growing into
	// homeCache's cached backing array (a cache-hit frame aliases it). The
	// join is plain concatenation: both sides are exact-width by
	// construction.
	m.hits = m.hits[:len(m.hits):len(m.hits)]
	frame := body + "\n" + m.renderStrip()
	// layered only when something floats over the body: the swipe wash, an
	// open menu, or both (a long-press mid-wash). Positioned layering MUST
	// go through NewCompositor -- a bare Canvas.Compose(layer) ignores
	// Layer.X/Y in v2.0.4 (draws at origin after clearing the area) -- and Z
	// is explicit because equal-z draw order is unspecified. The wash sits
	// under the menu: a modal must never render as static.
	layers := []*lipgloss.Layer{lipgloss.NewLayer(frame)}
	if l := m.xition.layer(1); l != nil {
		layers = append(layers, l)
	}
	if m.overlay != nil {
		layers = append(layers,
			lipgloss.NewLayer(m.overlay.box).X(m.overlay.anchor.x).Y(m.overlay.anchor.y).Z(2))
	}
	if len(layers) == 1 {
		v.SetContent(frame)
		return v
	}
	v.SetContent(lipgloss.NewCanvas(m.width, m.height).Compose(lipgloss.NewCompositor(layers...)).Render())
	return v
}

// renderSkewStub is the loud config-skew body: home/keyboard are the shipped
// kinds, anything else renders a warn stub, never a silent freeze.
func (m *model) renderSkewStub(bodyH int) string {
	m.resetHits()
	return renderTitledBox("",
		[]string{chromeWarn.Render(" layout " + m.layout + ": kind " + m.layoutKind() + " has no renderer")},
		m.width, bodyH)
}

// renderStrip is the one-row band under the body, nav ahead of status
// content: the home icon (bare while a home layout shows -- the icon IS
// home's tab), config section tabs (band labels, the active tab notching
// through to the bare background), toggle cups, the kitty_mod chord
// note, the always-present battery readout, the act-fail warn cell while
// one is fresh, then layout, bus state, and gesture tally, the clock flush
// right. Everything outside the active notch sits on the stripSurface
// fill. Registers the strip hits as it places the band: icon, tabs, cups,
// kitty_mod, battery, act-fail, then a whole-strip consume rect so strip
// taps never leak into the body (first-match table).
func (m *model) renderStrip() string {
	yTop := m.height - stripH
	srf := m.stripSurface()
	var bot strings.Builder
	x := 0
	// icon draws a glyph cell on the band (or on the bare background when
	// it is the active view's notch); the caller pre-tones the style
	icon := func(glyph string, style lipgloss.Style, notch bool, key string, do func(int, int)) {
		// force painted cells == stripIconW (fitCell convention): nerd
		// glyphs are the ambiguous-width poster child
		if notch {
			bot.WriteString(fitCellPad(" "+style.Render(glyph)+" ", stripIconW))
		} else {
			bot.WriteString(srf.pad(srf.run(x, 0, 1)+style.Render(glyph), stripIconW))
		}
		m.hits = append(m.hits, hitRegion{area: rect{x, yTop, stripIconW, stripH}, do: do, pressKey: key})
		x += stripIconW
	}

	if m.width >= stripIconW {
		// the home icon IS home's tab: it notches bare while home shows
		home, _ := m.homeLayout()
		notch := m.layout == home
		style := m.sty.brand
		if !notch {
			style = srf.style(style)
		}
		if m.flashLive("icon:home") {
			style = m.tapStyle(style)
		}
		icon(homeGlyph, style, notch, "icon:home", func(x, y int) {
			m.flash("icon:home")
			m.homeTap(x, y)
		})
	}
	if m.cfg != nil && m.cfg.Strip != nil {
		// section tabs, the kb bar's band idiom: padded labels ride the
		// band and the ACTIVE tab sits on the bare background, notching the
		// band open -- full-height panels are borderless above the strip,
		// so the notch is continuous with the panel body (the lipgloss tab
		// notch, minus the frame). No walls, no border welds: the
		// band-vs-bare contrast draws the tabs.
		for _, e := range m.cfg.Strip.Entries {
			// an alt-carrying tab is active on either side of its pair and
			// its tap flips to the other side (the reno presenter<->recorder
			// pair); plain tabs keep the one-target exact match
			active := e.Target == m.layout || (e.Alt != "" && e.Alt == m.layout)
			target := e.Target
			// a pair's tab names the side it is SHOWING, not the side it
			// goes to: an identical label on both halves hides that the
			// other half exists, which is how a control pad behind an alt
			// stayed invisible
			text := e.Label
			if e.Alt != "" && m.layout == e.Target {
				target = e.Alt
			} else if e.Alt != "" && m.layout == e.Alt && e.AltLabel != "" {
				text = e.AltLabel
			}
			// both halves budget one width, so flipping never reflows the
			// strip out from under the thumb that flipped it
			if w := max(lipgloss.Width(e.Label), lipgloss.Width(e.AltLabel)); w > lipgloss.Width(text) {
				text += strings.Repeat(" ", w-lipgloss.Width(text))
			}
			label, style := text, srf.label(chromeFG)
			if active {
				style = chromeAccent.Bold(true)
			}
			if m.flashLive(e.Label) {
				// the "soon" stub flash is the informative one: it outranks
				// the tap restyle
				label, style = "soon", srf.style(chromeWarn)
			} else if m.flashLive("tab:" + e.Label) {
				style = m.tapStyle(style)
			}
			label = " " + label + " "
			lw := lipgloss.Width(label)
			if x+lw > m.width {
				break
			}
			// force painted cells == the budgeted width (fitCell
			// convention): an ambiguous-width label must not desync the
			// row from the hit rect registered below
			bot.WriteString(fitCellPad(style.Render(label), lw))
			m.hits = append(m.hits, hitRegion{
				area: rect{x, yTop, lw, stripH},
				do: func(int, int) {
					m.flash("tab:" + e.Label)
					m.trayActivate(target, e.Label)
				},
				pressKey: "tab:" + e.Label,
			})
			x += lw
		}
		degraded := m.bus != busConnected || m.skew
		for i, tg := range m.cfg.Strip.Toggles {
			if x+1+stripIconW > m.width {
				break
			}
			bot.WriteString(srf.run(x, 0, 1))
			x++
			if tg.Kind != "caffeinate" {
				// unknown kind: LOOK dead -- dim glyph, consumed no-op tap
				// (config ahead of the binary stays visible, never healthy)
				g := tg.Off
				if g == "" {
					g = "?"
				}
				icon(g, srf.label(chromeDim), false, "", consumeTap)
				continue
			}
			glyph, style := tg.Off, srf.label(chromeFG)
			if glyph == "" {
				glyph = cupOffGlyph
			}
			if m.caffeinate == "on" {
				glyph, style = tg.On, srf.style(chromeAccent)
				if glyph == "" {
					glyph = cupOnGlyph
				}
			}
			if degraded {
				// a tap that cannot land must look dead, never silently no-op
				style = srf.style(chromeWarn)
			}
			key := "cup:" + strconv.Itoa(i)
			if m.flashLive(key) {
				style = m.tapStyle(style)
			}
			icon(glyph, style, false, key, func(int, int) {
				m.flash(key)
				m.sendCaffeinateToggle()
			})
		}
		// kitty_mod chord note: a "kitty_mod" text label ahead of the compact
		// modifier glyphs (bare glyphs read as keys floating in the strip),
		// a readout (consumeTap). Empty renders nothing -- no cell, no hit.
		// Budgeted like a tab (lipgloss.Width + 2) so the ambiguous-width
		// glyphs never desync the row from the hit rect.
		if km := kittyModLabel(m.cfg.Strip.KittyMod); km != "" {
			label := "kitty_mod " + km
			w := lipgloss.Width(label) + 2
			if x+w <= m.width {
				bot.WriteString(srf.pad(srf.run(x, 0, 1)+srf.label(chromeDim).Render(label), w))
				m.hits = append(m.hits, hitRegion{area: rect{x, yTop, w, stripH}, do: consumeTap})
				x += w
			}
		}
	}

	// battery readout: always-present chrome (outside the strip guard above).
	// A fixed-width multi-col cell -- glyph + integer pct exceed stripIconW --
	// so the layout never shifts: a neutral placeholder with no data, the SoC
	// bucket glyph + pct otherwise, dimmed once the frame ages past logiStale.
	// A readout: consumeTap owns the hit.
	if x+batteryCellW <= m.width {
		// mouseGlyph prefixes every state: a bare battery glyph reads as
		// the machine's charge, and this cell is the MX mouse's.
		glyph, tone := batUnknownGlyph, chromeDim
		label := mouseGlyph + " " + glyph
		if m.logi != nil {
			glyph = batteryGlyph(m.logi.SoC, m.logi.Charging)
			tone = chromeFG
			if m.now.Sub(time.Unix(0, m.logi.TimeNS)) > logiStale {
				tone = chromeDim
			}
			label = mouseGlyph + " " + glyph + " " + strconv.Itoa(m.logi.SoC) + "%"
		}
		bot.WriteString(srf.pad(srf.run(x, 0, 1)+srf.label(tone).Render(label), batteryCellW))
		m.hits = append(m.hits, hitRegion{area: rect{x, yTop, batteryCellW, stripH}, do: consumeTap})
		x += batteryCellW
	}

	// act-fail warn cell: the bus's latest failed act/verb (TypeActFail),
	// rendered in the warn tone while fresher than actFailFor; expired or
	// absent = no cell at all. Budgeted like a tab (lipgloss.Width + 2, the
	// ambiguous-width convention); a readout: consumeTap owns the hit.
	if m.actFail != nil && m.now.Sub(time.Unix(0, m.actFail.TimeNS)) <= actFailFor {
		label := "! " + m.actFail.Msg
		w := lipgloss.Width(label) + 2
		if x+w <= m.width {
			bot.WriteString(srf.pad(srf.run(x, 0, 1)+srf.style(chromeWarn).Render(label), w))
			m.hits = append(m.hits, hitRegion{area: rect{x, yTop, w, stripH}, do: consumeTap})
			x += w
		}
	}

	// status remainder, width-fitted; everything left of it is exact-width
	// by construction
	rem := m.width - x
	left := lipgloss.JoinHorizontal(lipgloss.Top,
		srf.label(m.sty.strip).Render(" "+m.layout+" | "),
		m.renderBusIndicator(srf),
		srf.label(m.sty.strip).Render(" | "+m.lastGestureLabel()),
	)
	clock := srf.label(m.sty.strip).Render(strings.ToLower(m.now.Format("Mon 15:04")))
	var status string
	gap := rem - lipgloss.Width(left) - lipgloss.Width(clock)
	switch {
	case gap >= 1:
		status = left + srf.run(x+lipgloss.Width(left), 0, gap) + clock
	case lipgloss.Width(clock)+1 <= rem:
		status = srf.pad(left, rem-lipgloss.Width(clock)-1) + srf.run(m.width-lipgloss.Width(clock)-1, 0, 1) + clock
	default:
		status = srf.pad(left+" "+clock, rem)
	}
	bot.WriteString(status)

	m.hits = append(m.hits, hitRegion{area: rect{0, yTop, m.width, stripH}, do: consumeTap})
	return bot.String()
}

// Strip surface: one solid band row tinted toward the house accent
// (color5 -- the theme background pulled toward it; the neutral fg pull is
// the accentless fallback), textured with glyphs pulled further along the
// same ramp. The active section tab -- and the home icon while home is
// showing -- sit on the bare background, notching the band open into the
// borderless panel above (the kb bar idiom). No palette = no surface; the
// indexed base renders exactly as before.
const (
	stripFillBlend = 0.22
	stripTexBlend  = 0.4
	stripTexture   = "dots:sparse"
)

// stripSurface carries the derived surface styles; the zero value (no
// palette) degrades every helper to its bare input.
type stripSurface struct {
	bg   color.Color // band tone
	text color.Color // quiet band text: the theme foreground, full contrast
	tex  lipgloss.Style
	cell func(x, y int) string
}

func (m *model) stripSurface() stripSurface {
	bg, ok := m.palette.blend("background", "color5", stripFillBlend)
	if !ok {
		if bg, ok = m.palette.blend("background", "foreground", stripFillBlend); !ok {
			return stripSurface{}
		}
	}
	s := stripSurface{bg: bg}
	s.text, _ = m.palette.color("foreground")
	if fg, ok := m.palette.blend("background", "color5", stripTexBlend); ok {
		s.cell, _ = kbview.TexCellFn(stripTexture)
		s.tex = lipgloss.NewStyle().Background(bg).Foreground(fg)
	}
	return s
}

// style seats a colored tone on the band, keeping its foreground; tones
// that already carry a background (the tap flash) keep theirs entirely.
// GetBackground yields NoColor, not nil, when unset.
func (s stripSurface) style(base lipgloss.Style) lipgloss.Style {
	if s.bg == nil {
		return base
	}
	if _, unset := base.GetBackground().(lipgloss.NoColor); !unset {
		return base
	}
	return base.Background(s.bg)
}

// label seats QUIET text on the band: foreground-toned at full contrast
// (glass-verified twice: dim and body-toned text both read faint on the
// band), band background.
func (s stripSurface) label(base lipgloss.Style) lipgloss.Style {
	if s.bg == nil {
		return base
	}
	st := base.Background(s.bg)
	if s.text != nil {
		st = st.Foreground(s.text)
	}
	return st
}

// run is n blank surface cells at absolute column x on strip row y,
// texture glyphs included; bare spaces without a palette.
func (s stripSurface) run(x, y, n int) string {
	if n <= 0 {
		return ""
	}
	if s.bg == nil {
		return strings.Repeat(" ", n)
	}
	return kbview.TexRun(s.cell, lipgloss.NewStyle().Background(s.bg), s.tex, x, y, n)
}

// pad crops/pads content to w cells, the padding on the bare surface.
func (s stripSurface) pad(content string, w int) string {
	t := fitCell(content, w)
	if p := w - lipgloss.Width(t); p > 0 {
		if s.bg == nil {
			return t + strings.Repeat(" ", p)
		}
		return t + lipgloss.NewStyle().Background(s.bg).Render(strings.Repeat(" ", p))
	}
	return t
}

// batteryGlyph picks the readout glyph by state-of-charge bucket
// (empty/quarter/half/three-quarter/full), or the charging glyph while wired.
func batteryGlyph(soc int, charging bool) string {
	if charging {
		return batChargingGlyph
	}
	switch {
	case soc < 13:
		return batEmptyGlyph
	case soc < 38:
		return batQuarterGlyph
	case soc < 63:
		return batHalfGlyph
	case soc < 88:
		return batThreeQuarterGlyph
	default:
		return batFullGlyph
	}
}

// kittyModGlyph maps a kitty_mod chord token to a compact modifier glyph
// (escaped so the source stays ASCII). Unknown tokens fall through to their
// raw text in kittyModLabel.
var kittyModGlyph = map[string]string{
	"ctrl":    "\u2303", // up arrowhead
	"control": "\u2303",
	"opt":     "\u2325", // option key
	"option":  "\u2325",
	"alt":     "\u2325",
	"shift":   "\u21E7", // upwards white arrow
	"cmd":     "\u2318", // place of interest sign
	"command": "\u2318",
	"super":   "\u2318",
}

// kittyModLabel renders a "+"-joined kitty_mod chord as its modifier glyphs
// (unknown tokens kept as short text); empty in, empty out.
func kittyModLabel(chord string) string {
	if chord == "" {
		return ""
	}
	var b strings.Builder
	for _, tok := range strings.Split(chord, "+") {
		tok = strings.ToLower(strings.TrimSpace(tok))
		if tok == "" {
			continue
		}
		if g, ok := kittyModGlyph[tok]; ok {
			b.WriteString(g)
		} else {
			b.WriteString(tok)
		}
	}
	return b.String()
}

// layoutKind is the active layout's engine kind; home is the only shipped
// kind, so an unknown or empty kind defaults to home.
func (m *model) layoutKind() string {
	if m.cfg == nil {
		return "home"
	}
	if l, ok := m.cfg.Layouts[m.layout]; ok && l.Kind != "" {
		return l.Kind
	}
	return "home"
}

func (m *model) renderBusIndicator(srf stripSurface) string {
	if m.bus == busConnected {
		return srf.label(m.sty.strip).Render("bus ok")
	}
	// degraded state is loud, never silent
	return srf.style(m.sty.warn).Render("bus absent -- mouse fallback")
}

func (m *model) lastGestureLabel() string {
	if m.lastGst == "" {
		return fmt.Sprintf("taps: %d", m.taps)
	}
	return fmt.Sprintf("taps: %d, last: %s", m.taps, m.lastGst)
}
