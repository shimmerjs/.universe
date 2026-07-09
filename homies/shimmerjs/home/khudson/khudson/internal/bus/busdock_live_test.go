//go:build darwin

package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
)

// TestBusDockLive runs the whole snapshot pipeline against a real kitty:
// bus.Run with the example topology, a protocol-level dock client (the TUI
// needs a terminal; the wire is what's under test), btop materialized by
// the scheduler, snapshots flowing back. Opt-in like the spike harness:
//
//	KHUDSON_SPIKE1=1 go test ./internal/bus -run TestBusDockLive -v
func TestBusDockLive(t *testing.T) {
	if os.Getenv("KHUDSON_SPIKE1") == "" {
		t.Skip("live e2e: set KHUDSON_SPIKE1=1 (spawns a GUI kitty)")
	}
	kittyBin, err := exec.LookPath("kitty")
	if err != nil {
		t.Fatalf("kitty not on PATH: %v", err)
	}
	btopBin := os.Getenv("KHUDSON_SPIKE1_BTOP")
	if btopBin == "" {
		btopBin, err = exec.LookPath("btop")
		if err != nil {
			t.Fatalf("btop not on PATH and KHUDSON_SPIKE1_BTOP unset: %v", err)
		}
	}

	stateDir := t.TempDir()
	p := paths.Paths{Dir: stateDir}

	kitty := exec.Command(kittyBin,
		"--config", "NONE",
		"-o", "allow_remote_control=socket-only",
		"--listen-on", "unix:"+p.KittySocket(),
		"--title", "khudson-e2e",
		"--start-as", "minimized",
	)
	kitty.Stdout = os.Stderr
	kitty.Stderr = os.Stderr
	if err := kitty.Start(); err != nil {
		t.Fatalf("start kitty: %v", err)
	}
	t.Cleanup(func() {
		_ = kitty.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _, _ = kitty.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = kitty.Process.Kill()
		}
	})
	client := rc.New(p.KittySocket())
	waitFor(t, 15*time.Second, "kitty RC socket answering ls", func() bool {
		_, err := client.LS()
		return err == nil
	})

	// example topology, btop pinned to the nix store binary
	cfgPath := filepath.Join(stateDir, "e2e.cue")
	cfg := fmt.Sprintf(`package khudson

widgets: btop: {
	title: "system"
	glyph: "x"
	render: {
		kind: "exec"
		argv: [%q, "--force-utf"]
		poll:      "500ms"
		keepAlive: true
	}
}
layouts: main: {kind: "dock-grid", tiles: ["btop"], panel: "btop"}
layout: "main"
`, btopBin)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	busDone := make(chan error, 1)
	go func() { busDone <- Run(ctx, Options{ConfigPath: cfgPath, Paths: p}) }()
	waitFor(t, 10*time.Second, "bus socket up", func() bool {
		_, err := os.Stat(p.BusSocket())
		return err == nil
	})

	// protocol-level dock: hello with a 120x30 panel region, then read
	conn, err := net.DialTimeout("unix", p.BusSocket(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial bus: %v", err)
	}
	defer conn.Close()
	enc := json.NewEncoder(conn)
	if err := enc.Encode(proto.Msg{
		Type: proto.TypeHello, Role: proto.RoleDock,
		Cols: 160, Rows: 33, PanelCols: 120, PanelRows: 30,
	}); err != nil {
		t.Fatal(err)
	}

	msgs := make(chan proto.Msg, 64)
	go func() {
		dec := json.NewDecoder(conn)
		for {
			var m proto.Msg
			if err := dec.Decode(&m); err != nil {
				close(msgs)
				return
			}
			msgs <- m
		}
	}()

	// first full snapshot: btop materialized minimized, painted, scraped,
	// fanned out (early polls legitimately catch the blank pre-paint
	// screen; await a real one)
	first := awaitSnapshot(t, msgs, 60*time.Second, 25, "")
	if first.Cols != 120 || first.Rows != 30 {
		t.Errorf("snapshot grid %dx%d, want 120x30", first.Cols, first.Rows)
	}
	if !strings.Contains(first.ANSI, "38:2:") {
		t.Error("snapshot has no truecolor; scrape path degraded")
	}
	if n := len(strings.Split(strings.TrimRight(first.ANSI, "\n"), "\n")); n != 30 {
		t.Errorf("snapshot has %d lines, want 30", n)
	}
	t.Logf("first snapshot: %d bytes ANSI", len(first.ANSI))

	// cadence: another snapshot arrives within ~2 poll intervals and the
	// screen content moves (btop redraws)
	second := awaitSnapshot(t, msgs, 5*time.Second, 25, first.ANSI)
	t.Logf("second snapshot: %d bytes, content changed", len(second.ANSI))

	// the scheduler must have sized the window to the panel region
	tree, err := client.LS()
	if err != nil {
		t.Fatalf("ls: %v", err)
	}
	w, ok := rc.FindWindowByUserVar(tree, UserVarWidget, "btop")
	if !ok {
		t.Fatal("btop window not bound by user var")
	}
	if w.Columns != 120 || w.Lines != 30 {
		t.Errorf("scrape window %dx%d, want 120x30", w.Columns, w.Lines)
	}

	// shutdown: bus releases the window on the way out
	cancel()
	select {
	case err := <-busDone:
		if err != nil {
			t.Fatalf("bus.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bus did not shut down")
	}
	tree, err = client.LS()
	if err != nil {
		t.Fatalf("ls after shutdown: %v", err)
	}
	if _, ok := rc.FindWindowByUserVar(tree, UserVarWidget, "btop"); ok {
		t.Error("btop window survived bus shutdown (releaseAll failed)")
	}
}

// awaitSnapshot reads bus messages until a non-error snapshot arrives with
// at least minLines lines that differs from prev (prev == "" accepts any).
func awaitSnapshot(t *testing.T, msgs <-chan proto.Msg, timeout time.Duration, minLines int, prev string) proto.Msg {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m, ok := <-msgs:
			if !ok {
				t.Fatal("bus connection closed while awaiting snapshot")
			}
			if m.Type != proto.TypeSnapshot {
				continue
			}
			if m.Error != "" {
				t.Logf("snapshot error (tolerated while btop starts): %s", m.Error)
				continue
			}
			if nonBlankLines(m.ANSI) < minLines {
				continue
			}
			if m.ANSI != prev {
				return m
			}
		case <-deadline:
			t.Fatalf("no fresh snapshot within %s", timeout)
			return proto.Msg{}
		}
	}
}
