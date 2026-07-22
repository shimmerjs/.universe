package bus

import (
	"os"
	"testing"
)

// TestObsLiveProbe exercises the real goobs transport against a running
// OBS (websocket server enabled). Gated: OBS is a homebrew cask + a
// user-side websocket toggle, so CI and the closure build never see it.
func TestObsLiveProbe(t *testing.T) {
	if os.Getenv("KHUDSON_OBS_LIVE") != "1" {
		t.Skip("set KHUDSON_OBS_LIVE=1 with OBS running to exercise the live transport")
	}
	obs, err := DialOBS(DefaultObsHost, ObsPassword())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer obs.Close()
	if obs.Version() == "" {
		t.Error("no version from probe")
	}
	t.Logf("obs %s, SetRecordDirectory=%v", obs.Version(), obs.CanSetRecordDirectory())
}
