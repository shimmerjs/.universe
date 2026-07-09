// khudson live config: composed home screen on the Edge. Shipped by
// module.nix as ~/.config/khudson/edge.cue: @-tokens are templated at build
// (replaceVars) and the rendered file is vetted against the schema embedded
// in the same khudson build, so config/schema drift fails the closure instead
// of the bus silently falling back to the embedded example at runtime.
package khudson

widgets: {
	"dock-rail": {
		title:  "dock"
		glyph:  ""
		chrome: true
		render: {
			kind:   "native"
			module: "dock-mirror"
			poll:   "5s"
			params: {
				nicknames: {"Google Chrome": "chrome", "Telegram": "tg"}
			}
		}
	}
	// left-column claude strip AND the full-fill control panel behind the
	// strip's "clod" tab: pinned detail zone (one expanded session;
	// header+outcome tap = focus) over the fixed-order collapsed list. Row
	// Acts exec `khudson claude focus <sid>` on the bus host; resume stays
	// staged behind the M9 allowlist.
	"claude-panel": {
		title: "claude"
		glyph: ""
		render: {
			kind:   "native"
			module: "claude-panel"
			poll:   "3s"
			params: {
				dir:    "@claudeSpool@"
				window: "6h"
				max:    10
			}
		}
	}
	// live keyboard region: the fullscreen keyboard view's selector strip +
	// active layer grid as a home-region widget, TypeKey highlights included.
	// Chrome (no bus module, no poll): the dock renders it from the Keymapp
	// store and the push-based key broadcasts. Hiding/restoring this column
	// is the strip's flip chevron (strip.flip below), not a widget
	// affordance -- the widget renders wherever a layout places it.
	"kb-live": {
		title:  "keyboard"
		glyph:  ""
		chrome: true
		render: {
			kind:   "chrome"
			module: "kb-live"
			// fill texture on non-base layers: none | <recipe>[:<density>],
			// density sparse | dense (bare recipe = normal). Recipe vocabulary
			// lives in internal/config (KBTextureRecipes); the build-time
			// `khudson config vet` in module.nix lists it on any bad value.
			params: {texture: "none"}
		}
	}
	resources: {
		title: "resources"
		glyph: ""
		render: {
			kind:   "native"
			module: "resources"
			poll:   "5s"
			params: {
				volumes: ["/"]
				window:  "6h"
				top:     10
			}
		}
	}
	btop: {
		title: "system monitor"
		glyph: ""
		render: {
			kind: "exec"
			argv: ["/usr/bin/env", "XDG_CONFIG_HOME=@btopCfgDir@", "@btop@", "--force-utf"]
			poll:      "1s"
			keepAlive: true
		}
	}
	sysmon: {
		title: "sysmon"
		glyph: ""
		render: {
			kind:   "native"
			module: "sysmon"
			poll:   "2s"
		}
	}
	"github-prs": {
		title: "pull requests"
		glyph: ""
		render: {
			kind:   "native"
			module: "github-prs"
			poll:   "120s"
			params: {
				search: "is:open author:@me"
				limit:  8
			}
		}
		gestures: {
			"long-press": {verb: "open-url", url: "https://github.com/pulls"}
		}
	}
	media: {
		title: "media"
		glyph: ""
		render: {
			kind: "exec"
			argv: ["@spotatui@"]
			poll:      "1s"
			keepAlive: true
		}
		gestures: {
			"swipe-left": {verb: "send-key", keys: "right", target: "hud-window:media"}
			"swipe-right": {verb: "send-key", keys: "left", target: "hud-window:media"}
		}
	}
	brightness: {
		title: "brightness"
		glyph: ""
		render: {
			kind:   "native"
			module: "brightness"
			poll:   "30s"
			params: {
				bin:     "@m1ddc@"
				display: "XENEON EDGE"
			}
		}
	}
	// main-kitty window list over the M9 RC socket; declaration only, not
	// in any layout yet (visual exposure is a later milestone). socket and
	// passwordFile default in Go to the state root's main-kitty.sock and
	// ~/.config/kitty/rc-password.conf.
	"kitty-sessions": {
		title: "kitty"
		glyph: ""
		render: {
			kind:   "native"
			module: "kitty-sessions"
			poll:   "5s"
		}
	}
}

