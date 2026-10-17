package bus

import (
	"crypto/rand"
	"errors"
	"net"
	"slices"

	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

type viewSession struct {
	id         string
	generation uint64
	layout     string
	hidden     bool
	writable   bool
}

var errViewLease = errors.New("view session or generation retired")

func (b *Bus) addViewLocked(conn net.Conn, hello proto.Msg, caps proto.ViewCapabilities) {
	if b.views == nil {
		b.views = make(map[net.Conn]*viewSession)
	}
	v := &viewSession{id: rand.Text(), generation: 1, layout: hello.Layout, hidden: hello.Hidden, writable: hello.Protocol == proto.Version}
	v.layout = b.compatibleLayoutLocked(v.layout, caps)
	b.views[conn] = v
}

func (b *Bus) compatibleLayoutLocked(name string, caps proto.ViewCapabilities) string {
	if supportsLayout(b.cfg, name, caps) {
		return name
	}
	if supportsLayout(b.cfg, b.cfg.Layout, caps) {
		return b.cfg.Layout
	}
	names := make([]string, 0, len(b.cfg.Layouts))
	for name := range b.cfg.Layouts {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if supportsLayout(b.cfg, name, caps) {
			return name
		}
	}
	return ""
}

// A lease belongs to one connection. Reconnection, hiding, navigation, config
// replacement and renderer replacement retire queued actions from that view.
func (b *Bus) bindViewAction(conn net.Conn, m *proto.Msg) bool {
	b.mu.Lock()
	v := b.views[conn]
	ok := v != nil && v.writable && !v.hidden && m.ViewID == v.id && m.ViewGeneration == v.generation
	if !ok && b.docks[conn] != nil {
		b.writeDockLocked(conn, b.docks[conn], proto.Msg{Type: proto.TypeActionStatus, OperationID: m.OperationID, OperationState: "discarded", Error: errViewLease.Error()})
	}
	b.mu.Unlock()
	if !ok {
		diagnostic.Default.Observe("view.action.lease", errViewLease, "discard; synchronize view")
		b.discardInput(*m, errViewLease.Error())
	}
	return ok
}

func (b *Bus) viewActionCurrentLocked(m proto.Msg) bool {
	for conn, v := range b.views {
		if v.id != m.ViewID {
			continue
		}
		ok := b.docks[conn] != nil && v.writable && !v.hidden && v.generation == m.ViewGeneration
		if ok && m.Widget != "" {
			ok = slices.Contains(b.cfg.Layouts[v.layout].WidgetIDs(), m.Widget)
		}
		if ok {
			return true
		}
		break
	}
	diagnostic.OperationGeneration("view.action.lease", m.OperationID, m.ViewGeneration, "discarded", "view no longer owns this action")
	return false
}

func (b *Bus) updateView(conn net.Conn, m proto.Msg) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v := b.views[conn]
	// Visibility reports describe the current terminal, even if a layout ack
	// crossed them in transit. Identity still must match this connection.
	if v == nil || b.docks[conn] == nil || m.ViewID != v.id {
		return
	}
	if v.hidden != m.Hidden {
		v.hidden = m.Hidden
		v.generation++
		b.notifyRelaysLocked()
		diagnostic.OperationGeneration("view.visibility", v.id, v.generation, "changed", "reconcile demand")
	}
	b.writeDockLocked(conn, b.docks[conn], proto.Msg{Type: proto.TypeViewState, Hidden: v.hidden})
}

func (b *Bus) setViewLayout(m proto.Msg) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.viewActionCurrentLocked(m) {
		return errViewLease
	}
	for conn, v := range b.views {
		if v.id != m.ViewID {
			continue
		}
		if !supportsLayout(b.cfg, m.Arg, b.dockCaps[conn]) {
			return errors.New("layout unsupported by this view")
		}
		if v.layout != m.Arg {
			v.layout = m.Arg
			v.generation++
		}
		b.notifyRelaysLocked()
		b.writeDockLocked(conn, b.docks[conn], proto.Msg{Type: proto.TypeLayout, Layout: v.layout})
		return nil
	}
	return errViewLease
}

func (b *Bus) invalidateViewsLocked() {
	for conn, v := range b.views {
		v.generation++
		v.layout = b.compatibleLayoutLocked(v.layout, b.dockCaps[conn])
	}
	b.notifyRelaysLocked()
}

func (b *Bus) visibleWidgetsLocked() map[string]bool {
	visible := make(map[string]bool)
	if b.views == nil {
		if b.layoutDemandedLocked(b.cfg.Layout) {
			for _, id := range b.cfg.Layouts[b.cfg.Layout].WidgetIDs() {
				visible[id] = true
			}
		}
		return visible
	}
	for conn, v := range b.views {
		if v.hidden || b.docks[conn] == nil || !supportsLayout(b.cfg, v.layout, b.dockCaps[conn]) {
			continue
		}
		for _, id := range b.cfg.Layouts[v.layout].WidgetIDs() {
			visible[id] = true
		}
	}
	return visible
}

func (b *Bus) relayDemandLocked() [3]bool {
	if b.views == nil {
		return [3]bool{b.edgeAvailableLocked(), len(b.docks) > 0, len(b.docks) > 0}
	}
	var demand [3]bool
	for conn, v := range b.views {
		if v.hidden || b.docks[conn] == nil {
			continue
		}
		demand[0] = demand[0] || b.dockCaps[conn].Edge
		demand[1] = demand[1] || b.cfg.Layouts[v.layout].Kind == "keyboard"
		for _, id := range b.cfg.Layouts[v.layout].WidgetIDs() {
			demand[1] = demand[1] || b.cfg.Widgets[id].Render.Module == "kb-live"
		}
		// Battery is displayed in every integrated view's status strip.
		demand[2] = true
	}
	return demand
}
