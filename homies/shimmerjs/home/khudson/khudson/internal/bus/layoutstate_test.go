package bus

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/actionjournal"
	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
)

// setLayout persists the switched-to name to the state file; a failed switch
// leaves it untouched.
func TestSetLayoutPersistsName(t *testing.T) {
	b, _, _ := schedTestBus(t)
	dir := t.TempDir()
	b.opts.Paths = paths.Paths{Dir: dir}
	b.cfg.Layouts["other"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}

	if err := b.setLayout("other"); err != nil {
		t.Fatalf("setLayout: %v", err)
	}
	file := filepath.Join(dir, layoutStateFileName)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if string(data) != "other" {
		t.Fatalf("state = %q, want %q", data, "other")
	}

	if err := b.setLayout("nope"); err == nil {
		t.Fatal("unknown layout accepted")
	}
	if data, _ := os.ReadFile(file); string(data) != "other" {
		t.Fatalf("state after failed switch = %q, want %q", data, "other")
	}
}

// adoptLayoutState restores a still-defined persisted name, ignores an
// undefined one WITHOUT deleting the file (a later config may define it
// again), and no-ops without a state root or state file.
func TestAdoptLayoutState(t *testing.T) {
	cfg := &config.Config{
		Layouts: map[string]config.Layout{"main": {}, "other": {}},
		Layout:  "main",
	}
	dir := t.TempDir()
	file := filepath.Join(dir, layoutStateFileName)
	if err := os.WriteFile(file, []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	adoptLayoutState(cfg, dir)
	if cfg.Layout != "other" {
		t.Fatalf("layout = %q, want other", cfg.Layout)
	}

	cfg.Layout = "main"
	if err := os.WriteFile(file, []byte("gone"), 0o600); err != nil {
		t.Fatal(err)
	}
	adoptLayoutState(cfg, dir)
	if cfg.Layout != "main" {
		t.Fatalf("undefined name adopted: %q", cfg.Layout)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("state file not left in place: %v", err)
	}

	adoptLayoutState(cfg, "")
	adoptLayoutState(cfg, t.TempDir())
	if cfg.Layout != "main" {
		t.Fatalf("no-op paths moved the layout: %q", cfg.Layout)
	}
}

// trySwapPending keeps the runtime layout selection when the reloaded config
// still defines it; only a config that dropped the layout falls back to the
// pending config's default.
func TestSwapPendingKeepsRuntimeLayout(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.cfg.Layouts["other"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}
	if err := b.setLayout("other"); err != nil {
		t.Fatalf("setLayout: %v", err)
	}

	pending := &config.Config{
		Widgets: b.cfg.Widgets,
		Layouts: map[string]config.Layout{
			"main":  {Kind: "dock-grid", Tiles: []string{"w"}},
			"other": {Kind: "dock-grid", Tiles: []string{"w"}},
		},
		Layout: "main",
	}
	b.mu.Lock()
	b.pending = pending
	b.mu.Unlock()
	b.trySwapPending(map[string]*schedEntry{})
	b.mu.Lock()
	got := b.cfg.Layout
	b.mu.Unlock()
	if got != "other" {
		t.Fatalf("layout after swap = %q, want other (the runtime selection)", got)
	}

	pending = &config.Config{
		Widgets: b.cfg.Widgets,
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"w"}}},
		Layout:  "main",
	}
	b.mu.Lock()
	b.pending = pending
	b.mu.Unlock()
	b.trySwapPending(map[string]*schedEntry{})
	b.mu.Lock()
	got = b.cfg.Layout
	b.mu.Unlock()
	if got != "main" {
		t.Fatalf("layout after fallback swap = %q, want main (the pending default)", got)
	}
}

func TestLayoutPersistenceFailureDoesNotPublishSuccess(t *testing.T) {
	b, _, _ := schedTestBus(t)
	b.opts.Paths.Dir = t.TempDir()
	b.cfg.Layouts["other"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}
	before := b.cfg.Layout
	if err := os.Mkdir(filepath.Join(b.opts.Paths.Dir, layoutStateFileName), 0700); err != nil {
		t.Fatal(err)
	}
	if err := b.setLayout("other"); err == nil {
		t.Fatal("reported success with no durable layout")
	}
	if b.cfg.Layout != before {
		t.Fatal("published layout despite failed persistence")
	}
}

func TestLayoutPersistenceDoesNotBlockBusState(t *testing.T) {
	for _, invalidate := range []bool{false, true} {
		b, _, _ := schedTestBus(t)
		b.cfg.Layouts["other"] = config.Layout{Kind: "dock-grid", Tiles: []string{"w"}}
		before := b.cfg.Layout
		writing, release := make(chan struct{}), make(chan struct{})
		b.layoutWrite = func(string) error { close(writing); <-release; return nil }
		done := make(chan error, 1)
		go func() { done <- b.setLayout("other") }()
		<-writing
		responsive := make(chan struct{})
		go func() {
			b.mu.Lock()
			if invalidate {
				delete(b.cfg.Layouts, "other")
			}
			b.mu.Unlock()
			close(responsive)
		}()
		select {
		case <-responsive:
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatal("storage blocked bus state")
		}
		close(release)
		err := <-done
		if invalidate {
			if err == nil || actionjournal.ExecutionState(err) != actionjournal.Uncertain || b.cfg.Layout != before {
				t.Fatalf("invalidated layout published: %s %v", b.cfg.Layout, err)
			}
		} else if err != nil || b.cfg.Layout != "other" {
			t.Fatalf("valid layout not published: %s %v", b.cfg.Layout, err)
		}
	}
}