// bus-owned theme service: `khudson ctl theme night` re-colors the HUD kitty
// via set-colors (black floor, high contrast -- survives low DDC luminance)
// and drops Edge luminance over DDC; day resets the kitty to its startup
// theme include and restores luminance.
theme: {
	night: colors: {
		background: "#000000"
		foreground: "#e6dcbe"
		color2:     "#b8d493"
		color3:     "#eccf8e"
		color8:     "#a8b0a0"
		color10:    "#b8d493"
		color11:    "#eccf8e"
	}
	luminance: {
		bin:     "@m1ddc@"
		display: "XENEON EDGE"
		night:   10
		day:     60
	}
}

// bus-owned caffeinate (display + idle assertions): ON at bus start; the
// strip cup and `khudson ctl caffeinate` toggle it at runtime. Supersedes the
// still-unapplied static power.sleep.display=never idea from edge-host.md --
// runtime wins, the static config stays unset.
caffeinate: on: true

// strip-hosted nav: tabs between the chrome-owned home icon and the
// caffeinate cup on the 2-row status strip. The home icon is chrome (homeTap
// resolves by layout KIND), never an entry. "sys" names no layout ON
// PURPOSE -- the "soon" flash IS the stub. Cup glyphs: filled (nf-md-coffee)
// while the bus holds the assertion, outline (nf-md-coffee_outline) when
// off; tap toggles. flip is the collapse/expand chevron beside the tabs:
// it hides/restores the kb column by flipping between the named layouts.
strip: {
	entries: [
		{label: "kb", target: "keyboard"},
		{label: "clod", target: "claude"},
		{label: "sys", target: "sys"},
	]
	toggles: [
		{kind: "caffeinate", on: "\U000F0176", off: "\U000F06CA"},
	]
	flip: {expanded: "home", collapsed: "home-no-kb"}
}

layouts: {
	home: {
		kind: "home"
		// peel order, load-bearing: kb-live right (75 cols: 73-col interior
		// is the exact uncropped floor -- kbKeyW(73)=4 at MainCols=7), then
		// claude panel right (73), then the left column stacks rail /
		// resources (196x24 glass -> 194x20 interior, stripH=2; left column
		// 194-75-73 = 46 cols: rail 46x8, resources 46x12).
		// SYNC with home-no-kb below: the two home-kind layouts share
		// claude-panel / dock-rail / resources; widget changes must land
		// in both region lists. kb-live is home-only -- the strip flip
		// hides the whole column.
		regions: [
			{widget: "kb-live", edge: "right", size: 75},
			{widget: "claude-panel", edge: "right", size: 73},
			{widget: "dock-rail", edge: "top", size: 8},
			{widget: "resources", edge: "fill"},
		]
	}
	// collapsed home variant behind the strip's flip chevron: NO keyboard,
	// claude-panel takes the freed width (148 = home's 73 + kb's 75).
	// SYNC with home above (shared widgets claude-panel / dock-rail /
	// resources; kb-live is now home-only). Not the config default, so the
	// dock wraps it in the right-edge return strip and the layout runs at
	// interior-3 cols (191 on this glass).
	"home-no-kb": {
		kind: "home"
		regions: [
			{widget: "claude-panel", edge: "right", size: 148},
			{widget: "dock-rail", edge: "top", size: 8},
			{widget: "resources", edge: "fill"},
		]
	}
	// static all-layers Moonlander view; the strip's "kb" tab targets
	// it. No tiles/regions: a bespoke full-region renderer.
	keyboard: {
		kind: "keyboard"
	}
	// claude control panel: home-kind, single fill region -- zero new dock
	// code; the home strip leads back. Behind the strip's "clod" tab.
	claude: {
		kind: "home"
		regions: [
			{widget: "claude-panel", edge: "fill"},
		]
	}
}

layout: "home"
