# reno/obs handoff -- 2026-08-23

open items from the session that landed the stacked-screen canvas, the sustained jitter and the stepper. everything below is unstarted unless it says otherwise. the investigation into the input-hang lead is running separately; nothing here depends on it.

## state you are starting from

all staged, not committed. full go suite + `go vet` + `nix build .#darwinConfigurations.aw-chainguard.system` green.

landed and verified against live obs:

- **stacked screens.** `StackBoxes` in `internal/reno/scene.go` lays screens as aspect-proportional rows sharing one width, replacing the old equal-column split. gutter is `StackGutterFrac` (1/64 of canvas width).
- **derived canvas.** `StackCanvas` picks the canvas from the sources: width = narrowest source so nothing upscales, height = rows + gutters. dell+edge -> 2560x2200, edge alone -> 2560x720 with the strip at scale 1.000. applied by `fitCanvas` in `internal/bus/renoscene.go`, which reads the canvas back before writing any transform -- obs rescales scene items asynchronously on a base-resolution change and transforms written into that gap get multiplied by the ratio of the two canvases.
- **webcam corner sizes.** `br+ tr+ bl+ tl+` are the quarter-canvas-area variants. `CamTransform` now takes `srcW, srcH` and shapes the box to the camera's aspect so a corner preset hugs its corner on a non-16:9 canvas.
- **sustained jitter.** level > 0 runs continuously in `FXJitterChunk` (3s) batches re-fired by `watchJitter` in `internal/module/reno/jitterwatch.go`. chunks are whole cycles so each ends on the pose it started from. amplitude fits the box's headroom symmetrically.
- **level scale 5 -> 20 steps.** `FXMaxLevel`. pixelate's curve is geometric (256 -> 16) since the old `256 >> (level-1)` hit zero.
- **stepper.** `module.RowStepper` + `module.End`, rendered by `stepperTile` in `internal/dock/pad.go`. one label, one reading, two independently-hittable ends. `Ends[].Argv` is harvested into the bus act allowlist in `internal/bus/scheduler.go` -- anything new carrying acts outside `Act`/`Menu` must be added there or the button lights and does nothing.

## 1. d-pad for slide directions

the `FX` band publishes one `slide <corner>` tile per direction. wanted: a single element with the four corners arranged spatially.

the stepper is the precedent -- copy its shape rather than inventing a second mechanism. `module.End` already carries `Label`/`Argv`/`Dim`/`Key` and is a slice, so a `RowDpad` kind with four ends needs no new wire type. the work is:

- `renderPad` in `internal/dock/pad.go` dispatches on `t.Kind`; add the case beside `RowStepper`.
- hit regions: `registerStepperHits` splits the interior into two spans. a d-pad splits into quadrants -- same pattern, four rects.
- `MinHeight` matters. the stepper needed 4 (two interior lines) or the label cropped to `purp`. a d-pad wants at least that.
- `internal/dock/reno.go` collects tiles by kind; add `RowDpad` to the filter next to `RowStepper`.
- `internal/dock/rows.go` needs a degraded case for the classic panel (no grid, no aiming).
- `padTile` in `internal/module/reno/record_test.go` is the test helper that finds a published element by key; it filters on kind.

note the corner presets are now eight (`br`,`br+`, ...) plus `big`. decide whether the d-pad drives the plain corners with size on a separate control, or grows a `+` end. the plain four plus a size stepper beside it is the smaller change and matches the remote idiom already on the pad.

## 2. lavalamp

a fourth level effect, "behaves similarly" to the others -- so `LevelFXKinds`, a 0..20 magnitude, off at 0.

two shapes are available and they are not equivalent:

- **filter-driven**, like pixelate and purple. add to `IsFilterFX`, give it `levelStages`. the constraint is hard: only stock filters exist, `nix owns no obs plugin on this machine`, and a level effect must fail loudly when its stock kind is absent. the confirmed-good kinds are `scale_filter` and `color_filter_v2`. a slow colour cycle through `color_filter_v2`'s `hue_shift` is the obvious candidate, but a *cycle* is motion and a filter setting is static -- which pushes it to:
- **driver-driven**, like jitter. re-fire batches that walk `hue_shift` around the wheel. `jitterwatch.go` is the whole pattern: `touchJitterLocked` / `jitterCheckLocked` / `watchJitter` / `fireJitterChunk`, ~100 lines, and the generalisation to "any sustained level fx" is mostly renaming. do that generalisation rather than copy-pasting a second watcher.

if you go driver-driven, the chunk arithmetic in `FXJitterChunk`'s comment is the thing to re-read: whole cycles per chunk is what stops successive chunks from drifting, and chunk length trades stutter-between-chunks against how far behind a tap on "off" lands.

## 3. lemmings

`browser_source` IS available in this obs (verified 2026-08-23 via `GetInputKindList`), so an html canvas composited over the cam is a real path and not a guess. sketch:

- a khudson-owned `reno-lemmings` browser source, sized and positioned over the `reno-cam` item's box.
- the html is a build artifact nix owns, referenced by local file url. it must not reach the network.
- `Plan.Stack` is bottom-first and already orders screens then cam; lemmings goes above the cam. the stack readback exists because getting this wrong is silent -- the source renders under a full-canvas screen capture and looks exactly like a source that never loaded.
- treat it as a level fx if you want density on the pad, or a plain toggle if not.

the honest risk: obs-browser is bundled with obs but the cask is unpinned and nix owns none of it, which is the same exposure the design already accepts for obs itself.

## 4. `reno obs quit`

there is no single verb to fully put obs down. today it is:

```
khudson reno stop          # end the run
khudson reno obs stop      # stop the recording
khudson reno obs release   # drop the hardware
osascript -e 'quit app "OBS"'
```

`Release` (`internal/bus/renoarm.go:265`) stops the virtual cam first because that holds the camera light, then disables every `IsRenoInput` item. it does not quit obs. a `quit` op running all four in order is small and removes a four-step ritual from the end of every session.

use a graceful quit, not `pkill -9`. obs's log opens with `Crash or unclean shutdown detected` after a hard kill, and the sentinel wedges the websocket on next launch.

## open questions for you, not blockers

- **jitter off-latency.** turning a jitter off can lag up to 3s while the in-flight chunk finishes. shorter chunks make "off" snappier and add a visible still moment between chunks, because the driver cannot dial the next batch until the last returns. currently biased toward smoothness.
- **canvas is not restored.** `fitCanvas` writes obs's base+output resolution and never puts it back. that follows from reno owning this obs, but it means switching to a non-reno scene leaves a canvas shaped for reno's. restoring on `release` is the obvious place if you want it.

## traps

- **`git add` before any nix build.** the flake only sees tracked files. an untracked new file builds fine under `nix build --impure --expr` with a plain path (which copies untracked files) and then fails the real system build with an undefined symbol. cost an hour once already.
- **the deployed daemon re-applies over you.** until a switch lands, `khudson reno config ...` makes the running (old-binary) daemon run its own apply, overwriting whatever you just applied with the new binary. it produced a bogus canvas reading and a duplicate `reno-screen-1 2` input during testing. apply twice, or switch first.
- **obs readback fields lie.** `sceneItemTransform.scaleX` reads stale after a bounds change; the item renders correctly anyway. trust a screenshot (`GetSourceScreenshot`) over the transform readback when they disagree.
