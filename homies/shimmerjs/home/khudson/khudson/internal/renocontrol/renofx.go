// Webcam FX executor: the thin half of the P3 vocabulary. The effect is
// computed pure in internal/reno (BuildCamFXPlan, unit-tested); this
// gathers the small live environment it needs and ships the resulting
// request sequence as one frame-locked batch.
//
// Deliberately dial-only, like the other transport ops: an FX is a tweak
// against OBS as it stands, and a stray tile must never cold-launch the
// app. Deliberately NOT a ProbeEnv either -- a mid-demo one-shot cannot
// afford a full device enumeration, and it must never conjure the cam
// item: an effect can only move something that is already there.
package renocontrol

import (
	"errors"
	"fmt"
	"time"

	renocore "github.com/shimmerjs/khudson/khudson/internal/reno"
)

// fxSlack is added to a move's own duration to bound the batch exchange:
// the response only lands once OBS has run the last request, so the
// budget has to cover playback plus connect and handshake.
const fxSlack = 10 * time.Second

// CamFX is the `khudson reno obs cam <kind> [args]` verb: one webcam
// effect against the live reno scene, journaled like every action and
// noted back for the recorder pad's readout.
func (v *RenoVerbs) CamFX(fx renocore.CamFX, dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr) error {
	target := fx.String()
	fail := func(err error) error {
		if ctl != nil {
			if _, nerr := ctl([]string{"fx-note", "error", err.Error()}); nerr != nil {
				err = errors.Join(err, nerr)
			}
		}
		return v.Act(corr, "cam", target, "error", err.Error(), err)
	}
	// A sustained effect fired as a SCRIPT STEP is about to be driven: the
	// bus that scheduled this argv adopts the level off the note below and
	// starts a driver for it. Running the two-second span here as well makes
	// the effect start twice, with a driver spawn's worth of stillness in the
	// seam. The note is the whole verb in that case -- correlation is the
	// signal, because only the bus's own executor issues one.
	if renocore.IsSustainFX(fx.Kind) && fx.On() && corr.Run != "" && ctl != nil {
		if _, err := ctl([]string{"fx-note", "ok", fx.String()}); err != nil {
			return fail(err)
		}
		return v.Act(corr, "cam", target, "ok", fx.String()+" (driven by the bus)", nil)
	}
	obs, err := dial()
	if err != nil {
		return fail(err)
	}
	defer obs.Close()
	env, err := fxEnv(obs, fx)
	if err != nil {
		return fail(err)
	}
	plan, err := renocore.BuildCamFXPlan(fx, env)
	if err != nil {
		return fail(err)
	}
	if len(plan.Requests) > 0 {
		budget := fx.Dur + fxSlack
		if err := obs.RunFrameBatch(fxBatchID(corr), plan.Requests, budget); err != nil {
			return fail(err)
		}
	}
	if ctl != nil {
		if _, err := ctl([]string{"fx-note", "ok", plan.Summary}); err != nil {
			return v.Act(corr, "cam", target, "unconfirmed",
				"fx landed ("+plan.Summary+") but the note failed: "+err.Error(), err)
		}
	}
	return v.Act(corr, "cam", target, "ok", plan.Summary, nil)
}

// fxEnv gathers exactly what the effect needs and nothing more: the
// canvas and frame rate always, the current pose only for a move, the
// filter probe only for a level effect.
func fxEnv(obs ObsTransport, fx renocore.CamFX) (renocore.FXEnv, error) {
	env := renocore.FXEnv{Scene: renocore.SceneName, Source: renocore.CamInput}
	var err error
	if env.CanvasW, env.CanvasH, env.FPS, err = obs.Canvas(); err != nil {
		return env, fmt.Errorf("canvas: %w", err)
	}
	// ensure=false: the cam item has to exist already, and the error says
	// how to get one instead of quietly staging a device-less black box
	if env.ItemID, err = obs.SceneItemID(env.Scene, env.Source, false); err != nil {
		return env, fmt.Errorf("no %s item in the %s scene -- set a webcam preset first (khudson reno config --webcam br): %w",
			env.Source, env.Scene, err)
	}
	// a move interpolates out of the current pose and a jitter shakes around
	// it, so both want the transform
	if fx.Kind == renocore.FXMove || fx.Kind == renocore.FXJitter {
		if env.From, err = obs.ItemTransform(env.Scene, env.ItemID); err != nil {
			return env, fmt.Errorf("current transform: %w", err)
		}
	}
	// Pixelate scales down and back up, and the size it goes back to has to
	// be the one it was handed or the item's scale stops matching its source.
	// A placement wants it too: a corner box is shaped to the CAMERA's aspect
	// so the preset hugs the corner it is named after on any canvas.
	if fx.Kind == renocore.FXPixelate || fx.Kind == renocore.FXMove || fx.Kind == renocore.FXJump {
		if env.SourceW, env.SourceH, err = obs.ItemSourceSize(env.Scene, env.ItemID); err != nil {
			return env, fmt.Errorf("source size: %w", err)
		}
	}
	if renocore.IsFilterFX(fx.Kind) {
		if env.FilterKinds, err = obs.FilterKinds(); err != nil {
			return env, fmt.Errorf("filter kinds: %w", err)
		}
		if env.Filters, err = obs.SourceFilters(env.Source); err != nil {
			return env, fmt.Errorf("filters on %s: %w", env.Source, err)
		}
	}
	return env, nil
}

