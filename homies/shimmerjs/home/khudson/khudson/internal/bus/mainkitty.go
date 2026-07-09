// Main-kitty socket health. The daily kitty binds main-kitty.sock at launch
// (CLI --listen-on via Launch Services); SIGKILL/crash skips kitty's atexit
// unlink, so the next LS launch binds EADDRINUSE against the corpse and runs
// RC-dead. The config layer must not rm -f the socket (locked decision), so
// the bus owns re-discovery: probe the fixed path on a slow cadence, unlink
// only a corpse that actively refuses connections, and surface the state via
// ctl status. The unlink helps the NEXT launch bind; the current daily kitty
// must be relaunched by hand, so stale stays surfaced until the socket
// accepts again.
package bus

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	// mainKittyProbeInterval is the health-probe cadence; this is not a hot
	// path, one bounded connect() per tick.
	mainKittyProbeInterval = 30 * time.Second
	// mainKittyDialTimeout bounds one probe. Only ECONNREFUSED unlinks; a
	// timeout (wedged-but-alive kitty) must never cost the socket.
	mainKittyDialTimeout = 2 * time.Second
)

// Main-kitty socket states, surfaced as Status.MainKitty.
const (
	mainKittyUnknown = "unknown" // no conclusive probe yet
	mainKittyAbsent  = "absent"  // no socket file: kitty not launched or not RC-integrated
	mainKittyHealthy = "healthy" // socket accepted a connection
	mainKittyStale   = "stale"   // corpse unlinked; relaunch the daily kitty by hand
)

// mainKittyProbe is one connect() classification; observe folds it into the
// surfaced state.
type mainKittyProbe int

const (
	probeAbsent mainKittyProbe = iota
	probeHealthy
	probeRefused
	probeInconclusive // timeout or any other error
)

// probeMainKitty classifies the socket at path with one bounded connect().
func probeMainKitty(path string, timeout time.Duration) mainKittyProbe {
	conn, err := net.DialTimeout("unix", path, timeout)
	if err == nil {
		conn.Close()
		return probeHealthy
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return probeAbsent
	case errors.Is(err, syscall.ECONNREFUSED):
		return probeRefused
	default:
		return probeInconclusive
	}
}

// mainKittyHealth holds the surfaced state; its mutex is a leaf, never held
// together with b.mu.
type mainKittyHealth struct {
	mu    sync.Mutex
	state string // "" until the first conclusive probe
}

// State is the surfaced value for ctl status.
func (h *mainKittyHealth) State() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state == "" {
		return mainKittyUnknown
	}
	return h.state
}

// observe folds one probe into the state and reports whether the socket
// file should be unlinked (refused only). Stale is sticky across absent:
// after the unlink there is no file until kitty is relaunched, and the
// relaunch prompt must persist until the socket goes healthy.
func (h *mainKittyHealth) observe(p mainKittyProbe) (state string, unlink bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch p {
	case probeHealthy:
		h.state = mainKittyHealthy
	case probeRefused:
		h.state = mainKittyStale
		unlink = true
	case probeAbsent:
		if h.state != mainKittyStale {
			h.state = mainKittyAbsent
		}
	case probeInconclusive:
		// not enough signal to flip anything; keep the last state
	}
	if h.state == "" {
		return mainKittyUnknown, unlink
	}
	return h.state, unlink
}

// checkMainKitty runs one probe cycle against sock: classify, unlink a
// refused corpse, log transitions.
func (b *Bus) checkMainKitty(sock string) {
	prev := b.mainKitty.State()
	state, unlink := b.mainKitty.observe(probeMainKitty(sock, mainKittyDialTimeout))
	if unlink {
		if err := os.Remove(sock); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("khudson bus: main kitty RC dead but unlink %s failed: %v", sock, err)
		} else {
			log.Printf("khudson bus: main kitty RC dead -- unlinked stale %s; relaunch kitty by hand", sock)
		}
		return
	}
	if state != prev {
		log.Printf("khudson bus: main kitty socket %s", state)
	}
}

// mainKittyLoop probes once at startup and then every `every` until ctx is
// done.
func (b *Bus) mainKittyLoop(ctx context.Context, every time.Duration) {
	sock := b.opts.Paths.MainKittySocket()
	b.checkMainKitty(sock)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.checkMainKitty(sock)
		}
	}
}
