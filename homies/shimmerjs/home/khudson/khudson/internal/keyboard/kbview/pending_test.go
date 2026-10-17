package kbview

import (
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

func TestPendingKeyStateSurvivesAsyncBoardLoad(t *testing.T) {
	var pending PendingKeys
	ev := proto.KeyEvent{Kind: proto.KeyEventKey, Row: 1, Col: 1, Pressed: true}
	pending.Apply(nil, 0, &ev)
	board := &keyboard.Board{Layers: []keyboard.Layer{{}, {}}}
	pending.Replay(board, 0)
	if !board.Held[keyboard.SlotAt(1, 1)] {
		t.Fatal("cold-start key lost")
	}
	pending.Apply(nil, 0, &ev)
	pending.Apply(nil, 0, &proto.KeyEvent{Kind: proto.KeyEventClear})
	board.Held = nil
	pending.Replay(board, 0)
	if len(board.Held) != 0 {
		t.Fatal("source disconnect replayed held key")
	}
}
