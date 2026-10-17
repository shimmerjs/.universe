package keyboard

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
)

func TestWatcherHiddenCancelsAndJoinsRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		loader := &Loader{GenDir: t.TempDir(), Poller: &usbserial.Poller{Read: func(ctx context.Context) (usbserial.Identity, error) {
			calls++
			<-ctx.Done()
			return usbserial.Identity{}, ctx.Err()
		}}}
		w := Watch(context.Background(), loader, nil)
		w.Visible(true)
		synctest.Wait()
		if calls != 1 {
			t.Fatalf("reads %d", calls)
		}
		w.Visible(false)
		synctest.Wait()
		time.Sleep(24 * time.Hour)
		synctest.Wait()
		if calls != 1 {
			t.Fatal("hidden read cadence continued")
		}
		select {
		case <-w.Updates():
			t.Fatal("cancelled read published")
		default:
		}
		w.Close()
	})
}
func TestLoaderRetiredFetchCannotPublish(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		l := &Loader{GenDir: t.TempDir(), Cache: missCache, Poller: pollerFor(usbserial.Identity{LayoutID: "layout", RevisionID: "old"}, nil), Fetch: func(context.Context, string, string) (*oryx.Layout, error) {
			close(started)
			<-release
			return &oryx.Layout{RevisionID: "old"}, nil
		}}
		l.Load(context.Background())
		<-started
		l.Suspend()
		close(release)
		synctest.Wait()
		l.mu.Lock()
		board := l.board
		l.mu.Unlock()
		if board != nil {
			t.Fatal("retired fetch published board")
		}
		l.Close()
	})
}
