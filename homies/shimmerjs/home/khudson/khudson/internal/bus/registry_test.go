package bus

import (
	"fmt"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/config"
	"github.com/shimmerjs/khudson/khudson/internal/rc"
)

func adoptTestRegistry() *Registry {
	cfg := &config.Config{
		Widgets: map[string]config.Widget{
			"w": {ID: "w", Render: config.Render{Kind: "exec", Argv: []string{"true"}}},
		},
		Layouts: map[string]config.Layout{"main": {Kind: "dock-grid", Tiles: []string{"w"}}},
		Layout:  "main",
	}
	return NewRegistry(cfg)
}

// TestAdoptTreeOrphanGC: a window whose user var names a widget the config
// no longer has is closed instead of leaking.
func TestAdoptTreeOrphanGC(t *testing.T) {
	reg := adoptTestRegistry()
	var closed []string
	closeWin := func(match string) error { closed = append(closed, match); return nil }

	tree := lsTree(rc.Window{ID: 9, UserVars: map[string]string{UserVarWidget: "removed"}})
	if n := adoptTree(tree, reg, closeWin); n != 0 {
		t.Fatalf("adopted %d, want 0", n)
	}
	if len(closed) != 1 || closed[0] != "id:9" {
		t.Fatalf("closed = %v, want [id:9]", closed)
	}
}

// TestAdoptTreeDuplicateGC: with the widget already bound, an extra window
// carrying the same user var is closed; the bound window wins.
func TestAdoptTreeDuplicateGC(t *testing.T) {
	reg := adoptTestRegistry()
	st, _ := reg.Get("w")
	st.setWindowID(3)
	var closed []string
	closeWin := func(match string) error { closed = append(closed, match); return nil }

	tree := lsTree(
		rc.Window{ID: 3, UserVars: map[string]string{UserVarWidget: "w"}},
		rc.Window{ID: 4, UserVars: map[string]string{UserVarWidget: "w"}},
	)
	if n := adoptTree(tree, reg, closeWin); n != 0 {
		t.Fatalf("adopted %d, want 0", n)
	}
	if id, _, _ := st.Binding(); id != 3 {
		t.Fatalf("binding moved to %d, want 3", id)
	}
	if len(closed) != 1 || closed[0] != "id:4" {
		t.Fatalf("closed = %v, want [id:4]", closed)
	}
}

// TestAdoptTreeRebind: an unbound widget adopts its leftover window; a
// var-less window is ignored.
func TestAdoptTreeRebind(t *testing.T) {
	reg := adoptTestRegistry()
	var closed []string
	closeWin := func(match string) error { closed = append(closed, match); return nil }

	tree := lsTree(
		rc.Window{ID: 2},
		rc.Window{ID: 5, UserVars: map[string]string{UserVarWidget: "w"}},
	)
	if n := adoptTree(tree, reg, closeWin); n != 1 {
		t.Fatalf("adopted %d, want 1", n)
	}
	st, _ := reg.Get("w")
	if id, _, _ := st.Binding(); id != 5 {
		t.Fatalf("bound to %d, want 5", id)
	}
	if len(closed) != 0 {
		t.Fatalf("closed = %v, want none", closed)
	}
}

// fakeWinOps stubs the post-launch window surface for finishEnsure.
type fakeWinOps struct {
	hideErr   error
	resizeErr error
	closed    []string
}

func (f *fakeWinOps) HideOSWindow(string) error             { return f.hideErr }
func (f *fakeWinOps) ResizeOSWindow(string, int, int) error { return f.resizeErr }
func (f *fakeWinOps) CloseWindow(match string) error        { f.closed = append(f.closed, match); return nil }

// TestFinishEnsureRollback: a failed hide or initial resize closes the
// half-configured window and drops the binding so materialize retries.
func TestFinishEnsureRollback(t *testing.T) {
	st := &WidgetState{Widget: config.Widget{ID: "w", Render: config.Render{Kind: "exec"}}}
	st.setSize(80, 24)

	ops := &fakeWinOps{hideErr: fmt.Errorf("hide failed")}
	if err := finishEnsure(ops, st, 7); err == nil {
		t.Fatal("hide failure not surfaced")
	}
	if len(ops.closed) != 1 || ops.closed[0] != "id:7" {
		t.Fatalf("closed = %v, want [id:7]", ops.closed)
	}
	if id, _, _ := st.Binding(); id != 0 {
		t.Fatalf("binding survived failed hide: %d", id)
	}

	ops = &fakeWinOps{resizeErr: fmt.Errorf("resize failed")}
	if err := finishEnsure(ops, st, 8); err == nil {
		t.Fatal("resize failure not surfaced")
	}
	if len(ops.closed) != 1 || ops.closed[0] != "id:8" {
		t.Fatalf("closed = %v, want [id:8]", ops.closed)
	}
	if id, _, _ := st.Binding(); id != 0 {
		t.Fatalf("binding survived failed resize: %d", id)
	}

	ops = &fakeWinOps{}
	if err := finishEnsure(ops, st, 9); err != nil {
		t.Fatal(err)
	}
	if id, _, _ := st.Binding(); id != 9 {
		t.Fatalf("bound to %d, want 9", id)
	}
	if len(ops.closed) != 0 {
		t.Fatalf("closed = %v, want none", ops.closed)
	}
}
