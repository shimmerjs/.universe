# reno devplan -- from here

state: rewritten 2026-08-22 at `7bd845b`, after the SECOND live session on glass. companion to reno-design.md (the doctrine); this file is the ordered work list and dies when it empties.

VERIFIED at that commit: `go build ./...`, `go vet ./...`, `go test ./...` (36 packages), `nix build .#darwinConfigurations.aw-chainguard.system`, and `nix build .#checks.aarch64-darwin.khudson-glyphs-aw-chainguard` all exit 0. NEVER `nix flake check` (kraken's darwinConfiguration fails eval, pre-existing).

SWITCHED at 2026-08-21 23:23, which is BEFORE every commit in this file. the running bus therefore has none of it. the way to check what is actually deployed is `strings ~/Library/Application\ Support/khudson/bin/khudson | grep <a string only the new code has>`, or compare `cat "~/Library/Application Support/khudson/bin/.khudson.store-path"` against the khudson path in a fresh `nix build` of the system.

everything below was verified by running the freshly BUILT binary out of its store path against live OBS. that works for the whole socket surface and only for it: AX binds to the responsible process, so a shell-spawned khudson is not the granted identity and the window path cannot be exercised that way.

## gate 0 is DISCHARGED except the window tile

what got seen on glass this session, with the method:

- **the scene composites.** a recorded take with `ffmpeg blackdetect` reporting no black interval at all, 3.3 Mbps of real content. the explicit-scale fix holds.
- **the good path still arms.** the no-frames gate does not false-positive on healthy captures; arm, record, stop all clean.
- **the webcam is IN the frame.** it was not, and that was a new bug -- see below.
- **the whole motion vocabulary.** `cam move bl 1500ms bounce`, polled at 12ms, shows 46 distinct intermediate poses travelling 3048,24 -> 24,1704 and landing exactly on the preset. jump lands. show/hide flip the item's enabled flag and read back.
- **both level effects, in a recorded take.** pixelate at 5 is hard 16x9 blocks; purple at 3 is the violet wash. level 0 disables every stage.
- **jitter.** polled: 13 distinct poses swinging x 0..79 and y 1670..1728 around 24,1704, landing back on 24,1704 exactly.
- **the clip verb.** `reno open-clip` on a real take opens it; on `/etc/hosts` it refuses by name. the TILE has still never been tapped, but the verb behind it is no longer a guess.
- **no glyph tofu anywhere.** all 21 nerd-font pins resolve in the installed font. two were the WRONG icon under a correct-reading comment -- `glyphWindow` was md-cursor_text, `glyphArm` a lightning bolt -- both fixed, and `khudson-glyphs-<host>` now fails the build on any codepoint whose font glyph name does not match its comment.
- **scale_filter's schema.** `GetSourceFilterDefaultSettings` confirms resolution/sampling/undistort on this machine, and color_filter_v2's eight settings with it.

still not verified, and it is the one thing a shell cannot reach:

- **the obs window tile.** AX binds to the responsible process, so only the bus can exercise it. its glyph was a text cursor until this session, which is worth knowing when reading the old "it did not take" reports -- the readback was honest, but the button never looked like a window either.

## found and fixed this session

1. **the webcam rendered behind the screen capture.** nothing ever set a scene-item index, so the stack was whatever creation order left behind, and a screen capture recreated last covers the whole canvas. the cam source was healthy, its transform correct, arm said ok, and the PiP was simply not in the pixels. the plan now names the visual items bottom-first and the apply assigns ascending indices, then reads the order back. VERIFIED: a recorded take with the cam in the corner.
2. **pixelate was a visual no-op.** a lone `scale_filter` downscale reports the small size -- so the item's transform math saw 16x9 -- but obs-filters renders it with `OBS_ALLOW_DIRECT_RENDERING`, so the parent goes straight to the destination rect at full resolution and the frame comes out sharp. it changed the reported source size and nothing else. it is a two-stage chain now, down then point-sampled back up, and the upscale is what forces the intermediate texture.
3. **and the upscale's size mattered more than it looked.** sizing it to the item's box makes the bounds scale come out at a tidy 1 while the effect is on, then breaks the moment the level drops to 0: the filters switch off, the source reverts to native, and a bounds box that derives its scale ONCE keeps the one it computed for the other size. the cam rendered 1920x1080 inside a 768x432 PiP, off the canvas, with the effect reading off. it scales back to the source's own dimensions now, and a level walk 4 -> 2 -> 0 holds scale 0.4 throughout.
4. **a pad tile could lose its readout or its icon.** the glyph took its own interior line before the value was accounted for, so the RE-PROBE alarm drew the icon and dropped "displays moved" entirely -- old queue item 5, and it was omission rather than the truncation it was filed as. and the glyph joined a label only when the whole label fit, so a screens label one cell too wide rendered bare beside neighbours that kept their monitor icon (old queue item 7). the value outranks the glyph's line now, and a long label crops rather than dropping the icon.

## settled -- do not relitigate

the transport is the USER'S design, given verbatim 2026-08-21: one button, ARM -> REC -> STOP; "end = disconnect / unarm"; "stop = pause stream but reno is still armed". REC deliberately does NOT appear while unarmed, so recording from cold is two taps on glass -- that is the point.

section labels ride a left gutter, chosen by the user over stacked label rows. the pad is far wider than its buttons need and rows are the scarce axis.

band heights depend only on the tiles in them, never on spare grid, so the pad does not reflow when a header alarm appears. leftover rows collect at the bottom. DO NOT spend them on taller choice tiles -- a band stretched into room it has nothing to put in gets a dead row inside every tile, which reads as a column of buttons whose readouts all failed. that was tried and reverted before it shipped.

a level effect is one integer khudson holds, walked by a plus/minus pair. it earns a tile because nothing downstream reports the magnitude back: a filter tells you its settings but not which step of the scale produced them, and a shake leaves no trace at all. the pad's old objection to pixelate is answered by ownership, not by readback.

purple drank is a colour MULTIPLY, not a hue rotation. a rotation moves whatever was in the frame; a tint is the same wash on any source, which is what a level promises. OBS packs a colour setting as 0xAABBGGRR -- the LOW byte is RED, verified against live OBS 32.2 by tinting a webcam with 0x0000FF and getting red.

the CLI seam and the tiles run one core, so a hand-run `khudson reno obs cam purple 4` is adopted from its own note and lights the pad.

## queued, in order

1. **the OBS crash-recovery wedge.** a crashed OBS comes back sitting on the safe-mode dialog, so obs-websocket never binds, and `reno obs ensure` burns its full two minutes and then blames "a first-launch Gatekeeper or permission dialog may be waiting". the actual unwedge is deleting `~/Library/Application Support/obs-studio/.sentinel/run_*` before launching -- OBS leaves the sentinel behind on an unclean exit and prompts on the next start. hit live this session: OBS died once, unreproducibly, five minutes after a take, and the same filter requests that were in flight then ran clean twice afterwards. at minimum ensure should name the crash dialog it is actually blocked on. clearing the sentinel on the launch path is the part worth arguing about, because it silently declines OBS's own safe-mode offer.
2. **the levels are never re-asserted.** khudson remembers each level across a bus restart and OBS holds the filters, but nothing reconciles the two. recovery deliberately does not push (a bus start must not reach out and change a camera nobody asked it to touch), which leaves arm as the honest place -- the same place the config converges. today a scene collection swapped under a running bus leaves every level tile lying.
3. **CLI help drift.** `main.go` documents `reno obs standby`; the op is `arm`. the top-level `reno: need a verb` list omits `open-clip`, which the pad's clip tile fires. both are one-liners.
4. **reno screenshots.** EXPLICITLY DEFERRED by the user 2026-08-21 ("go build everything but screenshot"), registered so it is not lost. a still-capture verb over `GetSourceScreenshot`. it diagnosed the black frame, it diagnosed the missing webcam, and it is how the pixelate no-op was caught -- three for three on the bugs that look fine from every readout.
5. **what to do with the grid's slack.** about four blank rows at the bottom on a 196x24 glass. options in preference order: leave it, one blank row BETWEEN bands as separation, or give it to the transport. USER'S CALL after they see it. the FX band grew six tiles this session (three level pairs), so look again before deciding.
6. **END RUN's representation.** parked BY THE USER until they have a better idea -- do not restyle it unasked. it shares `glyphStop` with the transport's STOP face, so two identically-shaped buttons sit adjacent doing different things. an unanswered proposal: STOP becomes `STOP TAKE`, END RUN gets its own glyph.

## the OBS install is STILL in ~/Applications

`homies/shimmerjs/home/khudson/nix/obs-cask.nix` pins the cask to `/Applications`, and the running OBS is still `~/Applications/OBS.app`. brew's `appdir` applies at INSTALL time, so a cask already installed never moves: the 2026-08-21 switch changed nothing and `/Applications/OBS.app` does not exist. it takes a `brew reinstall obs`, which nix will not do for you.

until then OBS's virtual camera extension can never install -- macOS refuses to activate a system extension whose host app lives anywhere else (`OSSystemExtensionErrorUnsupportedParentBundleLocation`) -- so the tray shows a virtual camera that is on while the socket honestly answers off. the log says so in as many words: "OBS cannot install the virtual camera if it's not in /Applications".

the cost when it happens: a re-download, a TCC re-grant (screen recording, camera, mic) because the bundle path changes, and a manual approval under System Settings > General > Login Items & Extensions > Camera Extensions. MDM is the wildcard -- a managed profile can disallow user approval of system extensions outright, and this is a managed Mac.

## carried from the design doc

- P4 post: mpv-IPC scrub surface + the moviemaker surfaces (reno-tab touch view and main-display app over one core). the clip tile is the on-ramp -- today it hands the file to the OS, later it opens the scrub surface.
- P3 remainder: the motion vocabulary landed and is now SEEN. dynamic paths beyond a single eased move, and filter-value animation, did not.
- couch client: standalone presenter TUI, blocked behind panel-surfaces phase 2.

## decisions the user should ratify (from the FX agent, still open)

1. pixelate is stock-only, no composite-blur path at all.
2. the script producer for FX is a step command, not a header.
3. ~~pixelate is deliberately absent from the pad~~ -- OVERRULED by the user 2026-08-21, and it is a plus/minus pair now.
4. an FX move writes `desired.Webcam`, so the cam band stays honest -- consequence being that a later full apply re-asserts the FX's destination, and an FX is therefore not purely ephemeral. jitter and the level effects deliberately do NOT write it: a shake returns to where it started, and a filter is not a placement.
5. a note-back failure downgrades a verb to `unconfirmed` + nonzero exit, so a successful `cam hide` from a bare shell with no bus running still exits nonzero.
6. NEW: a level effect's scale is khudson's table, not a knob. the old explicit `pixelate on <blocks>` argument is gone -- one concept, five steps, and a script that wants a specific grid has to want a level instead.

## how these bugs were actually found, because the next one will hide the same way

three bugs across two sessions, all invisible to every readout. the scene composited to black, the webcam was absent, the pixelate did nothing -- and in all three the journal said ok, the verb exited 0, the transform read back correct, and the source was healthy. the only thing that ever disagreed was the pixels.

what works, in order:

- `GetSourceScreenshot` per SOURCE versus per SCENE splits the problem in half in one step. sources fine and scene empty points at the item, not at capture, permissions, or the display. it found the black frame and it found the z-order.
- `GetSceneItemList`'s `sceneItemIndex` IS the stacking order, ascending, highest on top. proved by moving the cam to the top index by hand and re-shooting the scene.
- `ffmpeg blackdetect` on the clip separates "black from frame 0" from "clipped at the end". cropping the PiP's rectangle out of a frame at full res separates "the cam is not there" from "the cam is there and dark" -- 2.7KB of flat desktop versus 135KB of frames.
- polling `GetSceneItemTransform` at ~12ms while an FX runs is how you prove a MOTION effect actually moves, rather than trusting a verb that reports ok after handing OBS a batch. it is the only way jitter is testable at all.
- OBS's own log (`~/Library/Application Support/obs-studio/logs/`) carries the capture-layer history, the virtual-camera refusal, and an unclean shutdown.
- when a filter's schema is in doubt, `GetSourceFilterDefaultSettings` answers it in one request. guessing is how the pixelate settings sat unconfirmed for a month; they were right, and the filter was still useless.
