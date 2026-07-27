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
				nicknames: {"Google Chrome": "chrome", "Telegram": "tg", "QuickTime Player": "qt", "Calendar": "cal"}
			}
		}
	}
	// left-column claude strip AND the full-fill control panel behind the
	// strip's "clod" tab: pinned detail zone (one expanded session;
	// header+outcome tap = focus) over the fixed-order collapsed list.
	// Lists ONLY sessions whose registry pid is a live process (the
	// live-kitty gate; no window param -- age never hides a live one). Row
	// Acts exec `khudson claude focus <sid>` on the bus host; resume is
	// CLI-only (no row publishes it).
	"claude-panel": {
		title: "clod"
		glyph: ""
		render: {
			kind:   "native"
			module: "claude-panel"
			poll:   "3s"
			params: {
				dir: "@claudeSpool@"
				max: 10
			}
		}
	}
	// live keyboard region: the fullscreen keyboard view's layer tab bar +
	// active layer grid as a home-region widget, TypeKey highlights included.
	// Chrome (no bus module, no poll): the dock resolves the board off the
	// USB serial (oryx cache / generations store behind it) and the
	// push-based key broadcasts. The widget renders wherever a layout places
	// it; swiping the column up trades it for the MX viewer (home-logi),
	// which is a layout switch, not a widget affordance.
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
				// the card shows no processes; five feed the tap bloom
				top: 5
				// disk reads neutral until free space drops toward this
				// floor (GiB); the whole disk row heats by free space,
				// never by used-fraction. 80 is a taste-pass setting so
				// the warm band is visible on this host (~54G free);
				// dial back to 40 once seen
				"free-floor": 80
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
	// main-kitty window list over the RC socket; declaration only, not
	// in any layout yet (visual exposure is a later milestone). socket
	// defaults in Go to the state root's main-kitty.sock (socket-only
	// trust, passwordless).
	"kitty-sessions": {
		title: "kitty"
		glyph: ""
		render: {
			kind:   "native"
			module: "kitty-sessions"
			poll:   "5s"
		}
	}
	// reno demo presenter/recorder: two widgets, one module, discriminated
	// by params.view; each fills its own layout behind the strip's "reno"
	// tab (tap flips presenter<->recorder via the entry's alt). 1s poll =
	// 1Hz timer chips while a reno layout is active; zero polls otherwise
	// (visibility-gated) -- the run itself lives in module state driven by
	// `khudson reno` ctl verbs and glass taps.
	"reno-presenter": {
		title:  "reno"
		glyph:  ""
		chrome: true
		render: {
			kind:   "native"
			module: "reno"
			poll:   "1s"
			params: {view: "presenter"}
		}
	}
	"reno-recorder": {
		title:  "reno rec"
		glyph:  ""
		chrome: true
		render: {
			kind:   "native"
			module: "reno"
			poll:   "1s"
			params: {view: "recorder"}
		}
	}
	// logiretch viewer: the on-glass MX Master 4 (dock twin of the
	// standalone `logiretch` TUI). 1s poll = the highlight decay clock
	// while its layout shows; zero polls otherwise (visibility-gated).
	// The module holds logiretch.sock open itself.
	"logiretch-view": {
		title:  "logiretch"
		glyph:  "\U000F037D"
		chrome: true
		render: {
			kind:   "native"
			module: "logiretch"
			poll:   "1s"
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
// caffeinate cup on the one-row strip band. The home icon is chrome (homeTap
// resolves by layout KIND), never an entry. "sys" lands the monitor layout
// (fullscreen btop) -- the same destination the resources bloom's
// double-tap converts to. Cup glyphs: filled (nf-md-coffee)
// while the bus holds the assertion, outline (nf-md-coffee_outline) when
// off; tap toggles.
strip: {
	entries: [
		// reno leads the strip. alt makes the tab a pair: active on either
		// reno layout, tap flips to the other side. The RECORDER (the
		// control pad) is the first tap -- it is the half reached for
		// without a script loaded -- and altLabel names the side showing,
		// so the presenter half is not hidden behind an identical tab.
		{label: "reno", target: "reno-recorder", alt: "reno-presenter", altLabel: "notes"},
		{label: "kb", target: "keyboard"},
		{label: "clod", target: "claude"},
		{label: "sys", target: "monitor"},
	]
	toggles: [
		{kind: "caffeinate", on: "\U000F0176", off: "\U000F06CA"},
	]
}

layouts: {
	home: {
		kind: "home"
		// peel order, load-bearing: kb-live right (75 cols: >=73 interior
		// cols keep the grid uncropped -- kbKeyW(73)=4 at MainCols=7; the
		// side panel spends 1 on the hairline), then claude panel right
		// (73), then the left column stacks rail / resources (196x24 glass
		// -> 196x23 body, stripH=1, no outer frame; left column 196-75-73 =
		// 48 cols: resources bottom 48x6 -- the vitals card, content ~32
		// cols left-anchored; rail left 33x17 = 5 tile rows x 3 flush
		// max-width tiles, pinned to the card's read so the column shares
		// one width. The 15x17 remainder right of the rail stays blank:
		// clawed-back space reserved for coming layout changes.
		// SYNC with home-logi below: both home-kind layouts share
		// claude-panel / dock-rail / resources; widget changes must land in
		// both region lists. kb-live is home-only -- home-logi swaps that
		// column for the MX viewer.
		regions: [
			// swipe up on the keyboard column swaps that column alone to the
			// MX viewer (home-logi); the rest of the screen holds
			{widget: "kb-live", edge: "right", size: 75, swipeUp: "home-logi"},
			{widget: "claude-panel", edge: "right", size: 73},
			{widget: "resources", edge: "bottom", size: 6},
			{widget: "dock-rail", edge: "left", size: 33},
		]
	}
	// home with the keyboard column swapped for the MX viewer: reached by
	// swiping up on that column, swipe down on the viewer restores the
	// keyboard. A sibling LAYOUT rather than a dock-local widget
	// substitution, so the bus's widget visibility (and logiretch-view's
	// polling) follows the switch.
	// SYNC with home above: same peel order and sizes, kb-live -> logiretch-view.
	"home-logi": {
		kind: "home"
		regions: [
			{widget: "logiretch-view", edge: "right", size: 75, swipeDown: "home"},
			{widget: "claude-panel", edge: "right", size: 73},
			{widget: "resources", edge: "bottom", size: 6},
			{widget: "dock-rail", edge: "left", size: 33},
		]
	}
	// static all-layers Moonlander view; the strip's "kb" tab targets
	// it. No tiles/regions: a bespoke full-region renderer.
	keyboard: {
		kind: "keyboard"
		// swipe up on the keyboard view lands on the logiretch mouse
		// viewer (its inverse -- swipe down -- returns here)
		swipeUp: "logiretch"
	}
	// logiretch mouse viewer: home-kind single-fill (the claude-layout
	// precedent, zero new dock engine). Reached by swipe-up from the
	// keyboard view; swipe down returns.
	logiretch: {
		kind: "home"
		regions: [
			{widget: "logiretch-view", edge: "fill"},
		]
		swipeDown: "keyboard"
	}
	// claude control panel: home-kind, single fill region -- zero new dock
	// code; the home strip leads back. Behind the strip's "clod" tab.
	claude: {
		kind: "home"
		regions: [
			{widget: "claude-panel", edge: "fill"},
		]
	}
	// fullscreen system monitor (the scraped btop, full-panel live):
	// behind the strip's "sys" tab AND the resources bloom's double-tap;
	// the strip leads back.
	monitor: {
		kind:  "full-panel"
		panel: "btop"
	}
	// reno demo views: home-kind single-fill (the claude-layout precedent,
	// zero new dock engine), one per view, paired at the strip tab.
	"reno-presenter": {
		kind: "home"
		regions: [
			{widget: "reno-presenter", edge: "fill"},
		]
	}
	"reno-recorder": {
		kind: "home"
		regions: [
			{widget: "reno-recorder", edge: "fill"},
		]
	}
}

layout: "home"
