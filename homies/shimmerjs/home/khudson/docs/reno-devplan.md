# reno devplan -- from here

state: rewritten 2026-08-21 after the queue's first four items landed. companion to reno-design.md (the doctrine); this file is the ordered work list and dies when it empties.

VERIFIED at that point: `go build ./...`, `go vet ./...` and `go test ./...` (36 packages) clean, and `nix build .#darwinConfigurations.aw-chainguard.system` exit 0. NEVER `nix flake check` (kraken's darwinConfiguration fails eval, pre-existing).

UNVERIFIED: all of it on glass, and all of it against real OBS. see gate 0. do not repeat a "works" claim from this file or from git history without running it.

where the new code lives: pad geometry (gutter labels, band budgeting) `internal/dock/pad.go` (`budgetBands`, `padBand.ask`, `padTile`); transport weight + tiles `internal/module/reno/view.go` (`transportTileH`, `transportTile`, `vcamTile`, `appendClip`, `probeTile`, `obsWindowTile`); latches and acts `internal/module/reno/adhoc.go` (`vcamEffectsLocked`, `noteVCamLocked`); verbs `internal/bus/renoarm.go` (`ArmRig`, `Release`, `ObsWindow`, `VirtualCam`, `OpenClip`); workspace root `internal/reno/run.go` (`WorkspaceRoot`); FX core `internal/reno/fx.go`, plan executor `internal/bus/renofx.go`, frame-locked batch `internal/bus/obsbatch.go`; ini seed + drift `internal/bus/obsini.go`.

## gate 0 -- eyes on glass (blocks everything)

nothing in the last five commits has run against real OBS or been seen on the Edge. that is the single biggest risk carried forward, and it grew with every commit.

- switch + HUD restart, then look at the recorder pad. expect: section labels in a LEFT GUTTER (not their own rows), a tall transport band whose tiles carry their glyph on a line of its own, and four tight bands under it -- screens, mic, cam, fx. about four blank rows at the bottom of the grid are deliberate; see item 1.
- the transport band should hold ARM, devices, obs window, VCAM, and END RUN once a run is live. a clip tile joins them after a take stops.
- first live FX: `khudson reno obs cam move big 600ms bounce` with a preset already applied. the frame-locked batch has only ever run against a protocol stub.
- first live arm/release: `khudson reno obs arm`, then END RUN, then confirm the camera light is actually off. the release path is the one that reaches for the virtual camera and it has never run.
- first live vcam: `khudson reno obs vcam status` with OBS up. arm and release both note this readout now, so the VCAM tile should be honest without a deliberate tap.
- first live clip: stop a take, tap the clip tile, confirm it opens. `reno open-clip` refuses anything resolving outside the workspace root, and that check is unit-tested but has never run against a real clip path.
- `scale_filter` pixelate settings (`resolution`, `sampling`) are from the core filter's known schema, unconfirmed on this machine. if wrong it fails loudly inside the batch; move/jump/show/hide are unaffected.
- `glyphArm` (U+F140B), `glyphWindow` (U+F05E7) and `glyphCamOff` (U+F0568) are inferred codepoints. tofu is a one-character fix each. the clip tile deliberately reuses `glyphCam` rather than add a fourth guess.

## settled -- do not relitigate

the transport is the USER'S design, given verbatim 2026-08-21: one button, ARM -> REC -> STOP; "end = disconnect / unarm"; "stop = pause stream but reno is still armed". REC deliberately does NOT appear while unarmed, so recording from cold is two taps on glass -- that is the point, not an oversight. the CLI `reno record` still does both phases so scripts are unaffected.

section labels ride a left gutter, chosen by the user 2026-08-21 over stacked label rows. the pad is far wider than its buttons need and its rows are the scarce axis; five label rows out of twenty-one was what starved the transport and dropped the last band.

## queued, in order

1. **what to do with the grid's slack.** band heights now depend only on the tiles in them (`padBand.ask`), never on how much grid is going spare -- so the pad does not reflow under a thumb when a header alarm appears. the cost is about four blank rows at the bottom on a 196x24 glass. options, in preference order: leave it (space reads as space), one blank row BETWEEN bands as separation, or give it to the transport. USER'S CALL after they see it. do not spend it on taller choice tiles: a band stretched into room it has nothing to put in gets a dead row inside every tile, which reads as a column of buttons whose readouts all failed. that was tried and reverted before it shipped.

2. **`probeTile`'s "displays moved" value truncates.** tiles are ~20 interior cells at the current widths but the value is `fitCell`ed against `inner`, and at a narrower key size "displays moved" (14) crops. this is the pad's loudest alarm and it is the one most likely to be cropped, because the warn state is the only one with a long value. pre-existing, not from the band work.

3. **END RUN's representation.** parked BY THE USER until they have a better idea -- do not restyle it unasked. today it shares `glyphStop` with the transport's STOP face, so two identically-shaped buttons sit adjacent doing different things. a proposal they have not answered: STOP becomes `STOP TAKE`, and END RUN gets its own glyph.

4. **the screens band's glyph joins inconsistently.** `display 1 3840x2160` is one cell too wide for `glyph + " " + text`, so that tile renders bare while its neighbour keeps its monitor icon. the band reads ragged. either shorten the generated label or let a too-wide tile drop the label rather than the glyph.

## carried from the design doc

- P4 post: mpv-IPC scrub surface + the moviemaker surfaces (reno-tab touch view and main-display app over one core). the clip tile is the on-ramp -- today it hands the file to the OS, later it opens the scrub surface instead.
- P3 remainder: the motion vocabulary landed; dynamic paths beyond a single eased move, and filter-value animation, did not.
- couch client: standalone presenter TUI, blocked behind panel-surfaces phase 2.

## decisions the user should ratify (from the FX agent, still open)

1. pixelate is stock-only, no composite-blur path at all.
2. the script producer for FX is a step command, not a header.
3. pixelate is deliberately absent from the pad: its state lives in an OBS filter nothing can read back, and this pad refuses buttons that cannot show their own state.
4. an FX move writes `desired.Webcam`, so the cam band stays honest -- consequence being that a later full apply re-asserts the FX's destination, and an FX is therefore not purely ephemeral.
5. a note-back failure downgrades a verb to `unconfirmed` + nonzero exit, so a successful `cam hide` from a bare shell with no bus running still exits nonzero.

## plugins, if the posture ever changes

nix owns neither OBS nor plugins here and cannot: `obs-studio.meta.platforms` is linux-only, so the brew cask is the only route and every plugin is a hand-dropped bundle. arch must match (OBS is arm64 native, verified) and a mismatch fails SILENTLY -- `lipo -archs` the bundle before debugging anything else.

worth it if the posture changes, in order: **Advanced Masks** (feathered/rounded cam, and its parameters are ordinary filter settings so reno could drive them per preset through the path pixelate already uses), **Source Record** (per-source files -- separate cam and screen tracks turn reframing into an edit instead of a reshoot), **obs-shaderfilter** (one plugin for a whole effects vocabulary). Move is now optional: sampled easing covers the bounce, and Move's remaining edge is cross-scene morphs and filter-value animation. avoid StreamFX.

for title cards and code snippets, no plugin is needed -- OBS's browser source is stock, does alpha natively (its default custom CSS sets a transparent body), and is drivable over obs-websocket via `SetInputSettings`. it is also the only option here that costs nothing in un-nixed drift.
