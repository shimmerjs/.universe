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
	"net"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
	"github.com/shimmerjs/khudson/khudson/provider"
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

// Mod starts its reader on the first demanded Poll and joins it when demand
// ends or the owning bus closes. Poll deadlines do not own this shared reader.
type Mod struct {
	sock     string
	now      func() time.Time
	dial     func(context.Context, string, string) (net.Conn, error)
	life     sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	disabled bool
	closed   bool

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
	if !m.started && !m.closed && !m.disabled && m.sock != "" {
		m.started = true
		readerCtx, cancel := context.WithCancel(context.Background())
		m.cancel = cancel
		m.done = make(chan struct{})
		done := m.done
		go func() { defer close(done); m.read(readerCtx) }()
	}
	m.expire()
	d := m.render()
	m.mu.Unlock()
	return d, nil
}

var _ module.Closer = (*Mod)(nil)
var _ module.DemandAware = (*Mod)(nil)

func (m *Mod) Close() error {
	m.life.Lock()
	defer m.life.Unlock()
	m.stopReader(true)
	return nil
}

func (m *Mod) SetDemand(wanted bool) {
	m.life.Lock()
	defer m.life.Unlock()
	if !wanted {
		m.stopReader(false)
		return
	}
	m.mu.Lock()
	m.disabled = false
	m.mu.Unlock()
}

func (m *Mod) stopReader(final bool) {
	m.mu.Lock()
	m.closed = m.closed || final
	m.disabled = true
	cancel, done := m.cancel, m.done
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	m.mu.Lock()
	m.cancel, m.done, m.started = nil, nil, false
	m.mu.Unlock()
}

func (m *Mod) read(ctx context.Context) {
	dial := m.dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: time.Second}).DialContext
	}
	for ctx.Err() == nil {
		conn, err := dial(ctx, "unix", m.sock)
		if ctx.Err() != nil {
			if conn != nil {
				conn.Close()
			}
			return
		}
		diagnostic.Default.Observe("module.logiretch.dial", err, "retry while demanded")
		if err == nil {
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			m.setSockUp(true)
			dec := socketio.NewDecoder(conn)
			for {
				var l wireLine
				err = dec.Decode(&l)
				if ctx.Err() != nil {
					break
				}
				diagnostic.Default.Observe("module.logiretch.read", err, "reset live state; reconnect while demanded")
				if err != nil {
					break
				}
				m.apply(l)
			}
			stop()
			conn.Close()
			m.setSockUp(false)
		}
		// Also pace accept-then-EOF and malformed feeds; they must not spin.
		timer := time.NewTimer(redial)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
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
