// khudson config contract. Closed vocabularies on purpose: new verbs,
// gestures, and native modules are Go changes.
package khudson

import "time"

// Two-finger variants only fire once the digitizer mode switch lands;
// single-touch setups just never see them.
#Gesture: "tap" | "long-press" | "drag" |
	"swipe-left" | "swipe-right" | "swipe-up" | "swipe-down" |
	"two-finger-swipe-left" | "two-finger-swipe-right" |
	"two-finger-swipe-up" | "two-finger-swipe-down"

// every effectful verb names its referent
#Target: "self" | =~"^hud-window:[a-z][a-z0-9-]*$" | =~"^main-kitty:.+$"

#Action: {verb: "focus", target: #Target} |
	{verb: "view", view: string} |
	{verb: "open-url", url: =~"^https?://"} |
	{verb: "send-key", keys: string, target: #Target} |
	{verb: "run", argv: [string, ...string], target: #Target} |
	{verb: "back"}


#Duration: string & time.Duration

// native modules compiled into khudson
#Module: "github-prs" | "claude-sessions" | "claude-panel" | "kitty-sessions" |
	"cheatsheets" | "demo-mode" | "brightness" | "sysmon" | "dock-mirror" |
	"media" | "cpumem" | "disk" | "procs" | "resources"

// chrome modules: no bus module; the dock renders them purely from config
// params
#ChromeModule: "nav-tray" | "kb-live"

// strip-hosted nav band: tab entries and state toggles hosted on the dock's
// status strip (chrome, not a region widget). The home icon is chrome-owned,
// never an entry. Unknown entry targets are allowed on purpose -- the "soon"
// flash IS the stub affordance; unknown toggle kinds render dead so a config
// ahead of the binary stays visible. flip is the collapse/expand chevron
// between the tabs and the toggles: expanded/collapsed name the layout pair
// it flips between -- both required together (a half pair fails vet here;
// Go checks the names against layouts).
#Strip: {
	entries: [...{label: string, target: string}] | *[]
	toggles: [...{kind: string, on?: string, off?: string}] | *[]
	flip?: {expanded: string, collapsed: string}
}

#ExecRender: {
	kind: "exec"
	argv: [string, ...string]
	poll:      #Duration | *"1s"
	keepAlive: bool | *false
	idleKill:  #Duration | *"15m"
	// scrape cadence floor: 250ms
	_pollNS: time.ParseDuration(poll)
	_pollNS: >=250_000_000
}

#NativeRender: {
	kind:   "native"
	module: #Module
	poll:   #Duration | *"5s"
	// native backends poll no faster than 1s; rate budgets live in the bus
	_pollNS: time.ParseDuration(poll)
	_pollNS: >=1_000_000_000
	views?: [...string]
	params: {...}
}

#ChromeRender: {
	kind:   "chrome"
	module: #ChromeModule
	params: {...}
}

#Widget: {
	id:    =~"^[a-z][a-z0-9-]*$"
	title: string
	glyph: string // nerd-font codepoint; png icons reserved for v2
	// chrome widgets draw their own frames; the home renderer skips the
	// titled region border for them
	chrome: bool | *false
	render: #ExecRender | #NativeRender | #ChromeRender
	gestures?: {[#Gesture]: #Action}
}

// one home-layout slot; region order in the layout is peel order
#Region: {
	widget: string // widget id
	edge:   "left" | "right" | "top" | "bottom" | "fill"
	// cells: cols for left/right, rows for top/bottom; unused for fill
	size?: int & >0
}

#Layout: {
	// keyboard is a bespoke full-region view (static Moonlander layout); the
	// grid/panel kinds have no renderer and only survive as config vocabulary
	kind: "dock-grid" | "full-panel" | "tray" | "home" | "keyboard"
	// widget ids in slot order; dock-grid fills 2 cols x 4 rows
	tiles: [...string] | *[]
	panel?: string
	// home only; order = peel order, fill regions split the remainder
	regions?: [...#Region]
}

// bus-owned theme service (`khudson ctl theme day|night`): night colors are
// applied to the HUD kitty via set-colors; day resets to the kitty's
// startup theme (the day include -- never an inlined palette). Each switch
// pairs an m1ddc luminance move; bin/display follow the brightness
// module's params.
#Theme: {
	night: {
		// kitty color names (kitty.conf syntax) -> "#rrggbb"
		colors: {[string]: =~"^#[0-9a-fA-F]{6}$"}
	}
	luminance: {
		bin:     string | *"m1ddc"
		display: string | *"XENEON EDGE"
		night:   int & >=0 & <=100 | *10
		day:     int & >=0 & <=100 | *60
	}
}

// bus-owned caffeinate supervisor: /usr/bin/caffeinate -di held while on.
// on is only the state at bus start; `khudson ctl caffeinate on|off|toggle`
// and the nav-tray cup move it at runtime. An absent block also means on
// (the Go default). The runtime toggle supersedes the still-unapplied
// static power.sleep.display=never idea (nix/edge-host.md): runtime wins,
// the static power config stays unset.
#Caffeinate: {
	on: bool | *true
}

#Config: {
	widgets: [Id=string]: #Widget & {id: Id}
	layouts: [string]: #Layout
	layout: string | *"main"
	strip?:      #Strip
	theme?:      #Theme
	caffeinate?: #Caffeinate
}
