# reno arc review a2409ae..HEAD (ab25171 -> 61ee8d6, 6 commits) -- synthesis

**Verdict: NO-GO on trusting reno recording as-is.** 28 confirmed verdicts reconcile to **22 distinct defects** (6 duplicate cross-lens reports merged): 6 high, 16 med. The theme is one cluster: the recording lifecycle's honesty guarantees (write-ahead journal, boundary/Final cleanup, arm-time snapshot) all have windows where OBS state and module state diverge -- several end in "OBS recording with no owner" or a corrupted journal, the exact outcomes the design exists to prevent.

**Tally:** confirmed 28 (-> 22 deduped) / refuted 0 surviving to this stage / unverified 35 below-floor (not findings; not silently dropped -- they exist but were never refute-verified, treat as unvetted leads only) / failed verifiers 0 reported. Found/fresh per-pass counts were not carried into this stage; the confirmed set is complete but pass-level coverage stats are a hole.

## HIGH (6)

1. **transition() lacks run==staged guard** (reno.go:198, 2 lenses) -- load script, tap record => ad hoc run walks the staged script and fires its machine actions; hash protection bypassed, step accounting skipped. *Fix: require m.run.Demo == m.staged.Name (or ScriptPath match) in transition + presenter live-view gate.*
2. **Final cleanup chain can be dropped at enqueue** (effects.go:118) -- full 16-slot queue drops [Boundary, obsStop(Final)] whole; run finalizes while OBS records unowned. *Fix: guaranteed delivery for Final/boundary chains (blocking send or reserved slot).*
3. **armConfigLocked shallow-copies Screens** (adhoc.go:160, 3 lenses) -- armed/journaled config aliases m.pending.Screens; in-place toggle corrupts the live run's journal ([A,B]->[B,B]) and races the post-unlock ArmResult marshal (bus.go:501). *Fix: deep-copy Screens in armConfigLocked (both branches).*
4. **Webcam "off" sends cfgDefault** (view.go:331) -- clears pending.Webcam instead of setting an off preset; record path skips scene reconciliation and records the still-enabled camera. *Fix: distinct "off" preset vs a separate "OBS as-is" menu item.*
5. **No stale-camera removal** (scene.go:360) -- screen/audio-only configs leave the prior reno-cam enabled despite Webcam=="" documented as "no webcam item". *Fix: planner emits disable for existing reno-cam when Webcam is empty (mirrors stale-screen pruning).*
6. **Staged script config displayed, pending config armed** (view.go:242) -- view shows script config on m.staged.Record alone, but a tap with no live scripted run arms m.pending. *Fix: gate one-tap record until the script starts, or display the config that will actually arm.*

## MED (16)

