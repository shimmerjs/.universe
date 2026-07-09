package kittysessions

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestPollLive drives Poll against the real main kitty: default socket,
// real rc-password.conf, env-var auth. Gated -- needs a running LS-launched
// kitty with mainKittyIntegration enabled.
func TestPollLive(t *testing.T) {
	if os.Getenv("KHUDSON_MAIN_KITTY_LIVE") == "" {
		t.Skip("KHUDSON_MAIN_KITTY_LIVE not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	d, err := Mod{}.Poll(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Title != "kitty" || len(d.Rows) == 0 {
		t.Fatalf("Poll = %+v, want kitty title and rows", d)
	}
	t.Logf("%d rows", len(d.Rows))
}
