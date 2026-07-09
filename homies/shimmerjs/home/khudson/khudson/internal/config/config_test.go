package config

import (
	"strings"
	"testing"
)

func TestLoadExample(t *testing.T) {
	c, err := LoadExample()
	if err != nil {
		t.Fatalf("example config must vet: %v", err)
	}
	btop, ok := c.Widgets["btop"]
	if !ok {
		t.Fatal("example missing btop widget")
	}
	if btop.Render.Kind != "exec" || !btop.Render.KeepAlive {
		t.Fatalf("btop render decoded wrong: %#v", btop.Render)
	}
	if btop.Render.IdleKill != "15m" {
		t.Fatalf("idleKill default not applied: %q", btop.Render.IdleKill)
	}
	prs := c.Widgets["github-prs"]
	if prs.Render.Kind != "native" || prs.Render.Module != "github-prs" {
		t.Fatalf("github-prs render decoded wrong: %#v", prs.Render)
	}
	if got := prs.Gestures["tap"]; got.Verb != "view" || got.View != "detail" {
		t.Fatalf("tap gesture decoded wrong: %#v", got)
	}
	if c.Layout != "home" {
		t.Fatalf("layout %q, want home", c.Layout)
	}
	home, ok := c.Layouts["home"]
	if !ok {
		t.Fatal("example missing home layout")
	}
	if home.Kind != "home" || len(home.Regions) != 3 {
		t.Fatalf("home layout decoded wrong: %#v", home)
	}
	rail := home.Regions[0]
	if rail.Widget != "dock-rail" || rail.Edge != "left" || rail.Size != 20 {
		t.Fatalf("rail region decoded wrong: %#v", rail)
	}
	if fill := home.Regions[2]; fill.Widget != "resources" || fill.Edge != "fill" || fill.Size != 0 {
		t.Fatalf("fill region decoded wrong: %#v", fill)
	}
	if !c.Widgets["dock-rail"].Chrome {
		t.Fatal("dock-rail chrome flag not decoded")
	}
	if c.Widgets["btop"].Chrome {
		t.Fatal("chrome must default false")
	}
}

const homePrelude = `
widgets: {
	w: {
		title: "w"
		glyph: "x"
		render: {kind: "native", module: "sysmon"}
	}
	f: {
		title: "f"
		glyph: "x"
		render: {kind: "native", module: "resources"}
	}
}
`

func TestHomeLayoutAccepted(t *testing.T) {
	c, err := Load("t.cue", []byte(homePrelude+`
layouts: main: {
	kind: "home"
	regions: [
		{widget: "w", edge: "left", size: 14},
		{widget: "f", edge: "fill"},
	]
}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	regs := c.Layouts["main"].Regions
	if len(regs) != 2 || regs[0].Widget != "w" || regs[1].Edge != "fill" {
		t.Fatalf("regions decoded wrong: %#v", regs)
	}
}

func TestHomeRejectsNoRegions(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {kind: "home"}
`, "needs regions")
}

func TestHomeRejectsDanglingRegionWidget(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "nope", edge: "fill"}]
}
`, "not a defined widget")
}

func TestHomeRejectsExecRegionWidget(t *testing.T) {
	// exec widgets scrape into hidden windows; home has no blit path
	vetFail(t, homePrelude+`
widgets: e: {
	title: "e"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
}
layouts: main: {
	kind: "home"
	regions: [
		{widget: "e", edge: "left", size: 14},
		{widget: "f", edge: "fill"},
	]
}
`, `layout "main": region 0 widget "e" renders exec`)
}

func TestChromeWidgetDecodes(t *testing.T) {
	c, err := Load("t.cue", []byte(homePrelude+`
widgets: nav: {
	title:  "nav"
	glyph:  "x"
	chrome: true
	render: {
		kind:   "chrome"
		module: "nav-tray"
		params: entries: [{label: "home", target: "home"}]
	}
}
layouts: main: {
	kind: "home"
	regions: [
		{widget: "nav", edge: "right", size: 12},
		{widget: "f", edge: "fill"},
	]
}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := c.Widgets["nav"].Render
	if r.Kind != "chrome" || r.Module != "nav-tray" {
		t.Fatalf("chrome render decoded wrong: %#v", r)
	}
	entries, ok := r.Params["entries"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("entries decoded wrong: %#v", r.Params["entries"])
	}
}

