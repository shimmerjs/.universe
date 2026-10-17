package keyfeed

import (
	"context"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
)

type inputConn struct{ io.Reader }

func (inputConn) Write(p []byte) (int, error)      { return len(p), nil }
func (inputConn) Close() error                     { return nil }
func (inputConn) LocalAddr() net.Addr              { return nil }
func (inputConn) RemoteAddr() net.Addr             { return nil }
func (inputConn) SetDeadline(time.Time) error      { return nil }
func (inputConn) SetReadDeadline(time.Time) error  { return nil }
func (inputConn) SetWriteDeadline(time.Time) error { return nil }

func TestBrokenStreamsBackOffAndClearHeldState(t *testing.T) {
	for _, input := range []string{"", "not-json\n"} {
		t.Run(input, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				var dials, clears atomic.Int32
				r := Reader{Dial: func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					return inputConn{strings.NewReader(input)}, nil
				}}
				done := make(chan struct{})
				go func() {
					defer close(done)
					r.Run(ctx, "fake", func(ev proto.KeyEvent) {
						if ev.Kind == proto.KeyEventClear {
							clears.Add(1)
						}
					})
				}()
				synctest.Wait()
				if dials.Load() != 1 || clears.Load() != 1 {
					t.Fatal("disconnect did not clear held state once")
				}
				time.Sleep(time.Second)
				synctest.Wait()
				if dials.Load() != 1 {
					t.Fatal("broken stream reconnect spun without backoff")
				}
				time.Sleep(time.Second)
				synctest.Wait()
				if dials.Load() != 2 || clears.Load() != 2 {
					t.Fatal("reader did not retry after backoff")
				}
				cancel()
				<-done
			})
		})
	}
}

func TestCancellationClosesSilentReader(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Consume(ctx, client, func(proto.KeyEvent) {}) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("silent reader returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("reader outlived owner cancellation")
	}
}
