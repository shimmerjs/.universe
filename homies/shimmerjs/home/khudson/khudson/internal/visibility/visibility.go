// Package visibility negotiates terminal visibility reports (DEC mode 2033).
// Unsupported terminals remain potentially visible; focus is never an input.
package visibility

import (
	"io"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const Query = "\x1b[?2033$p"
const Enable = "\x1b[?2033h"
const Disable = "\x1b[?2033l"

type Tracker struct {
	supported, restore bool
	hidden             bool
}

func (t *Tracker) Init() tea.Cmd { return tea.Raw(Query) }
func (t *Tracker) Hidden() bool  { return t.hidden }
func (t *Tracker) Restore(w io.Writer) error {
	if !t.restore {
		return nil
	}
	_, err := io.WriteString(w, Disable)
	return err
}

// Update returns a state change separately from the command enabling reports.
// Duplicate reports do not create additional timer/subscription lifetimes.
func (t *Tracker) Update(msg tea.Msg) (changed bool, cmd tea.Cmd) {
	switch v := msg.(type) {
	case tea.ModeReportMsg:
		if v.Mode != ansi.DECMode(2033) || t.supported {
			return false, nil
		}
		if v.Value == ansi.ModeSet || v.Value == ansi.ModeReset || v.Value == ansi.ModePermanentlySet {
			t.supported = true
			t.restore = v.Value == ansi.ModeReset
			return false, tea.Raw(Enable + "\x1b[?998n")
		}
	case uv.UnknownCsiEvent:
		if !t.supported {
			return false, nil
		}
		hidden := false
		switch string(v) {
		case "\x1b[?999;1n", "\x9b?999;1n":
		case "\x1b[?999;2n", "\x9b?999;2n":
			hidden = true
		default:
			return false, nil
		}
		changed = t.hidden != hidden
		t.hidden = hidden
	}
	return changed, nil
}
