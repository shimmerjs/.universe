package bus

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// liveSocket binds a listening unix socket at path; the kernel completes
// connects into the backlog, so no accept loop is needed.
func liveSocket(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	ul := ln.(*net.UnixListener)
	t.Cleanup(func() { ul.Close() })
	return ul
}

// corpseSocket binds a unix socket at path and closes the listener without
// unlinking, leaving a connect-refused inode -- the SIGKILL corpse shape.
func corpseSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corpse socket did not survive close: %v", err)
	}
}

func TestProbeMainKitty(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "mk.sock")
		liveSocket(t, sock)
		if got := probeMainKitty(sock, time.Second); got != probeHealthy {
			t.Fatalf("probe = %d, want probeHealthy", got)
		}
	})
	t.Run("absent", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "mk.sock")
		if got := probeMainKitty(sock, time.Second); got != probeAbsent {
			t.Fatalf("probe = %d, want probeAbsent", got)
		}
	})
	t.Run("refused", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "mk.sock")
		corpseSocket(t, sock)
		if got := probeMainKitty(sock, time.Second); got != probeRefused {
			t.Fatalf("probe = %d, want probeRefused", got)
		}
	})
	t.Run("notsock", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "mk.sock")
		if err := os.WriteFile(sock, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := probeMainKitty(sock, time.Second); got != probeInconclusive {
			t.Fatalf("probe = %d, want probeInconclusive", got)
		}
	})
}

// Subtest names stay short: the socket lives in t.TempDir(), and macOS
// sun_path caps unix socket paths at 104 bytes (an over-long path fails
// bind and dial alike with EINVAL).
func TestCheckMainKitty(t *testing.T) {
	// no file: no action
	t.Run("absent", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "mk.sock")
		b := &Bus{}
		b.checkMainKitty(sock)
		if got := b.mainKitty.State(); got != mainKittyAbsent {
			t.Fatalf("state = %q, want %q", got, mainKittyAbsent)
		}
		if _, err := os.Stat(sock); !os.IsNotExist(err) {
			t.Fatalf("stat after check: %v, want not-exist", err)
		}
	})

	// connect OK: socket untouched
	t.Run("healthy", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "mk.sock")
		liveSocket(t, sock)
		b := &Bus{}
		b.checkMainKitty(sock)
		if got := b.mainKitty.State(); got != mainKittyHealthy {
			t.Fatalf("state = %q, want %q", got, mainKittyHealthy)
		}
		if _, err := os.Stat(sock); err != nil {
			t.Fatalf("healthy socket was disturbed: %v", err)
		}
	})

	// connect refused: unlink the corpse, surface stale
	t.Run("refused", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "mk.sock")
		corpseSocket(t, sock)
		b := &Bus{}
		b.checkMainKitty(sock)
		if got := b.mainKitty.State(); got != mainKittyStale {
			t.Fatalf("state = %q, want %q", got, mainKittyStale)
		}
		if _, err := os.Stat(sock); !os.IsNotExist(err) {
			t.Fatalf("stat after check: %v, want corpse unlinked", err)
		}
	})

	// non-refused failure (wedged kitty, junk file): never unlink
	t.Run("wedged", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "mk.sock")
		if err := os.WriteFile(sock, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		b := &Bus{}
		b.checkMainKitty(sock)
		if got := b.mainKitty.State(); got != mainKittyUnknown {
			t.Fatalf("state = %q, want %q", got, mainKittyUnknown)
		}
		if _, err := os.Stat(sock); err != nil {
			t.Fatalf("non-refused path was unlinked: %v", err)
		}
	})
}

// TestMainKittyStaleLifecycle walks the full arc: corpse -> stale + unlink,
// stale sticky while the (now absent) socket waits for a relaunch, cleared
// on healthy, plain absent after a clean disappearance.
func TestMainKittyStaleLifecycle(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "mk.sock")
	b := &Bus{}

	corpseSocket(t, sock)
	b.checkMainKitty(sock)
	if got := b.mainKitty.State(); got != mainKittyStale {
		t.Fatalf("after corpse: state = %q, want %q", got, mainKittyStale)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("corpse not unlinked: %v", err)
	}

	// no file until the user relaunches: the prompt must persist
	b.checkMainKitty(sock)
	if got := b.mainKitty.State(); got != mainKittyStale {
		t.Fatalf("stale not sticky across absent: state = %q", got)
	}

	// relaunched kitty binds fresh: healthy clears the prompt
	ln := liveSocket(t, sock)
	b.checkMainKitty(sock)
	if got := b.mainKitty.State(); got != mainKittyHealthy {
		t.Fatalf("after relaunch: state = %q, want %q", got, mainKittyHealthy)
	}

	// clean exit unlinks; absent is not stale
	ln.Close()
	b.checkMainKitty(sock)
	if got := b.mainKitty.State(); got != mainKittyAbsent {
		t.Fatalf("after clean exit: state = %q, want %q", got, mainKittyAbsent)
	}
}

// TestMainKittyStatusSurface asserts the ctl status payload carries the
// indicator while stale and clears it when the socket goes healthy.
func TestMainKittyStatusSurface(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "mk.sock")
	cfg := &config.Config{
		Widgets: map[string]config.Widget{},
		Layouts: map[string]config.Layout{"main": {Kind: "home"}},
		Layout:  "main",
	}
	b := &Bus{cfg: cfg, reg: NewRegistry(cfg)}

	status := func() proto.Status {
		t.Helper()
		resp := b.handleCtl(proto.Msg{Type: proto.TypeCtl, Cmd: "status"})
		if !resp.OK {
			t.Fatalf("ctl status: %s", resp.Error)
		}
		var st proto.Status
		if err := json.Unmarshal(resp.Data, &st); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		return st
	}

	corpseSocket(t, sock)
	b.checkMainKitty(sock)
	if st := status(); st.MainKitty != mainKittyStale {
		t.Fatalf("status.MainKitty = %q, want %q", st.MainKitty, mainKittyStale)
	}

	liveSocket(t, sock)
	b.checkMainKitty(sock)
	if st := status(); st.MainKitty != mainKittyHealthy {
		t.Fatalf("status.MainKitty = %q, want %q", st.MainKitty, mainKittyHealthy)
	}
}

// TestMainKittyLoop asserts the loop probes once at startup, before the
// first ticker fire, and stops on ctx cancel.
func TestMainKittyLoop(t *testing.T) {
	dir := t.TempDir()
	corpseSocket(t, filepath.Join(dir, "main-kitty.sock"))
	b := &Bus{opts: Options{Paths: paths.Paths{Dir: dir}}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.mainKittyLoop(ctx, time.Hour)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for b.mainKitty.State() != mainKittyStale {
		if time.Now().After(deadline) {
			t.Fatalf("startup check never ran: state = %q", b.mainKitty.State())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("mainKittyLoop did not stop on ctx cancel")
	}
}
