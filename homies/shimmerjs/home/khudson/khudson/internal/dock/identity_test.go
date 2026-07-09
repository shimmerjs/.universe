package dock

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/module"
)

var hueLabels = []string{"red", "green", "yellow", "blue", "magenta", "cyan",
	"bright-red", "bright-green", "bright-yellow", "bright-blue",
	"bright-magenta", "bright-cyan"}

func hueLabel(c color.Color) string {
	for i, h := range identityHues {
		if h == c {
			return hueLabels[i]
		}
	}
	return fmt.Sprintf("%v", c)
}

// Same key = same hue on every call, always inside the identity palette.
func TestIdentityHueStable(t *testing.T) {
	keys := []string{"kitty", "safari", "chrome", "khudson", "keymapp", "telegram"}
	for _, key := range keys {
		a, b := identityHue(key), identityHue(key)
		if a != b {
			t.Errorf("identityHue(%q) flickered: %v then %v", key, a, b)
		}
		found := false
		for _, h := range identityHues {
			if h == a {
				found = true
			}
		}
		if !found {
			t.Errorf("identityHue(%q) = %v, outside the identity palette", key, a)
		}
		t.Logf("hue %-10s -> %s", key, hueLabel(a))
	}
	distinct := map[color.Color]bool{}
	for _, key := range keys {
		distinct[identityHue(key)] = true
	}
	if len(distinct) < 2 {
		t.Error("degenerate hash: every demo key landed on one hue")
	}
}

// Explicit overrides beat the hash: hex, ANSI names, and ANSI numbers.
func TestIdentityOverridePrecedence(t *testing.T) {
	ov := map[string]string{"kitty": "#fb4934", "slack": "bright-magenta", "mail": "4"}
	c := identityColor("kitty", ov)
	r, g, b, _ := c.RGBA()
	if uint8(r>>8) != 0xfb || uint8(g>>8) != 0x49 || uint8(b>>8) != 0x34 {
		t.Errorf("hex override = %v, want #fb4934", c)
	}
	if got := identityColor("slack", ov); got != lipgloss.BrightMagenta {
		t.Errorf("name override = %v, want bright-magenta", got)
	}
	if got := identityColor("mail", ov); got != lipgloss.Blue {
		t.Errorf("numeric override = %v, want ANSI 4 (blue)", got)
	}
	if got := identityColor("unlisted", ov); got != identityHue("unlisted") {
		t.Errorf("missing override must fall back to the hash, got %v", got)
	}
}

// Running rail tiles carry the identity hue on the name (borderless dense
// grid: state is color, not frame); params.colors hex overrides beat the
// hash.
func TestRailIdentityColors(t *testing.T) {
	m := newHomeModel(320, 18)
	widenRail(m, 22)
	w := m.cfg.Widgets["dock-rail"]
	w.Render.Params = map[string]any{"colors": map[string]any{"Mail": "#ff0000"}}
	m.cfg.Widgets["dock-rail"] = w
	m.widgetData["dock-rail"] = railData()
	lines := strings.Split(m.renderHome(17), "\n")

	// bordered tiles: the name sits on the band's middle row
	hue := lipgloss.NewStyle().Foreground(identityHue("safari"))
	if !strings.Contains(lines[2], hue.Render("safari")) {
		t.Errorf("safari name not identity-hued: %q", lines[2])
	}
	over := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff0000"))
	if !strings.Contains(lines[2], over.Render("mail")) {
		t.Errorf("mail hex override not applied: %q", lines[2])
	}
}

// A span Ident sets the hue; StyleTitle keeps bold; the row's live/stale
// tone modulates on top (bold live, faint stale). Ident absent = the prior
// behavior exactly.
func TestSpanIdentityComposition(t *testing.T) {
	mk := func(rowStyle string) module.Row {
		r := module.SpansRow(module.Span{Text: "kraken", Style: module.StyleTitle, Ident: "kraken"})
		r.Style = rowStyle
		return r
	}
	lines, _ := renderChromeRows(module.Data{Rows: []module.Row{
		mk(module.StyleAccent), mk(module.StyleDim),
	}}, 60, 5, chromeRowStyles)
	hue := lipgloss.NewStyle().Foreground(identityHue("kraken"))
	if want := hue.Bold(true).Render("kraken"); !strings.Contains(lines[0], want) {
		t.Errorf("live name = %q, want bold identity hue %q", lines[0], want)
	}
	if want := hue.Faint(true).Render("kraken"); !strings.Contains(lines[1], want) {
		t.Errorf("stale name = %q, want faint identity hue %q", lines[1], want)
	}
	if ansi.Strip(lines[0]) != ansi.Strip(lines[1]) {
		t.Error("hue changed the text itself")
	}
	if lines[0] == lines[1] {
		t.Error("liveness no longer reads over the hue")
	}

	plain := module.SpansRow(module.Span{Text: "kraken", Style: module.StyleTitle})
	plain.Style = module.StyleAccent
	pl, _ := renderChromeRows(module.Data{Rows: []module.Row{plain}}, 60, 5, chromeRowStyles)
	if want := chromeAccent.Bold(true).Render("kraken"); !strings.Contains(pl[0], want) {
		t.Errorf("Ident-less title changed: %q, want %q", pl[0], want)
	}
}
