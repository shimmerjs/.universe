package rc

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A completed poll releases its map entry: match keys are per-window-id, so
// a kept entry per ever-seen window would grow for the bus lifetime.
func TestTextPollerReleasesState(t *testing.T) {
	// no listener: every GetText fails fast, which still drives run to its
	// terminal branch (errors go to the sink, not the lifecycle)
	c := New(filepath.Join(t.TempDir(), "gone.sock"))
	c.Timeout = 100 * time.Millisecond
	p := NewTextPoller(c)

	states := func() int {
		p.mu.Lock()
		defer p.mu.Unlock()
		return len(p.states)
	}
	waitEmpty := func(what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for states() != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("%s: %d entries still held", what, states())
			}
			time.Sleep(time.Millisecond)
		}
	}

	done := make(chan struct{})
	p.Request(GetTextOpts{Match: "id:1"}, func(string, error) { close(done) })
	<-done
	// the sink fires just before the terminal branch: poll for the release
	waitEmpty("single poll")

	// churn over many distinct match keys: the map stays bounded (drains
	// back to zero), never one entry per ever-polled window
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		p.Request(GetTextOpts{Match: fmt.Sprintf("id:%d", i)}, func(string, error) { wg.Done() })
	}
	wg.Wait()
	waitEmpty("churn loop")
}
