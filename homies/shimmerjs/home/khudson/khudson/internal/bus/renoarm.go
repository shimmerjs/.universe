package bus

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shimmerjs/khudson/khudson/internal/ax"
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

	detail := "obs " + obs.Version()
	if req.Config != nil {
		if err := ApplyRecordConfig(obs, *req.Config); err != nil {
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
		return fail(err.Error(), err)
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

	var done []string
	var errs []error
	// the virtual camera first: it is the one holding the camera light
	if active, err := obs.VirtualCamActive(); err != nil {
		errs = append(errs, fmt.Errorf("virtual cam status: %w", err))
	} else if active {
		if err := obs.StopVirtualCam(); err != nil {
			errs = append(errs, fmt.Errorf("stop virtual cam: %w", err))
		} else {
			done = append(done, "virtual cam stopped")
		}
	}
	n, err := releaseSceneItems(obs)
	if err != nil {
		errs = append(errs, err)
	}
	if n > 0 {
		done = append(done, fmt.Sprintf("%d sources released", n))
	}
	detail := "nothing was holding hardware"
	if len(done) > 0 {
		detail = strings.Join(done, ", ")
	}
	if len(errs) > 0 {
		joined := errors.Join(errs...)
		if _, nerr := ctl([]string{"arm-note", "error", "release: " + joined.Error()}); nerr != nil {
			joined = errors.Join(joined, nerr)
		}
		return v.Act(corr, "obs", "release", "error", joined.Error(), joined)
	}
	if err := noteWithRetry(ctl, []string{"arm-note", "released", detail}); err != nil {
		return v.Act(corr, "obs", "release", "unconfirmed",
			"released ("+detail+") but the note failed: "+err.Error(), err)
	}
	fmt.Println("released: " + detail)
	return v.Act(corr, "obs", "release", "ok", detail, nil)
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
