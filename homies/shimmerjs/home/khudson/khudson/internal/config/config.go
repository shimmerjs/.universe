// Package config decodes khudson CUE configs through unification with the
// embedded schema: user value & schema.#Config, validated concrete, then
// decoded. Violations surface as real cue errors, not zero values.
package config

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/errors"

	"github.com/shimmerjs/khudson/khudson/schema"
)

// Config is the decoded, vetted configuration.
type Config struct {
	Widgets map[string]Widget `json:"widgets"`
	Layouts map[string]Layout `json:"layouts"`
	Layout  string            `json:"layout"`
	// Strip is the strip-hosted nav band (tab entries + toggles); nil
	// renders a status-only strip -- the chrome home icon stays regardless.
	Strip *Strip `json:"strip,omitempty"`
	// Theme is the bus-owned theme service config; nil when the config has
	// no theme block (theme switches then leave the kitty colors alone).
	Theme *Theme `json:"theme,omitempty"`
	// Caffeinate is the bus caffeinate supervisor's boot state; nil when the
	// config has no caffeinate block (which still means ON -- see
	// CaffeinateOn).
	Caffeinate *Caffeinate `json:"caffeinate,omitempty"`
}

// Strip is the strip-hosted nav band: tab entries between the chrome-owned
// home icon and the state toggles on the dock's status strip.
type Strip struct {
	Entries []StripEntry  `json:"entries,omitempty"`
	Toggles []StripToggle `json:"toggles,omitempty"`
	// Flip is the collapse/expand chevron between the tabs and the toggles;
	// nil renders no chevron.
	Flip *StripFlip `json:"flip,omitempty"`
}

// StripFlip is the flip chevron's layout pair: the chevron renders only
// while the active layout is one of the two, and its tap navigates to the
// other. Both names must be defined layouts (checkStrip).
type StripFlip struct {
	Expanded  string `json:"expanded"`
	Collapsed string `json:"collapsed"`
}

// StripEntry is one strip tab: label on glass, target layout on tap.
// Unknown targets are allowed -- the "soon" flash is the stub affordance.
type StripEntry struct {
	Label  string `json:"label"`
	Target string `json:"target"`
}

// StripToggle is one strip state toggle; Kind names the bus state it
// reflects ("caffeinate" is the only kind). Unset glyphs take the dock's
// cup defaults; unknown kinds render dead so a config ahead of the binary
// stays visible, never silent.
type StripToggle struct {
	Kind string `json:"kind"`
	On   string `json:"on,omitempty"`
	Off  string `json:"off,omitempty"`
}

// Caffeinate configures the bus-owned caffeinate supervisor
// (internal/bus/caffeinate.go): On is only the state at bus start; `ctl
// caffeinate on|off|toggle` and the tray cup move it at runtime.
type Caffeinate struct {
	On bool `json:"on"`
}

// CaffeinateOn is the boot state for the caffeinate supervisor: default ON
// even without a config block -- the Edge is a HUD, its host must not sleep
// out from under it. This runtime-toggled assertion supersedes the
// still-unapplied static power.sleep.display=never idea from nix/edge-host.md:
// runtime wins (a static never-sleep could not be toggled off from the
// glass), so the static power config stays unset.
func (c *Config) CaffeinateOn() bool {
	if c.Caffeinate == nil {
		return true
	}
	return c.Caffeinate.On
}

// Theme configures `ctl theme day|night`: the night palette applied to the
// HUD kitty via set-colors (day resets to the kitty's startup theme) and
// the paired m1ddc luminance move.
type Theme struct {
	Night     NightTheme     `json:"night"`
	Luminance ThemeLuminance `json:"luminance"`
}

// NightTheme is the night color override set: kitty color names (kitty.conf
// syntax: foreground, background, color0..) to "#rrggbb". Day needs no
// counterpart -- the HUD kitty starts on the day theme include, so day is a
// set-colors reset, never a hand-inlined palette.
type NightTheme struct {
	Colors map[string]string `json:"colors,omitempty"`
}

// ThemeLuminance pairs display backlight with the theme; bin/display follow
// the brightness module's params.
type ThemeLuminance struct {
	Bin     string `json:"bin"`
	Display string `json:"display"`
	Night   int    `json:"night"`
	Day     int    `json:"day"`
}

