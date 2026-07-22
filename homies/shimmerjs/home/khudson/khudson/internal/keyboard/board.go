// Board is the static, offline layout model the keyboard view renders: the
// deployed revision's layers, each key placed on the Moonlander geometry
// with its resolved tap/hold legends. The build itself is static -- once
// from an Oryx layout payload (Loader resolves it from the local caches or
// the network), working unplugged; Held is the one live join point, the
// overlay a renderer draws on top (design doctrine: static view = static
// only, live view = static + HID overlay).
package keyboard

import (
	"strconv"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/keydict"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
)

// Board is a parsed layout: a title, its revision id, and the layers.
// Held is the live-highlight overlay -- slot index -> currently pressed --
// fed by the bus TypeKey stream; FromLayout leaves it nil (allocated
// lazily by the writer) and the static render ignores a nil map.
// LayoutID and Geometry are the layout's Oryx slug and board geometry --
// together with RevisionID they address the layout in the configure.zsa.io
// web configurator.
type Board struct {
	Title      string
	RevisionID string
	LayoutID   string
	Geometry   string
	Layers     []Layer
	Held       map[int]bool
}

// Layer is one layer's title and its 72 placed keys, in slot order.
type Layer struct {
	Title string
	Keys  []PlacedKey
}

// PlacedKey is one key on the geometry: its slot plus the resolved legends.
// Tap is the primary legend; Hold is the secondary (mod-tap / layer-tap), ""
// when absent. TapLayer/HoldLayer name a layer index when the binding is a
// layer switch (MO/LT/OSL/TO), -1 otherwise -- the view tints those.
type PlacedKey struct {
	Slot      Slot
	Tap       string
	Hold      string
	TapLayer  int
	HoldLayer int
}

// FromLayout builds a Board from an Oryx layout payload, resolving every
// key's legends through dict. Layers with an empty title get a positional
// fallback name.
func FromLayout(l *oryx.Layout, dict keydict.Dict) *Board {
	b := &Board{}
	if l != nil {
		b.Title = l.Title
		b.RevisionID = l.RevisionID
		b.LayoutID = l.HashID
		b.Geometry = l.Geometry
	}
	slots := MoonlanderSlots()
	for i, ly := range layersOf(l) {
		layer := Layer{Title: layerTitle(ly.Title, i)}
		for pos, key := range ly.Keys {
			if pos >= len(slots) {
				break
			}
			layer.Keys = append(layer.Keys, placeKey(slots[pos], key, dict))
		}
		b.Layers = append(b.Layers, layer)
	}
	return b
}

func layersOf(l *oryx.Layout) []oryx.Layer {
	if l == nil {
		return nil
	}
	return l.Layers
}

func layerTitle(t string, i int) string {
	if t != "" && t != "Layer" {
		return t
	}
	return "L" + strconv.Itoa(i)
}

// placeKey resolves one Oryx key's tap/hold slots into display legends.
func placeKey(slot Slot, k oryx.Key, dict keydict.Dict) PlacedKey {
	pk := PlacedKey{Slot: slot, TapLayer: -1, HoldLayer: -1}
	// a customLabel overrides the tap legend entirely (the user's own text)
	switch {
	case k.CustomLabel != "":
		pk.Tap = k.CustomLabel
		if k.Tap != nil && k.Tap.Layer != nil {
			pk.TapLayer = *k.Tap.Layer
		}
	case k.Tap != nil:
		pk.Tap = dict.Legend(k.Tap.Code)
		if k.Tap.Layer != nil {
			pk.TapLayer = *k.Tap.Layer
		}
	}
	if k.Hold != nil {
		pk.Hold = dict.Legend(k.Hold.Code)
		if k.Hold.Layer != nil {
			pk.HoldLayer = *k.Hold.Layer
		}
	}
	return pk
}
