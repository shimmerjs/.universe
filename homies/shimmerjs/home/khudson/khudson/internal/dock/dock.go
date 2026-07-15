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
			h.longPress(x, y)
			return true
		}
	}
	return false
}

// resetHits starts a fresh hit table; every layout renderer calls it before
// appending its own targets, so the table always matches the frame on glass.
func (m *model) resetHits() {
	m.hits = m.hits[:0]
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

	// keyboard view state: the static Moonlander board, loaded once (lazy),
	// the shown layer index, and the load error/empty message. No polling --
	// the layout is static and offline.
	kbBoard  *keyboard.Board
	kbErr    string
	kbLayer  int
	kbLoaded bool

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
	// was not anchored in
	m.overlay = nil
}

// stripH is the status strip under the body: two rows, so the strip-hosted
// nav icons draw at double height. Strip-chrome geometry, not a region
// size.
const stripH = 2

// stripIconW is one strip icon's width in cells: a nerd-font glyph with a
// space either side, sitting on the BOTTOM strip row at the text baseline;
// the hit rect stays 2 rows tall for touch. Bigger-than-a-cell icons are a
// dead end twice over: OSC 66 scaled runs die at the compositor
// (ultraviolet forwards only SGR and OSC 8 -- TestStripSurvivesCompositor
// pins that class), and 4x2 quadrant block art has ~8x4 pixel resolution,
// which read as blobs on glass. One crisp designed glyph
// beats both.
const stripIconW = 3

// homeGlyph is the chrome-owned home icon on the strip; not a config
// entry -- homeTap resolves its target by layout kind. md-home-variant:
// the plain md-home read as an up arrow at strip size.
const homeGlyph = "\U000F02DD"

// Strip flip chevrons (nerd font material design icons, the cup-glyph
// convention): one control between the tabs and the toggles that flips the
// strip.flip layout pair -- collapse hides the kb column, expand restores it.
const (
	stripCollapseGlyph = "\U000F0140" // nf-md-chevron_down
	stripExpandGlyph   = "\U000F0143" // nf-md-chevron_up
)

// batteryCellW is the strip battery readout's fixed width in cells: a
// nerd-font battery glyph plus the integer pct, budgeted so the largest
// reading ("100%") fits and the always-present cell never shifts the layout.
const batteryCellW = 8

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

