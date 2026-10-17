package main

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
)

func TestKeyboardRenderingSkipsHardwareReadsAndHideDisarmsFlash(t *testing.T) {
	reads := 0
	m := &model{w: 80, h: 24, armed: time.Now(), armedRev: "revision", loader: &keyboard.Loader{
		GenDir: t.TempDir(), Poller: &usbserial.Poller{Read: func(context.Context) (usbserial.Identity, error) {
			reads++
			return usbserial.Identity{}, usbserial.ErrNotPresent
		}},
	}}
	m.Update(tea.ModeReportMsg{Mode: ansi.DECMode(2033), Value: ansi.ModeReset})
	m.Update(uv.UnknownCsiEvent("\x1b[?999;2n"))
	m.View()
	m.View()
	if reads != 0 {
		t.Fatal("hidden keyboard queried USB")
	}
	if !m.armed.IsZero() || m.armedRev != "" {
		t.Fatal("hide retained firmware confirmation")
	}
	m.Update(uv.UnknownCsiEvent("\x1b[?999;1n"))
	m.View()
	if reads != 0 {
		t.Fatal("rendering queried hardware; watcher must own reads")
	}
}
