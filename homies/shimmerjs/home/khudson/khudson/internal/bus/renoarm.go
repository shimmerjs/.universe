package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shimmerjs/khudson/khudson/internal/ax"
	"github.com/shimmerjs/khudson/khudson/internal/paths"
	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// obsBundleID is OBS's bundle identifier, the handle the window verb hides
// and shows by. obs-websocket has no request that touches OBS's own main
// window, so window visibility is an AX concern, not a socket one.
const obsBundleID = "com.obsproject.obs-studio"

// Standby is phase one of recording: get OBS up and the scene matching the
// configured posture, and stop there. It exists because phase one is the
// slow half -- a cold OBS costs an app launch -- and welding it to the
// record tap meant "record" could mean "wait two minutes". Run it early and
// the record tap is the sub-second thing its name promises.
//
// It is idempotent by construction: warm OBS plus an already-matching scene
// makes it a confirmation rather than a change, so it stays tappable at any
// time instead of being a button you have to know when to press.
func (v *RenoVerbs) ArmRig(dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	fail := func(detail string, err error) error {
		if _, nerr := ctl([]string{"arm-note", "error", detail}); nerr != nil {
			err = errors.Join(err, nerr)
		}
		return v.Act(corr, "obs", "arm", "error", detail, err)
	}
	// the same payload the apply verb reads, for the same reason: taps that
	// landed while this was queued are already folded in
	raw, err := ctl([]string{"apply-begin"})
	if err != nil {
		return v.Act(corr, "obs", "arm", "error", "begin: "+err.Error(), err)
	}
	var req ApplyRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return fail("begin decode: "+err.Error(), err)
	}
	obs, err := dial()
	if err != nil {
		// unlike apply, a cold OBS IS the failure here: booting it is the
		// entire job, so a dial that could not is a standby that did not
		return fail(err.Error(), err)
	}
	defer obs.Close()

	// the rig coming up is the moment to find out whether something else is
	// already holding the camera -- the pad would otherwise report idle until
	// the next deliberate tap
	if active, err := obs.VirtualCamActive(); err == nil {
		state, why := "off", "nothing running it"
		if active {
			state, why = "on", "holding the camera"
		}
		noteVCam(ctl, state, why)
	}

	detail := "obs " + obs.Version()
	if req.Config != nil {
		dead, err := ApplyRecordConfig(obs, *req.Config)
		if err != nil {
			return fail(err.Error(), err)
		}
		if len(dead) > 0 {
			// a rig whose captures produce nothing is not armed, whatever
			// the scene looks like from here. REC must not appear over it:
			// the whole point of arming is that the next tap records
			// something, and this one would record black.
			err := fmt.Errorf("no frames from %s", strings.Join(dead, ", "))
			return fail(err.Error(), err)
		}
		detail += ", " + req.Config.Summary()
	} else {
		detail += ", unconfigured"
	}
	if err := noteWithRetry(ctl, []string{"arm-note", "ok", detail}); err != nil {
		return v.Act(corr, "obs", "arm", "unconfirmed",
			"armed ("+detail+") but the note failed: "+err.Error(), err)
	}
	fmt.Println("armed: " + detail)
	return v.Act(corr, "obs", "arm", "ok", detail, nil)
}

