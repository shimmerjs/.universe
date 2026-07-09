package module

import (
	"testing"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/config"
)

func TestRingWrap(t *testing.T) {
	r := NewRing(4)
	r.Push(0.1)
	r.Push(0.2)
	got := r.Samples()
	if len(got) != 2 || got[0] != 0.1 || got[1] != 0.2 {
		t.Fatalf("pre-wrap samples = %v, want [0.1 0.2]", got)
	}
	for i := range 9 {
		r.Push(float64(i))
	}
	got = r.Samples()
	if len(got) != 4 {
		t.Fatalf("post-wrap len = %d, want 4", len(got))
	}
	if got[0] != 5 || got[3] != 8 {
		t.Errorf("post-wrap samples = %v, want [5 6 7 8]", got)
	}
	// Samples copies: a later push must not mutate an emitted slice.
	r.Push(99)
	if got[0] != 5 {
		t.Errorf("emitted samples mutated by a later push: %v", got)
	}
}

func TestAge(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{30 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{26 * time.Hour, "1d"},
		{2 * 24 * time.Hour, "2d"},
		{20 * 24 * time.Hour, "2w"},
	} {
		if got := Age(now.Add(-tc.ago), now); got != tc.want {
			t.Errorf("Age(-%v) = %q, want %q", tc.ago, got, tc.want)
		}
	}
}

