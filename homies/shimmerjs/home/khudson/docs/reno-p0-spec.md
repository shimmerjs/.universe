# reno P0 spec -- script + presenter

status: as built, reconciled against the 2026-07-22 design re-review (11 confirmed findings: 5 fatal, 6 major; dispositions at the end of this doc). Settles what reno-design.md deferred: the script syntax, the effect protocol types, the ctl wire shape, and the strip vocabulary for the tab-flip. Supersedes nothing; reno-design.md remains the design of record. Corrections from the 2026-07-22 subsystem map are folded in and marked (map). Ad hoc scriptless recording (user, 2026-07-22) shapes the run lifecycle here even though its OBS half lands in P1.

## script format (settled)

One txtar archive per demo. The comment section (before the first `-- file --` marker) is the script; file sections are markdown speaker notes. Parsed with golang.org/x/tools/txtar; the engine implements its own stepper over the parsed archive (registry pattern from go-internal testscript, not the runner).

```
demo my-demo
budget 20m

step intro
note intro.md
budget 90s
launch-session work --cwd ~/src/thing --keep-focus -- claude

step code
note code.md
budget 3m
focus-session work

step slides
budget 2m
open-url https://slides.example.com/deck

-- intro.md --
# welcome
say the thing about the thing.

-- code.md --
walk the diff bottom-up.
```

Grammar, line-oriented, `#` comments and blank lines skipped:

- header (before the first `step`): `demo <name>` required, name pinned `^[a-z][a-z0-9-]*$` (the widget-id regex); `budget <duration>` optional whole-talk budget. Recording config lines are P1 vocabulary (`record ...`, `scene ...`); the parser reserves the words now (vet error "lands in P1" rather than "unknown command").
- `step <id>` opens a step; id pinned to the same slug regex, unique per script. Everything until the next `step` belongs to it.
- `note <file.md>` binds the step's notes: either a file section in the archive or a relative path resolved against the script's directory. Containment vetted: no absolute paths, no `..` escape above the script dir. At most one note per step; a step may have none (ad hoc-ish scripted demos with static or no notes are legal by construction -- a script with one step and no commands is valid).
- `budget <duration>` per step, optional. Go duration syntax.
- every other line is a command from the closed P0 registry: `focus-session <handle>`, `launch-session <handle> [--cwd <dir>] [--keep-focus] [-- <argv>...]`, `focus-app <bundle-id>`, `open-url <url>`. Unknown command or wrong arity = loud vet error naming the line. `wait` and capture gates (`hide`/`show`) are reserved words that vet as "lands in P1" (they pair with recording).
- steps may carry zero commands (a pure note/timer step).

`khudson reno vet <script>`: parse + registry + arity + name/step slugs + note resolution + containment. Same code path the bus uses at load (one implementation, package internal/reno/script). The example script ships as testdata vetted by unit tests, which run inside the closure build (doCheck) -- that is the nix vet of committed scripts until real scripts land somewhere else.

## effect transitions (settled types)

New optional module capability beside ActHandler (internal/module/module.go):

```go
// Effect is one exec side effect of an in-process act: argv plus the
// debounce key the bus records it under (run id + step ordinal + action
// index for reno -- consecutive steps legally repeat identical argvs, so
// the key, not the argv, is the retap identity).
type Effect struct {
    Key  string
    Argv []string
}

// EffectHandler handles an act in-process AND returns exec effects.
// Handled acts repoll like ActHandler; effects exec bus-side under
// process-group tracking. Implementations must be safe concurrent with
// Poll.
type EffectHandler interface {
    HandleActEffects(argv []string) (handled bool, effects []Effect)
}
```

Bus dispatch (internal/bus/input.go handleRowAct): under the same one-config-snapshot vet as today, resolve EffectHandler first, plain ActHandler second. Handled-with-effects => pokePoll + dispatch each effect; plain handled => today's path, untouched. Effect dispatch (internal/bus/effects.go, shaped by review findings 3/4/6):