// The strip block vets and decodes: unknown entry targets are allowed on
// purpose (the "soon" flash is the stub affordance).
func TestStripBlockVets(t *testing.T) {
	c, err := Load("t.cue", []byte(homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: {
	entries: [
		{label: "kb", target: "keyboard"},
		{label: "sys", target: "sys"},
	]
	toggles: [{kind: "caffeinate", on: "a", off: "b"}]
}
`))
	if err != nil {
		t.Fatalf("valid strip rejected: %v", err)
	}
	if c.Strip == nil || len(c.Strip.Entries) != 2 || len(c.Strip.Toggles) != 1 {
		t.Fatalf("strip decoded wrong: %#v", c.Strip)
	}
	if e := c.Strip.Entries[0]; e.Label != "kb" || e.Target != "keyboard" {
		t.Fatalf("entry decoded wrong: %#v", e)
	}
	if tg := c.Strip.Toggles[0]; tg.Kind != "caffeinate" || tg.On != "a" || tg.Off != "b" {
		t.Fatalf("toggle decoded wrong: %#v", tg)
	}
}

func TestStripRejectsEmptyLabel(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: entries: [{label: "", target: "home"}]
`, "needs a label")
}

func TestStripRejectsEmptyTarget(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: entries: [{label: "kb", target: ""}]
`, "needs a target")
}

// The dock's flash registry namespaces keys on ":" ("tab:<label>"): a
// colon in a strip label could collide with another entry's key.
func TestStripRejectsColonLabel(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: entries: [{label: "kb:x", target: "home"}]
`, `label "kb:x" must not contain ":"`)
}

// Labels are the flash-registry identity ("soon" key + "tab:<label>");
// duplicates would flash together on one tap.
func TestStripRejectsDuplicateLabel(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: entries: [
	{label: "kb", target: "home"},
	{label: "kb", target: "main"},
]
`, `duplicate entry label "kb"`)
}

func TestStripRejectsKindlessToggle(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: toggles: [{kind: ""}]
`, "needs a kind")
}

// the chrome module enum does not admit nav-pills; nav lives in the
// top-level strip block.
func TestVetRejectsNavPillsModule(t *testing.T) {
	vetFail(t, homePrelude+`
widgets: nav: {
	title:  "nav"
	glyph:  "x"
	chrome: true
	render: {kind: "chrome", module: "nav-pills"}
}
layouts: main: {
	kind: "home"
	regions: [
		{widget: "nav", edge: "bottom", size: 3},
		{widget: "f", edge: "fill"},
	]
}
`, "")
}

func TestVetRejectsSignalModule(t *testing.T) {
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "native", module: "signal"}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`, "")
}

func TestHomeRejectsSizelessEdge(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [
		{widget: "w", edge: "left"},
		{widget: "f", edge: "fill"},
	]
}
`, "needs a size")
}

func TestHomeRejectsEdgeAfterFill(t *testing.T) {
	// the fill split is one pass: nothing peels after it
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [
		{widget: "f", edge: "fill"},
		{widget: "w", edge: "left", size: 14},
	]
}
`, "follows a fill region")
}

func TestHomeRejectsBadEdge(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "w", edge: "middle", size: 2}]
}
`, "")
}

func TestHomeRejectsNegativeSize(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "w", edge: "left", size: -3}]
}
`, "")
}

func TestRegionsRejectedOutsideHome(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "dock-grid"
	tiles: ["w"]
	regions: [{widget: "w", edge: "fill"}]
}
`, "home-only")
}

