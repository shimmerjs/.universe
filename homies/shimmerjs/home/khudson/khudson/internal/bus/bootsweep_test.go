package bus

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

// Bus boot sweeps the hook spool: an 8d-dead entry seeded before Run is
// gone by the time Ready fires; a fresh entry survives. State root is a
// bare MkdirTemp (the sun_path idiom): the bus socket lives under it.
func TestBootSpoolSweep(t *testing.T) {
	dir, err := os.MkdirTemp("", "khboot")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	p := paths.Paths{Dir: dir}
	spool := p.ClaudeSpool()
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	dead := filepath.Join(spool, "dead.json")
	if err := os.WriteFile(dead, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(dead, old, old); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(spool, "fresh.json")
	if err := os.WriteFile(fresh, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	// inert topology: one native widget on a 1h poll, caffeinate off so no
	// child process spawns
	cfgPath := filepath.Join(dir, "cfg.cue")
	cfg := `package khudson

widgets: demo: {
	title: "demo"
	glyph: "x"
	render: {kind: "native", module: "demo-mode", poll: "1h", params: {}}
}
layouts: main: {kind: "dock-grid", tiles: ["demo"]}
layout: "main"
caffeinate: on: false
`
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{ConfigPath: cfgPath, Paths: p, Ready: func() { close(ready) }})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("bus exited before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("bus never became ready")
	}
	if _, err := os.Stat(dead); err == nil {
		t.Error("boot sweep kept an 8d-dead spool")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("boot sweep removed a fresh spool")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bus did not shut down")
	}
}
