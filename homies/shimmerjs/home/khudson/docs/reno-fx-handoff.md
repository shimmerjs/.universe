# reno/obs handoff -- 2026-08-23, second pass

the previous handoff's three open items and the devplan's first three queued
ones all landed. what is left in the reno queue is three USER CALLS and
nothing else.

## state

committed on main, `1a4e43d` and `c2a4106`. `go build ./...`, `go vet ./...`,
`gofmt -l`, `go test -race ./...` (all packages), `nix build
.#darwinConfigurations.aw-chainguard.system`, and the khudson-glyphs and
khudson-posture checks all exit 0. the nix build runs the go suite in-sandbox
(`doCheck = true`), so the tests are green twice over.

`nix flake check` still does NOT pass and still did not before any of this:
`nixosConfigurations.snake` fails to eval on `attribute 'buildDTBs' missing`
from nixpkgs' `hardware/device-tree.nix`. pinned-nixpkgs problem, not a config
one.

**still nothing here has been run against live OBS.** the previous session's
five glass items are all still owed, and this session added its own. every
claim below is unit tests and the build.

## what landed

**the crash wedge** (`1a4e43d`). OBS drops a `.sentinel/run_<uuid>` marker per
live instance and deletes it on a clean exit, so a marker with no OBS running
is a dead instance's -- and the next launch answers it with the safe-mode
dialog, which binds no websocket. the launch path clears those markers, which
DECLINES safe mode on khudson's behalf and says so on stderr. only the launch
path, and only after `running()` said no instance is up, so a hand-started OBS
still gets the offer.

the other half is the OBS already sitting on the dialog, where khudson is not
the one launching. `safeModeWaiting` reads the head of the newest OBS log for
"Crash or unclean shutdown detected" with no later `[Safe Mode]` line, and the
running-but-dead-socket refusal names the dialog instead of talking about a
config that binds at launch. both marker strings are from this machine's own
logs, not from docs.

**the canvas goes back** (`c2a4106`). the first resize stashes what OBS had in
`<state>/reno/canvas.json` -- only the first, since every later apply is reno
resizing its own canvas -- and `restoreCanvas` puts it back from
`releaseHardware`, so release and quit both do it. the stash is forgotten
unconditionally, restore or no: a stash kept after a failure is an old
session's frame waiting to land on top of a later one.

**the levels re-assert on arm** (`c2a4106`). khudson owns the magnitude, OBS
owns the filter or the pose it produced, and nothing kept them married -- a
scene collection swapped under a running bus leaves every tile lying. `arm-note
ok` now emits the one-shots for the filter levels and reconciles the drivers.
a sustained kind gets no one-shot: its driver owns the camera while it runs,
and `touchEverySustainLocked` starts one only where the level is up and nothing
is driving it.

**the camera going away parks them** (`c2a4106`). a stopped driver put its own
pose back, but a filter is state: hiding the webcam left the lamp's tint
sitting on a source nobody was driving. `parkFXLocked` switches those off and
zeroes the levels -- the tiles read off, because there is no tint on a camera
that is not there -- and `unparkFXLocked` brings them back when a camera is
staged again. the ordering is what makes it work: filters off BEFORE the item
goes (with the webcam unset the apply removes the input entirely), and back on
AFTER the placement or apply that stages it, which is why the unset path
unparks off `apply-note ok` rather than off the tap. a level tapped while
parked outranks the parked one.

fxParked is deliberately not persisted: a bus that restarted while the camera
was off comes back with the effects off, which is what the pad says.

**a scripted sustained effect no longer starts twice** (`c2a4106`). `cam jitter
8` as a script step used to run its own 2s span and then get adopted and
driven, with a driver spawn's stillness in the seam. run correlation is the
signal -- only the bus's own executor issues one -- so a correlated sustained
one-shot notes back and returns, and the driver does all of it. a hand-run one
still runs its visible span. `adoptFXLevelLocked` now touches the supervisor
even when the level did not move, because a step at the level already showing
would otherwise do nothing at all.

**bus tests stopped being able to write the real state root** (`c2a4106`).
`TestMain` redirects `renoStateDir` package-wide. `paths.Resolve` reads `$HOME`
not `XDG_STATE_HOME`, and the canvas stash found the trap the same way the
lemmings page did -- it wrote `canvas.json` into
`~/Library/Application Support/khudson/reno/` on the first test run.

## what to check on glass

the previous session's five are unchanged and still first:

1. the lemmings page renders at all (obs-browser settings keys are from the
   documented schema, not probed).
2. the driver's stdin stop, with a batch in flight.
3. 250ms chunks against real OBS.
4. the d-pad's hit regions under a thumb.
5. `reno obs quit`'s AppleEvent, which needs the one-time Automation grant.

new, from this session:

6. **the sentinel clear against a real crashed OBS.** kill OBS with `kill -9`,
   then `reno obs ensure`, and watch it come up without a dialog. the stderr
   line naming the decline is the confirmation.
7. **the canvas restore.** apply a multi-screen config (which resizes), then
   END RUN, then look at OBS's video settings. `canvas.json` should be gone.
8. **arm re-asserting into a swapped scene collection.** swap the collection
   under a running bus, tap ARM, and the pixelate/purple levels the tiles show
   should be back on the camera.
9. **the park.** with purple up, tap hide; the filter should go off, not just
   out of view. tap a placement and it should come back at the same level.

## still open

- **the lamp's filter and a camera that never comes back.** parking handles the
  webcam going to off or unset. a camera that dies at the DEVICE level, with
  the config still naming it, still leaves a filter on a dead source. nothing
  reconciles that, and it is not clear anything should.