// drainFlashArmed reports and clears the tap-flash mark flash() set during
// the current dispatch; Update turns it into flashTick.
func (m *model) drainFlashArmed() bool {
	armed := m.flashArmed
	m.flashArmed = false
	return armed
}

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
		// kitty mouse fallback: works with or without the bus
		m.taps++
		m.resolveTap(msg.X, msg.Y)
		if m.drainFlashArmed() {
			return m, flashTick()
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
		if m.drainFlashArmed() {
			// a gesture tap flashed: arm the expiry redraw WITHOUT
			// replacing the bus wait -- dropping it would wedge the reader
			return m, tea.Batch(waitBus(m.busGen, m.busCh), flashTick())
		}
		return m, waitBus(m.busGen, m.busCh)

	case tickMsg:
		m.now = time.Time(msg)
		m.sendHeartbeat()
		return m, tick()

	case flashTickMsg:
		// advance the clock so the expired flash drops off the frame this
		// redraw composes; no re-arm -- the 1 s tick owns steady state
		m.now = time.Time(msg)
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
		case proto.GestureTwoFingerSwipe:
			// tray gesture; tray engine is a later milestone
			m.lastGst = "two-finger-swipe-" + g.Dir + " (tray stub)"
		case proto.GestureWheel:
			m.lastGst = fmt.Sprintf("wheel %+d,%+d", g.DX, g.DY)
		}
	}
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
			body = m.homeCache.body
			m.hits = m.homeCache.hits
		} else {
			body = m.renderHome(bodyH)
			m.homeCache = homeCache{body: body, hits: slices.Clone(m.hits), ok: true}
		}
	case "keyboard":
		if m.trayFlashLive() {
			m.homeCache.ok = false
			body = m.renderKeyboard(bodyH)
		} else if m.homeCache.ok {
			body = m.homeCache.body
			m.hits = m.homeCache.hits
		} else {
			body = m.renderKeyboard(bodyH)
			m.homeCache = homeCache{body: body, hits: slices.Clone(m.hits), ok: true}
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
	if m.overlay != nil {
		// open only: composite the pre-built overlay box over the frame on
		// the P0-pinned Canvas/GraphemeWidth path. Positioned layering MUST
		// go through NewCompositor -- a bare Canvas.Compose(layer) ignores
		// Layer.X/Y in v2.0.4 (draws at origin after clearing the area) --
		// and Z is explicit because equal-z draw order is unspecified.
		comp := lipgloss.NewCompositor(
			lipgloss.NewLayer(frame),
			lipgloss.NewLayer(m.overlay.box).X(m.overlay.anchor.x).Y(m.overlay.anchor.y).Z(1),
		)
		v.SetContent(lipgloss.NewCanvas(m.width, m.height).Compose(comp).Render())
		return v
	}
	v.SetContent(frame)
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

// renderStrip is the 2-row strip under the body, hosting the nav band ahead
// of the status content: the drawn home icon, config tab labels (active
// target accented, stub targets flashing "soon"), the flip chevron (only
// while the active layout is one of the strip.flip pair), drawn toggle cups,
// the kitty_mod chord note, the always-present battery readout, the act-fail
// warn cell while one is fresh, then layout, bus state, and gesture tally,
// the clock flush right. Icons occupy both rows as real cells (see the strip
// art block for why escapes cannot); 1x text sits on the BOTTOM row with
// blank cells above it. Registers the strip hits as it places the band:
// icon, tabs, chevron, cups, kitty_mod, battery, act-fail, then a
// whole-strip consume rect so strip taps never leak into the body
// (first-match table).
func (m *model) renderStrip() string {
	yTop := m.height - stripH
	var top, bot strings.Builder
	x := 0
	icon := func(glyph string, style lipgloss.Style, do func(int, int)) {
		top.WriteString(strings.Repeat(" ", stripIconW))
		// force painted cells == stripIconW (fitCell convention): nerd
		// glyphs are the ambiguous-width poster child
		bot.WriteString(fitCellPad(" "+style.Render(glyph)+" ", stripIconW))
		m.hits = append(m.hits, hitRegion{area: rect{x, yTop, stripIconW, stripH}, do: do})
		x += stripIconW
	}

	if m.width >= stripIconW {
		style := m.sty.brand
		if m.flashLive("icon:home") {
			style = m.tapStyle(style)
		}
		icon(homeGlyph, style, func(x, y int) {
			m.flash("icon:home")
			m.homeTap(x, y)
		})
	}
	if m.cfg != nil && m.cfg.Strip != nil {
		for _, e := range m.cfg.Strip.Entries {
			label, style := e.Label, chromeFG
			if e.Target == m.layout {
				style = chromeAccent
			}
			if m.flashLive(e.Label) {
				// the "soon" stub flash is the informative one: it outranks
				// the tap restyle
				label, style = "soon", chromeWarn
			} else if m.flashLive("tab:" + e.Label) {
				style = m.tapStyle(style)
			}
			w := lipgloss.Width(label) + 2
			if x+w > m.width {
				break
			}
			top.WriteString(strings.Repeat(" ", w))
			// force painted cells == the budgeted w (fitCell convention):
			// an ambiguous-width label must not desync the row from the
			// hit rect registered below
			bot.WriteString(fitCellPad(" "+style.Render(label)+" ", w))
			m.hits = append(m.hits, hitRegion{
				area: rect{x, yTop, w, stripH},
				do: func(int, int) {
					m.flash("tab:" + e.Label)
					m.trayActivate(e.Target, e.Label)
				},
			})
			x += w
		}
		// flip chevron: a control (chromeFG, not a state light), rendered
		// only while the active layout is one of the pair; tap flips to the
		// other side. The icon closure does not width-guard, so guard here.
		if f := m.cfg.Strip.Flip; f != nil && x+stripIconW <= m.width {
			glyph, target := "", ""
			switch m.layout {
			case f.Expanded:
				glyph, target = stripCollapseGlyph, f.Collapsed
			case f.Collapsed:
				glyph, target = stripExpandGlyph, f.Expanded
			}
			if glyph != "" {
				style := chromeFG
				if m.flashLive("icon:chevron") {
					style = m.tapStyle(style)
				}
				icon(glyph, style, func(int, int) {
					m.flash("icon:chevron")
					m.trayActivate(target, "kb")
				})
			}
		}
		degraded := m.bus != busConnected || m.skew
		for i, tg := range m.cfg.Strip.Toggles {
			if x+1+stripIconW > m.width {
				break
			}
			top.WriteString(" ")
			bot.WriteString(" ")
			x++
			if tg.Kind != "caffeinate" {
				// unknown kind: LOOK dead -- dim glyph, consumed no-op tap
				// (config ahead of the binary stays visible, never healthy)
				g := tg.Off
				if g == "" {
					g = "?"
				}
				icon(g, chromeDim, consumeTap)
				continue
			}
			glyph, style := tg.Off, chromeFG
			if glyph == "" {
				glyph = cupOffGlyph
			}
			if m.caffeinate == "on" {
				glyph, style = tg.On, chromeAccent
				if glyph == "" {
					glyph = cupOnGlyph
				}
			}
			if degraded {
				// a tap that cannot land must look dead, never silently no-op
				style = chromeWarn
			}
			key := "cup:" + strconv.Itoa(i)
			if m.flashLive(key) {
				style = m.tapStyle(style)
			}
			icon(glyph, style, func(int, int) {
				m.flash(key)
				m.sendCaffeinateToggle()
			})
		}
		// kitty_mod chord note: the configured kitty_mod rendered as compact
		// modifier glyphs, a readout (consumeTap). Empty renders nothing --
		// no cell, no hit. Budgeted like a tab (lipgloss.Width + 2) so the
		// ambiguous-width glyphs never desync the row from the hit rect.
		if km := kittyModLabel(m.cfg.Strip.KittyMod); km != "" {
			w := lipgloss.Width(km) + 2
			if x+w <= m.width {
				top.WriteString(strings.Repeat(" ", w))
				bot.WriteString(fitCellPad(" "+chromeDim.Render(km)+" ", w))
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
		glyph, tone := batUnknownGlyph, chromeDim
		label := glyph
		if m.logi != nil {
			glyph = batteryGlyph(m.logi.SoC, m.logi.Charging)
			tone = chromeFG
			if m.now.Sub(time.Unix(0, m.logi.TimeNS)) > logiStale {
				tone = chromeDim
			}
			label = glyph + " " + strconv.Itoa(m.logi.SoC) + "%"
		}
		top.WriteString(strings.Repeat(" ", batteryCellW))
		bot.WriteString(fitCellPad(" "+tone.Render(label)+" ", batteryCellW))
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
			top.WriteString(strings.Repeat(" ", w))
			bot.WriteString(fitCellPad(" "+chromeWarn.Render(label)+" ", w))
			m.hits = append(m.hits, hitRegion{area: rect{x, yTop, w, stripH}, do: consumeTap})
			x += w
		}
	}

	// status remainder, width-fitted; everything left of it is exact-width
	// by construction
	rem := m.width - x
	left := lipgloss.JoinHorizontal(lipgloss.Top,
		m.sty.strip.Render(" "+m.layout+" | "),
		m.renderBusIndicator(),
		m.sty.strip.Render(" | "+m.lastGestureLabel()),
	)
	clock := m.sty.strip.Render(strings.ToLower(m.now.Format("Mon 15:04")))
	var status string
	gap := rem - lipgloss.Width(left) - lipgloss.Width(clock)
	switch {
	case gap >= 1:
		status = left + strings.Repeat(" ", gap) + clock
	case lipgloss.Width(clock)+1 <= rem:
		status = fitCellPad(left, rem-lipgloss.Width(clock)-1) + " " + clock
	default:
		status = fitCellPad(left+" "+clock, rem)
	}
	top.WriteString(strings.Repeat(" ", rem))
	bot.WriteString(status)

	m.hits = append(m.hits, hitRegion{area: rect{0, yTop, m.width, stripH}, do: consumeTap})
	return top.String() + "\n" + bot.String()
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

func (m *model) renderBusIndicator() string {
	if m.bus == busConnected {
		return m.sty.strip.Render("bus ok")
	}
	// degraded state is loud, never silent
	return m.sty.warn.Render("bus absent -- mouse fallback")
}

func (m *model) lastGestureLabel() string {
	if m.lastGst == "" {
		return fmt.Sprintf("taps: %d", m.taps)
	}
	return fmt.Sprintf("taps: %d, last: %s", m.taps, m.lastGst)
}