// sustainSlack bounds one chunk's exchange. A chunk is short by
// construction, so almost all of this is connect-free round trip on
// loopback -- it is a wedge detector, not a budget.
const sustainSlack = 5 * time.Second

// sustainLifetime caps a driver even if nothing ever stops it. The module
// that starts one also kills it, but a bus killed outright (kickstart -k)
// reaps nothing, and an orphan shaking a camera forever is worse than an
// effect that quietly stops. The module notices a dead driver and starts
// another, so the visible cost of the cap is one skipped frame every
// half hour.
const sustainLifetime = 30 * time.Minute

// SustainFX drives one sustained effect for as long as it is wanted: the
// shake and the lamp, the two level effects that are motion rather than a
// setting. It is a long-lived verb -- one process, one socket, one phase --
// and stopping it is closing stop.
//
// One process rather than a re-fired one-shot is the whole design. A
// one-shot per chunk pays a process spawn and a websocket handshake between
// every batch, which is why the chunk had to be 3 seconds long, which is
// why turning a shake off used to take 3 seconds. Holding the socket makes
// a chunk cheap, so FXSustainChunk is a quarter second and that is now the
// worst an "off" can cost.
//
// Home is owed on every exit path. A chunk ends wherever its phase left it,
// so the pose the camera started in and the filter the lamp switched on
// only ever come back here.
func (v *RenoVerbs) SustainFX(fx renocore.CamFX, dial func() (ObsTransport, error), ctl CtlCall, corr RenoCorr, stop <-chan struct{}) error {
	target := fx.String() + " sustained"
	fail := func(err error) error {
		if ctl != nil {
			if _, nerr := ctl([]string{"fx-note", "error", err.Error()}); nerr != nil {
				err = errors.Join(err, nerr)
			}
		}
		return v.Act(corr, "cam", target, "error", err.Error(), err)
	}
	if !renocore.IsSustainFX(fx.Kind) {
		err := fmt.Errorf("cam %s does not sustain", fx.Kind)
		return v.Act(corr, "cam", target, "error", err.Error(), err)
	}
	if !fx.On() {
		// level 0 is the driver not existing, so there is nothing to run.
		// Refusing beats starting a process that immediately restores home
		// over whatever the real driver is in the middle of doing.
		err := fmt.Errorf("cam %s 0 is the driver stopping, not a driver to start", fx.Kind)
		return v.Act(corr, "cam", target, "error", err.Error(), err)
	}
	obs, err := dial()
	if err != nil {
		return fail(err)
	}
	defer obs.Close()
	// read ONCE, before anything moves: env.From is what home means, and a
	// re-read mid-effect would enshrine the camera's shaken pose as its place
	env, err := fxEnv(obs, fx)
	if err != nil {
		return fail(err)
	}
	setup, err := renocore.SustainSetup(fx, env)
	if err != nil {
		return fail(err)
	}
	if err := obs.RunFrameBatch(fxBatchID(corr)+"-setup", setup.Requests, sustainSlack); err != nil {
		return fail(err)
	}
	if ctl != nil {
		// the tile lights on the setup landing rather than on the first
		// chunk: by then the effect IS on, and a readout that waited for a
		// chunk would blink off every time a driver restarted
		if _, err := ctl([]string{"fx-note", "ok", fx.String()}); err != nil {
			return v.Act(corr, "cam", target, "unconfirmed",
				"driver up but the note failed: "+err.Error(), err)
		}
	}

	phase, chunks := 0.0, 0
	deadline := time.Now().Add(sustainLifetime)
	var runErr error
	for !sustainStopped(stop) && time.Now().Before(deadline) {
		var plan *renocore.FXPlan
		plan, phase, runErr = renocore.SustainChunk(fx, env, phase, renocore.FXSustainChunk)
		if runErr != nil {
			break
		}
		if runErr = obs.RunFrameBatch(fxBatchID(corr), plan.Requests, sustainSlack); runErr != nil {
			break
		}
		chunks++
	}

	// home before the outcome, and regardless of it: a driver that died
	// mid-chunk is exactly when the camera is somewhere nobody put it
	detail := fmt.Sprintf("%s ran %d chunks", fx.String(), chunks)
	if home, herr := renocore.SustainHome(fx, env); herr != nil {
		runErr = errors.Join(runErr, herr)
	} else if herr := obs.RunFrameBatch(fxBatchID(corr)+"-home", home.Requests, sustainSlack); herr != nil {
		runErr = errors.Join(runErr, herr)
	}
	if runErr != nil {
		return fail(fmt.Errorf("%s: %w", detail, runErr))
	}
	fmt.Println(detail)
	return v.Act(corr, "cam", target, "ok", detail, nil)
}

// sustainStopped reads the stop signal without blocking. A nil channel is
// never stopped, which is what a caller with no supervisor wants: the
// lifetime cap is then the only thing that ends the run.
func sustainStopped(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

// fxBatchID correlates the batch with the run in an OBS log; a bare verb
// has no run, so it falls back to the verb name.
func fxBatchID(corr RenoCorr) string {
	if corr.Run == "" {
		return "khudson-cam"
	}
	return fmt.Sprintf("khudson-cam-%s-%d-%d", corr.Run, corr.Step, corr.Act)
}