- **a level tapped while the camera is away** fires a one-shot at a camera that
  is not there, which notes an error. the level still lands, so the tile is
  honest and the unpark picks it up, but the pad shows a failure for something
  that was not wrong.

## traps (still true)

- **`git add` before any nix build.** the flake only sees tracked files.
- **the deployed daemon re-applies over you.** until a switch lands, `khudson
  reno config ...` makes the running old-binary daemon run its own apply over
  yours. apply twice, or switch first.
- **obs readback fields lie.** `sceneItemTransform.scaleX` reads stale after a
  bounds change; trust a screenshot over the transform readback.
- **tests must not resolve the real state root.** now enforced package-wide in
  `internal/bus` by `TestMain`; the module tests get it from `testMod`'s
  stateDir. anything new that resolves paths itself is back in the trap.
- **another session may be in this repo.** this one ran alongside a second
  agent working `internal/hudlaunch` and `internal/cgdisplay`, which committed
  broadly twice and swept part of this work into `0faf2e5` -- the park tests in
  `adhoc_test.go` are in a commit whose message is about display selectors.
  path-scope every `git add`.

---

# third pass -- 2026-08-24, from a live session on the rig

this pass was driven by the rig failing in front of the user, not by the
queue. everything below was root-caused against the running machine, which
makes it the first part of this arc with evidence behind it rather than unit
tests. `b83f87a` is the tip; `nix build .#darwinConfigurations.aw-chainguard.system`
exits 0 (measured directly, not through a pipe).

## what was actually broken

**the HUD did not come back when the Edge was plugged in.** the launcher sat
in "display not connected; waiting" with the display attached and AppKit
reporting it. root cause: `CGDisplayRegisterReconfigurationCallback`
registers fine from a locked thread on its own run loop and then NEVER
FIRES. measured with a probe holding the registration while
`cgdisplay.Active()` polled beside it -- a real unplug and replug showed up
in the poll (2 -> 1 -> 2) and produced zero callbacks. `internal/cgdisplay/watch.{go,c}`
is deleted; the watcher is a 2s in-process enumeration (`d2565ef`). the
waiting branch has its timer back: blocking on an event with no backstop is
what turned one missed signal into a dead HUD.

**arm never reached REC.** the apply failed on `NO FRAMES from
reno-screen-0`, and arm refuses a rig that would record black. two causes
stacked. the config held the Edge TWICE -- `"XENEON"` from the CLI and its
UUID from the pad -- so the plan built two full-size SCK captures of one
panel and one of them never produced a frame (`0faf2e5`). and the pad's
"attached" check was exact-value equality while the plan resolves by name,
index or substring, so a good `--screen XENEON` rendered as a warn tile
beside the display it selects.

**no camera.** three layers, peeled in this order:

1. OBS's camera TCC was DENIED (`AUTHREQ_RESULT ... kTCCServiceCamera ...
   authValue=0`). the user granted it; a running OBS keeps the cached
   decision, so it needs a relaunch to take.
2. with it granted, still nothing -- because khudson had SELECTED THE WRONG
   CAMERA. `BuildScenePlan` set the device to `env.Cams[0]`, the first
   camera OBS enumerated, on every apply, with no knob anywhere. the comment
   said "a query knob can come later". with the USB webcam gone that
   promoted the built-in FaceTime camera on a CLAMSHELL laptop
   (`AppleClamshellState = Yes`) -- a camera aimed at a closed lid.
   `4abe9dc` adds `RecordConfig.Camera`, a device band on the pad beside the
   mics, and `--camera`; a placement now writes no device at all.
3. the Kiyo was plugged in the whole time and macOS was not enumerating it.
   it hangs off the Edge's built-in CORSAIR hub, and the display replug this
   session asked for took the hub down without bringing the camera back.
   replugging the camera fixed it. **if you ask for a display replug on this
   rig, you are also replugging every USB device on that hub.**

## what else landed

**a single screen gets a cam band** (`b83f87a`, user request). the canvas was
sized to exactly the screens, so an Edge-only capture left the webcam
nowhere but on top of the strip. a single-screen canvas now grows by
`CamBand(screenH)` below the picture, solved so a plain corner clears the
picture by the same `CamPad` it keeps from every other edge: 720 -> a 240
band, a 2560x960 canvas, cam at y=744. `StackBoxes` takes the reserved band
so the picture sits at the top rather than centering through it. `+` and
`big` still overlay by design. stacked screens get no band -- they already
have gutters to sit in.

## corrections to the earlier passes

- the second pass's "`0faf2e5` swept part of this work" note is right, and it
  is worse than stated: three park tests AND the display-selector work share
  that commit. `c2a4106` was amended to describe its real contents.
- "still nothing has been run against live OBS" is no longer true for the
  scene/arm/camera path -- it was run against live OBS repeatedly tonight.
  the FX work (drivers, lava, lemmings, d-pad) is still unverified on glass.

## still owed on glass

the five from the first pass stand, minus the one now answered:

- `reno obs quit` reaches its AppleEvent and fails there: khudson has no
  Automation grant toward OBS. the same osascript from a terminal quits it
  cleanly, so it is a per-binary TCC identity, and the verb's error names it.
  **that grant is still owed.**
- lemmings rendering, the driver's stdin stop under a live batch, 250ms
  chunks, and the d-pad under a thumb are all still untested on glass.
- the cam band has never been seen; the geometry is unit-tested only.

## trap worth adding

**do not ask for a display replug to test something.** on this rig the
displays carry the USB hub, and the hub carries the camera. cost one
confused hour tonight.