// Widget is one tile-able unit, native or scraped-exec.
type Widget struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Glyph string `json:"glyph"`
	// Chrome widgets draw their own frames; the home renderer skips the
	// titled region border for them.
	Chrome   bool              `json:"chrome"`
	Render   Render            `json:"render"`
	Gestures map[string]Action `json:"gestures,omitempty"`
}

// Render is the flattened exec|native disjunction; Kind says which fields
// are live.
type Render struct {
	Kind string `json:"kind"` // exec | native | chrome

	// exec
	Argv      []string `json:"argv,omitempty"`
	KeepAlive bool     `json:"keepAlive,omitempty"`
	IdleKill  string   `json:"idleKill,omitempty"`

	// native
	Module string         `json:"module,omitempty"`
	Views  []string       `json:"views,omitempty"`
	Params map[string]any `json:"params,omitempty"`

	Poll string `json:"poll,omitempty"`
}

// PollInterval parses Poll; the schema guarantees it parses and respects
// the per-kind floor.
func (r Render) PollInterval() time.Duration {
	d, err := time.ParseDuration(r.Poll)
	if err != nil {
		return time.Second
	}
	return d
}

// IdleKillAfter parses IdleKill for exec widgets; zero for native.
func (r Render) IdleKillAfter() time.Duration {
	if r.IdleKill == "" {
		return 0
	}
	d, err := time.ParseDuration(r.IdleKill)
	if err != nil {
		return 0
	}
	return d
}

// Action is one gesture binding; effectful verbs carry Target.
type Action struct {
	Verb   string   `json:"verb"`
	View   string   `json:"view,omitempty"`
	URL    string   `json:"url,omitempty"`
	Keys   string   `json:"keys,omitempty"`
	Argv   []string `json:"argv,omitempty"`
	Target string   `json:"target,omitempty"`
}

// Layout is a dock view state instance; engines live in Go.
type Layout struct {
	Kind  string   `json:"kind"` // dock-grid | full-panel | tray | home | keyboard
	Tiles []string `json:"tiles"`
	Panel string   `json:"panel,omitempty"`
	// Regions is home-only; order is peel order, fill regions split the
	// remainder.
	Regions []Region `json:"regions,omitempty"`
}

// WidgetIDs returns every widget id the layout references (tiles, then
// region widgets), deduped in first-appearance order. New widget-carrying
// layout fields must be added here.
func (l Layout) WidgetIDs() []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, id := range l.Tiles {
		add(id)
	}
	for _, r := range l.Regions {
		add(r.Widget)
	}
	return ids
}

// Region is one home-layout slot peeled off the content box.
type Region struct {
	Widget string `json:"widget"`
	Edge   string `json:"edge"`           // left | right | top | bottom | fill
	Size   int    `json:"size,omitempty"` // cells: cols for left/right, rows for top/bottom
}

// Load vets src against the schema and decodes it. filename is for error
// positions only.
func Load(filename string, src []byte) (*Config, error) {
	v, err := vet(filename, src)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := v.Decode(&c); err != nil {
		return nil, fmt.Errorf("decode %s: %s", filename, cueDetails(err))
	}
	if err := c.check(); err != nil {
		return nil, fmt.Errorf("%s: %w", filename, err)
	}
	return &c, nil
}

// LoadFile reads and Loads path.
func LoadFile(path string) (*Config, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(path, src)
}

// LoadExample loads the embedded example config.
func LoadExample() (*Config, error) {
	return Load("schema/example.cue", schema.Example)
}

func vet(filename string, src []byte) (cue.Value, error) {
	ctx := cuecontext.New()

	sv := ctx.CompileBytes(schema.Schema, cue.Filename("khudson.cue"))
	if err := sv.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("embedded schema broken: %s", cueDetails(err))
	}
	def := sv.LookupPath(cue.ParsePath("#Config"))
	if err := def.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("embedded schema missing #Config: %s", cueDetails(err))
	}

	uv := ctx.CompileBytes(src, cue.Filename(filename))
	if err := uv.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("parse %s: %s", filename, cueDetails(err))
	}

	res := def.Unify(uv)
	if err := res.Validate(cue.Concrete(true)); err != nil {
		return cue.Value{}, fmt.Errorf("vet %s: %s", filename, cueDetails(err))
	}
	return res, nil
}

