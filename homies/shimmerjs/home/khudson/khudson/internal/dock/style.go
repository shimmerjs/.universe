// Style derivation. Base chrome is indexed-ANSI / default fg-bg: the kitty
// theme IS the theme, and the first frame must render correctly before any
// palette broadcast arrives. Beyond-ANSI accents -- the heat ramp, the gauge
// gradient, blended emphasis tones -- derive from the bus TypeTheme palette
// (the HUD kitty's effective colors) via lipgloss CIELAB blends, so they sit
// adjacent to the terminal theme instead of fighting it. A nil palette or a
// missing/unparseable key falls back to the indexed-ANSI default for that
// tone only -- never to a hardcoded truecolor.
package dock

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// palette is the bus-broadcast HUD kitty palette: kitty color name ->
// "#rrggbb" (bus get-colors, see TypeTheme). nil = no broadcast yet.
type palette map[string]string

// day is the pre-broadcast default: no palette, so every style stays
// indexed-ANSI until the bus TypeTheme lands.
var day palette

// color returns the named palette entry when present and parseable.
func (p palette) color(name string) (color.Color, bool) {
	v := p[name]
	if !strings.HasPrefix(v, "#") {
		return nil, false
	}
	c := lipgloss.Color(v)
	if _, bad := c.(lipgloss.NoColor); bad {
		return nil, false
	}
	return c, true
}

// blend derives the tone t (0..1) of the way from palette color a toward b.
func (p palette) blend(a, b string, t float64) (color.Color, bool) {
	ca, ok := p.color(a)
	if !ok {
		return nil, false
	}
	cb, ok := p.color(b)
	if !ok {
		return nil, false
	}
	return blendToward(ca, cb, t), true
}

// blendToward picks the tone t (0..1) along the CIELAB ramp from a to b.
func blendToward(a, b color.Color, t float64) color.Color {
	const steps = 32
	ramp := lipgloss.Blend1D(steps, a, b)
	i := int(min(max(t, 0), 1)*float64(steps-1) + 0.5)
	return ramp[i]
}

// Blend distances: emphasis tones pull their hue toward the theme foreground
// so they read as text, not decoration; the gauge track lifts just off the
// background so empty extent stays visible.
const (
	brandBlend    = 0.25
	emphasisBlend = 0.35
	trackBlend    = 0.18
)

// Attention border ramp: warn crawling to dim chrome over attentionRampLen
// steps, advancing one step per dock tick.
const (
	attentionRampLen  = 10
	attentionDimBlend = 0.35
)

// attentionRamp is the marching attention border's foreground ramp. Nil when
// the palette lacks either tone -- the renderer falls back to the indexed
// warn/dim alternation.
func (m *model) attentionRamp() []color.Color {
	warn, ok := m.palette.color("color3")
	if !ok {
		return nil
	}
	dim, ok := m.palette.blend("background", "foreground", attentionDimBlend)
	if !ok {
		return nil
	}
	return lipgloss.Blend1D(attentionRampLen, warn, dim)
}

// styles is the strip-level style set: indexed base, brand emphasis derived
// from the palette when one has been broadcast.
type styles struct {
	strip lipgloss.Style
	warn  lipgloss.Style
	brand lipgloss.Style
}

func buildStyles(p palette) styles {
	s := styles{
		strip: chromeDim,
		warn:  chromeWarn,
		brand: lipgloss.NewStyle().Foreground(lipgloss.Green).Bold(true),
	}
	if c, ok := p.blend("color2", "foreground", brandBlend); ok {
		s.brand = lipgloss.NewStyle().Foreground(c).Bold(true)
	}
	return s
}

// newRowStyles derives the render-time row vocabulary from the broadcast
// palette. Text tones (fg/dim/warn) stay indexed base chrome; heat, the
// gauge, and the accent/highlight emphasis tones derive, each falling back
// to its indexed default independently when its palette inputs are missing.
func newRowStyles(p palette) rowStyles {
	ss := chromeRowStyles
	if c, ok := p.color("color2"); ok {
		ss.heat[0] = lipgloss.NewStyle().Foreground(c)
		ss.gaugeFill = lipgloss.NewStyle().Background(c)
	}
	if c, ok := p.color("color3"); ok {
		ss.heat[1] = lipgloss.NewStyle().Foreground(c)
	}
	if c, ok := p.color("color1"); ok {
		ss.heat[2] = lipgloss.NewStyle().Foreground(c)
	}
	if c, ok := p.blend("background", "foreground", trackBlend); ok {
		ss.gaugeTrack = lipgloss.NewStyle().Background(c)
	}
	if c, ok := p.blend("color2", "foreground", emphasisBlend); ok {
		ss.accent = lipgloss.NewStyle().Foreground(c)
	}
	if c, ok := p.blend("color6", "foreground", emphasisBlend); ok {
		ss.highlight = lipgloss.NewStyle().Foreground(c).Bold(true)
	}
	// the gauge gradient needs the whole cool -> hot ramp; partial palettes
	// keep the flat fill
	if g, okG := p.color("color2"); okG {
		if y, okY := p.color("color3"); okY {
			if r, okR := p.color("color1"); okR {
				ss.gaugeStops = []color.Color{g, y, r}
			}
		}
	}
	return ss
}
