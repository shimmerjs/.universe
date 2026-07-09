package sockclaim

import (
	"net"
	"os"
	"path/filepath"
	"testing"
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