// check enforces the referential rules the schema does not express.
func (c *Config) check() error {
	if len(c.Layouts) == 0 {
		return fmt.Errorf("no layouts defined")
	}
	if _, ok := c.Layouts[c.Layout]; !ok {
		return fmt.Errorf("layout %q is not defined in layouts", c.Layout)
	}
	for name, l := range c.Layouts {
		if l.Kind == "dock-grid" && len(l.Tiles) > 8 {
			return fmt.Errorf("layout %q: dock-grid holds at most 8 tiles, got %d", name, len(l.Tiles))
		}
		for _, id := range l.WidgetIDs() {
			if _, ok := c.Widgets[id]; !ok {
				return fmt.Errorf("layout %q: widget %q is not a defined widget", name, id)
			}
		}
		if l.Panel != "" {
			if _, ok := c.Widgets[l.Panel]; !ok {
				return fmt.Errorf("layout %q: panel %q is not a defined widget", name, l.Panel)
			}
		}
		if err := c.checkRegions(name, l); err != nil {
			return err
		}
	}
	for id, w := range c.Widgets {
		if err := c.checkWidgetParams(id, w); err != nil {
			return err
		}
	}
	// send-key gesture targets must name a defined exec widget: the bus's
	// input worker drops misses silently, so a dead target has to fail vet
	for id, w := range c.Widgets {
		for g, a := range w.Gestures {
			if a.Verb != "send-key" {
				continue
			}
			tid, ok := strings.CutPrefix(a.Target, "hud-window:")
			tw, defined := c.Widgets[tid]
			if !ok || !defined || tw.Render.Kind != "exec" {
				return fmt.Errorf("widget %q: gesture %q: send-key target %q is not a defined exec widget", id, g, a.Target)
			}
		}
	}
	return c.checkStrip()
}

// checkStrip validates the strip nav band the way the dock's trayActivate
// treats it: an unknown entry target is allowed (it flashes "soon"), but an
// empty label or target is a dead tab -- rejected so `khudson config vet`
// fails the closure build -- and a ":" in a label (or a duplicate label)
// would collide in the dock's ":"-namespaced flash registry. A toggle must name its kind; unknown kinds are
// allowed (rendered dead, a config ahead of the binary stays visible). The
// flip pair must name defined layouts on both sides -- a chevron whose tap
// leads nowhere is a silently dead control.
func (c *Config) checkStrip() error {
	if c.Strip == nil {
		return nil
	}
	labels := make(map[string]bool, len(c.Strip.Entries))
	for i, e := range c.Strip.Entries {
		if e.Label == "" {
			return fmt.Errorf("strip: entry %d needs a label", i)
		}
		if strings.Contains(e.Label, ":") {
			// the dock's flash registry namespaces keys on ":" ("tab:<label>");
			// a colon label could collide with another entry's key or demote
			// its flash window
			return fmt.Errorf("strip: entry %d label %q must not contain \":\"", i, e.Label)
		}
		if labels[e.Label] {
			// labels are the flash-registry identity ("soon" key + "tab:<label>");
			// duplicates flash together on one tap
			return fmt.Errorf("strip: duplicate entry label %q", e.Label)
		}
		labels[e.Label] = true
		if e.Target == "" {
			return fmt.Errorf("strip: entry %d (%q) needs a target", i, e.Label)
		}
	}
	for i, tg := range c.Strip.Toggles {
		if tg.Kind == "" {
			return fmt.Errorf("strip: toggle %d needs a kind", i)
		}
	}
	if f := c.Strip.Flip; f != nil {
		for _, name := range []string{f.Expanded, f.Collapsed} {
			if name == "" {
				return fmt.Errorf("strip: flip needs both expanded and collapsed layouts")
			}
			if _, ok := c.Layouts[name]; !ok {
				return fmt.Errorf("strip: flip layout %q is not defined in layouts", name)
			}
		}
		if f.Expanded == f.Collapsed {
			// a chevron flipping a layout onto itself is a permanently
			// dead control that vet exists to reject
			return fmt.Errorf("strip: flip expanded and collapsed name the same layout %q", f.Expanded)
		}
	}
	return nil
}