// ObsWindow shows, hides, or toggles OBS's own window. This is the AX
// hide/show primitive the demo verbs already use for every other app, NOT
// an obs-websocket call -- the protocol has no request for OBS's own
// window. The resolved state notes back so the pad can render it: kAXHidden
// reads back, so unlike an OBS filter this control can show its own state.
func (v *RenoVerbs) ObsWindow(op string, ctl CtlCall, corr RenoCorr) error {
	fail := func(detail string, err error) error {
		if _, nerr := ctl([]string{"window-note", "error", detail}); nerr != nil {
			err = errors.Join(err, nerr)
		}
		return v.Act(corr, "obs", "window "+op, "error", detail, err)
	}
	var want bool
	switch op {
	case "hide":
		want = true
	case "show":
		want = false
	case "toggle":
		// read first: a toggle that assumed a state would fight the user
		// every time OBS was hidden or raised by anything else
		hidden, err := ax.AppHidden(obsBundleID)
		if err != nil {
			return fail(err.Error(), err)
		}
		want = !hidden
	default:
		err := fmt.Errorf("reno obs window: need show | hide | toggle, got %q", op)
		return v.Act(corr, "obs", "window "+op, "error", err.Error(), err)
	}
	if err := ax.SetAppHidden(obsBundleID, want); err != nil {
		// Unhiding is not enough to bring OBS back, and the failure is the
		// posture khudson itself seeds: SysTrayMinimizeToTray stows the main
		// window in the menu bar, which is not the app-level AXHidden flag.
		// An OBS started to tray owns no window at all, so the unhide has
		// nothing to reveal, the readback never changes, and SetAppHidden
		// reports it did not take -- correctly. Cooperative activation (the
		// reopen `open -b` sends) is what OBS answers by restoring the
		// window, so on the show path that is the verb, not a fallback.
		if want {
			return fail(err.Error(), err)
		}
		if ferr := ax.FocusApp(obsBundleID); ferr != nil {
			return fail(errors.Join(err, ferr).Error(), errors.Join(err, ferr))
		}
	}
	state := "shown"
	if want {
		state = "hidden"
	}
	if err := noteWithRetry(ctl, []string{"window-note", state}); err != nil {
		return v.Act(corr, "obs", "window "+op, "unconfirmed",
			"obs window "+state+" but the note failed: "+err.Error(), err)
	}
	fmt.Println("obs window " + state)
	return v.Act(corr, "obs", "window "+op, "ok", state, nil)
}

// VirtualCam reports OBS's virtual camera output and, on "stop", takes it
// down. The output is the one thing that can hold the camera with nothing
// accounting for it: khudson never starts it, macOS does, the moment any app
// opens the OBS Virtual Camera device -- and Release could only ever stop it
// on the way out, silently.
//
// There is no start op, deliberately: nothing in this rig wants one running,
// so the tile behind this verb is safe to hit in any state.
//
// Dial-only, and a cold OBS is a definite "off" rather than an unknown -- the
// output is OBS's, so no OBS means nothing is running it.
func (v *RenoVerbs) VirtualCam(op string, dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	if op != "status" && op != "stop" {
		err := fmt.Errorf("reno obs vcam: need status | stop, got %q", op)
		return v.Act(corr, "obs", "vcam "+op, "error", err.Error(), err)
	}
	fail := func(detail string, err error) error {
		if _, nerr := ctl([]string{"vcam-note", "error", detail}); nerr != nil {
			err = errors.Join(err, nerr)
		}
		return v.Act(corr, "obs", "vcam "+op, "error", detail, err)
	}
	note := func(state, detail string) error {
		if err := noteWithRetry(ctl, []string{"vcam-note", state, detail}); err != nil {
			return v.Act(corr, "obs", "vcam "+op, "unconfirmed",
				"virtual cam "+state+" but the note failed: "+err.Error(), err)
		}
		fmt.Println("virtual cam " + state + ": " + detail)
		return v.Act(corr, "obs", "vcam "+op, "ok", state, nil)
	}
	obs, err := dial()
	if err != nil {
		return note("off", "obs cold")
	}
	defer obs.Close()

	active, err := obs.VirtualCamActive()
	if err != nil {
		return fail("virtual cam status: "+err.Error(), err)
	}
	if !active {
		return note("off", "nothing running it")
	}
	if op == "status" {
		return note("on", "holding the camera")
	}
	if err := obs.StopVirtualCam(); err != nil {
		return fail("stop virtual cam: "+err.Error(), err)
	}
	return note("off", "stopped")
}

