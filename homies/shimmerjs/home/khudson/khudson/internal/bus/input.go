package bus

import (
	"fmt"
	"log"
	"os/exec"
	"slices"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// wheelBurstCap bounds how many wheel reports one forwarded gesture may
// inject.
const wheelBurstCap = 5

// inputWorker drains forwarded pointer events and config actions on one
// goroutine so a tap's press/release pair can never interleave with
// another injection.
func (b *Bus) inputWorker() {
	for m := range b.input {
		switch m.Type {
		case proto.TypeForward:
			b.handleForward(m)
		case proto.TypeAction:
			b.handleAction(m)
		case proto.TypeRowAct:
			b.handleRowAct(m)
		}
	}
}

// handleRowAct executes a tapped module row's argv (module.Row.Act) -- but
// only argv the bus itself published: the widget's last successful poll's
// row acts, or a config gesture's "run" argv. khudson.sock being 0600 keeps
// strangers out, but a misbehaving peer on it must not turn the bus into an
// arbitrary exec service, so anything else is refused loudly.
func (b *Bus) handleRowAct(m proto.Msg) {
	if len(m.Argv) == 0 {
		return
	}
	if !b.rowActAllowed(m) {
		log.Printf("khudson bus: row act %s: argv %v not published by the bus; refused", m.Widget, m.Argv)
		b.broadcast(proto.Msg{Type: proto.TypeNotice,
			Error: fmt.Sprintf("row act %s: argv refused (not published by the bus)", m.Widget)})
		return
	}
	wait, err := b.startArgv(m.Argv)
	if err != nil {
		// start failures are the config-typo class; as loud as exits
		log.Printf("khudson bus: row act %v: %v", m.Argv, err)
		b.broadcast(proto.Msg{Type: proto.TypeNotice,
			Error: fmt.Sprintf("row act %s failed to start: %v", m.Widget, err)})
		return
	}
	log.Printf("khudson bus: row act %s: %v", m.Widget, m.Argv)
	go b.reapCmd("row act", m.Argv, wait)
}

// rowActAllowed reports whether m.Argv matches a row act from the widget's
// last successful poll or a "run" gesture in its config -- the two argv
// sources the bus itself vetted.
func (b *Bus) rowActAllowed(m proto.Msg) bool {
	b.mu.Lock()
	reg := b.reg
	w, cfgOK := b.cfg.Widgets[m.Widget]
	b.mu.Unlock()
	if st, ok := reg.Get(m.Widget); ok {
		for _, act := range st.acts() {
			if slices.Equal(act, m.Argv) {
				return true
			}
		}
	}
	if cfgOK {
		for _, a := range w.Gestures {
			if a.Verb == "run" && slices.Equal(a.Argv, m.Argv) {
				return true
			}
		}
	}
	return false
}

// startArgv starts argv through the exec seam; the returned wait reaps it.
func (b *Bus) startArgv(argv []string) (wait func() error, err error) {
	if b.execStart != nil {
		return b.execStart(argv)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Wait, nil
}

// reapCmd waits an exec'd argv out and surfaces a failed exit loudly (log +
// TypeNotice broadcast) instead of discarding it.
func (b *Bus) reapCmd(kind string, argv []string, wait func() error) {
	if err := wait(); err != nil {
		log.Printf("khudson bus: %s %v: %v", kind, argv, err)
		b.broadcast(proto.Msg{Type: proto.TypeNotice,
			Error: fmt.Sprintf("%s %v: %v", kind, argv, err)})
	}
}

// enqueueInput hands a dock message to the worker; a full queue drops the
// event loudly (input is ephemeral, blocking the dock reader is worse).
func (b *Bus) enqueueInput(m proto.Msg) {
	select {
	case b.input <- m:
	default:
		log.Printf("khudson bus: input queue full, dropped %s for %s", m.Type, m.Widget)
	}
}

// handleForward injects a widget-relative pointer event into the widget's
// window as SGR mouse reports (delivered verbatim to the child PTY; apps react).
func (b *Bus) handleForward(m proto.Msg) {
	if m.Gesture == nil {
		return
	}
	b.mu.Lock()
	reg := b.reg
	b.mu.Unlock()
	st, ok := reg.Get(m.Widget)
	if !ok {
		return
	}
	winID, cols, rows := st.Binding()
	if winID == 0 {
		return
	}
	match := fmt.Sprintf("id:%d", winID)
	x := min(max(m.Gesture.Col+1, 1), max(cols, 1))
	y := min(max(m.Gesture.Row+1, 1), max(rows, 1))

	var err error
	switch m.Gesture.Kind {
	case proto.GestureTap:
		if err = b.inj.SendSGR(match, 0, x, y, false); err == nil {
			err = b.inj.SendSGR(match, 0, x, y, true)
		}
	case proto.GestureLongPress:
		// long-press lands as a right click
		if err = b.inj.SendSGR(match, 2, x, y, false); err == nil {
			err = b.inj.SendSGR(match, 2, x, y, true)
		}
	case proto.GestureWheel:
		burst := func(delta, negBtn, posBtn int) error {
			btn, n := negBtn, delta
			if n > 0 {
				btn = posBtn
			} else {
				n = -n
			}
			n = min(n, wheelBurstCap)
			for range n {
				if err := b.inj.SendSGR(match, btn, x, y, false); err != nil {
					return err
				}
			}
			return nil
		}
		if err = burst(m.Gesture.DY, 64, 65); err == nil { // negative DY = up
			err = burst(m.Gesture.DX, 66, 67) // negative DX = left
		}
	default:
		return
	}
	if err != nil {
		log.Printf("khudson bus: forward %s to %s: %v", m.Gesture.Kind, m.Widget, err)
	}
}

// handleAction executes a config gesture action (effectful verbs carry a
// target). view/back/focus are dock-local and never reach the bus.
func (b *Bus) handleAction(m proto.Msg) {
	b.mu.Lock()
	w, ok := b.cfg.Widgets[m.Widget]
	reg := b.reg
	b.mu.Unlock()
	if !ok {
		return
	}
	a, ok := w.Gestures[m.Arg]
	if !ok {
		return
	}
	switch a.Verb {
	case "send-key":
		id, isHud := strings.CutPrefix(a.Target, "hud-window:")
		if !isHud {
			log.Printf("khudson bus: send-key target %q not a hud window", a.Target)
			return
		}
		st, ok := reg.Get(id)
		if !ok {
			return
		}
		winID, _, _ := st.Binding()
		if winID == 0 {
			return
		}
		if err := b.inj.SendKey(fmt.Sprintf("id:%d", winID), a.Keys); err != nil {
			log.Printf("khudson bus: action send-key %s: %v", m.Widget, err)
		}
	case "run":
		if len(a.Argv) == 0 {
			return
		}
		wait, err := b.startArgv(a.Argv)
		if err != nil {
			log.Printf("khudson bus: action run %v: %v", a.Argv, err)
			b.broadcast(proto.Msg{Type: proto.TypeNotice,
				Error: fmt.Sprintf("run %v failed to start: %v", a.Argv, err)})
			return
		}
		go b.reapCmd("action run", a.Argv, wait)
	case "open-url":
		argv := []string{"open", a.URL}
		wait, err := b.startArgv(argv)
		if err != nil {
			log.Printf("khudson bus: action open-url %s: %v", a.URL, err)
			b.broadcast(proto.Msg{Type: proto.TypeNotice,
				Error: fmt.Sprintf("open-url failed to start: %v", err)})
			return
		}
		go b.reapCmd("action open-url", argv, wait)
	}
}
