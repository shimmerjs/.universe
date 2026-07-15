// MX-device battery ingest: the logiretch.sock twin of keyLoop. The logiretch
// daemon reads HID++ battery state and serves LogiState lines on
// logiretch.sock; the bus caches the latest and rebroadcasts each one to every
// dock as TypeLogiState (the dock only talks to the bus). Unlike keys.sock,
// a lost source broadcasts NO synthetic clear -- battery just goes stale; the
// dock dims the readout against LogiState.TimeNS. Absent/refused socket is
// normal (logiretch may not be running), so reconnect quietly with backoff.
package bus

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

// logiRedial is the quiet reconnect cadence; logiretch absent is a normal
// state (the readout simply has no data / goes stale).
const logiRedial = 2 * time.Second

// logiLoop dials the logiretch socket, caching and rebroadcasting battery
// state. Reconnects forever, quietly; transitions are logged once.
func (b *Bus) logiLoop(ctx context.Context) {
	for ctx.Err() == nil {
		conn, err := net.Dial("unix", b.opts.Paths.LogiSocket())
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(logiRedial):
			}
			continue
		}
		log.Printf("khudson bus: logi source connected")
		b.consumeLogi(ctx, conn)
		conn.Close()
		// the source is gone: NO synthetic clear -- the last reading stands and
		// the dock dims it once it ages past the staleness horizon
		log.Printf("khudson bus: logi source lost")
	}
}

// consumeLogi decodes LogiState lines until the connection or ctx dies,
// caching the latest bus-side (greeting replay) and broadcasting each.
func (b *Bus) consumeLogi(ctx context.Context, conn net.Conn) {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	dec := json.NewDecoder(conn)
	for {
		var st proto.LogiState
		if err := dec.Decode(&st); err != nil {
			return
		}
		s := st
		b.mu.Lock()
		b.lastLogi = &s
		b.mu.Unlock()
		b.broadcast(proto.Msg{Type: proto.TypeLogiState, Logi: &s})
	}
}
