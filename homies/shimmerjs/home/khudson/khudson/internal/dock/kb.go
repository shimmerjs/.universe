// Keyboard layout kind: the dock's imperative shell over the kbview
// functional core (internal/keyboard/kbview). This file owns the I/O and
// host wiring -- loading the board (ensureBoard), folding TypeKey broadcasts
// into view state (handleKeyMsg), building the theme from the dock palette,
// mapping the core's hits onto the dock tap table, and homeCache
// invalidation. The pure render + event fold live in kbview so the same
// keyboard viewer drives the dock, a standalone terminal client, or any host.
package dock

import (
	"maps"
	"os"
	"os/exec"
	"slices"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/kbview"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/keymappdb"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// ensureBoard loads the static board once. Since kb-live sits on the default
// home layout the first attempt fires at dock startup, so a MISSING store
// must not latch: stay unlatched behind a cheap stat and adopt a later
// Keymapp sync without a dock restart. A store that exists but fails to load
// still latches (no per-frame re-parse of a corrupt DB). Never fatal.
func (m *model) ensureBoard() {
	if m.kbLoaded {
		return
	}
	path, err := keymappdb.DefaultPath()
	if err != nil {
		m.kbLoaded = true
		m.kbErr = err.Error()
		return
	}
	if _, err := os.Stat(path); err != nil {
		m.kbErr = err.Error()
		return
	}
	m.kbLoaded = true
	m.kbErr = ""
	rev, err := keymappdb.Active(path)
	if err != nil {
		m.kbErr = err.Error()
		return
	}
	m.kbBoard = keyboard.FromRevision(rev)
	if m.kbBoard == nil || len(m.kbBoard.Layers) == 0 {
		m.kbErr = "layout has no layers"
	}
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
// palette-less render the golden pins), and the identity-hue function.
func (m *model) kbTheme() kbview.Theme {
	bg, _ := m.palette.color("background")
	return kbview.Theme{FG: chromeFG, Dim: chromeDim, Background: bg, Hue: identityHue}
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

// renderKeyboard draws the fullscreen keyboard view: the kbview body in a
// titled box, rebuilding the hit table (selector jumps + oryx from the core,
// then a whole-interior cycle target -- or a consume on the empty state).
func (m *model) renderKeyboard(bodyH int) string {
	m.ensureBoard()
	m.resetHits()
	th := m.kbTheme()
	interior := kbview.Rect{X: 1, Y: 1, W: m.width - 2, H: bodyH - 2}
	body, hits := kbview.Body(m.kbBoard, m.kbErr, m.kbLayer, interior, "", m.kbTexture(), keymappdb.ErrNoRevision.Error(), th)
	m.addKbHits(hits)
	drect := rect{1, 1, m.width - 2, bodyH - 2}
	if kbview.Empty(m.kbBoard, m.kbErr) {
		m.hits = append(m.hits, hitRegion{area: drect, do: consumeTap})
	} else {
		m.hits = append(m.hits, m.kbCycleHit(drect))
	}
	title := "keyboard"
	if m.kbBoard != nil && m.kbBoard.Title != "" {
		title = "keyboard: " + m.kbBoard.Title
	}
	return kbview.TitledBox(title, body, m.width, bodyH, kbview.LayerEdge(m.kbBoard, m.kbLayer, th), th)
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
// kbview body in a titled box at rr. The layer-cycle hit covers ONLY the
// selector row (the board is display glass here), and the whole region
// consumes any other tap (appended last; the hit table is first-match). No
// resetHits: region renderers never reset.
func (m *model) renderKBLive(w config.Widget, rr rect) string {
	m.ensureBoard()
	th := m.kbTheme()
	interior := kbview.Rect{X: rr.x + 1, Y: rr.y + 1, W: rr.w - 2, H: rr.h - 2}
	mode, _ := w.Render.Params["mode"].(string)
	texture, _ := w.Render.Params["texture"].(string)
	body, hits := kbview.Body(m.kbBoard, m.kbErr, m.kbLayer, interior, mode, texture, keymappdb.ErrNoRevision.Error(), th)
	m.addKbHits(hits)
	title := w.Title
	if m.kbBoard != nil && m.kbBoard.Title != "" {
		title = "keyboard: " + m.kbBoard.Title
	}
	if !kbview.Empty(m.kbBoard, m.kbErr) {
		m.hits = append(m.hits, m.kbCycleHit(rect{interior.X, interior.Y, interior.W, 1}))
	}
	box := kbview.TitledBox(title, body, rr.w, rr.h, kbview.LayerEdge(m.kbBoard, m.kbLayer, th), th)
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
