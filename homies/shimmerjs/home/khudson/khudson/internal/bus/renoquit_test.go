package bus

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/shimmerjs/khudson/khudson/internal/ax"
)

// fakeQuitApp swaps the app-level quit for a recorder. gone is what the
// departure check reports, so a test can have OBS leave, linger, or never
// have been there.
func fakeQuitApp(t *testing.T, quitErr error, gone bool) *int {
	t.Helper()
	calls := 0
	oldQuit, oldRunning := quitObsApp, obsAppRunning
	quitObsApp = func() error { calls++; return quitErr }
	obsAppRunning = func() (bool, error) { return !gone, nil }
	t.Cleanup(func() { quitObsApp, obsAppRunning = oldQuit, oldRunning })
	return &calls
}

// quitCtl serves one quit-begin payload then records the notes.
func quitCtl(t *testing.T, req QuitRequest, beginErr error) (CtlCall, *[][]string) {
	t.Helper()
	notes := &[][]string{}
	return func(args []string) (json.RawMessage, error) {
		if args[0] == "quit-begin" {
			if beginErr != nil {
				return nil, beginErr
			}
			raw, _ := json.Marshal(req)
			return raw, nil
		}
		*notes = append(*notes, args)
		return nil, nil
	}, notes
}

// The whole point of one verb is that the steps happen in the one order
// that works: the take stops before the scene is released (a release
// mid-recording writes a clip of disabled sources), and the socket closes
// before the app is asked to quit (a quit racing a request in flight is
// how a clip comes out half written).
func TestQuitOrdersStopReleaseThenTheApp(t *testing.T) {
	v, _ := renoTestVerbs(t)
	calls := fakeQuitApp(t, nil, true)
	ctl, notes := quitCtl(t, QuitRequest{Run: "r9", Recording: true}, nil)
	obs := &fakeObs{vcam: true, output: "/clips/take.mkv",
		inputs:  map[string]string{"reno-cam": "avcapture"},
		itemIDs: map[string]int{"reno/reno-cam": 7}}
	if err := v.Quit(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{Step: -1, Act: -1}); err != nil {
		t.Fatalf("quit: %v", err)
	}
	stop := slices.Index(obs.ops, "stop")
	vcamStop := slices.Index(obs.ops, "vcam-stop")
	close := slices.Index(obs.ops, "close")
	switch {
	case stop < 0 || vcamStop < 0 || close < 0:
		t.Fatalf("quit skipped an op: %v", obs.ops)
	case stop > vcamStop:
		t.Errorf("the take must stop before the release: %v", obs.ops)
	case close != len(obs.ops)-1:
		t.Errorf("the socket must close last: %v", obs.ops)
	}
	if *calls != 1 {
		t.Errorf("the app was asked to quit %d times, want 1", *calls)
	}
	// the clip path has to reach the run it belongs to, or the take is a
	// file on disk with nothing on the pad pointing at it
	var stopped []string
	for _, n := range *notes {
		if n[0] == "record-note" {
			stopped = n
		}
	}
	want := []string{"record-note", "r9", "stopped", "/clips/take.mkv"}
	if !slices.Equal(stopped, want) {
		t.Errorf("record note = %v, want %v", stopped, want)
	}
}

// Nothing recording means nothing to stop: the verb must not send a
// StopRecord OBS would refuse, and must not invent a stop note for a run
// whose journal shows no open take.
func TestQuitWithNoTakeSkipsTheStop(t *testing.T) {
	v, _ := renoTestVerbs(t)
	fakeQuitApp(t, nil, true)
	ctl, notes := quitCtl(t, QuitRequest{Run: "r9"}, nil)
	obs := &fakeObs{}
	if err := v.Quit(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{}); err != nil {
		t.Fatalf("quit: %v", err)
	}
	if slices.Contains(obs.ops, "stop") {
		t.Errorf("no open take, so no StopRecord: %v", obs.ops)
	}
	for _, n := range *notes {
		if n[0] == "record-note" {
			t.Errorf("no open take, so no record note: %v", n)
		}
	}
}

// The journal thinking a take is open while OBS says otherwise is the
// recoverable case, not a failure: close the take without a path so the
// run stops claiming it is recording, and carry on quitting.
func TestQuitClosesATakeObsIsNotRolling(t *testing.T) {
	v, _ := renoTestVerbs(t)
	calls := fakeQuitApp(t, nil, true)
	ctl, notes := quitCtl(t, QuitRequest{Run: "r9", Recording: true}, nil)
	obs := &fakeObs{fail: map[string]error{"stop": fmt.Errorf("output not running")}}
	if err := v.Quit(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{}); err != nil {
		t.Fatalf("quit: %v", err)
	}
	var stopped []string
	for _, n := range *notes {
		if n[0] == "record-note" {
			stopped = n
		}
	}
	if !slices.Equal(stopped, []string{"record-note", "r9", "stopped"}) {
		t.Errorf("record note = %v, want a pathless stop", stopped)
	}
	if *calls != 1 {
		t.Errorf("a take OBS was not rolling must not stop the quit (%d app quits)", *calls)
	}
}

