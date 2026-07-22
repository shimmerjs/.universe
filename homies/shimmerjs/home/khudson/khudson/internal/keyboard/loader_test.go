package keyboard

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/keyboard/generations"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/oryx"
	"github.com/shimmerjs/khudson/khudson/internal/keyboard/usbserial"
)

// pollerFor fakes the serial read via the Poller's Read seam, so Load
// never execs ioreg in tests.
func pollerFor(id usbserial.Identity, err error) *usbserial.Poller {
	return &usbserial.Poller{
		TTL:  time.Hour,
		Read: func(context.Context) (usbserial.Identity, error) { return id, err },
	}
}

// missCache is the Cache seam for hermetic tests: the fixture layout could
// coincide with the developer machine's real oryx cache otherwise.
func missCache(string) (*oryx.Layout, error) {
	return nil, errors.New("no cache")
}

// The generations store is a full local payload source: a deployed record
// resolves the board with no oryx cache and no network.
func TestLoaderFromGenerations(t *testing.T) {
	dir := t.TempDir()
	l := fixtureLayout(t)
	_, err := generations.Append(dir, generations.Record{
		FlashedAt: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
		LayoutID:  l.HashID, RevisionID: l.RevisionID, Layout: l,
	})
	if err != nil {
		t.Fatal(err)
	}
	ld := &Loader{
		Poller: pollerFor(usbserial.Identity{LayoutID: l.HashID, RevisionID: l.RevisionID}, nil),
		GenDir: dir,
		Cache:  missCache,
		Fetch: func(context.Context, string, string) (*oryx.Layout, error) {
			t.Error("network fetch ran with a local payload available")
			return nil, errors.New("no")
		},
	}
	st := ld.Load(context.Background())
	if st.Board == nil || st.Board.Title != "aw4" || !st.Present {
		t.Fatalf("state = %+v", st)
	}
	// second load is the memo: same board pointer, still no fetch
	if st2 := ld.Load(context.Background()); st2.Board != st.Board {
		t.Error("memo missed on an unchanged revision")
	}
}

// A missing payload kicks exactly one async fetch and reports the pending
// state; the fetched payload lands on a later tick.
func TestLoaderAsyncFetch(t *testing.T) {
	l := fixtureLayout(t)
	fetched := make(chan struct{})
	ld := &Loader{
		Poller: pollerFor(usbserial.Identity{LayoutID: l.HashID, RevisionID: l.RevisionID}, nil),
		GenDir: t.TempDir(),
		Cache:  missCache,
		Fetch: func(_ context.Context, hashID, revisionID string) (*oryx.Layout, error) {
			defer close(fetched)
			if hashID != l.HashID || revisionID != l.RevisionID {
				t.Errorf("fetch %s/%s, want %s/%s", hashID, revisionID, l.HashID, l.RevisionID)
			}
			return l, nil
		},
	}
	st := ld.Load(context.Background())
	if st.Board != nil || st.Err == "" {
		t.Fatalf("first tick should be pending, got %+v", st)
	}
	select {
	case <-fetched:
	case <-time.After(5 * time.Second):
		t.Fatal("fetch never ran")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		st = ld.Load(context.Background())
		if st.Board != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fetched payload never landed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if st.Board.Title != "aw4" {
		t.Fatalf("state = %+v", st)
	}
}

// Unplugged with a populated generations store renders the last deployed
// snapshot; unplugged cold renders the no-board hint.
func TestLoaderAbsent(t *testing.T) {
	l := fixtureLayout(t)
	dir := t.TempDir()
	if _, err := generations.Append(dir, generations.Record{
		FlashedAt: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
		LayoutID:  l.HashID, RevisionID: l.RevisionID, Layout: l,
	}); err != nil {
		t.Fatal(err)
	}
	ld := &Loader{Poller: pollerFor(usbserial.Identity{}, usbserial.ErrNotPresent), GenDir: dir}
	st := ld.Load(context.Background())
	if st.Board == nil || st.Present {
		t.Fatalf("state = %+v, want last-generation board, not present", st)
	}

	cold := &Loader{Poller: pollerFor(usbserial.Identity{}, usbserial.ErrNotPresent), GenDir: t.TempDir()}
	st = cold.Load(context.Background())
	if st.Board != nil || st.Err != ErrNoBoard.Error() {
		t.Fatalf("cold state = %+v, want the no-board hint", st)
	}
}

// A local-payload miss is memoized per revision -- the disk is not
// re-probed every tick (constant-cost invariant), even if a record for the
// revision lands out of band -- and Invalidate re-arms the probe.
func TestLoaderMissMemoized(t *testing.T) {
	l := fixtureLayout(t)
	dir := t.TempDir()
	ld := &Loader{
		Poller: pollerFor(usbserial.Identity{LayoutID: l.HashID, RevisionID: l.RevisionID}, nil),
		GenDir: dir,
		Cache:  missCache,
		Fetch: func(context.Context, string, string) (*oryx.Layout, error) {
			return nil, errors.New("offline")
		},
	}
	if st := ld.Load(context.Background()); st.Board != nil {
		t.Fatalf("first tick = %+v, want pending", st)
	}
	if _, err := generations.Append(dir, generations.Record{
		FlashedAt: time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC),
		LayoutID:  l.HashID, RevisionID: l.RevisionID, Layout: l,
	}); err != nil {
		t.Fatal(err)
	}
	if st := ld.Load(context.Background()); st.Board != nil {
		t.Fatalf("memoized miss re-probed the disk: %+v", st)
	}
	ld.Invalidate()
	if st := ld.Load(context.Background()); st.Board == nil {
		t.Fatal("post-Invalidate probe missed the record")
	}
}

// UpdateCheck caches per TTL window and swaps the caching fetch out for the
// meta fetch by default (asserted here only by seam injection).
func TestUpdateCheck(t *testing.T) {
	calls := 0
	u := &UpdateCheck{
		TTL: time.Hour,
		Fetch: func(_ context.Context, hashID, revisionID string) (*oryx.Layout, error) {
			calls++
			if revisionID != oryx.RevisionLatest {
				t.Errorf("revision = %q, want latest", revisionID)
			}
			return &oryx.Layout{HashID: hashID, RevisionID: "newRev", Title: "aw4"}, nil
		},
	}
	for i := 0; i < 3; i++ {
		rev, title, err := u.Get(context.Background(), "bqMJp")
		if err != nil || rev != "newRev" || title != "aw4" {
			t.Fatalf("Get = %s,%s,%v", rev, title, err)
		}
	}
	if calls != 1 {
		t.Errorf("fetch calls = %d, want 1 (TTL cache)", calls)
	}
}

// fixture sanity: the extracted layout.json still decodes to the shape the
// oryx cache writes (guards the extraction against drift).
func TestFixtureShape(t *testing.T) {
	raw, err := os.ReadFile("testdata/layout.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"hashId", "revisionId", "layers"} {
		if _, ok := m[k]; !ok {
			t.Errorf("fixture missing %q", k)
		}
	}
}
