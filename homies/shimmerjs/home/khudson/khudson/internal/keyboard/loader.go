// Loader resolves the display board keymapp-free. The USB serial names the
// deployed revision (ground truth); the payload comes from the oryx disk
// cache, the generations store, or -- asynchronously, never blocking a
// render tick -- a network fetch. Per-frame cost is bounded per the
// constant-cost invariant: one TTL'd ioreg exec via the shared Poller and
// map/field checks; the disk probes (oryx cache, generations store) run at
// most once per revision -- a miss is memoized, not retried per tick.
package keyboard

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/keydict"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
)

// ErrNoBoard is the cold-start empty state: no board on the bus and no
// local snapshot to render from. Hosts pass its text to kbview as the hint
// sentinel.
var ErrNoBoard = errors.New("keyboard: no board seen yet -- plug it in")

// LoadState is one Load result. Board may be a stale snapshot: a present
// board whose deployed revision the snapshot does not match yet stays on
// glass while the fetch runs.
type LoadState struct {
	Board    *Board
	Identity usbserial.Identity
	Present  bool
	Err      string
}

// fetchBackoff throttles retrying a revision fetch that keeps failing, so
// an offline host is one attempt a minute, not one per tick.
const fetchBackoff = time.Minute

// fetchTimeout bounds the async payload fetch.
const fetchTimeout = 30 * time.Second

// Loader memoizes the built Board per deployed revision. The zero value
// needs a Poller; everything else defaults to the real environment.
type Loader struct {
	// Poller is the shared TTL'd serial reader.
	Poller *usbserial.Poller
	// GenDir is the generations store; "" resolves DefaultDir once.
	GenDir string
	// Fetch is the exact-revision payload fetch; nil = oryx.FetchLayout
	// (write-through cached, so the fetched payload lands on disk too).
	Fetch func(ctx context.Context, hashID, revisionID string) (*oryx.Layout, error)
	// Cache is the disk-cache probe; nil = oryx.LoadCached. A seam so
	// tests never read the real per-user state dir.
	Cache func(hashID string) (*oryx.Layout, error)

	mu       sync.Mutex
	board    *Board
	inFlight bool
	failedAt time.Time
	// probedRev is the revision localPayload last missed on; coldProbed
	// marks the one-shot lastGeneration probe. Both memoize disk misses so
	// a persistent miss costs field checks per tick, not reads.
	probedRev  string
	coldProbed bool
	genDirOnce sync.Once
	genDir     string
}

func (l *Loader) dir() string {
	l.genDirOnce.Do(func() {
		if l.GenDir != "" {
			l.genDir = l.GenDir
			return
		}
		if d, err := generations.DefaultDir(); err == nil {
			l.genDir = d
		}
	})
	return l.genDir
}

// Load resolves the current display state. It never blocks on the network:
// a missing payload kicks one bounded background fetch and reports the
// stale board (or the fetching hint) meanwhile.
func (l *Loader) Load(ctx context.Context) LoadState {
	id, err := l.Poller.Get(ctx)
	l.mu.Lock()
	defer l.mu.Unlock()

	if err != nil {
		st := LoadState{Board: l.board}
		if l.board == nil {
			if !l.coldProbed {
				l.coldProbed = true
				if rec := l.lastGeneration(); rec != nil {
					l.board = FromLayout(rec.Layout, keydict.Embedded())
				}
			}
			st.Board = l.board
			if l.board == nil {
				if errors.Is(err, usbserial.ErrNotPresent) {
					st.Err = ErrNoBoard.Error()
				} else {
					st.Err = err.Error()
				}
			}
		}
		return st
	}

	st := LoadState{Identity: id, Present: true}
	if l.board != nil && l.board.RevisionID == id.RevisionID {
		st.Board = l.board
		return st
	}
	if l.probedRev != id.RevisionID {
		l.probedRev = id.RevisionID
		if payload := l.localPayload(id); payload != nil {
			l.board = FromLayout(payload, keydict.Embedded())
			st.Board = l.board
			return st
		}
	}
	l.kickFetch(id)
	st.Board = l.board
	if l.board == nil {
		st.Err = "fetching layout " + id.RevisionID + " from oryx"
	}
	return st
}

// lastGeneration is the unplugged cold-start fallback: the newest deployed
// payload. Errors degrade to nil -- an unreadable store renders the hint.
func (l *Loader) lastGeneration() *generations.Record {
	d := l.dir()
	if d == "" {
		return nil
	}
	rec, err := generations.Latest(d)
	if err != nil || rec == nil || rec.Layout == nil {
		return nil
	}
	return rec
}

// localPayload resolves id's exact revision without the network: the oryx
// disk cache when its snapshot matches, else the generations store.
func (l *Loader) localPayload(id usbserial.Identity) *oryx.Layout {
	cache := l.Cache
	if cache == nil {
		cache = oryx.LoadCached
	}
	if cached, err := cache(id.LayoutID); err == nil && cached.RevisionID == id.RevisionID {
		return cached
	}
	if d := l.dir(); d != "" {
		if rec, err := generations.Find(d, id.RevisionID); err == nil && rec != nil && rec.Layout != nil {
			return rec.Layout
		}
	}
	return nil
}

// kickFetch starts one background fetch for id's revision, single-flight
// and backoff-throttled. Delivery is direct into the memo under mu; the
// default fetch also writes the disk cache through.
func (l *Loader) kickFetch(id usbserial.Identity) {
	if l.inFlight || time.Since(l.failedAt) < fetchBackoff {
		return
	}
	fetch := l.Fetch
	if fetch == nil {
		fetch = oryx.FetchLayout
	}
	l.inFlight = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		// a payload beside an error is FetchLayout's cache-write-failure
		// contract: the fetch succeeded, so render it either way
		payload, _ := fetch(ctx, id.LayoutID, id.RevisionID)
		l.mu.Lock()
		defer l.mu.Unlock()
		l.inFlight = false
		if payload == nil {
			l.failedAt = time.Now()
			return
		}
		l.board = FromLayout(payload, keydict.Embedded())
	}()
}

// Invalidate drops the memo, the miss memos, and the poller cache: hosts
// call it after a deploy (kuiboard's flash-done handler) so the next tick
// re-reads the serial and rebuilds.
func (l *Loader) Invalidate() {
	l.Poller.Invalidate()
	l.mu.Lock()
	l.board = nil
	l.failedAt = time.Time{}
	l.probedRev = ""
	l.coldProbed = false
	l.mu.Unlock()
}
