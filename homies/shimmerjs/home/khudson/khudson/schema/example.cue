// Example khudson config: a scraped system monitor, a native PR list, and
// an exec cheatsheet. `khudson config vet schema/example.cue` accepts it;
// `khudson bus` / `khudson dock` fall back to it when no -config is given.
package khudson

widgets: {
	btop: {
		title: "system"
		glyph: "\uf085"
		render: {
			kind: "exec"
			argv: ["btop", "--force-utf"]
			poll:      "1s"
			keepAlive: true
		}
		gestures: {
			"swipe-left": {verb: "send-key", keys: "right", target: "hud-window:btop"}
			"swipe-right": {verb: "send-key", keys: "left", target: "hud-window:btop"}
		}
	}
	"github-prs": {
		title: "pull requests"
		glyph: "\uf113"
		render: {
			kind:   "native"
			module: "github-prs"
			poll:   "60s"
			views: ["list", "detail"]
			params: {
				search: "is:open review-requested:@me"
				limit:  20
			}
		}
		gestures: {
			tap: {verb: "view", view: "detail"}
			"long-press": {verb: "open-url", url: "https://github.com/pulls"}
			"swipe-right": {verb: "back"}
		}
	}
	cheatsheets: {
		title: "cheatsheets"
		glyph: "\uf02d"
		render: {
			kind: "exec"
			argv: ["clod-cheat"]
			poll:     "2s"
			idleKill: "5m"
		}
		gestures: {
			"long-press": {verb: "run", argv: ["open", "https://sw.kovidgoyal.net/kitty/"], target: "self"}
		}
	}
	"dock-rail": {
		title:  "dock"
		glyph:  "\uf313"
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
	"nav-tray": {
		title:  "nav"
		glyph:  "\uf0c9"
		chrome: true
		render: {
			kind:   "chrome"
			module: "nav-tray"
			params: {
				entries: [
					{label: "home", target: "home"},
					{label: "claude", target: "claude"},
				]
				// state toggles pinned to the tray bottom: the caffeinate
				// cup, filled (nf-md-coffee) while the bus holds the
				// assertion, outline (nf-md-coffee_outline) when off;
				// tap toggles
				toggles: [
					{kind: "caffeinate", on: "\U000F0176", off: "\U000F06CA"},
				]
			}
		}
	}
	resources: {
		title: "resources"
		glyph: "\uf2db"
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
	// full-fill claude control panel behind the nav-tray "claude" target:
	// pinned detail zone (one expanded session, header+outcome tap to
	// focus) over a fixed-order collapsed list. Rows exec `khudson claude
	// focus <sid>` on the bus host.
	"claude-panel": {
		title: "claude"
		glyph: ""
		render: {
			kind:   "native"
			module: "claude-panel"
			poll:   "3s"
			params: {
				window: "6h"
			}
		}
	}
	// main-kitty window list over the RC socket. params.socket defaults to
	// the khudson state root's main-kitty.sock (passwordless socket-only
	// trust; the socket file's mode is the auth).
	"kitty-sessions": {
		title: "kitty"
		glyph: "\uf120"
		render: {
			kind:   "native"
			module: "kitty-sessions"
			poll:   "5s"
		}
	}
}

// `khudson ctl theme night` applies these colors to the HUD kitty (black
// floor, high contrast -- survives low DDC luminance) and dims the panel;
// day resets the kitty to its startup theme and restores luminance.
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
		night: 10
		day:   60
	}
}

// bus-owned caffeinate: ON at bus start; the tray cup and `khudson ctl
// caffeinate` toggle it at runtime (schema #Caffeinate documents why the
// static power config stays unset).
caffeinate: on: true

layouts: {
	home: {
		kind: "home"
		// peel order: rail off the left, tray off the right, fills split
		// the remainder
		regions: [
			{widget: "dock-rail", edge: "left", size: 20},
			{widget: "nav-tray", edge: "right", size: 12},
			{widget: "resources", edge: "fill"},
		]
	}
	// claude control panel: a home-kind layout whose single fill region IS
	// the panel -- zero new dock code; the always-on brand tap leads home.
	claude: {
		kind: "home"
		regions: [
			{widget: "claude-panel", edge: "fill"},
		]
	}
}

layout: "home"
