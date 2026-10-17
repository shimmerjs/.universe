package bus

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/proto"
	"github.com/shimmerjs/khudson/khudson/internal/socketio"
)

func TestRelayDemandOwnsReadersAndJoinsBeforeRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		b := &Bus{docks: map[net.Conn]*socketio.Writer{}, dockCaps: map[net.Conn]proto.ViewCapabilities{}, relayWake: make(chan struct{}, 1)}
		var starts, active [3]atomic.Int32
		var runners [3]func(context.Context)
		for i := range runners {
			runners[i] = func(ctx context.Context) {
				starts[i].Add(1)
				if active[i].Add(1) != 1 {
					t.Errorf("reader %d overlaps", i)
				}
				defer active[i].Add(-1)
				<-ctx.Done()
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); b.runRelays(ctx, runners) }()
		synctest.Wait()
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		for i := range starts {
			if starts[i].Load() != 0 {
				t.Fatal("reader started without demand")
			}
		}
		a, z := net.Pipe()
		defer a.Close()
		defer z.Close()
		b.mu.Lock()
		b.docks[a] = nil
		b.dockCaps[a] = proto.ViewCapabilities{Layouts: []string{"home"}}
		b.notifyRelaysLocked()
		b.mu.Unlock()
		synctest.Wait()
		if active[0].Load() != 0 || active[1].Load() != 1 || active[2].Load() != 1 {
			t.Fatal("terminal viewer claimed touch or missed state readers")
		}
		b.mu.Lock()
		b.docks[z] = nil
		b.dockCaps[z] = proto.ViewCapabilities{Edge: true}
		b.notifyRelaysLocked()
		b.notifyRelaysLocked()
		b.mu.Unlock()
		synctest.Wait()
		for i := range starts {
			if starts[i].Load() != 1 {
				t.Fatalf("duplicate reader %d", i)
			}
		}
		b.mu.Lock()
		b.removeDockLocked(z)
		b.mu.Unlock()
		synctest.Wait()
		if active[0].Load() != 0 || active[1].Load() != 1 {
			t.Fatal("Edge loss failed to stop touch or stopped terminal state reader")
		}
		b.mu.Lock()
		b.removeDockLocked(a)
		b.mu.Unlock()
		synctest.Wait()
		for i := range active {
			if active[i].Load() != 0 {
				t.Fatalf("reader %d outlived final consumer", i)
			}
		}
		b.mu.Lock()
		b.docks[z] = nil
		b.dockCaps[z] = proto.ViewCapabilities{Edge: true}
		b.notifyRelaysLocked()
		b.mu.Unlock()
		synctest.Wait()
		for i := range starts {
			if starts[i].Load() != 2 {
				t.Fatalf("reader %d did not restart exactly once", i)
			}
		}
		cancel()
		<-done
		for i := range active {
			if active[i].Load() != 0 {
				t.Fatal("shutdown failed to join readers")
			}
		}
	})
}

func TestTouchCancellationJoinsDecoderAndDiscardsContact(t *testing.T) {
	b := &Bus{}
	b.setGrid(100, 20)
	a, z := net.Pipe()
	defer a.Close()
	defer z.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); b.consumeFrames(ctx, z) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("touch decoder blocked cancellation")
	}
	if _, err := a.Write([]byte("{}\n")); err == nil {
		t.Fatal("decoder socket survived cancellation")
	}
}