- ORDERED execution: one worker goroutine drains a FIFO of chains; effects within a chain run strictly sequentially (start, wait, next) so a step's launch completes before its focus resolves, and two fast transitions never interleave machine actions. A failed effect is loud (log + TypeNotice + ActFail) but does not stop the rest of its chain: half a step beats none mid-show, and the events journal records which action died. The queue is bounded (16 chains); overflow drops the whole chain loudly -- the input worker never stalls behind a wedged effect.
- argv shape gate at the dispatch site: effects must be `khudson reno <verb> ...` (argv[0] bare `khudson` or an absolute khudson path -- the TCC identity). The script vet is the semantic gate; this is the structural one, so a buggy or foreign EffectHandler cannot turn the executor into a general exec service (finding 6; the design's "vetted startArgv" line was wrong -- startArgv vets nothing).
- debounce: keyed by Effect.Key (run/step/VISIT/act -- re-entering a step via prev/goto is a fresh visit with fresh keys; only true replays of one transition debounce). Same debounceRepeat helper, 2s window, drops loud. The raw-argv debounce stays for the exec fall-through path. Double-advance protection is module-side: a 400ms transitionGap suppresses a lagging-panel retap before it moves the cursor -- which also collapses duplicate acts from multiple attached docks (finding 2: gestures broadcast to every dock and each republishes; the gap is the reno-scoped dedup).
- process ownership: effect children run under Setpgid; clean shutdown SIGTERMs the live group, 2s grace, SIGKILL. The SIGKILL-bus orphan window (kickstart -k) is accepted and BOUNDED instead of pidfile-reaped (finding 4 delta): every reno verb self-terminates -- runKitten's 10s sub-deadline plus a 30s hard deadline on the whole verb (renoVerbDeadline) -- so nothing long-lived can leak. Revisit if effects ever grow beyond self-bounded verbs.
- transport (finding 1 delta): full request-id/ack/retry for row acts is NOT built. The accepted contract: dock-side send failures surface loudly on the status line (no silent swallow), bus-side queue drops log + notice, the cursor truth repaints at 1s poll cadence, and a tap that visibly did nothing is retapped -- idempotent within the transitionGap, fresh-visit-keyed beyond it. Revisit as a P1+ decision if stage practice shows dropped taps.
- effect argvs carry correlation flags (`--run <id> --step <n> --act <i>`) so the exec'd verb journals correlated confirmation events.

## run lifecycle + ctl wire (settled)

proto.Msg grows `Args []string` (omitempty, wire-compatible); reno ctl verbs use Cmd="reno", Args=[subverb, ...]. `Arg` stays untouched for existing verbs. cmdReno opens its own RoleCtl connection (cmdCtl grammar untouched); bus side is one `case "reno"` in handleCtl delegating to the module singleton through a control interface asserted from b.mods["reno"].

Verbs: `khudson reno load <script> [--replace] | start | stop | status | next | prev | goto <step-id>`. load: bus reads + vets + stages (atomic -- vet fully before swap; CLI pre-vets for fast rich errors, bus is authority; the CLI absolutizes the path before it crosses to the bus process). start stamps a run id (UTC compact timestamp + short random suffix) and ARMS the run at cursor -1: arming acts on nothing, the elapsed clock is idle, and the first advance enters step 0, fires its effects, and starts the show clock. start REFUSES if the journal cannot be written -- a run that cannot recover after a crash must not begin. stop finalizes. One run at a time; load-while-running needs stop or --replace. next/prev/goto from the CLI go through ctl -> module -> the module returns effects to the BUS side, which execs them exactly like a tapped act -- one dispatch site, one ordering.

Scriptless (ad hoc) runs: the run struct's script is optional. P0 keeps the internals script-agnostic (journal, run id, workspace name "adhoc" reserved by the name regex vet); the user-facing `reno record` verb lands with OBS in P1. A one-step no-command script is the degenerate scripted path today.

Run journal: `<state root>/reno/runs/<run-id>.json`, hookspool writeAtomic shape (version-stamped JSON, atomic tmp+rename), written on every transition (O(1), small). Fields: version, run id, demo name, script path + sha256, started/show-started/stopped stamps, cursor, per-step visit counts + accumulated actuals. Journal ORDERING (finding 7, settled): the cursor mutation and the journal write commit together under the module lock BEFORE any effect starts -- write-ahead of effects -- and recovery is RECONCILE, NOT REPLAY: a recovered run restores cursor and timers and never re-fires step effects (the machine's state is whatever it is; the presenter re-drives it). Recovery: reno.New() (bus boot, one-shot) scans for the newest live journal; if its script still exists and hashes match, cursor + timers + staged script restore; on mismatch the run restores as stalled with a loud status row (never silently resume a different script). Finalized journals sweep at boot after 30d. Empty paths.Dir = no-op (bare test bus convention); a mid-run journal write failure is loud (status row + log) but does not kill the live show. P1 extends the record with recording intent + identity before any OBS effect fires (finding 5).

Reload: module instance survives; staged script + run state carry across. A load with a different script hash resets the cursor (design mandate; the map confirmed reload keeps module instances so this is the only correct shape).

## reno action verbs + confirmation events

`khudson reno focus-session <handle>` / `launch-session <handle> [--cwd] [--keep-focus] [-- argv...]` / `focus-app <bundle-id>` / `open-url <url>`, plus correlation flags. RenoVerbs struct in package bus (claudeverb shape: socket, log path, kittenRunner seam) reusing runKitten + rc types.

- kitty resolution: `--var reno_handle=<handle>` planted at launch; resolution = fresh ls, collect ALL matches of the user var, duplicate handles = loud error naming both windows (FindWindowByUserVar is first-match-only, so reno walks itself -- map). focus never sets no_response. launch-session default `--type tab`; `--keep-focus` passes through.
- focus-app: new ax verb (internal/ax). bundle-id -> pids via the existing resolveBundlePids (reuse -- map corrected the design's "budget as new work" for this leg); activation `open -b <bundle>`; AX raise on the app's focused/main window with the readback post-condition (write, re-read, Code-0 AXError on no-effect); Space-blind behavior stated now: other-Space windows surface ErrNotFound with explanation (P0 ships no space switching). Published argv[0] = os.Executable() (dockmirror TCC pattern); kitty/open verbs may publish bare khudson.
- confirmation events: `<state root>/reno/log/events.jsonl`, one JSON object per action: {ts, runId, step, act, verb, target, outcome, detail}. Post-action verification for kitty focus = second ls confirming the focused window is the resolved id. Write failures are loud (nonzero exit + ActFail surface), not swallowed. stderr keeps the human line.

## dock: strip vocabulary + presenter/recorder views

Strip tab-flip is NEW vocabulary (map: the shipped flip is one global pair, occupied by home/home-no-kb; tabs have exactly one target with exact-match active state):

- schema #Strip entries gain `alt?: string`: `{label: "reno", target: "reno-presenter", alt: "reno-recorder"}`. Tap: inactive tab -> target; active on target -> alt; active on alt -> target. Active-notch when m.layout is either. checkStrip: alt must be a defined layout and != target. The global flip pair is untouched.
- two kind-"home" layouts (map: non-home kinds hit the skew stub): `reno-presenter` and `reno-recorder`, each a single fill region wrapping its widget (the claude layout precedent -- zero new layout engine). Two widgets, one module: `reno-presenter` and `reno-recorder` declare module "reno" with `params: {view: "presenter" | "recorder"}` (kb-live mode-param precedent, vetted in checkWidgetParams).
- chromeRenderers["reno"]: one dock-side renderer keyed by module name serving both widgets, discriminated by params.view. It owns geometry: wraps the note body to the region rect, draws step title + next-step preview + elapsed/step timer chips, registers big next/prev tap rects (pressKey touch light) and a long-press goto menu, ends with the whole-region consume rect. Timer heat rides PctHeat-style budget fractions through the existing textHeat ramp; overtime = warn bucket + negative count. All painted runs through fitCell/fitCellPad.
- module Data: title row, note text rows (unwrapped -- the renderer wraps), chip row with budget fractions, acts `["reno:next"]` / `["reno:prev"]` published every poll (vet allowlist is last-successful-poll only), goto menu as Row.Menu windowed around the cursor (overlay keeps ~22 rows head-first; earlier/later shift acts re-window in-process, reopen to see the shifted window -- accepted P0 UX debt).
- recorder view in P0: run status + clip-workspace pointer + "transport lands with OBS (P1)" disabled state -- the layout, tab-flip, and view plumbing land complete now so P1 only fills the transport in. With no script loaded it shows the ad hoc affordance disabled likewise.
- 1s poll while visible = 1Hz timer flips (design poll floor). Elapsed is computed from journaled wall-clock stamps at poll time, never accumulated ticks (O(1), restart-safe). Module implements Essential().
- stale/disabled treatment is the renderer's own duty (map: chrome renderers get no free stale marks).
- goldens: one 196x24 presenter golden (cell-equality, KHUDSON_RENO_GOLDEN=update, overlay-golden pattern); width behavior loop-tested like the rest of the dock (map: width-bucket goldens do not exist as a convention; not introducing one).

## config/nix deltas

- schema/khudson.cue: #Module += "reno"; #Strip entry alt?; nothing else.
- internal/config: checkStrip alt rule; checkWidgetParams `case "reno"` (view enum).
- nix/edge.cue: 2 widgets, 2 layouts, 1 strip entry (fits the 196-col strip beside the existing 3 tabs -- verified against the map's width inventory before landing).
- go.mod: golang.org/x/tools (txtar only). vendorHash bump (git add first -- the helper reads the working tree, the flake reads the index; map).
- state root: `<state>/reno/{runs,log}` under the install -d -m 700 discipline (khudsonRuntimeDirs).
- full gate: nix build .#darwinConfigurations.aw-chainguard.system. Never nix flake check (kraken).

## review dispositions (2026-07-22 aw-design-review, 11 confirmed)

- (1, fatal) unacknowledged tap transport: PARTIAL -- dock send failures now loud, queue drops loud, cursor truth at 1s; full ack protocol deferred with rationale (see effect dispatch). Revisit on stage evidence.
- (2, fatal) multi-dock duplicate resolution: MITIGATED for reno by the module transitionGap; the general per-gesture token is pre-existing scope, not taken.
- (3, fatal) unordered effect execution: FIXED -- serialized FIFO worker, in-chain sequential waits.
- (4, fatal) SIGKILL orphan effects: BOUNDED -- every verb self-terminates (10s kitten + 30s verb deadline); pidfile reaping deliberately not built.
- (5, fatal) recording ownership/ingest: P1 -- recording intent + start identity + expected output journaled write-ahead, two-phase clip ingest on recovery (folded into reno-design P1 preconditions).
- (6, major) effect argv policy: FIXED -- structural gate at the dispatch site.
- (7, major) journal ordering unspecified: SETTLED -- write-ahead of effects, reconcile-not-replay (implemented).
- (8, major) kitty pin drift: FIXED -- nix/kitty-pin-check.nix fails the closure when nixpkgs kitty != rc.go pin.
- (9, major) OBS referent revalidation: P1 -- `reno vet --live` resolves scene UUIDs/names against the running OBS at load (folded into reno-design P1 preconditions).
- (10, major) module lifecycle manager: P1 precondition text extended in reno-design.
- (merged 6b) TCC identity enforcement: the gate admits bare `khudson` (socket verbs) and absolute khudson paths (AX verbs); the module builds focus-app argvs on os.Executable().

Round 2 (rekicked feasibility/scale legs reviewed the implementation; 2 fatal re-raises + 7 majors):

- (F1 re-raise) multi-dock double-fire: disposition stands (transitionGap); the general gesture-token rework remains out of scope.
- (F2 re-raise) SIGKILL orphan reaping: disposition stands (bounded self-terminating verbs); pidfile reaping still deliberately not built.
- (M1, crash-loop) recovered scriptless/stale journal nil-derefs the presenter view and crash-loops the bus: FIXED -- explicit live-without-usable-script branch in both views + regression test.
- (M2, shutdown panic) closing the multi-producer effect queue: FIXED -- the queue never closes; worker exits via stopCh, late dispatches refused on the closed flag.
- (M3, run boundaries) queued chains from a stopped run executing into the next: FIXED -- module.Boundary() effects from stop/start/load--replace bump the runner epoch (queued chains drop loud) and SIGTERM the live child; doubles as the mid-run abort.
- (M4, revisit semantics) prev/goto into a launch step failed on "already planted": FIXED -- launch-session is ensure-semantics (one existing match = ok; >1 = ambiguous). Goto-forward stays a JUMP -- skipped steps' effects do NOT replay (divergence from the review's catch-up recommendation: a mid-show effect burst is worse than a loud focus miss the presenter can goto back from).
- (M5, two-binary skew) bare `khudson` effect heads resolve the live profile mid-switch: FIXED -- every effect head is os.Executable() (the bus's fixed install).
- (M6, runtime pin preflight) P1: `reno vet --live` grows kitty version + socket + handle preflight beside the OBS referent pass.
- (M7, workspace GC) P1: SweepRuns extends to workspace dirs of finalized runs past the horizon, with an explicit keep-mark; boot-only work.

## P0 milestones

1. script package: txtar parse, registry, vet + `khudson reno vet` + testdata example script.
2. module + run lifecycle: reno module (Poll both views, Essential, locking), journal + recovery, ctl seam (proto Args, handleCtl reno, cmdReno), load/start/stop/status/next/prev/goto.
3. bus effects: Effect/EffectHandler, executor (pgid tracking, keyed debounce, shutdown kill), dispatch in handleRowAct.
4. action verbs: RenoVerbs (kitty focus/launch + duplicate-handle rejection), ax focus-app, open-url, correlated events journal.
5. dock: strip alt vocabulary, renderReno presenter + recorder shell, goto overlay, timer chips, golden + hit-table tests.
6. config/nix: schema, edge.cue, vendorHash, full gate build.

Each milestone gates on khudson-{build,vet,test}; the arc-end review runs over the accumulated range.