// OpenClip hands a finished take to the OS. The pad cannot publish a bare
// `open` -- the effect executor admits `khudson reno <verb>` argvs only --
// and that constraint is what puts the path check here, where it belongs: the
// argv arrives over the bus, so a verb that opened whatever it was handed
// would be one publishing bug away from a general file-opener.
//
// Symlinks resolve BEFORE the containment test, not after. A prefix test on
// the unresolved path passes for a link that lives in the workspace and
// points anywhere on the disk.
func (v *RenoVerbs) OpenClip(ctx context.Context, clip string, corr RenoCorr) error {
	fail := func(detail string, err error) error {
		return v.Act(corr, "open-clip", clip, "error", detail, err)
	}
	p, err := paths.Resolve()
	if err != nil {
		return fail(err.Error(), err)
	}
	root := renocore.WorkspaceRoot(filepath.Join(p.Dir, "reno"))
	// EvalSymlinks resolves the file AND every parent, which on macOS also
	// collapses /var -> /private/var; the root has to make the same trip or
	// every real clip path fails the compare
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fail("workspace root: "+err.Error(), err)
	}
	target, err := filepath.EvalSymlinks(clip)
	if err != nil {
		return fail(err.Error(), err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		err := fmt.Errorf("reno open-clip: %s resolves outside the workspace (%s)", clip, root)
		return fail(err.Error(), err)
	}
	if err := v.openStart(ctx, []string{"open", target}); err != nil {
		return fail(err.Error(), err)
	}
	fmt.Println("opened " + target)
	return v.Act(corr, "open-clip", filepath.Base(target), "ok", target, nil)
}

// noteVCam refreshes the pad's virtual-camera readout from a verb that had
// the socket open anyway. Best-effort by design: arming and releasing are the
// jobs, and neither fails because a readout did not land.
func noteVCam(ctl CtlCall, state, detail string) {
	_, _ = ctl([]string{"vcam-note", state, detail})
}

// Release is the counterpart to ArmRig, and what END RUN performs: put the
// rig down. Stopping a take does NOT do this -- between takes you want the
// devices held so the next one is instant -- but ending the session does,
// because the failure this exists to prevent is walking away with the
// camera light still on.
//
// Two things hold hardware after a recording stops. The reno scene's items
// stay enabled, so the screen capture keeps capturing and the cam keeps the
// camera open; and OBS's VIRTUAL CAMERA may be running, which nothing here
// ever started -- macOS starts it on demand the moment any app opens the
// OBS Virtual Camera device, and it then holds the camera with no trace on
// this pad. Both go down here.
//
// Dial-only, and a cold OBS is success: an OBS that is not running is
// already as released as it can get.
func (v *RenoVerbs) Release(dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	obs, err := dial()
	if err != nil {
		if _, nerr := ctl([]string{"arm-note", "released", "obs cold"}); nerr != nil {
			return v.Act(corr, "obs", "release", "error", "obs cold and the note failed: "+nerr.Error(), errors.Join(err, nerr))
		}
		return v.Act(corr, "obs", "release", "ok", "obs already down", nil)
	}
	defer obs.Close()

	detail, err := releaseHardware(obs, ctl)
	if err != nil {
		if _, nerr := ctl([]string{"arm-note", "error", "release: " + err.Error()}); nerr != nil {
			err = errors.Join(err, nerr)
		}
		return v.Act(corr, "obs", "release", "error", err.Error(), err)
	}
	if err := noteWithRetry(ctl, []string{"arm-note", "released", detail}); err != nil {
		return v.Act(corr, "obs", "release", "unconfirmed",
			"released ("+detail+") but the note failed: "+err.Error(), err)
	}
	fmt.Println("released: " + detail)
	return v.Act(corr, "obs", "release", "ok", detail, nil)
}

