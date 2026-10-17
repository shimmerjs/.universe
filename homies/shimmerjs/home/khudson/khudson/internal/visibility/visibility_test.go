package visibility

import (
	"bytes"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

func decoded(t *testing.T, raw string) tea.Msg {
	t.Helper()
	var decoder uv.EventDecoder
	n, event := decoder.Decode([]byte(raw))
	if n != len(raw) {
		t.Fatalf("parser consumed %d/%d bytes", n, len(raw))
	}
	if report, ok := event.(uv.ModeReportEvent); ok {
		return tea.ModeReportMsg(report)
	}
	return event
}

func TestNegotiationAndVisibilityThroughPinnedParser(t *testing.T) {
	var tracker Tracker
	if changed, _ := tracker.Update(decoded(t, "\x1b[?999;2n")); changed {
		t.Fatal("accepted unnegotiated visibility")
	}
	if _, cmd := tracker.Update(decoded(t, "\x1b[?2033;2$y")); cmd == nil {
		t.Fatal("supported mode was not enabled")
	}
	if changed, _ := tracker.Update(decoded(t, "\x1b[?999;2n")); !changed || !tracker.Hidden() {
		t.Fatal("hide not adopted")
	}
	if changed, _ := tracker.Update(decoded(t, "\x1b[?999;2n")); changed {
		t.Fatal("duplicate hide changed state")
	}
	if changed, _ := tracker.Update(tea.FocusMsg{}); changed || !tracker.Hidden() {
		t.Fatal("focus confused with visibility")
	}
	if changed, _ := tracker.Update(decoded(t, "\x1b[?999;3n")); changed || !tracker.Hidden() {
		t.Fatal("unknown state changed visibility")
	}
	if changed, _ := tracker.Update(decoded(t, "\x1b[?999;1n")); !changed || tracker.Hidden() {
		t.Fatal("show not adopted")
	}
	var out bytes.Buffer
	if err := tracker.Restore(&out); err != nil || out.String() != Disable {
		t.Fatalf("mode not restored: %q %v", out.String(), err)
	}
}

func TestUnsupportedAndInheritedMode(t *testing.T) {
	for _, state := range []string{"0", "1", "4"} {
		var tracker Tracker
		tracker.Update(decoded(t, "\x1b[?2033;"+state+"$y"))
		var out bytes.Buffer
		tracker.Restore(&out)
		if out.Len() != 0 {
			t.Fatal("disabled a mode not enabled by this owner")
		}
	}
}
