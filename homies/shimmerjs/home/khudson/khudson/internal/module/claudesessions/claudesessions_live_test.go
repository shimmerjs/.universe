package claudesessions

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestPollLiveHost polls this host's real projects tree and statusline
// cache; gated so CI stays hermetic.
func TestPollLiveHost(t *testing.T) {
	if os.Getenv("KHUDSON_CLAUDE_LIVE") == "" {
		t.Skip("live host poll: set KHUDSON_CLAUDE_LIVE=1")
	}
	// TestMain pins the identity seam for fixture determinism; the real
	// host registry needs the real probe
	prev := procStartTime
	procStartTime = sysctlProcStartReal
	defer func() { procStartTime = prev }()
	data, err := New().Poll(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	t.Logf("title=%q", data.Title)
	for _, r := range data.Rows {
		if r.Kind == "spans" {
			var b strings.Builder
			for _, s := range r.Spans {
				b.WriteString("[" + s.Style + "]" + s.Text)
			}
			t.Logf("kind=%-6s style=%-6q %s", r.Kind, r.Style, b.String())
			continue
		}
		t.Logf("kind=%-6s style=%-6q text=%q", r.Kind, r.Style, r.Text)
	}
}