// Quit puts OBS all the way down, which nothing else here does. Release
// drops the hardware but leaves the app running, and a session therefore
// ended in four verbs -- close the run, stop the take, release, quit the
// app -- with the last one an osascript nobody had wrapped. This is those
// four, in the only order that works.
//
// The order matters twice over. The take stops before the release, because
// releasing a scene mid-recording writes a clip of disabled sources; and
// the app quits last, over a socket already closed, because a quit
// delivered while a request is in flight is how you get a half-written
// clip. And it is a GRACEFUL quit -- an AppleEvent, never a signal. OBS
// opens its next log with "Crash or unclean shutdown detected" after a hard
// kill, and the sentinel that leaves behind wedges the websocket on the
// following launch.
func (v *RenoVerbs) Quit(dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	fail := func(detail string, err error) error {
		return v.Act(corr, "obs", "quit", "error", detail, err)
	}
	// closes the run and hands back the take still owing a stop note. It
	// emits no release and no record-stop of its own: those are the two
	// things this verb is about to do itself, and a second pair of children
	// would race it to an OBS that is on its way out.
	raw, err := ctl([]string{"quit-begin"})
	if err != nil {
		return fail("begin: "+err.Error(), err)
	}
	var req QuitRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return fail("begin decode: "+err.Error(), err)
	}

	var done []string
	obs, err := dial()
	if err != nil {
		// a cold socket is not a cold app: OBS with obs-websocket off, or
		// still starting, still needs the quit below
		done = append(done, "obs socket down")
	} else {
		if req.Recording {
			path, serr := obs.StopRecord()
			switch {
			case serr != nil && !isOutputNotRunning(serr):
				obs.Close()
				return fail("stop recording: "+serr.Error(), serr)
			case serr != nil:
				// the journal thinks a take is open and OBS says otherwise;
				// close it without a path rather than leave it open forever
				// (the ObsOp stop precedent)
				noteRecordStopped(ctl, req.Run, "")
				done = append(done, "no active recording")
			default:
				noteRecordStopped(ctl, req.Run, path)
				done = append(done, "recording stopped ("+path+")")
			}
		}
		detail, rerr := releaseHardware(obs, ctl)
		if rerr != nil {
			obs.Close()
			return fail("release: "+rerr.Error(), rerr)
		}
		done = append(done, detail)
		// closed HERE, not on a defer: the quit below must not race a
		// request still on the wire
		obs.Close()
	}
	if _, err := ctl([]string{"arm-note", "released", strings.Join(done, ", ")}); err != nil {
		// the rig is down either way, and saying so is worth more than the
		// readout that failed to land
		fmt.Fprintln(os.Stderr, "khudson reno obs quit: release note: "+err.Error())
	}

	switch err := quitObsApp(); {
	case errors.Is(err, ax.ErrNotFound):
		done = append(done, "obs was not running")
	case err != nil:
		return fail("quit obs: "+err.Error()+
			" (an AppleEvent quit needs the one-time Automation grant for khudson -> OBS)", err)
	default:
		done = append(done, obsQuitOutcome())
	}
	detail := strings.Join(done, ", ")
	fmt.Println("quit: " + detail)
	return v.Act(corr, "obs", "quit", "ok", detail, nil)
}

// The app-level half of the quit, injectable for the same reason the ax
// package makes its own osascript and kill seams injectable: a test that
// exercised the real ones would quit an app on the machine running it.
var (
	quitObsApp    = func() error { return ax.QuitBundle(obsBundleID) }
	obsAppRunning = func() (bool, error) { return ax.BundleRunning(obsBundleID) }
)

// ObsQuitGrace bounds the wait for OBS to actually go. A quit AppleEvent
// returns once it is DELIVERED, so without this the verb would report a
// dead app while it was still flushing its recording. Generous, because
// what OBS spends the time on is finalising a file.
const ObsQuitGrace = 15 * time.Second

// obsQuitPoll is how often the departure is checked; each check is an
// lsappinfo exec, so it is coarse.
const obsQuitPoll = 250 * time.Millisecond

