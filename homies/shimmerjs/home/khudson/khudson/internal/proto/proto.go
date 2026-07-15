// Package proto defines the ndjson messages spoken on khudson.sock (bus <->
// dock/ctl), touch.sock, and keys.sock (touchd -> bus). One struct per
// socket direction, discriminated by Type, so a single json.Decoder per
// connection handles everything.
package proto

import (
	"encoding/json"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
)

// Msg types on khudson.sock.
const (
	TypeHello    = "hello"    // dock/ctl -> bus, first message on a connection
	TypeGrid     = "grid"     // dock -> bus, cell grid changed
	TypeCtl      = "ctl"      // ctl -> bus, one command; dock -> bus, layout nav (no resp)
	TypeResp     = "resp"     // bus -> ctl
	TypeGesture  = "gesture"  // bus -> dock
	TypeLayout   = "layout"   // bus -> dock, switch layout
	TypeReload   = "reload"   // bus -> dock, full config after a reload (and in the dock greeting)
	TypeSnapshot = "snapshot" // bus -> dock, scraped widget screen
	TypeForward  = "forward"  // dock -> bus, raw pointer event for a scraped widget
	TypeAction   = "action"   // dock -> bus, config gesture action to execute
	// TypeWidgetData carries a native module's view model (module.Data
	// marshaled into Msg.Data) for Msg.Widget.
	TypeWidgetData = "widget-data"
	TypeTheme      = "theme"   // bus -> dock, theme name (day|night) + HUD kitty palette
	TypeRowAct     = "row-act" // dock -> bus, tapped row's argv to execute
	TypeKey        = "key"     // bus -> dock, live Moonlander key/layer event
	// TypeCaffeinate carries the bus caffeinate supervisor's state ("on" |
	// "off" in Msg.Caffeinate) to docks; toggles ride TypeCtl cmd=caffeinate.
	TypeCaffeinate = "caffeinate"
	// TypeNotice carries a transient bus-side warning (refused row act, an
	// exec'd argv exiting nonzero) to docks in Msg.Error.
	TypeNotice = "notice"
	// TypePing is the dock's idle keepalive (no payload, no response): all
	// other dock->bus traffic is event-driven, and the bus reaps a dock
	// silent past its read grace.
	TypePing = "ping"
	// TypeLogiState carries the latest MX-device battery frame (Msg.Logi) to
	// docks. The bus caches the last one and replays it in the dock greeting
	// (the TypeCaffeinate pattern); a distinct type because the battery
	// cadence is unrelated to the theme channel it would otherwise ride.
	TypeLogiState = "logi"
	// TypeActFail carries the bus's latest failed act/verb exec (Msg.ActFail)
	// to docks: ONE slot, overwritten by each failure, cached and replayed in
	// the dock greeting (the TypeLogiState pattern) so a dock connecting
	// after the failure still renders the strip warn cell until it decays.
	TypeActFail = "act-fail"
)

// HeartbeatEvery is the dock's TypePing cadence while connected; the bus
// sizes its read deadline as a small multiple of this, so late beats
// survive and a mute dock is reaped.
const HeartbeatEvery = 5 * time.Second

// Roles for TypeHello.
const (
	RoleDock = "dock"
	RoleCtl  = "ctl"
)