func TestIntParam(t *testing.T) {
	params := map[string]any{
		"i":   7,
		"i64": int64(8),
		"f":   float64(9),
		"s":   "10",
	}
	for _, tc := range []struct {
		name string
		key  string
		want int
	}{
		{"int", "i", 7},
		{"int64", "i64", 8},
		{"float64", "f", 9},
		{"absent", "nope", 3},
		{"wrong type", "s", 3},
	} {
		if got := IntParam(params, tc.key, 3); got != tc.want {
			t.Errorf("%s: IntParam = %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestHistWindow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		params  map[string]any
		wantCap int
		want    string
	}{
		{"nil params", nil, 4320, "6h"},
		{"missing key", map[string]any{}, 4320, "6h"},
		{"custom window", map[string]any{"window": "30m"}, 360, "30m"},
		{"deep window uncapped at 5s", map[string]any{"window": "48h"}, 34560, "48h"},
		{"tiny window floored", map[string]any{"window": "1s"}, 1, "1s"},
		{"junk falls back", map[string]any{"window": "soon"}, 4320, "6h"},
		{"non-positive falls back", map[string]any{"window": "-1h"}, 4320, "6h"},
		{"wrong type falls back", map[string]any{"window": 6}, 4320, "6h"},
		// the scheduler-injected real cadence divides the window; the
		// assumed 5s is only the fallback
		{"1s poll x 6h", map[string]any{"window": "6h", "poll-interval": time.Second}, 21600, "6h"},
		{"1s poll x 24h at cap", map[string]any{"window": "24h", "poll-interval": time.Second}, 86400, "24h"},
		{"1s poll x 48h capped", map[string]any{"window": "48h", "poll-interval": time.Second}, 86400, "48h"},
		{"5s poll x 6h matches fallback", map[string]any{"window": "6h", "poll-interval": 5 * time.Second}, 4320, "6h"},
		{"non-positive cadence falls back", map[string]any{"window": "6h", "poll-interval": -time.Second}, 4320, "6h"},
		{"wrong cadence type falls back", map[string]any{"window": "6h", "poll-interval": "1s"}, 4320, "6h"},
	} {
		gotCap, gotHint := HistWindow(tc.params)
		if gotCap != tc.wantCap || gotHint != tc.want {
			t.Errorf("%s: HistWindow = (%d, %q), want (%d, %q)", tc.name, gotCap, gotHint, tc.wantCap, tc.want)
		}
	}
}

func TestHistCadence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		want   time.Duration
	}{
		{"nil params", nil, 5 * time.Second},
		{"missing key", map[string]any{}, 5 * time.Second},
		{"injected", map[string]any{"poll-interval": time.Second}, time.Second},
		{"zero falls back", map[string]any{"poll-interval": time.Duration(0)}, 5 * time.Second},
		{"negative falls back", map[string]any{"poll-interval": -time.Second}, 5 * time.Second},
		{"wrong type falls back", map[string]any{"poll-interval": "1s"}, 5 * time.Second},
	} {
		if got := HistCadence(tc.params); got != tc.want {
			t.Errorf("%s: HistCadence = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSnapRestoreRing pins the histsnap conversion pair: samples survive
// the float32 round trip oldest-first, and a restored ring keeps growing.
func TestSnapRestoreRing(t *testing.T) {
	if got := SnapRing(nil); got != nil {
		t.Errorf("SnapRing(nil) = %v, want nil", got)
	}

	r := NewRing(4)
	for _, v := range []float64{0.25, 0.5, 0.75} {
		r.Push(v)
	}
	snap := SnapRing(r)
	if len(snap) != 3 || snap[0] != 0.25 || snap[1] != 0.5 || snap[2] != 0.75 {
		t.Fatalf("SnapRing = %v, want [0.25 0.5 0.75]", snap)
	}

	restored := RestoreRing(snap)
	if got := restored.Samples(); len(got) != 3 || got[0] != 0.25 || got[2] != 0.75 {
		t.Fatalf("restored samples = %v, want [0.25 0.5 0.75]", got)
	}
	restored.Push(1)
	if got := restored.Samples(); len(got) != 3 || got[0] != 0.5 || got[2] != 1 {
		t.Errorf("post-restore push = %v, want capacity-3 ring [0.5 0.75 1]", got)
	}

	if got := RestoreRing(nil); got == nil || len(got.Samples()) != 0 {
		t.Errorf("RestoreRing(nil) = %v, want fresh empty ring", got)
	}
}

func TestResizeRing(t *testing.T) {
	r := NewRing(4)
	for i := 1; i <= 4; i++ {
		r.Push(float64(i))
	}
	if got := ResizeRing(r, 4); got != r {
		t.Error("same-capacity resize should return the ring unchanged")
	}

	grown := ResizeRing(r, 8)
	grown.Push(5)
	if got := grown.Samples(); len(got) != 5 || got[0] != 1 || got[4] != 5 {
		t.Errorf("grown samples = %v, want [1 2 3 4 5]", got)
	}

	shrunk := ResizeRing(grown, 2)
	if got := shrunk.Samples(); len(got) != 2 || got[0] != 4 || got[1] != 5 {
		t.Errorf("shrunk samples = %v, want newest [4 5]", got)
	}

	if got := ResizeRing(nil, 3); got == nil || len(got.Samples()) != 0 {
		t.Errorf("nil resize = %v, want fresh empty ring", got)
	}
}

func TestBucketMax(t *testing.T) {
	short := []float64{0.1, 0.2}
	if got := BucketMax(short, 60); len(got) != 2 {
		t.Errorf("short input downsampled: %v", got)
	}

	// 120 -> 60: two samples per bucket, max wins, so an odd-index spike
	// survives the squeeze
	long := make([]float64, 120)
	long[41] = 0.9 // bucket 20
	got := BucketMax(long, 60)
	if len(got) != 60 {
		t.Fatalf("len = %d, want 60", len(got))
	}
	if got[20] != 0.9 {
		t.Errorf("bucket 20 = %v, want spike 0.9 kept by max", got[20])
	}

	// uneven split: 7 -> 3 buckets covering all samples
	got = BucketMax([]float64{1, 2, 3, 4, 5, 6, 7}, 3)
	if len(got) != 3 || got[0] != 2 || got[1] != 4 || got[2] != 7 {
		t.Errorf("7->3 buckets = %v, want [2 4 7]", got)
	}
}

// TestSeriesCap pins the emission cap: oversized input keeps the newest
// MaxSeries samples.
func TestSeriesCap(t *testing.T) {
	long := make([]float64, MaxSeries+40)
	long[len(long)-1] = 1
	r := Series("cpu", long, "x")
	if len(r.Series) != MaxSeries {
		t.Fatalf("len = %d, want MaxSeries %d", len(r.Series), MaxSeries)
	}
	if r.Series[MaxSeries-1] != 1 {
		t.Error("cap dropped the newest sample instead of the oldest")
	}
}

func TestSpansRow(t *testing.T) {
	r := SpansRow(
		Span{Text: "name", Style: StyleTitle},
		Span{Text: " 12:34", Style: StyleHighlight},
	)
	if r.Kind != RowSpans || len(r.Spans) != 2 {
		t.Fatalf("SpansRow = %+v", r)
	}
	if r.Spans[0] != (Span{Text: "name", Style: StyleTitle}) ||
		r.Spans[1] != (Span{Text: " 12:34", Style: StyleHighlight}) {
		t.Errorf("spans = %+v", r.Spans)
	}
}

// TestIntParamFromConfig pins the CUE decode shape: config ints arrive as
// int64, the type the bus hands every module.
func TestIntParamFromConfig(t *testing.T) {
	c, err := config.Load("t.cue", []byte(`
widgets: w: {
	title: "w"
	glyph: "x"
	render: {
		kind:   "native"
		module: "github-prs"
		params: {limit: 20}
	}
}
layouts: main: {kind: "full-panel", tiles: ["w"]}
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	params := c.Widgets["w"].Render.Params
	if _, ok := params["limit"].(int64); !ok {
		t.Fatalf("limit decoded as %T, want int64", params["limit"])
	}
	if got := IntParam(params, "limit", 10); got != 20 {
		t.Errorf("IntParam(limit) = %d, want 20", got)
	}
}