// obsQuitWait is the grace the departure check actually spends; tests
// shorten it rather than sit out the real one.
var obsQuitWait = ObsQuitGrace

// obsQuitOutcome waits out the grace and says what actually happened. An
// OBS still up at the end is NOT an error -- it is most likely sitting on
// its own "save changes?" sheet, which is a human's to answer -- but the
// verb must not claim it quit.
func obsQuitOutcome() string {
	deadline := time.Now().Add(obsQuitWait)
	for {
		running, err := obsAppRunning()
		if err != nil {
			return "obs asked to quit (could not confirm: " + err.Error() + ")"
		}
		if !running {
			return "obs quit"
		}
		if time.Now().After(deadline) {
			return fmt.Sprintf("obs asked to quit but still up after %s (a dialog may be waiting)", obsQuitWait)
		}
		time.Sleep(obsQuitPoll)
	}
}

// noteRecordStopped closes the take in the journal. Best effort with a
// retry: the clip is already written, and a lost note costs the output
// path on the pad, never the file.
func noteRecordStopped(ctl CtlCall, run, path string) {
	args := []string{"record-note", run, "stopped"}
	if path != "" {
		args = append(args, path)
	}
	if err := noteWithRetry(ctl, args); err != nil {
		fmt.Fprintln(os.Stderr, "khudson reno obs quit: record note: "+err.Error())
	}
}

// releaseHardware drops everything holding a device: OBS's virtual camera
// first, because that is the output holding the camera light, then every
// reno scene item. It returns the human summary the caller notes back.
//
// Shared by Release and Quit, and the sharing is the point -- a quit that
// re-implemented this would be the one path that eventually forgot the
// virtual camera, which is precisely the failure Release exists to prevent.
func releaseHardware(obs ObsTransport, ctl CtlCall) (string, error) {
	var done []string
	var errs []error
	if active, err := obs.VirtualCamActive(); err != nil {
		errs = append(errs, fmt.Errorf("virtual cam status: %w", err))
	} else if active {
		if err := obs.StopVirtualCam(); err != nil {
			errs = append(errs, fmt.Errorf("stop virtual cam: %w", err))
		} else {
			done = append(done, "virtual cam stopped")
			noteVCam(ctl, "off", "released")
		}
	} else {
		noteVCam(ctl, "off", "nothing running it")
	}
	n, err := releaseSceneItems(obs)
	if err != nil {
		errs = append(errs, err)
	}
	if n > 0 {
		done = append(done, fmt.Sprintf("%d sources released", n))
	}
	// last, and after the items are down: the canvas is a profile-global
	// setting reno wrote to fit its own composition, so a scene that is not
	// reno's would otherwise render into a frame shaped for one.
	if detail, err := restoreCanvas(obs); err != nil {
		errs = append(errs, err)
	} else if detail != "" {
		done = append(done, detail)
	}
	if len(errs) > 0 {
		return "", errors.Join(errs...)
	}
	if len(done) == 0 {
		return "nothing was holding hardware", nil
	}
	return strings.Join(done, ", "), nil
}

// releaseSceneItems disables every item khudson owns in the reno scene,
// reporting how many went down. Only OUR items: a hand-added source in the
// same scene is the user's, and a release verb that reached past its own
// inputs would be editing someone else's scene.
func releaseSceneItems(obs ObsTransport) (int, error) {
	inputs, err := obs.Inputs()
	if err != nil {
		return 0, fmt.Errorf("inputs: %w", err)
	}
	var errs []error
	n := 0
	for name := range inputs {
		if !renocore.IsRenoInput(name) {
			continue
		}
		id, err := obs.SceneItemID(renocore.SceneName, name, false)
		if err != nil {
			continue // not in the scene: nothing to disable
		}
		if err := obs.SetSceneItemEnabled(renocore.SceneName, id, false); err != nil {
			errs = append(errs, fmt.Errorf("disable %s: %w", name, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}
