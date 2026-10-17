// Package keyfeed consumes the shared HID daemon without owning HID devices.
package keyfeed

import (
	"context"
	"net"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
)

type Reader struct {
	Dial func(context.Context, string, string) (net.Conn, error)
}

func (r Reader) Run(ctx context.Context, socket string, publish func(proto.KeyEvent)) {
	dial := r.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: time.Second}).DialContext
	}
	for ctx.Err() == nil {
		conn, err := dial(ctx, "unix", socket)
		diagnostic.Default.Observe("keys.dial", err, "retry after backoff")
		if err == nil {
			err = Consume(ctx, conn, publish)
			conn.Close()
			publish(proto.KeyEvent{Kind: proto.KeyEventClear})
			if ctx.Err() == nil {
				diagnostic.Default.Observe("keys.read", err, "clear held state; reconnect after backoff")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func Consume(ctx context.Context, conn net.Conn, publish func(proto.KeyEvent)) error {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	dec := socketio.NewDecoder(conn)
	for {
		var ev proto.KeyEvent
		if err := dec.Decode(&ev); err != nil {
			return err
		}
		diagnostic.Default.Observe("keys.read", nil, "receiving")
		publish(ev)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}
