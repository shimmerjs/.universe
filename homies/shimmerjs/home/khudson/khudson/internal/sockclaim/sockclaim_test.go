package sockclaim

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// shortSockPath keeps the socket path under the macOS sun_path limit;
// t.TempDir under the darwin test runner can exceed it.
func shortSockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sockclaim")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s.sock")
}

func TestClaimSocketRefusesLive(t *testing.T) {
	path := shortSockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	if got, err := ClaimSocket(path); err == nil {
		got.Close()
		t.Fatal("claimed a live socket")
	}
	// the live socket is left intact and still answers
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("live socket damaged by the refused claim: %v", err)
	}
	conn.Close()
}

// bindCorpse leaves a socket file with no listener behind it.
func bindCorpse(t *testing.T, path string) {
	t.Helper()
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	corpse, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	corpse.SetUnlinkOnClose(false)
	corpse.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corpse setup: %v", err)
	}
}

func TestFreeRemovesRefusedCorpse(t *testing.T) {
	path := shortSockPath(t)
	bindCorpse(t, path)

	if err := Free(path); err != nil {
		t.Fatalf("corpse not freed: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("corpse file not removed: stat = %v", err)
	}
}

// stubDial makes the probe return err for the duration of the test.
func stubDial(t *testing.T, err error) {
	t.Helper()
	orig := dialTimeout
	dialTimeout = func(network, addr string, d time.Duration) (net.Conn, error) {
		return nil, err
	}
	t.Cleanup(func() { dialTimeout = orig })
}

// The probe outcomes that must not remove the file cannot all be produced
// with a real socket (darwin refuses a full-backlog connect instead of
// blocking to the deadline), so they are injected through the dial seam;
// a real listener holds the path so removal would be observable.
func TestFreeProbeClassification(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		wedged bool
	}{
		{"deadline exceeded", &net.OpError{Op: "dial", Net: "unix", Err: os.ErrDeadlineExceeded}, true},
		{"etimedout", &net.OpError{Op: "dial", Net: "unix", Err: syscall.ETIMEDOUT}, true},
		{"unclassified", &net.OpError{Op: "dial", Net: "unix", Err: syscall.EACCES}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := shortSockPath(t)
			ln, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			stubDial(t, tc.err)

			err = Free(path)
			if err == nil {
				t.Fatal("freed a path it could not classify as a corpse")
			}
			if got := errors.Is(err, errWedged); got != tc.wedged {
				t.Fatalf("errors.Is(err, errWedged) = %v, want %v (err: %v)", got, tc.wedged, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("socket file removed on a non-corpse probe outcome: %v", err)
			}
			conn, err := net.Dial("unix", path)
			if err != nil {
				t.Fatalf("live listener damaged by the refused free: %v", err)
			}
			conn.Close()
		})
	}
}

// A refused probe against a path a live process still holds is the darwin
// full-backlog wedge (the 2026-07-14 class): the file must survive and the
// error must classify as wedged, or a pegged HUD's socket gets stolen.
func TestFreeRefusesHeldRefusedPath(t *testing.T) {
	path := shortSockPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	stubDial(t, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED})
	origHolder := pathHolder
	pathHolder = func(string) bool { return true }
	t.Cleanup(func() { pathHolder = origHolder })

	err = Free(path)
	if err == nil || !errors.Is(err, errWedged) {
		t.Fatalf("held refused path not classified wedged: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("held socket file removed: %v", err)
	}
}

func TestFreeRemovesOnStubbedRefused(t *testing.T) {
	path := shortSockPath(t)
	bindCorpse(t, path)
	stubDial(t, &net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED})

	if err := Free(path); err != nil {
		t.Fatalf("refused corpse not freed: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("corpse file not removed: stat = %v", err)
	}
}

func TestClaimSocketReplacesCorpse(t *testing.T) {
	path := shortSockPath(t)
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	corpse, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	corpse.SetUnlinkOnClose(false)
	corpse.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("corpse setup: %v", err)
	}

	ln, err := ClaimSocket(path)
	if err != nil {
		t.Fatalf("corpse not claimed: %v", err)
	}
	ln.Close()
}
