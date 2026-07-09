// Package sockclaim takes over unix socket paths without stealing a live
// listener (touchd's broadcaster pattern): dial-probe first, refuse a path
// that answers, remove only a dead file.
package sockclaim

// KNOWN ACCEPTED: probe-then-claim is not atomic --
// two concurrent starters can both see a corpse and the loser's Remove
// can unlink the winner's fresh socket (split-brain in a ~ms window).
// launchd label serialization makes the realistic trigger a manual run
// racing the agent; an flock'd sidecar closes it if it ever bites.

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"time"
)

// probeTimeout bounds the liveness dial; a live local listener answers well
// inside it.
const probeTimeout = 500 * time.Millisecond

// Free probes path and clears a dead socket file: an answering listener
// means another process owns the path (error, file untouched); a refused
// dial means a corpse, which is removed. For callers whose bind happens
// elsewhere (kitty --listen-on); ClaimSocket adds the listen.
func Free(path string) error {
	if conn, err := net.DialTimeout("unix", path, probeTimeout); err == nil {
		conn.Close()
		return fmt.Errorf("%s already in use", path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ClaimSocket frees path (refusing a live listener) and binds it.
func ClaimSocket(path string) (net.Listener, error) {
	if err := Free(path); err != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}
