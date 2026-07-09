// Moonlander key ingest: the keys.sock twin of touchLoop. touchd decodes
// the board's raw-HID stream and serves KeyEvent lines on keys.sock; the
// bus rebroadcasts each one to every dock as TypeKey (the dock only talks
// to the bus). No state is cached bus-side -- highlights are transient, so
// a reconnecting dock simply starts dark -- but a lost source broadcasts a
// synthetic clear so no dock is left holding stuck highlights.
package bus

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// keysRedial is the quiet reconnect cadence; touchd absent (or the board
// unplugged long enough for touchd to drop the socket) is a normal state.
const keysRedial = 2 * time.Second

// keyLoop dials touchd's keys socket, rebroadcasting events to docks.
// Reconnects forever, quietly; transitions are logged once.
func (b *Bus) keyLoop(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := net.Dial("unix", b.opts.Paths.KeysSocket())
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(keysRedial):
			}
			continue
		}
		log.Printf("khudson bus: keys source connected")
		b.consumeKeys(ctx, conn)
		conn.Close()
		// the source is gone: any key still down will never see its keyup,
		// so docks must drop every held highlight
		b.broadcast(proto.Msg{Type: proto.TypeKey, Key: &proto.KeyEvent{Kind: proto.KeyEventClear}})
		log.Printf("khudson bus: keys source lost")
	}
}

// consumeKeys decodes KeyEvent lines until the connection or ctx dies.
// Events pass straight through: the dock owns matrix->key resolution (it
// holds the geometry), the bus owns only transport.
func (b *Bus) consumeKeys(ctx context.Context, conn net.Conn) {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	dec := json.NewDecoder(conn)
	for {
		var ev proto.KeyEvent
		if err := dec.Decode(&ev); err != nil {
			return
		}
		b.broadcast(proto.Msg{Type: proto.TypeKey, Key: &ev})
	}
}
