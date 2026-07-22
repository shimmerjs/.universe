// Keyboard layout kind: the dock's imperative shell over the kbview
// functional core (internal/keyboard/kbview). This file owns the I/O and
// host wiring -- loading the board (ensureBoard), folding TypeKey broadcasts
// into view state (handleKeyMsg), building the theme from the dock palette,
// mapping the core's hits onto the dock tap table, and homeCache
// invalidation. The pure render + event fold live in kbview so the same
// keyboard viewer drives the dock, a standalone terminal client, or any host.
package dock

import (
	"context"
	"maps"
	"os/exec"
	"slices"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/kbview"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// kbSerialTTL bounds the loader's serial poll: at most one ioreg exec per
// window regardless of frame rate (constant-cost invariant).
const kbSerialTTL = 10 * time.Second

// ensureBoard resolves the board through the keyboard.Loader: the USB
// serial names the deployed revision, the payload comes from the local
// caches (network only asynchronously, never blocking a tick). A flash is
// adopted on the next serial poll without a dock restart; a resolve that
// yields nothing keeps the board already on glass. Never fatal.
func (m *model) ensureBoard() {
	if m.kbLoader == nil {
		m.kbLoader = &keyboard.Loader{Poller: &usbserial.Poller{TTL: kbSerialTTL}}
	}
	st := m.kbLoader.Load(context.Background())
	if st.Board == nil || len(st.Board.Layers) == 0 {
		if m.kbBoard == nil {
			err := st.Err
			if st.Board != nil && st.Err == "" {
				err = "layout has no layers"
			}
			if err != m.kbErr {
				// hint transitions (no board -> fetching) redraw too
				m.kbErr = err
				m.homeCache.ok = false
			}
		}
		return // stale board beats a blank view mid-session
	}
	if st.Board == m.kbBoard {
		return
	}
	m.kbBoard = st.Board
	m.kbErr = ""
	if m.kbLayer >= len(st.Board.Layers) {
		m.kbLayer = 0
	}
	m.homeCache.ok = false
}

// handleKeyMsg folds one TypeKey broadcast into the keyboard view state via
// the pure kbview.ApplyKey, then invalidates the home cache when a visible
// keyboard surface changed. The first event lazy-loads the static board.
//
// Wiring (dock.go handleBusMsg): case proto.TypeKey: m.handleKeyMsg(msg)
func (m *model) handleKeyMsg(msg proto.Msg) {
	m.ensureBoard()
	ev := msg.Key
	if ev == nil || m.kbBoard == nil {
		return
	}
	newLayer, changed := kbview.ApplyKey(m.kbBoard, m.kbLayer, ev)
	m.kbLayer = newLayer
	// with kb-live visible every press/release recomposes the whole home
	// frame; a layout switch resets the cache anyway.
	if changed && (m.layoutKind() == "keyboard" || m.kbLiveVisible()) {
		m.homeCache.ok = false
	}
}

// kbLiveVisible reports whether the active layout places a kb-live widget.
// Module-keyed: TypeKey carries no widget id, so invalidateHome's id match
// does not apply.
func (m *model) kbLiveVisible() bool {
	if m.cfg == nil {
		return false
	}
	l, ok := m.cfg.Layouts[m.layout]
	if !ok {
		return false
	}
	for _, r := range l.Regions {
		if m.cfg.Widgets[r.Widget].Render.Module == "kb-live" {
			return true
		}
	}
	return false
}

// kbTheme builds the kbview capability set from the dock chrome + palette:
// chrome styles, the theme background (nil when no palette broadcast -- the
// palette-less render the golden pins), the house accent for the bar band,
// and the identity-hue function.
func (m *model) kbTheme() kbview.Theme {
	bg, _ := m.palette.color("background")
	ac, _ := m.palette.color("color5")
	return kbview.Theme{FG: chromeFG, Dim: chromeDim, Background: bg, Accent: ac, Hue: identityHue}
}

// addKbHits maps the core's returned hits onto the dock tap table, owning the
// side effects the pure core cannot: layer state + homeCache for a selector
// jump, the OS opener for the oryx link.
func (m *model) addKbHits(hits []kbview.Hit) {
	for _, h := range hits {
		a := rect{h.Area.X, h.Area.Y, h.Area.W, h.Area.H}
		switch h.Kind {
		case kbview.HitLayerJump:
			idx := h.Layer
			m.hits = append(m.hits, hitRegion{area: a, do: func(int, int) {
				m.kbLayer = idx
				m.homeCache.ok = false
			}})
		case kbview.HitOryx:
			u := h.URL
			m.hits = append(m.hits, hitRegion{area: a, do: func(int, int) {
				if m.openURL != nil {
					m.openURL(u)
				}
			}})
		}
	}
}

// renderKeyboard draws the fullscreen keyboard view: the tab bar band caps
// the TOP as the panel's interactive header (layer tabs, the board title
// as its note, oryx flush right), the kbview grid fills the rest,
// rebuilding the hit table (selector jumps + oryx from the bar, a bar-band
// cycle target, then a whole-interior cycle target -- or a consume on the
// empty state).
func (m *model) renderKeyboard(bodyH int) string {
	m.ensureBoard()
	m.resetHits()
	th := m.kbTheme()
	body := kbview.Body(m.kbBoard, m.kbErr, m.kbLayer, m.width, bodyH-1, "", m.kbTexture(), keyboard.ErrNoBoard.Error(), th)
	drect := rect{0, 1, m.width, bodyH - 1}
	title := "keyboard"
	if m.kbBoard != nil && m.kbBoard.Title != "" {
		title = "keyboard: " + m.kbBoard.Title
	}
	bar := ""
	if kbview.Empty(m.kbBoard, m.kbErr) {
		m.hits = append(m.hits, hitRegion{area: drect, do: consumeTap})
	} else {
		var barHits []kbview.Hit
		bar, barHits = kbview.Bar(m.kbBoard, m.kbLayer, m.width, title, th)
		m.addKbHits(barHits) // bar-local Y=0 IS the top row
		m.hits = append(m.hits, m.kbCycleHit(rect{0, 0, m.width, 1}))
		m.hits = append(m.hits, m.kbCycleHit(drect))
	}
	return kbview.Panel(title, body, m.width, bodyH, bar, false, kbview.LayerEdge(m.kbBoard, m.kbLayer, th), th)
}

// kbCycleHit is the tap-to-next-layer target both keyboard hosts append after
// the core's selector hits; only valid with a loaded, non-empty board.
func (m *model) kbCycleHit(area rect) hitRegion {
	n := len(m.kbBoard.Layers)
	return hitRegion{area: area, do: func(int, int) {
		m.kbLayer = (m.kbLayer + 1) % n
		m.homeCache.ok = false
	}}
}

// renderKBLive draws the keyboard as a home-region chrome widget: the same
// kbview grid in a side-border panel at rr (left border only when a
// neighbor abuts), the tab bar band capping the TOP as the interactive
// header. The layer-cycle hit covers ONLY the tab-bar row (the board is
// display glass here), and the whole region consumes any other tap
// (appended last; the hit table is first-match). No resetHits: region
// renderers never reset.
func (m *model) renderKBLive(w config.Widget, rr rect) string {
	m.ensureBoard()
	th := m.kbTheme()
	inset := 0
	if rr.x > 0 {
		inset = 1
	}
	mode, _ := w.Render.Params["mode"].(string)
	texture, _ := w.Render.Params["texture"].(string)
	body := kbview.Body(m.kbBoard, m.kbErr, m.kbLayer, rr.w-inset, rr.h-1, mode, texture, keyboard.ErrNoBoard.Error(), th)
	title := w.Title
	if m.kbBoard != nil && m.kbBoard.Title != "" {
		title = "keyboard: " + m.kbBoard.Title
	}
	bar := ""
	if !kbview.Empty(m.kbBoard, m.kbErr) {
		var barHits []kbview.Hit
		bar, barHits = kbview.Bar(m.kbBoard, m.kbLayer, rr.w, title, th)
		for i := range barHits {
			barHits[i].Area.X += rr.x
			barHits[i].Area.Y = rr.y
		}
		m.addKbHits(barHits)
		m.hits = append(m.hits, m.kbCycleHit(rect{rr.x, rr.y, rr.w, 1}))
	}
	box := kbview.Panel(title, body, rr.w, rr.h, bar, inset == 1, kbview.LayerEdge(m.kbBoard, m.kbLayer, th), th)
	m.hits = append(m.hits, hitRegion{area: rr, do: consumeTap})
	return box
}

// kbTexture is the kb-live texture param: the fullscreen keyboard kind has no
// params channel, so it resolves from the config's kb-live widget
// module-keyed, in stable widget order. "" or "none" means the plain fill.
func (m *model) kbTexture() string {
	if m.cfg == nil {
		return ""
	}
	for _, id := range slices.Sorted(maps.Keys(m.cfg.Widgets)) {
		if w := m.cfg.Widgets[id]; w.Render.Module == "kb-live" {
			s, _ := w.Render.Params["texture"].(string)
			return s
		}
	}
	return ""
}

// openWithMacOS hands a URL to LaunchServices; the reap goroutine only
// collects the child (open's own failures surface in its UI, not ours).
func openWithMacOS(u string) {
	cmd := exec.Command("/usr/bin/open", u)
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}
