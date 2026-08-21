package bus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// TestObsLiveProbe exercises the real goobs transport against a running
// OBS. Gated: OBS is a homebrew cask, so CI and the closure build never
// see it. ObsBoot launches OBS itself -- a cold machine needs no
// hand-enabled websocket, only the one-time Gatekeeper / TCC dialogs on
// the very first run.
func TestObsLiveProbe(t *testing.T) {
	if os.Getenv("KHUDSON_OBS_LIVE") != "1" {
		t.Skip("set KHUDSON_OBS_LIVE=1 to exercise the live transport (launches OBS)")
	}
	boot, err := NewObsBoot(DefaultObsHost)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := boot.Ensure(EnsureDeadline)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	defer obs.Close()
	if obs.Version() == "" {
		t.Error("no version from probe")
	}
	t.Logf("obs %s, SetRecordDirectory=%v", obs.Version(), obs.CanSetRecordDirectory())
}

// TestObsLiveOrchestration is the P1+expansion live verification end to
// end: ensure (seed config + launch), probe the environment, apply a
// real RecordConfig (first display, audio none), record ~3s, stop, and
// assert the clip landed in the workspace. The ctl seam is faked --
// this validates the OBS side without needing a bus running the new
// binary.
func TestObsLiveOrchestration(t *testing.T) {
	if os.Getenv("KHUDSON_OBS_LIVE") != "1" {
		t.Skip("set KHUDSON_OBS_LIVE=1 to exercise the live orchestration (launches OBS, records)")
	}
	boot, err := NewObsBoot(DefaultObsHost)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := boot.Ensure(EnsureDeadline)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	defer obs.Close()

	env, err := ProbeEnv(obs)
	if err != nil {
		t.Fatalf("probe env: %v", err)
	}
	t.Logf("kinds: screen=%q cam=%q displayProp=%q", env.ScreenKind, env.CamKind, env.DisplayProp)
	for _, d := range env.Displays {
		t.Logf("display: %s = %s", d.Name, d.Value)
	}
	for _, m := range env.Mics {
		t.Logf("mic: %s", m.Name)
	}
	for _, c := range env.Cams {
		t.Logf("cam: %s", c.Name)
	}
	if len(env.Displays) == 0 {
		t.Fatal("no displays enumerated")
	}

	dir := t.TempDir()
	v, err := NewRenoVerbs()
	if err != nil {
		t.Fatal(err)
	}
	v.EventsPath = filepath.Join(dir, "events.jsonl")
	cfg := &renocore.RecordConfig{
		Screens: []renocore.ScreenSel{{Display: env.Displays[0].Value}},
		Audio:   renocore.AudioNone,
	}
	notes := [][]string{}
	ctl := func(args []string) (json.RawMessage, error) {
		if args[0] == "record-open" {
			raw, _ := json.Marshal(RecordOpen{Run: "live-test", Directory: dir, Config: cfg})
			return raw, nil
		}
		notes = append(notes, args)
		return nil, nil
	}
	dial := func() (ObsTransport, error) { return DialOBS(DefaultObsHost, ObsPassword()) }
	if err := v.Record(dial, ctl, RenoCorr{Run: "live-test", Step: -1, Act: -1}); err != nil {
		t.Fatalf("record: %v", err)
	}
	time.Sleep(3 * time.Second)
	if err := v.ObsOp("stop", "", dial, ctl, RenoCorr{Run: "live-test", Step: -1, Act: -1}); err != nil {
		t.Fatalf("stop: %v", err)
	}

	var clip string
	for _, n := range notes {
		if len(n) >= 4 && n[2] == "stopped" {
			clip = n[3]
		}
	}
	if clip == "" {
		t.Fatalf("no stopped note with an output path: %v", notes)
	}
	// OBS finalizes the file asynchronously after StopRecord returns
	// (live-observed: 0 bytes at the instant of the stop note); poll
	// briefly for the flush
	var info os.FileInfo
	for range 20 {
		var err error
		if info, err = os.Stat(clip); err == nil && info.Size() > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if info == nil {
		t.Fatalf("clip never appeared: %s", clip)
	}
	t.Logf("clip: %s (%d bytes)", clip, info.Size())
	if info.Size() == 0 {
		t.Error("clip is empty after 10s")
	}
	if filepath.Dir(clip) != dir {
		t.Logf("note: clip landed outside the workspace (%s) -- SetRecordDirectory capability=%v", filepath.Dir(clip), obs.CanSetRecordDirectory())
	}
}