// checkWidgetParams enforces the per-module param rules the schema's open
// params cannot express: kb-live mode must be a known render mode, and
// claude-panel max must be a positive integer -- caught here so
// `khudson config vet` fails the closure build instead of the typo being
// silently swallowed at poll time (module.IntParam).
func (c *Config) checkWidgetParams(id string, w Widget) error {
	switch w.Render.Module {
	case "kb-live":
		if v, ok := w.Render.Params["mode"]; ok {
			if s, isStr := v.(string); !isStr || (s != "full" && s != "compact") {
				return fmt.Errorf("widget %q: param \"mode\" must be \"full\" or \"compact\", got %v", id, v)
			}
		}
		if v, ok := w.Render.Params["texture"]; ok {
			if s, isStr := v.(string); !isStr || !validKBTexture(s) {
				return fmt.Errorf("widget %q: param \"texture\" must be one of \"none\", %s, each optionally \":sparse\" or \":dense\" suffixed, got %v",
					id, kbTextureVocab(), v)
			}
		}
	case "claude-panel":
		if v, ok := w.Render.Params["max"]; ok {
			n, ok := intParam(v)
			if !ok {
				return fmt.Errorf("widget %q: param \"max\" must be an integer, got %v", id, v)
			}
			if n < 1 {
				return fmt.Errorf("widget %q: param \"max\" must be positive, got %d", id, n)
			}
		}
	}
	return nil
}

// KBTextureRecipes is the kb-live texture vocabulary (dock kbTexCellFn
// mirrors it): each takes an optional ":sparse"/":dense" density suffix,
// bare = normal density.
var KBTextureRecipes = []string{
	"dots", "oct-dot", "circle-small", "dots-column", "grabber",
	"dots-grid", "crosshair", "dot-grid", "line-grid",
}

// validKBTexture reports whether s is "none", a recipe, or a
// recipe:density in the kb-live texture grammar.
func validKBTexture(s string) bool {
	if s == "none" {
		return true
	}
	recipe, density, has := strings.Cut(s, ":")
	if has && density != "sparse" && density != "dense" {
		return false
	}
	return slices.Contains(KBTextureRecipes, recipe)
}

// kbTextureVocab lists the recipe vocabulary for the vet error.
func kbTextureVocab() string {
	quoted := make([]string, len(KBTextureRecipes))
	for i, r := range KBTextureRecipes {
		quoted[i] = strconv.Quote(r)
	}
	return strings.Join(quoted, ", ")
}

// intParam extracts an integer from the numeric shapes CUE decoding produces
// (int/int64/float64); a fractional float64 is not an integer.
func intParam(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

// checkRegions enforces the home-layout region rules: widgets exist, edged
// regions carry a size, and fill regions trail every edged region (the fill
// split is a single pass over whatever the peels leave).
func (c *Config) checkRegions(name string, l Layout) error {
	if l.Kind != "home" {
		if len(l.Regions) > 0 {
			return fmt.Errorf("layout %q: regions are home-only, kind is %q", name, l.Kind)
		}
		return nil
	}
	if len(l.Regions) == 0 {
		return fmt.Errorf("layout %q: home layout needs regions", name)
	}
	fillSeen := false
	for i, r := range l.Regions {
		w, ok := c.Widgets[r.Widget]
		if !ok {
			return fmt.Errorf("layout %q: region %d widget %q is not a defined widget", name, i, r.Widget)
		}
		if w.Render.Kind == "exec" {
			return fmt.Errorf("layout %q: region %d widget %q renders exec (scraped); home regions take native or chrome widgets", name, i, r.Widget)
		}
		if r.Edge == "fill" {
			fillSeen = true
			continue
		}
		if fillSeen {
			return fmt.Errorf("layout %q: region %d (%s %q) follows a fill region; fill regions come last", name, i, r.Edge, r.Widget)
		}
		if r.Size < 1 {
			return fmt.Errorf("layout %q: region %d (%s %q) needs a size in cells", name, i, r.Edge, r.Widget)
		}
	}
	return nil
}

func cueDetails(err error) string {
	return errors.Details(err, nil)
}