// Msg is every message on khudson.sock; unused fields stay zero.
type Msg struct {
	Type string `json:"type"`

	// hello / grid; PanelCols/PanelRows is the dock's active-panel content
	// region -- the cell grid the bus sizes scraped windows to
	Role      string `json:"role,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	PanelCols int    `json:"panelCols,omitempty"`
	PanelRows int    `json:"panelRows,omitempty"`

	// ctl
	Cmd string `json:"cmd,omitempty"`
	Arg string `json:"arg,omitempty"`

	// resp; Error doubles as the failure surface on snapshot
	OK    bool            `json:"ok,omitempty"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`

	// gesture
	Gesture *Gesture `json:"gesture,omitempty"`

	// layout
	Layout string `json:"layout,omitempty"`

	// reload: the bus's full decoded config; the dock re-derives every
	// cfg-dependent state from it (its startup copy is otherwise stale)
	Config *config.Config `json:"config,omitempty"`

	// theme: Theme is the name (day|night); Palette is the HUD kitty's
	// effective colors (kitty color name -> "#rrggbb", from get-colors),
	// present once the bus has fetched them
	Theme   string            `json:"theme,omitempty"`
	Palette map[string]string `json:"palette,omitempty"`

	// caffeinate: desired assertion state as broadcast ("on"|"off")
	Caffeinate string `json:"caffeinate,omitempty"`

	// row-act: argv from a tapped module row (module.Row.Act)
	Argv []string `json:"argv,omitempty"`

	// key: one live Moonlander event
	Key *KeyEvent `json:"key,omitempty"`

	// logi: the latest MX-device battery state (bus -> dock)
	Logi *LogiState `json:"logi,omitempty"`

	// act-fail: the latest failed act/verb exec (bus -> dock)
	ActFail *ActFail `json:"actFail,omitempty"`

	// snapshot: one get-text --ansi capture of Widget's window; Cols/Rows
	// carry the scraped grid size. Stale marks a frame older than 3x the
	// widget's poll interval (an ANSI-less stale pulse or a greeting
	// replay); a fresh snapshot's zero value clears it.
	// forward: Widget + Gesture with WIDGET-relative cell coords.
	// action: Widget + Arg naming the config gesture to execute.
	Widget string `json:"widget,omitempty"`
	ANSI   string `json:"ansi,omitempty"`
	Stale  bool   `json:"stale,omitempty"`
}

// Gesture kinds. Press is the touch acknowledgment: every contact opens
// with one, resolved by the tap/long-press/drag-start that follows (docks
// restyle the pressed element in between).
const (
	GesturePress          = "press"
	GestureTap            = "tap"
	GestureLongPress      = "long-press"
	GestureDragStart      = "drag-start"
	GestureDragMove       = "drag-move"
	GestureDragEnd        = "drag-end"
	GestureSwipe          = "swipe"
	GestureTwoFingerSwipe = "two-finger-swipe"
	GestureWheel          = "wheel"
)

// Gesture is one recognized touch gesture in dock cell space (panel px
// carried along for scraped-region SGR forwarding).
type Gesture struct {
	Kind     string `json:"kind"`
	Col      int    `json:"col"`
	Row      int    `json:"row"`
	PX       int    `json:"px"`
	PY       int    `json:"py"`
	StartCol int    `json:"startCol,omitempty"`
	StartRow int    `json:"startRow,omitempty"`
	Dir      string `json:"dir,omitempty"`
	DX       int    `json:"dx,omitempty"` // drag-move: px delta; wheel: cell cols crossed
	DY       int    `json:"dy,omitempty"` // drag-move: px delta; wheel: cell rows crossed
	Cells    int    `json:"cells,omitempty"`
}

// TouchContact is one digitizer slot inside a TouchFrame.
type TouchContact struct {
	ID  uint8  `json:"id"`
	Tip bool   `json:"tip"`
	X   uint16 `json:"x"`
	Y   uint16 `json:"y"`
}

// TouchFrame is one line on touch.sock: a digitizer report from touchd.
type TouchFrame struct {
	Scan     uint16         `json:"scan"`
	Count    uint8          `json:"count"`
	TimeNS   int64          `json:"t"` // wall clock, unix nanoseconds
	Contacts []TouchContact `json:"contacts,omitempty"`
}

// KeyEvent kinds. Key and layer lines come from touchd's Moonlander reader
// (keys.sock, mirrored wire shape); clear is synthesized -- by touchd on
// Moonlander device loss, and by the bus when the keys source disconnects
// -- so docks drop every held highlight.
const (
	KeyEventKey   = "key"
	KeyEventLayer = "layer"
	KeyEventClear = "clear"
)

// KeyEvent is one line on keys.sock and the payload of a TypeKey broadcast:
// a Moonlander matrix key press/release (kind "key": Row/Col are QMK matrix
// coordinates, Pressed distinguishes down from up), an active-layer change
// (kind "layer"), or a clear (kind "clear").
type KeyEvent struct {
	TimeNS  int64  `json:"t,omitempty"` // wall clock, unix nanoseconds
	Kind    string `json:"kind"`
	Row     int    `json:"row,omitempty"`
	Col     int    `json:"col,omitempty"`
	Pressed bool   `json:"pressed,omitempty"`
	Layer   int    `json:"layer,omitempty"`
}

// LogiState is one line on logiretch.sock and the payload of a TypeLogiState
// broadcast: an MX-device battery reading. Kind names the device, SoC is
// state-of-charge percent (0..100), Charging is the wired/charging flag, and
// State is the raw HID++ battery-status code. TimeNS is the read wall clock
// (unix nanoseconds); the dock dims the readout once it goes stale against it.
type LogiState struct {
	TimeNS   int64  `json:"t"`
	Kind     string `json:"kind"`
	SoC      int    `json:"soc"`
	Charging bool   `json:"charging"`
	State    int    `json:"state"`
}

// ActFail is the payload of a TypeActFail broadcast: the bus's latest failed
// act/verb exec. Msg is the argv head plus the trimmed error; TimeNS is the
// failure wall clock (unix nanoseconds) -- the dock renders its strip warn
// cell against it while fresh and drops the cell once it decays.
type ActFail struct {
	TimeNS int64  `json:"t"`
	Msg    string `json:"msg"`
}

// Status is the resp payload for `khudson ctl status`.
type Status struct {
	ConfigPath string   `json:"configPath"` // "" = embedded example
	Layout     string   `json:"layout"`
	Widgets    []string `json:"widgets"`
	Docks      int      `json:"docks"`
	Touch      string   `json:"touch"` // connected | absent
	// MainKitty is the daily kitty RC socket's health: unknown | absent |
	// healthy | stale. Stale means the bus unlinked a connect-refused corpse
	// and the daily kitty needs a hand relaunch; it clears on healthy.
	MainKitty string `json:"mainKitty"`
	// Caffeinate is the bus caffeinate supervisor's state: off | on |
	// "on (starting)" while a spawn or restart backoff is pending.
	Caffeinate string `json:"caffeinate"`
	// SnapshotAges is each exec widget's last-scrape age (rounded), or
	// "never" before the first scrape lands.
	SnapshotAges map[string]string `json:"snapshotAges,omitempty"`
	Uptime       string            `json:"uptime"`
}
