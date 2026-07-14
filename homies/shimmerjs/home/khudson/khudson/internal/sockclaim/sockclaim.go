// Package sockclaim takes over unix socket paths without stealing a live
// listener (touchd's broadcaster pattern): dial-probe first, refuse a path
// that answers, times out, or is held by a live process (a wedged listener
// with a full backlog REFUSES on darwin, indistinguishable from a corpse at
// the errno level), remove only an unheld refused corpse or nothing.
package sockclaim

// KNOWN ACCEPTED: probe-then-claim is not atomic --
// two concurrent starters can both see a corpse and the loser's Remove
// can unlink the winner's fresh socket (split-brain in a ~ms window).
// launchd label serialization makes the realistic trigger a manual run
// racing the agent; the hudlaunch launcher flock closes it.

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// probeTimeout bounds the liveness dial; a live local listener answers well
// inside it.
const probeTimeout = 500 * time.Millisecond

// dialTimeout is a seam for tests: darwin refuses a full-backlog unix
// connect instead of blocking, so a probe timeout cannot be produced with
// a real socket.
var dialTimeout = net.DialTimeout

// errWedged marks a listener that holds the path but did not answer the
// probe in time; the file must not be removed.
var errWedged = errors.New("listener wedged")

// pathHolder reports a live process holding path. darwin refuses a
// full-backlog unix connect exactly like a corpse (never blocks to the
// probe deadline), so ECONNREFUSED alone cannot distinguish a
// wedged-under-load listener from a dead one -- the incident scenario.
// lsof exits 1 with no output when nothing holds the path; a failed exec
// falls back to corpse semantics (the pre-lsof behavior). Var: test seam.
var pathHolder = func(path string) bool {
	out, err := exec.Command("/usr/sbin/lsof", "-t", "--", path).Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

// Free probes path and clears a dead socket file: an answering listener
// means another process owns the path (error, file untouched); a refused
// or absent dial means a corpse, which is removed; a probe timeout means a
// live-but-wedged listener (errWedged, file untouched). Any other dial
// error refuses without removing -- never remove what cannot be
// classified. For callers whose bind happens elsewhere (kitty
// --listen-on); ClaimSocket adds the listen.
func Free(path string) error {
	conn, err := dialTimeout("unix", path, probeTimeout)
	if err == nil {
		conn.Close()
		return fmt.Errorf("%s already in use", path)
	}
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return fmt.Errorf("%s: %w (no answer in %s; not removed)", path, errWedged, probeTimeout)
	case errors.Is(err, fs.ErrNotExist):
		return nil // no file: nothing to clear
	case errors.Is(err, syscall.ECONNREFUSED):
		// refused is a corpse ONLY when nothing live holds the path: a
		// full backlog on a wedged listener refuses identically on darwin.
		if pathHolder(path) {
			return fmt.Errorf("%s: %w (refused but a live process holds the path; not removed)", path, errWedged)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	default:
		return fmt.Errorf("probe of %s unclassified (not removed): %w", path, err)
	}
}

// ClaimSocket frees path (refusing a live listener) and binds it.
func ClaimSocket(path string) (net.Listener, error) {
	if err := Free(path); err != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}
