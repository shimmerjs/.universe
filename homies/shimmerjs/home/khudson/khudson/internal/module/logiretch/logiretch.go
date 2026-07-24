// Package logiretch is the on-glass MX Master 4 viewer widget: the dock
// twin of the standalone `logiretch` TUI. A background reader holds
// logiretch.sock open (the same retained state/battery + coalesced
// input feed the daemon serves), caches the latest snapshot, and Poll
// renders it -- an ASCII mouse with controls lit as they physically
// activate, plus the asserted mapping state with device readbacks. The
// module never dials the device; touchd owns all HID I/O.
package logiretch

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/module"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

// redial is the quiet reconnect cadence; the daemon absent (no mouse,
// touchd down) is a normal state.
const redial = 2 * time.Second

// litFor is how long a control stays highlighted after its last event;
// held buttons stay lit until released.
const litFor = 400 * time.Millisecond

// invStale ages the battery readout on glass; the standalone viewer's
// staleness alarm.
const batteryStaleAfter = 10 * time.Minute

// wireLine is the superset of logiretch.sock line kinds, Kind-discriminated.
type wireLine struct {
	T        int64           `json:"t"`
	Kind     string          `json:"kind"`
	SoC      int             `json:"soc"`
	Charging bool            `json:"charging"`
	Conn     bool            `json:"connected"`
	DevIdx   int             `json:"devIdx"`
	Features int             `json:"features"`
	Settings map[string]wSet `json:"settings"`
	Remaps   []wRemap        `json:"remaps"`
	Takeover string          `json:"takeover"`
	Pending  string          `json:"pending"`
	Held     []string        `json:"held"`
	Pressed  []string        `json:"pressed"`
	Released []string        `json:"released"`
	Wheel    int             `json:"wheel"`
	Pan      int             `json:"pan"`
	DX       int             `json:"dx"`
	DY       int             `json:"dy"`
}

type wSet struct {
	Want     string `json:"want"`
	Readback string `json:"readback"`
	Error    string `json:"error"`
}

type wRemap struct {
	CID    string `json:"cid"`
	Action string `json:"action"`
	Target string `json:"target"`
	Flags  string `json:"flags"`
}

// Mod is the viewer widget singleton. The reader goroutine starts on the
// first Poll (lazy: a config that never shows the widget opens no
// socket, per the module lifecycle doctrine) and runs for the process
// life.
type Mod struct {
	sock string
	now  func() time.Time

	mu       sync.Mutex
	started  bool
	sockUp   bool
	state    *wireLine
	battery  *wireLine
	batAt    time.Time
	held     map[string]bool
	lit      map[string]time.Time
	wheelDir int
	panDir   int
}

// New builds the singleton; the socket reader is deferred to first Poll.
func New() *Mod {
	sock := ""
	if p, err := paths.Resolve(); err == nil {
		sock = p.LogiSocket()
	}
	return &Mod{
		sock: sock,
		now:  time.Now,
		held: map[string]bool{},
		lit:  map[string]time.Time{},
	}
}

// Name matches the schema's #Module vocabulary.
func (m *Mod) Name() string { return "logiretch" }

// Poll renders the cached snapshot; the first call starts the reader.
// No I/O on the poll path -- the reader goroutine owns the socket.
func (m *Mod) Poll(ctx context.Context, _ map[string]any) (module.Data, error) {
	m.mu.Lock()
	if !m.started && m.sock != "" {
		m.started = true
		go m.read(context.Background())
	}
	m.expire()
	d := m.render()
	m.mu.Unlock()
	return d, nil
}

// read dials logiretch.sock and folds decoded lines into the snapshot,
// reconnecting forever; a lost socket resets the live state so the
// widget reads "daemon down", never a frozen last frame.
func (m *Mod) read(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := net.Dial("unix", m.sock)
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(redial):
			}
			continue
		}
		m.setSockUp(true)
		dec := json.NewDecoder(conn)
		for {
			var l wireLine
			if err := dec.Decode(&l); err != nil {
				break
			}
			m.apply(l)
		}
		conn.Close()
		m.setSockUp(false)
	}
}

func (m *Mod) setSockUp(up bool) {
	m.mu.Lock()
	m.sockUp = up
	if !up {
		// no release will ever arrive from a dead feed: drop held state
		m.held = map[string]bool{}
	}
	m.mu.Unlock()
}

func (m *Mod) apply(l wireLine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	switch l.Kind {
	case "battery":
		b := l
		m.battery = &b
		m.batAt = now
	case "state":
		s := l
		m.state = &s
	case "input":
		for _, b := range l.Pressed {
			m.held[b] = true
			m.lit[b] = now
		}
		for _, b := range l.Released {
			delete(m.held, b)
			m.lit[b] = now
		}
		if l.Wheel != 0 {
			m.lit["wheel"] = now
			m.wheelDir = sign(l.Wheel)
		}
		if l.Pan != 0 {
			m.lit["thumb"] = now
			m.panDir = sign(l.Pan)
		}
		if l.DX != 0 || l.DY != 0 {
			m.lit["motion"] = now
		}
	}
}

// expire drops highlights past their window; held buttons stay lit.
// Caller holds mu.
func (m *Mod) expire() {
	now := m.now()
	for k, at := range m.lit {
		if m.held[k] {
			continue
		}
		if now.Sub(at) >= litFor {
			delete(m.lit, k)
		}
	}
}

func sign(v int) int {
	if v < 0 {
		return -1
	}
	return 1
}
