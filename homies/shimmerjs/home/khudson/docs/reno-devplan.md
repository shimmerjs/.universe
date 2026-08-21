# reno devplan -- from here

state: written 2026-08-21, after `bdcb1d0`. companion to reno-design.md (the doctrine); this file is the ordered work list and dies when it empties. everything below is UNVERIFIED ON GLASS -- see gate 0.

## gate 0 -- eyes on glass (blocks everything)

nothing in the last four commits has run against real OBS or been seen on the Edge. that is the single biggest risk carried forward, and it grew with every commit.

- switch + HUD restart, then look at the recorder pad. expect: one transport button reading ARM, a `devices` tile with an age, an `obs window` tile, and the fx band.
- the readouts are the specific thing to check. they were invisible before `a718c83` and the fold that fixed them is arithmetic, not eyes -- if a tile still shows only its label, the fold is not landing at the real tile height.
- first live FX: `khudson reno obs cam move big 600ms bounce` with a preset already applied. the frame-locked batch has only ever run against a protocol stub.
- first live arm/release: `khudson reno obs arm`, then END RUN, then confirm the camera light is actually off. the release path is the one that reaches for the virtual camera and it has never run.
- `scale_filter` pixelate settings (`resolution`, `sampling`) are from the core filter's known schema, unconfirmed on this machine. if wrong it fails loudly inside the batch; move/jump/show/hide are unaffected.
- `glyphArm` (U+F140B), `glyphWindow` (U+F05E7) and `glyphCamOff` (U+F0568) are inferred codepoints. tofu is a one-character fix each.

## queued, in order

1. **clip button.** after a take the head row flashes the clip name and nothing can be done with it. the header is unpressable by contract ("anything you can press is a tile"), so this is a tile that appears only when a stopped recording has an output, tapping to open the clip. needs a reno verb -- the effect executor only admits `khudson reno <verb>` argvs -- so `reno open-clip <path>`, path-checked against the workspace root. later this becomes the entry to the P4 scrub surface instead of a plain open.

2. **virtual camera readout.** `Release` stops it, but nothing SHOWS it. it reads back over the socket (`GetVirtualCamStatus`), so by this pad's own rule it earns a tile. the case that matters: an output you never started, holding your camera, with the pad reporting idle.

3. **band budgeting + the round REC button.** the fold in `a718c83` is a workaround; the underlying problem is that five bands split `gridH` evenly and nothing can be taller than the choice tiles. a real record button -- a red blob with REC under it, the ask -- needs four interior rows against the one it has. transport should get weight, choice bands the remainder. this also unblocks anything else that wants vertical room.

4. **END RUN's representation.** parked by the user until they have a better idea. today it shares `glyphStop` with the transport's STOP face, so two identically-shaped buttons sit adjacent doing different things. at minimum they should not look the same.

5. **`invStale` is now dead.** the constant survives with no consumer after the age warning came out. delete it or give it a job.

## carried from the design doc

- P4 post: mpv-IPC scrub surface + the moviemaker surfaces (reno-tab touch view and main-display app over one core). item 1 above is the on-ramp.
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