// A dead socket is not a dead app. OBS with obs-websocket switched off, or
// still coming up, is exactly the state you most want a quit to work in --
// so the dial failing skips the OBS-side cleanup and still quits.
func TestQuitWithNoSocketStillQuitsTheApp(t *testing.T) {
	v, _ := renoTestVerbs(t)
	calls := fakeQuitApp(t, nil, true)
	ctl, _ := quitCtl(t, QuitRequest{}, nil)
	err := v.Quit(func() (ObsTransport, error) {
		return nil, fmt.Errorf("connection refused")
	}, ctl, RenoCorr{})
	if err != nil {
		t.Fatalf("quit: %v", err)
	}
	if *calls != 1 {
		t.Errorf("a cold socket must still quit the app (%d quits)", *calls)
	}
	ev := readEvents(t, v)
	if last := ev[len(ev)-1]; last.Outcome != "ok" || !strings.Contains(last.Detail, "socket down") {
		t.Errorf("cold-socket quit = %+v, want an ok that says the socket was down", last)
	}
}

// An app that was never running is a quit that has already succeeded.
func TestQuitOfAColdAppIsOk(t *testing.T) {
	v, _ := renoTestVerbs(t)
	fakeQuitApp(t, ax.ErrNotFound, true)
	ctl, _ := quitCtl(t, QuitRequest{}, nil)
	if err := v.Quit(func() (ObsTransport, error) {
		return nil, fmt.Errorf("connection refused")
	}, ctl, RenoCorr{}); err != nil {
		t.Fatalf("quitting a cold app must succeed: %v", err)
	}
	ev := readEvents(t, v)
	if last := ev[len(ev)-1]; !strings.Contains(last.Detail, "not running") {
		t.Errorf("cold app = %+v, want it said so", last)
	}
}

// A release that could not put the hardware down must fail the verb rather
// than quit over it: the whole reason this exists is not walking away with
// the camera light on, and an app quit on top of a failed release hides the
// one thing you needed to know.
func TestQuitFailsOnAFailedRelease(t *testing.T) {
	v, _ := renoTestVerbs(t)
	calls := fakeQuitApp(t, nil, true)
	ctl, _ := quitCtl(t, QuitRequest{}, nil)
	obs := &fakeObs{fail: map[string]error{"vcam-status": fmt.Errorf("socket gone")}}
	if err := v.Quit(func() (ObsTransport, error) { return obs, nil }, ctl, RenoCorr{}); err == nil {
		t.Fatal("a failed release must fail the quit")
	}
	if *calls != 0 {
		t.Errorf("a failed release must not go on to quit the app (%d quits)", *calls)
	}
	if !slices.Contains(obs.ops, "close") {
		t.Errorf("the socket must close even on the failure path: %v", obs.ops)
	}
}

// The verb reports what it saw, not what it asked for: an OBS still up at
// the end of the grace is almost always sitting on its own save-changes
// sheet, and saying "quit" over that is the lie the readback exists to
// prevent.
func TestQuitReportsAnAppThatDidNotGo(t *testing.T) {
	v, _ := renoTestVerbs(t)
	oldWait := obsQuitWait
	obsQuitWait = 0
	t.Cleanup(func() { obsQuitWait = oldWait })
	fakeQuitApp(t, nil, false)
	ctl, _ := quitCtl(t, QuitRequest{}, nil)
	if err := v.Quit(func() (ObsTransport, error) {
		return nil, fmt.Errorf("connection refused")
	}, ctl, RenoCorr{}); err != nil {
		t.Fatalf("a lingering app is not an error: %v", err)
	}
	ev := readEvents(t, v)
	if last := ev[len(ev)-1]; !strings.Contains(last.Detail, "still up") {
		t.Errorf("lingering app = %+v, want it said so", last)
	}
}

// quit-begin is the handshake that closes the run; without it the verb has
// no idea whether a take is owed a stop note, so a failure there stops the
// quit before anything is dialled.
func TestQuitRefusesWithoutTheHandshake(t *testing.T) {
	v, _ := renoTestVerbs(t)
	calls := fakeQuitApp(t, nil, true)
	ctl, _ := quitCtl(t, QuitRequest{}, fmt.Errorf("bus not reachable"))
	if err := v.Quit(func() (ObsTransport, error) {
		t.Fatal("a failed handshake must not dial")
		return nil, nil
	}, ctl, RenoCorr{}); err == nil {
		t.Fatal("a failed quit-begin must fail the verb")
	}
	if *calls != 0 {
		t.Errorf("a failed handshake must not quit the app (%d quits)", *calls)
	}
}