7. **stopLocked skips obsStop for unproven starts** (reno.go:317, 2 lenses) -- stop during in-flight record start (esp. hand-run verb, unreachable by boundary kill) finalizes the run while OBS records on. *Fix: emit Final obsStop whenever record intent exists and StoppedAt is zero.*
8. **Failed-start re-arm keeps stale rec.Config** (record.go:79, 3 lenses) -- config-caused failures unrecoverable without stopping the run, contradicting the "applies at next arm" contract. *Fix: re-run armConfigLocked in the re-arm branch.*
9. **Re-arm clears Failed before journaling** (record.go:81) -- a WriteRun failure permanently disables scripted retry (distinct root cause from #8). *Fix: clear Failed only after the journal lands, or restore on failure.*
10. **Arm journal failure leaves phantom run** (record.go:87) -- refused arm keeps the never-journaled ad hoc run in m.run. *Fix: roll back m.run=nil, matching startLocked.*
11. **recordEffect debounce key constant per run** (record.go:160) -- 2s debounce can eat the scripted re-arm retry, wedging at "armed (start pending)" forever. *Fix: per-arm key (IntentAt-stamped), preserving tap double-fire protection.*
12. **Lost 'started' note orphans recording** (renoverb.go:323) -- ctl failure after StartRecord leaves intent-unproven state no code reconciles. *Fix: retry the note, or treat unproven intent as stop-worthy at run stop (pairs with #7).*
13. **Lost 'stopped' note unrecoverable, clip path lost** (renoverb.go:349) -- retry's StopRecord errors "output not active" and never notes. *Fix: emit the stopped note when StopRecord reports no active output.*
14. **Unscoped stop notes bind to whichever run is current** (record.go:104) -- hand-run recovery stop can write run A's output path into run B. *Fix: unscoped notes require the current run's recording Active, else error naming the ambiguity.*
15. **Effect epoch stamped at enqueue, not under Mod.mu** (effects.go:109) -- concurrent boundary between transition and dispatch stamps run A's chain with run B's epoch. *Fix: capture epoch (or worker-validated run ID) while Mod.mu is held.*
16. **boundary() SIGTERMs a live Final child** (effects.go:127) -- quick start/load--replace kills the in-flight obsStop cleanup; kill path untested. *Fix: track Final-ness of the live child and exempt it from the boundary kill.*
17. **MicDevice contract unimplemented -- hot mic** (scene.go:284) -- Audio=="" leaves reno-mic enabled+unmuted from the prior take; two dead struct fields. *Fix: emit mute/disable for existing reno-mic when Audio is empty (or delete the dead fields and doc the gap).*
18. **loadLocked: unbounded ReadFile of wire path under m.mu** (reno.go:411) -- /dev/zero OOMs the bus; a FIFO wedges the single input worker (all dock input). *Fix: stat-vet regular file + size cap before reading (the maxNotesBytes discipline); same for setNotesLocked's blocking open.*
19. **Seed enables obs-websocket on all interfaces, persistently** (obsboot.go:163) -- port 4455 (auth'd but cleartext ws://) exposed on every LAN, surviving khudson. *Fix: pf/loopback gating or disable server_enabled on teardown; at minimum surface the exposure at seed time.*
20. **Ensure discards the dial error** (obsboot.go:115) -- auth failure misdiagnosed as "websocket is down". *Fix: carry the dial error into every fallback branch's message.*
21. **Inputs adopted by name without kind check** (renoscene.go:24) -- post-upgrade kind mismatch never converges (display_uuid settings on a legacy-kind input). *Fix: compare in.Kind and recreate mismatched reno-owned inputs.*
22. **displayLabel = WxH only** (renoscene.go:145) -- identical displays get identical labels; ResolveItem picks the first, capturing the wrong monitor silently. *Fix: disambiguate labels with index/UUID.*
23. **CGDisplayPixelsWide returns points on Retina** (cgdisplay.go:54) -- PxW/PxH == logical size, so the pre-TCC region-crop fallback computes in the wrong space; the test fake (PxW=3024) encodes the bug. *Fix: use CGDisplayModeGetPixelWidth/Height and correct the fake.*

(#22/#23 are listed under renoscene.go/cgdisplay.go per the verifier line anchors; scene.go ResolveItem is the shared consumer for #22.)

## Recommended path and its cost

Fix in dependency order: the ownership/cleanup cluster first (#2, #7, #12, #13, #14, #16 -- these are one design decision: "record intent without proven start/stop is always stop-worthy and cleanup delivery is guaranteed"), then the arm/config-honesty cluster (#3, #4, #6, #8-#11), then scene reconciliation (#5, #17, #21-#23), then hardening (#18-#20, #15, #1's guard). This trades away three things, stated plainly: guaranteed Final delivery means stop can block briefly under a backed-up queue instead of never; deep copies and per-arm keys add small allocations per arm; and the scene fixes (#4, #5, #21) deliberately stop preserving hand-tweaked OBS state in the khudson-owned scene -- recreating kind-mismatched inputs discards manually attached filters. That loss of "leave my hand edits alone" is the real cost, and it is the right trade: the scene is documented as reno-owned, and silent hot-mic/hot-cam capture (#5, #17) is strictly worse than losing a filter.

The 35 below-floor unverified findings were not examined here; if any overlaps the clusters above, triage it during the corresponding fix rather than as a separate pass.
---

## Dispositions (2026-07-23, same session)

ALL 23 items fixed in the arc-2-fixes commit; every fix cites its
number in a code comment at the site. Notables: #15 landed as
Bus.DispatchTransition (one dispatchMu serializing module transition +
effect dispatch across producers); #2 as a bounded 5s blocking enqueue
for Final-bearing chains (drop stays possible but only past a wedged
queue, and the notice names the hand recovery verb); #13 converges a
lost stopped note by closing the journal pathless when OBS reports
OutputNotRunning; #23 switched cgdisplay to CGDisplayModeGetPixelWidth/
Height (CGDisplayPixelsWide returns points on Retina). The scene fixes
(#4/#5/#17/#21) deliberately stop preserving hand-tweaked OBS state in
the reno-owned scene, per the synthesis's stated trade. The 35
below-floor unverified leads remain unexamined.
