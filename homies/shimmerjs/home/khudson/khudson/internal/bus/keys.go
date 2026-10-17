package bus

import (
	"context"
	"net"

	"github.com/shimmerjs/khudson/khudson/internal/keyfeed"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

func (b *Bus) publishKey(ev proto.KeyEvent) { b.broadcast(proto.Msg{Type: proto.TypeKey, Key: &ev}) }
func (b *Bus) keyLoop(ctx context.Context) {
	(keyfeed.Reader{}).Run(ctx, b.opts.Paths.KeysSocket(), b.publishKey)
}
func (b *Bus) consumeKeys(ctx context.Context, conn net.Conn) {
	_ = keyfeed.Consume(ctx, conn, b.publishKey)
}
