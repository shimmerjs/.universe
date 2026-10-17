// Loader resolves keyboard snapshots from USB identity, caches, and Oryx.
// Hosts call it from an owned worker; identity and disk reads may block.
package keyboard

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/diagnostic"
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

	mu         sync.Mutex
	cancel     context.CancelFunc
	workers    sync.WaitGroup
	closed     bool
	generation uint64
	board      *Board
	inFlight   bool
	failedAt   time.Time
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
		d, err := generations.DefaultDir()
		diagnostic.Default.Observe("keyboard.generations.path", err, "skip unavailable generation store")
		if err == nil {
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
	diagnostic.Default.Observe("keyboard.identity.read", err, "retain board; retry while demanded")
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
	l.kickFetch(ctx, id)
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
	diagnostic.Default.Observe("keyboard.generations.latest", err, "retain empty state; use identity or network")
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
	cached, err := cache(id.LayoutID)
	diagnostic.Default.Observe("keyboard.layout.cache", err, "try generation store or network")
	if err == nil && cached != nil && cached.RevisionID == id.RevisionID {
		return cached
	}
	if d := l.dir(); d != "" {
		rec, err := generations.Find(d, id.RevisionID)
		diagnostic.Default.Observe("keyboard.generations.find", err, "fetch exact revision if cache unavailable")
		if err == nil && rec != nil && rec.Layout != nil {
			return rec.Layout
		}
	}
	return nil
}

// kickFetch starts one background fetch for id's revision, single-flight
// and backoff-throttled. Delivery is direct into the memo under mu; the
// default fetch also writes the disk cache through.
func (l *Loader) kickFetch(parent context.Context, id usbserial.Identity) {
	if l.closed || parent.Err() != nil || l.inFlight || time.Since(l.failedAt) < fetchBackoff {
		return
	}
	fetch := l.Fetch
	if fetch == nil {
		fetch = oryx.FetchLayout
	}
	l.inFlight = true
	ctx, cancel := context.WithTimeout(parent, fetchTimeout)
	l.cancel = cancel
	generation := l.generation
	l.workers.Add(1)
	go func() {
		defer l.workers.Done()
		defer cancel()
		// a payload beside an error is FetchLayout's cache-write-failure
		// contract: the fetch succeeded, so render it either way
		payload, err := fetch(ctx, id.LayoutID, id.RevisionID)
		diagnostic.Default.Observe("keyboard.layout.fetch", err, "retain cache; back off failed fetch")
		l.mu.Lock()
		defer l.mu.Unlock()
		l.inFlight = false
		if ctx.Err() != nil || generation != l.generation || l.closed {
			return
		}
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
	l.generation++
	if l.cancel != nil {
		l.cancel()
	}
	l.board = nil
	l.failedAt = time.Time{}
	l.probedRev = ""
	l.coldProbed = false
	l.mu.Unlock()
}

// Suspend cancels only speculative reads. A firmware job has a separate owner.
func (l *Loader) Suspend() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.generation++
	if l.cancel != nil {
		l.cancel()
	}
}
func (l *Loader) Close() {
	l.mu.Lock()
	l.closed = true
	if l.cancel != nil {
		l.cancel()
	}
	l.mu.Unlock()
	l.workers.Wait()
}
