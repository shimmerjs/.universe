package bus

import (
	"fmt"
	"slices"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

func negotiateView(hello proto.Msg) (proto.ViewCapabilities, error) {
	if hello.Protocol == 0 {
		// Older clients may still display native state during a rollout. They
		// cannot claim physical input or start a renderer without negotiation.
		return proto.ViewCapabilities{Layouts: []string{"home", "keyboard"}}, nil
	}
	if hello.Protocol != proto.Version || hello.Capabilities == nil || len(hello.Capabilities.Layouts) == 0 {
		return proto.ViewCapabilities{}, fmt.Errorf("unsupported view protocol/capabilities: version %d", hello.Protocol)
	}
	caps := *hello.Capabilities
	caps.Layouts = slices.Clone(caps.Layouts)
	return caps, nil
}

func supportsLayout(cfg *config.Config, name string, caps proto.ViewCapabilities) bool {
	layout, ok := cfg.Layouts[name]
	if !ok || !slices.Contains(caps.Layouts, layout.Kind) {
		return false
	}
	for _, id := range layout.WidgetIDs() {
		if cfg.Widgets[id].Render.Kind == "exec" && !caps.Scraped {
			return false
		}
	}
	return true
}

// Caller holds b.mu. Directly constructed test buses can omit capabilities.
func (b *Bus) edgeAvailableLocked() bool {
	if b.dockCaps == nil {
		return len(b.docks) > 0
	}
	for conn := range b.docks {
		if b.dockCaps[conn].Edge {
			return true
		}
	}
	return false
}

func (b *Bus) layoutDemandedLocked(name string) bool {
	if b.dockCaps == nil {
		return len(b.docks) > 0
	}
	for conn := range b.docks {
		if supportsLayout(b.cfg, name, b.dockCaps[conn]) {
			return true
		}
	}
	return false
}
