package bus

import (
	"os"
	"path/filepath"
	"testing"

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
