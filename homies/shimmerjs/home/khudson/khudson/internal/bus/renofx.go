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
package bus

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
	// pixelate scales down and back up, and the size it goes back to has to
	// be the one it was handed or the item's scale stops matching its source
	if fx.Kind == renocore.FXPixelate {
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

// fxBatchID correlates the batch with the run in an OBS log; a bare verb
// has no run, so it falls back to the verb name.
func fxBatchID(corr RenoCorr) string {
	if corr.Run == "" {
		return "khudson-cam"
	}
	return fmt.Sprintf("khudson-cam-%s-%d-%d", corr.Run, corr.Step, corr.Act)
}