func TestPollDefaults(t *testing.T) {
	c, err := Load("t.cue", []byte(`
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	r := c.Widgets["w"].Render
	if r.Poll != "1s" || r.KeepAlive || r.IdleKill != "15m" {
		t.Fatalf("exec lifecycle defaults wrong: %#v", r)
	}
}

func vetFail(t *testing.T, src, wantSub string) {
	t.Helper()
	_, err := Load("t.cue", []byte(src))
	if err == nil {
		t.Fatalf("config accepted, want rejection containing %q", wantSub)
	}
	if wantSub != "" && !strings.Contains(err.Error(), wantSub) {
		t.Fatalf("error %q does not mention %q", err, wantSub)
	}
}

func TestVetRejectsTargetlessSendKey(t *testing.T) {
	// effectful verbs require a target
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
	gestures: tap: {verb: "send-key", keys: "q"}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`, "")
}

// send-key targets must name a defined exec widget: the bus's input worker
// drops misses silently, so vet is the only loud failure point.
func TestCheckRejectsSendKeyToUndefinedWidget(t *testing.T) {
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
	gestures: tap: {verb: "send-key", keys: "q", target: "hud-window:nope"}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`, "send-key target")
}

func TestCheckRejectsSendKeyToNativeWidget(t *testing.T) {
	vetFail(t, `
widgets: {
	w: {
		title: "w"
		glyph: "x"
		render: {kind: "exec", argv: ["true"]}
		gestures: tap: {verb: "send-key", keys: "q", target: "hud-window:n"}
	}
	n: {
		title: "n"
		glyph: "y"
		render: {kind: "native", module: "cpumem"}
	}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`, "send-key target")
}

func TestCheckRejectsSendKeyToSelf(t *testing.T) {
	// the schema admits "self" but handleAction rejects it: vet must too
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
	gestures: tap: {verb: "send-key", keys: "q", target: "self"}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`, "send-key target")
}

func TestSendKeyToExecWidgetLoads(t *testing.T) {
	_, err := Load("t.cue", []byte(`
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
	gestures: tap: {verb: "send-key", keys: "q", target: "hud-window:w"}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestVetRejectsSubFloorPoll(t *testing.T) {
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"], poll: "100ms"}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`, "")
}

func TestVetRejectsUnknownVerb(t *testing.T) {
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
	gestures: tap: {verb: "explode"}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`, "")
}

func TestVetRejectsUnknownModule(t *testing.T) {
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "native", module: "not-a-module"}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`, "")
}

func TestVetRejectsBadWidgetID(t *testing.T) {
	vetFail(t, `
widgets: "Bad_ID": {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
}
layouts: main: {kind: "full-panel", tiles: []}
`, "")
}

func TestCheckRejectsDanglingTile(t *testing.T) {
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
}
layouts: main: {kind: "dock-grid", tiles: ["nope"]}
`, "not a defined widget")
}

func TestCheckRejectsMissingLayout(t *testing.T) {
	vetFail(t, `
widgets: w: {
	title: "w"
	glyph: "x"
	render: {kind: "exec", argv: ["true"]}
}
layouts: other: {kind: "full-panel", tiles: ["w"]}
`, "not defined")
}

// The strip flip pair vets and decodes; the chevron targets must name
// defined layouts.
func TestStripFlipVets(t *testing.T) {
	c, err := Load("t.cue", []byte(homePrelude+`
layouts: {
	main: {
		kind: "home"
		regions: [{widget: "f", edge: "fill"}]
	}
	"main-no-kb": {
		kind: "home"
		regions: [{widget: "w", edge: "fill"}]
	}
}
strip: flip: {expanded: "main", collapsed: "main-no-kb"}
`))
	if err != nil {
		t.Fatalf("valid flip rejected: %v", err)
	}
	f := c.Strip.Flip
	if f == nil || f.Expanded != "main" || f.Collapsed != "main-no-kb" {
		t.Fatalf("flip decoded wrong: %#v", f)
	}
}

func TestStripFlipRejectsUnknownLayout(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: flip: {expanded: "main", collapsed: "nope"}
`, `flip layout "nope" is not defined`)
}

