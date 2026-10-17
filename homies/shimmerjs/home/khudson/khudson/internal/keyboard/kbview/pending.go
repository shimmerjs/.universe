package kbview

import (
	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// PendingKeys retains current key state while the first board snapshot loads.
// It stores at most one event per valid matrix slot, not an event history.
type PendingKeys struct {
	keys  map[int]proto.KeyEvent
	layer *proto.KeyEvent
}

func (p *PendingKeys) Apply(board *keyboard.Board, layer int, ev *proto.KeyEvent) (int, bool) {
	if board != nil {
		return ApplyKey(board, layer, ev)
	}
	if ev == nil {
		return layer, false
	}
	switch ev.Kind {
	case proto.KeyEventClear:
		clear(p.keys)
		p.layer = nil
	case proto.KeyEventLayer:
		copy := *ev
		p.layer = &copy
	case proto.KeyEventKey:
		slot := keyboard.SlotAt(ev.Row, ev.Col)
		if slot < 0 {
			return layer, false
		}
		if !ev.Pressed {
			delete(p.keys, slot)
		} else {
			if p.keys == nil {
				p.keys = make(map[int]proto.KeyEvent)
			}
			p.keys[slot] = *ev
		}
	}
	return layer, false
}
func (p *PendingKeys) Replay(board *keyboard.Board, layer int) int {
	if board == nil {
		return layer
	}
	for _, ev := range p.keys {
		layer, _ = ApplyKey(board, layer, &ev)
	}
	if p.layer != nil {
		layer, _ = ApplyKey(board, layer, p.layer)
	}
	clear(p.keys)
	p.layer = nil
	return layer
}
