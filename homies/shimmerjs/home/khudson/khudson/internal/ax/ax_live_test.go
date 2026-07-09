package ax

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Live walk/press against the running host's Dock, env-gated like the
// kittysessions/claudesessions live suites: with KHUDSON_AX unset every
// test self-skips, so buildGoModule's doCheck never needs the
// Accessibility grant or a GUI session.

func liveGate(t *testing.T) {
	t.Helper()
	if os.Getenv("KHUDSON_AX") == "" {
		t.Skip("KHUDSON_AX not set")
	}
	if !Trusted() {
		t.Skip("Accessibility not granted to this process; grant the host terminal and rerun")
	}
}

func TestDockMinimizedItemsLive(t *testing.T) {
	liveGate(t)
	items, err := DockMinimizedItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live: %d minimized dock item(s)", len(items))
	for _, it := range items {
		t.Logf("live minimized: %q", it.Title)
	}
}

// The press path re-walks and refuses anything but an exact title: a
// nonsense title is ErrNotFound, never a near-miss press.
func TestPressMinimizedItemNotFoundLive(t *testing.T) {
	liveGate(t)
	err := PressMinimizedItem("khudson: no such window title 8b1f")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("press of a nonsense title = %v, want ErrNotFound", err)
	}
}

// TestPressMinimizedItemLive presses the first minimized dock item; it
// really restores that window on the host, which is the point of the
// gated live proof.
func TestPressMinimizedItemLive(t *testing.T) {
	liveGate(t)
	items, err := DockMinimizedItems(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Skip("no minimized windows to press")
	}
	if err := PressMinimizedItem(items[0].Title); err != nil {
		t.Fatal(err)
	}
	t.Logf("live: pressed %q", items[0].Title)
}
