# reno devplan -- from here

state: rewritten 2026-08-21 at `d156c57`, after the FIRST live session on glass. companion to reno-design.md (the doctrine); this file is the ordered work list and dies when it empties.

VERIFIED at that commit: `go build ./...`, `go vet ./...`, `go test ./...` (36 packages) and `nix build .#darwinConfigurations.aw-chainguard.system` all exit 0. NEVER `nix flake check` (kraken's darwinConfiguration fails eval, pre-existing).

SWITCHED at 2026-08-21 23:23, confirmed by the deployed binary carrying the dead-capture gate (`strings ~/Library/Application\ Support/khudson/bin/khudson | grep 'no frames from'`). so the scale fix and the arm gate ARE running. anything committed after that timestamp is not; check before trusting this line.

## gate 0 is PARTLY discharged -- what glass actually showed

the recorder ran against real OBS for the first time on 2026-08-21. it found five defects; four are fixed, and the fixes for the two biggest are themselves unverified on glass.

seen working: the pad renders and every tile presses. ARM -> REC -> STOP cycled. END RUN released. the vcam tile ran twice and reported honestly. `cam move tl 500ms smooth` executed ok. a take recorded, stopped, and landed a file in the workspace.

found and FIXED (all unverified on glass -- this is the new gate):

1. **the scene composited to black.** 52 seconds of it, with every readout saying ok. NOT permissions, NOT the `type` setting (OBS defaults it to display), NOT the HDR filter, NOT BetterDisplay, and an OBS restart did not fix it. Both sources were healthy the whole time -- OBS source screenshots were 32KB and 270KB of real frames while the SCENE screenshot was 603 bytes. the scene item transforms had `scale 0`: a scale-inner bounds box derives its scale ONCE, when bounds are set, against whatever size OBS has for the source at that instant, and a freshly created capture answers 0x0. it never recomputes. khudson now sends the scale explicitly, settle-polls for a live source size first, and reads it back. **verify by recording a take and confirming it is not black.**
2. **arm reported ok over dead captures.** the same apply that produced the black scene returned clean. `ApplyRecordConfig` now reports inputs producing no frames; arm FAILS, record REFUSES, apply says NO FRAMES. **verify the good path still arms** -- this gate is new and a false positive locks out recording entirely.
3. **the pad resized under the thumb.** tile width came from the widest label across all tiles, so probing devices (which only adds tiles to the CHOICE bands) widened the shared key and moved every button, including the transport row. key size now comes from the glass. this likely explains a mis-tap that logged `cam move tl` alongside `apply webcam bl` in the same second.
4. **gutter labels sat on the tile's top border row**, reading as floating above the band rather than naming it. now on the band's middle row.
5. **the obs window tile could not pull OBS out of the tray.** khudson's own ini seed sets `SysTrayMinimizeToTray`, and a tray-stowed window is not the app-level AXHidden flag -- an OBS started to tray owns no window, so unhiding reveals nothing and the readback correctly reports it did not take. the show path now pairs the unhide with `FocusApp`. `open -b com.obsproject.obs-studio` was verified by hand to flip the app visible; THE WIRING IS UNVERIFIED, because AX binds to the responsible process and a shell-spawned khudson is not the granted identity. only the bus can exercise it.

still unverified from the original gate 0, untouched by this session:

- **the clip tile.** a take completed, so it should have appeared. nobody looked. tapping it should open the file; `reno open-clip` refuses anything resolving outside the workspace root (unit-tested, never run against a real clip path).
- **the FX vocabulary on glass.** `cam move` returned ok but the frame was black, so no motion has ever been SEEN. re-run every FX now that the scene renders.
- **glyph tofu.** `glyphArm` (U+F140B), `glyphWindow` (U+F05E7), `glyphCamOff` (U+F0568) are inferred codepoints; one character each to fix. the clip tile deliberately reuses `glyphCam` rather than add a fourth guess.
- **pixelate's `scale_filter` settings** (`resolution`, `sampling`) are from the core filter's known schema, unconfirmed on this machine. fails loudly inside the batch if wrong; move/jump/show/hide unaffected.

## settled -- do not relitigate

the transport is the USER'S design, given verbatim 2026-08-21: one button, ARM -> REC -> STOP; "end = disconnect / unarm"; "stop = pause stream but reno is still armed". REC deliberately does NOT appear while unarmed, so recording from cold is two taps on glass -- that is the point.

section labels ride a left gutter, chosen by the user over stacked label rows. the pad is far wider than its buttons need and rows are the scarce axis.

band heights depend only on the tiles in them, never on spare grid, so the pad does not reflow when a header alarm appears. leftover rows collect at the bottom. DO NOT spend them on taller choice tiles -- a band stretched into room it has nothing to put in gets a dead row inside every tile, which reads as a column of buttons whose readouts all failed. that was tried and reverted before it shipped.

pixelate goes ON the pad after all. the old objection was that its state lives in an OBS filter nothing can read back; as a LEVEL that khudson itself commanded, khudson owns the state and can show it honestly.

## queued, in order

1. **level FX as +/- pairs.** the user's framing, verbatim: "you can split the pixelate button in half: plus/minus like a remote". a level, not a toggle. `FXPixelate` already exists in `internal/reno/fx.go` driving the stock `scale_filter`; what is missing is the level, its persistence, and the two tiles. then the same shape for **purple drank** (stock `color_filter_v2` -- hue/saturation, no plugin) and anything else with a magnitude. khudson owns the level, so the tile shows it.
2. **jitter FX.** does not exist. the vocabulary is move/jump/show/hide/pixelate with smooth/bounce easing (`CamFXKinds`). a shake around the current placement, level-driven per item 1.
3. **reno screenshots.** EXPLICITLY DEFERRED by the user 2026-08-21 ("go build everything but screenshot"), registered so it is not lost. a still-capture verb over `GetSourceScreenshot` -- it is what diagnosed the black-frame bug (source shots fine, scene shot 603 bytes, which localised it to the item transforms). worth having as a first-class verb for exactly that diagnostic reason.
4. **what to do with the grid's slack.** about four blank rows at the bottom on a 196x24 glass. options in preference order: leave it, one blank row BETWEEN bands as separation, or give it to the transport. USER'S CALL after they see it.
5. **`probeTile`'s "displays moved" value truncates.** the value is `fitCell`ed against `inner`, and "displays moved" (14 chars) crops at the current key width. this is the pad's loudest alarm and the only state with a long value. pre-existing.
6. **END RUN's representation.** parked BY THE USER until they have a better idea -- do not restyle it unasked. it shares `glyphStop` with the transport's STOP face, so two identically-shaped buttons sit adjacent doing different things. an unanswered proposal: STOP becomes `STOP TAKE`, END RUN gets its own glyph.
7. **the screens band's glyph joins inconsistently.** `display 1 3840x2160` is one cell too wide for `glyph + " " + text`, so that tile renders bare while its neighbour keeps its monitor icon. either shorten the generated label or drop the label rather than the glyph.

## the OBS install moved -- one-time cost outstanding

`homies/shimmerjs/home/khudson/nix/obs-cask.nix` now pins the cask to `/Applications`, overriding the global `caskArgs.appdir`. macOS refuses to activate a system extension whose host app lives anywhere else (`OSSystemExtensionErrorUnsupportedParentBundleLocation`), and `~/Applications` has never counted. from there OBS's virtual camera extension can NEVER install, which is why its tray shows a virtual camera that is on while the socket honestly answers off.

on the next switch this costs: a re-download/move of the cask, a TCC re-grant (screen recording, camera, mic) because the bundle path changes, and a manual approval under System Settings > General > Login Items & Extensions > Camera Extensions. MDM is the wildcard -- a managed profile can disallow user approval of system extensions outright, and this is a managed Mac.

## carried from the design doc

- P4 post: mpv-IPC scrub surface + the moviemaker surfaces (reno-tab touch view and main-display app over one core). the clip tile is the on-ramp -- today it hands the file to the OS, later it opens the scrub surface.
- P3 remainder: the motion vocabulary landed; dynamic paths beyond a single eased move, and filter-value animation, did not.
- couch client: standalone presenter TUI, blocked behind panel-surfaces phase 2.

## decisions the user should ratify (from the FX agent, still open)

1. pixelate is stock-only, no composite-blur path at all.
2. the script producer for FX is a step command, not a header.
3. ~~pixelate is deliberately absent from the pad~~ -- OVERRULED by the user 2026-08-21, see queue item 1.
4. an FX move writes `desired.Webcam`, so the cam band stays honest -- consequence being that a later full apply re-asserts the FX's destination, and an FX is therefore not purely ephemeral.
5. a note-back failure downgrades a verb to `unconfirmed` + nonzero exit, so a successful `cam hide` from a bare shell with no bus running still exits nonzero.

## plugins, if the posture ever changes

nix owns neither OBS nor plugins here and cannot: `obs-studio.meta.platforms` is linux-only, so the brew cask is the only route and every plugin is a hand-dropped bundle. arch must match (OBS is arm64 native, verified) and a mismatch fails SILENTLY -- `lipo -archs` the bundle before debugging anything else.

worth it if the posture changes, in order: **Advanced Masks** (feathered/rounded cam, and its parameters are ordinary filter settings so reno could drive them per preset through the path pixelate already uses), **Source Record** (per-source files -- separate cam and screen tracks turn reframing into an edit instead of a reshoot), **obs-shaderfilter** (one plugin for a whole effects vocabulary). Move is now optional: sampled easing covers the bounce, and Move's remaining edge is cross-scene morphs and filter-value animation. avoid StreamFX.

for title cards and code snippets, no plugin is needed -- OBS's browser source is stock, does alpha natively (its default custom CSS sets a transparent body), and is drivable over obs-websocket via `SetInputSettings`. it is also the only option here that costs nothing in un-nixed drift.

## how the black-frame bug was actually found, because the next one will hide the same way

every readout said ok. the journal said ok. the file existed and had a plausible size. the only thing that disagreed was the pixels.

what worked, in order: `ffmpeg blackdetect` on the clip proved it was black from frame 0, not clipped at the end. OBS's own log (`~/Library/Application Support/obs-studio/logs/`) gave the capture-layer history. `GetSourceScreenshot` per SOURCE versus the SCENE split the problem in half in one step -- sources fine, scene empty -- which pointed at the item transforms rather than at capture, permissions, or the display. `GetSceneItemTransform` then showed `scale 0` against a live `sourceWidth`.

the A/B that proved the fix: delete the screen input over the socket so khudson recreates it cold, apply with the deployed binary (scale 0.0000, 785-byte scene screenshot), apply with the fixed one (scale 1.0000, 44734 bytes).
