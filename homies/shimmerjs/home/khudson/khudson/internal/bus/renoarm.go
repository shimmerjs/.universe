package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

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