func TestStripFlipRejectsHalfPair(t *testing.T) {
	// both members are required inside the optional struct: CUE vet alone
	// rejects a half pair, before the Go layout check runs
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: flip: {expanded: "main"}
`, "")
}

// kb-live requires no params: a bare declaration vets clean.
func TestKBLiveWithoutParamsVets(t *testing.T) {
	_, err := Load("t.cue", []byte(homePrelude+`
widgets: kb: {
	title:  "kb"
	glyph:  "x"
	chrome: true
	render: {kind: "chrome", module: "kb-live"}
}
layouts: main: {
	kind: "home"
	regions: [
		{widget: "kb", edge: "right", size: 75},
		{widget: "f", edge: "fill"},
	]
}
`))
	if err != nil {
		t.Fatalf("target-less kb-live rejected: %v", err)
	}
}

func TestKBLiveRejectsUnknownMode(t *testing.T) {
	vetFail(t, homePrelude+`
widgets: kb: {
	title:  "kb"
	glyph:  "x"
	chrome: true
	render: {
		kind:   "chrome"
		module: "kb-live"
		params: {mode: "compct"}
	}
}
layouts: main: {
	kind: "home"
	regions: [
		{widget: "kb", edge: "bottom", size: 9},
		{widget: "f", edge: "fill"},
	]
}
`, `param "mode" must be "full" or "compact"`)
}

func TestClaudePanelRejectsNonIntegerMax(t *testing.T) {
	vetFail(t, homePrelude+`
widgets: cp: {
	title: "cp"
	glyph: "x"
	render: {kind: "native", module: "claude-panel", params: {max: 2.5}}
}
layouts: main: {
	kind: "home"
	regions: [{widget: "cp", edge: "fill"}]
}
`, `param "max" must be an integer`)
}

func TestClaudePanelRejectsNonPositiveMax(t *testing.T) {
	vetFail(t, homePrelude+`
widgets: cp: {
	title: "cp"
	glyph: "x"
	render: {kind: "native", module: "claude-panel", params: {max: 0}}
}
layouts: main: {
	kind: "home"
	regions: [{widget: "cp", edge: "fill"}]
}
`, `param "max" must be positive`)
}

func TestWidgetParamChecksAcceptValid(t *testing.T) {
	_, err := Load("t.cue", []byte(homePrelude+`
widgets: {
	kb: {
		title:  "kb"
		glyph:  "x"
		chrome: true
		render: {
			kind:   "chrome"
			module: "kb-live"
			params: {mode: "compact"}
		}
	}
	cp: {
		title: "cp"
		glyph: "x"
		render: {kind: "native", module: "claude-panel", params: {max: 5}}
	}
}
layouts: main: {
	kind: "home"
	regions: [
		{widget: "kb", edge: "bottom", size: 9},
		{widget: "cp", edge: "right", size: 20},
		{widget: "f", edge: "fill"},
	]
}
`))
	if err != nil {
		t.Fatalf("valid params rejected: %v", err)
	}
}

func TestStripFlipRejectsSelfPair(t *testing.T) {
	vetFail(t, homePrelude+`
layouts: main: {
	kind: "home"
	regions: [{widget: "f", edge: "fill"}]
}
strip: flip: {expanded: "main", collapsed: "main"}
`, "name the same layout")
}

func TestKBLiveRejectsUnknownTexture(t *testing.T) {
	vetFail(t, homePrelude+`
widgets: kb: {
	title:  "kb"
	glyph:  "x"
	chrome: true
	render: {
		kind:   "chrome"
		module: "kb-live"
		params: {texture: "plaid"}
	}
}
layouts: main: {
	kind: "home"
	regions: [
		{widget: "kb", edge: "right", size: 75},
		{widget: "f", edge: "fill"},
	]
}
`, `param "texture" must be one of`)
}

// Texture grammar: none | <recipe> | <recipe>:<density>, density
// sparse|dense. Unknown recipes and densities fail with the vocabulary
// listed.
func TestKBLiveTextureGrammar(t *testing.T) {
	src := func(tex string) string {
		return homePrelude + `
widgets: kb: {
	title:  "kb"
	glyph:  "x"
	chrome: true
	render: {
		kind:   "chrome"
		module: "kb-live"
		params: {texture: "` + tex + `"}
	}
}
layouts: main: {
	kind: "home"
	regions: [
		{widget: "kb", edge: "right", size: 75},
		{widget: "f", edge: "fill"},
	]
}
`
	}
	for _, tex := range []string{
		"none", "dots", "dots:sparse", "line-grid:dense", "oct-dot",
		"circle-small", "dots-column", "grabber", "dots-grid", "crosshair",
		"dot-grid",
	} {
		if _, err := Load("t.cue", []byte(src(tex))); err != nil {
			t.Errorf("texture %q rejected: %v", tex, err)
		}
	}
	for _, tex := range []string{
		"shade", "hatch", "dense-hatch", "static", "dots:mega", "bogus", ":dense",
	} {
		vetFail(t, src(tex), `param "texture" must be one of`)
	}
}
