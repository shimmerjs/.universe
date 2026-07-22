package keyboard

import (
	"context"
	"sync"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
)

// UpdateCheck polls Oryx for a layout's latest revision at a bounded
// cadence. Get blocks on the network past the TTL, so hosts call it off
// the render path (kuiboard runs it in a tea.Cmd).
type UpdateCheck struct {
	// TTL is the re-poll window; zero means 5 minutes.
	TTL time.Duration
	// Fetch is the meta fetch; nil = oryx.FetchLayoutMeta (NOT the caching
	// fetch: an undeployed latest must not clobber the snapshot the Loader
	// renders from).
	Fetch func(ctx context.Context, hashID, revisionID string) (*oryx.Layout, error)

	mu     sync.Mutex
	at     time.Time
	rev    string
	title  string
	err    error
	forLay string
}

// checkTimeout bounds one meta fetch: u.mu is held across it (single-flight
// by design), so an unbounded hang would wedge every later Get.
const checkTimeout = 30 * time.Second

// Get returns the latest revision hash and title for layoutID, cached per
// TTL window. Errors are cached the same as hits so an offline host polls,
// not hammers.
func (u *UpdateCheck) Get(ctx context.Context, layoutID string) (rev, title string, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	ttl := u.TTL
	if ttl == 0 {
		ttl = 5 * time.Minute
	}
	if u.forLay == layoutID && !u.at.IsZero() && time.Since(u.at) < ttl {
		return u.rev, u.title, u.err
	}
	fetch := u.Fetch
	if fetch == nil {
		fetch = oryx.FetchLayoutMeta
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	l, err := fetch(ctx, layoutID, oryx.RevisionLatest)
	u.at, u.forLay = time.Now(), layoutID
	if err != nil {
		u.rev, u.title, u.err = "", "", err
		return "", "", err
	}
	u.rev, u.title, u.err = l.RevisionID, l.Title, nil
	return u.rev, u.title, nil
}
