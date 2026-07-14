# khudson overlay / popover subsystem -- design

status: proposed (design workflow, 9 agents; the input-focus and pid lenses each ran a source-grounded verify). Long-press-triggered modal overlays; first instances: dockmirror context menu (quit/force-quit) and the keyboard firmware picker. All anchors verified against source; go.mod:7 lipgloss/v2 v2.0.4.

## recommendation: `Popover` -- a lipgloss.Canvas-composited modal overlay, delivered to modules as DATA via a new `module.Row.Menu`

Three candidate mechanisms; each of the other two had a FATAL grounded flaw, so the shape is forced:

- **compositing = lipgloss.Canvas** (already imported, zero new deps: lipgloss/v2 v2.0.4, ultraviolet, x/ansi -- dock.go:22/home.go:14). Single injection point at `View()` (dock.go:681). REJECTED hand-compositing (candidate B): splicing a box over the base string means `ansi.Cut` seam surgery with SGR carryover and cuts at ambiguous-width nerd-glyph boundaries -- the exact glyph class `fitCell`/`fixedBlock` (home.go:804-839) and the OSC-66 strip guard exist to avoid. Canvas does it per-cell. The box is opaque by construction (renderTitledBox home.go:844 fills its interior with real cells that overwrite the base layer).
- **input focus = khudson-owned cell rects, NOT lipgloss `Compositor.Hit`.** REJECTED candidate A: it composed the menu as ONE `renderTitledBox` layer, but `Compositor.Hit` returns the top-most LAYER id (layer.go:295), so every interior cell reports the same id -- item 1 vs item 3 are indistinguishable. And `Hit`'s Compositor is a separate object from the render Canvas (layer.go:323). Taps already arrive as `(col,row)` cell coords, so item rects computed at build time are exact, testable without a canvas, and keep the GraphemeWidth coupling out of the hit path.
- **delivery = a data contract, NOT a registry.** REJECTED candidate C's synchronous per-module popover registry: it is non-executable because modules live on the BUS process and the dock is a SEPARATE process that imports zero module impls and only unmarshals `module.Data` (dock.go:527-533). So a module ships its menu as DATA: a new `module.Row.Menu []Act` field (module.go:88).

## P0 -- BLOCKING feasibility gate (do this before building anything)

`lipgloss.Canvas` hard-wires `GraphemeWidth` with no setter (canvas.go:27, verified). The existing `*SurvivesCompositor` tests (strip_test.go:149, kb_test.go:871) round-trip a hand-rolled `uv.NewScreenBuffer` that defaults to `WcWidth` -- they do NOT exercise the `GraphemeWidth` Canvas path this design ships. Kitty reports mode 2027, so bubbletea's on-glass renderer also uses GraphemeWidth and the double round-trip *should* be idempotent -- but that is capability-gated, not API-guaranteed. So P0 is a spike/golden that composes the REAL frame (strip + keyboard body) through actual `lipgloss.NewCanvas(w,h).Compose(NewLayer(base)).Render()` and diffs cell-for-cell against today's plain `View().Content`. If any nerd-font/ambiguous-width glyph shifts, the fallback is body-region-only compositing (strip stays on the untouched hand-built concat path) -- with the open sub-question of how boxes anchored adjacent to the strip behave across the two paths. This gate is why the subsystem is not just "add an overlay."

## the overlay model

`m.overlay *overlayState`, a nil-when-closed field on the dock model. `overlayState{ anchor rect; box string (built once on open / on selection change, NOT per tick); items []menuItem{label; kind; argv; area rect; destructive bool}; sel int; confirm *pendingConfirm }`. The CLOSED path is byte-identical to today (`v.SetContent(body + "\n" + m.renderStrip())`) -- zero added per-tick cost, and the existing goldens don't churn. Only when open does the frame go through the Canvas compose.

## trigger + focus (the fatal-class trap, and a hardware constraint)

Open by wiring the currently-no-op `GestureLongPress` branch (dock.go:567 only sets `lastGst` today). A second long-press while open RE-ANCHORS (close+reopen at the new press) -- defined, not left ambiguous.

**The modal focus gate:** while `m.overlay != nil`, the tap handler MUST branch to the overlay and EARLY-RETURN before `m.resolveTap` ever runs against the base `m.hits` (which is still rebuilt with base+strip rects while open). Miss this and the base tile/strip under a dismiss tap fires too -- the fatal-class bug. Three regions: (a) tap on an item rect -> fire + consume; (b) tap inside the box but not on an item (border/title/padding) -> consume, STAY open (fat-finger near-misses must not dismiss); (c) tap outside the box -> dismiss + consume. Base is never tappable through the overlay.

**Hardware constraint baked into the interaction idiom:** after a long-press the recognizer sits in `stateHeld`, swallows all motion, and emits nothing on release (recognizer.go:130-134). So **slide-to-select is impossible.** The idiom is long-press (open) -> LIFT -> a separate Tap on the item. Menu targets must be sized large enough for a clean lift-between-touches, not a drag.

## menu actions + the right-pid guarantee

New `module.Row.Menu []Act`; the scheduler collects each row's Menu argvs into `st.Acts` alongside `row.Act` (scheduler.go:430-431), so menu items inherit the existing allowlist (`vetRowAct`, input.go:57) + the 2s exec debounce + reaping via `startArgv` unchanged. Route device-affecting items through the published-act/startArgv EXEC path, NOT `module.ActHandler.HandleAct` (that skips the debounce/reaping; it's for cheap in-process view flips).

**Right-pid guarantee** -- the sharp correctness point: the pid stays OUT of the published argv. Candidate C's literal `["kill","-9",pid]` is doubly wrong -- `vetRowAct` refuses it (it's in no poll's rows), and the pid is stale/wrong by tap time. Instead dockmirror publishes stable-key verbs `<exe> ax quit --bundle <id>` / `force-quit --bundle <id>` (BUNDLE id, not display name -- pgrep on the lsappinfo display name mis-resolves "Google Chrome" vs comm "Google Chrome Helper" and can match multiple pids). `parseRunning` is extended to capture bundle id per running app; the new `khudson ax quit|force-quit` verb resolves bundle-id -> current pid(s) at EXEC time (on-demand, constant-cost preserved), re-validates the bundle still matches, then `osascript quit` / `kill -9`. These run as plain LOCAL subprocesses via startArgv (like the existing `ax unminimize`, dockmirror.go:74/234) -- NO kitty remote-control / socket needed (an earlier assumption that the RC path was required is wrong).

**Confirm is structural, not a P2 afterthought:** destructive items (`destructive:true`, e.g. force-quit) require a confirm sub-state -- the first tap sets `overlay.confirm` and re-renders a confirm target; only a SECOND explicit tap on Confirm execs. The 2s debounce is amplification protection, not intent confirmation.

## depends on: the gesture keystone

A LongPress at (col,row) must yield (widgetID, entry, rect). Today `m.hits`/`hitRegion` carry no widget id and no long-press slot (dock.go:72-96) and `resolveTap` is first-match with no modal notion -- that's the gesture keystone from the standalone/interaction threads; do NOT rebuild it here. **P1 bridge if the keystone hasn't landed:** add an optional `longPress func(x,y)` slot to `hitRegion` so dockmirror's app-tile rects carry their own "open menu for app X" opener -- smaller than blocking P1 on the full keystone.

## phases

0. **BLOCKING GraphemeWidth fidelity spike** (above). Deployable/standalone: it's a test.
1. **Overlay primitive end-to-end + dockmirror long-press quit/force-quit menu**, with the right-pid guarantee and a confirm step on force-quit. `overlay.go` (overlayState + Popover + modal tap gate with early-return before base resolveTap); Canvas compose at dock.go:681 (closed path byte-identical); the long-press opener (keystone contract or the hitRegion bridge); `module.Row.Menu` collected into st.Acts; dockmirror captures bundle id + publishes per-app menu acts; new `khudson ax quit|force-quit --bundle <id>` verb resolving+validating at exec.
2. **Keyboard firmware picker** over the kb board region + mandatory flashing guardrails -- SEPARATE spec, gated behind the P1 primitive. Highest-stakes (device write + DFU). nix-provisioned firmware dir + ctime-sorted lister (newest first); the keyboard module publishes per-file `khudson ax flash <file>` menu acts; the flash verb takes the filename POSITIONALLY before flag.Parse. Everything about the flasher is greenfield (see open questions).

## what gets worse

- Per-frame CPU while OPEN: the whole base double-round-trips through ultraviolet (Canvas.Render, then bubbletea's flush re-parse). Bounded, only while open; closed stays constant-cost.
- Coupling to an unsettable lipgloss internal (GraphemeWidth, canvas.go:27) -- if the deployed kitty ever stops reporting mode 2027, the composited frame is measured at a different width than the glass. P0 is the guard; the fallback is body-only compositing.
- Larger allowlist + exec surface: dockmirror's st.Acts grows ~2 per running app, and a device-affecting verb (later `flash`, a device WRITE) joins the read-mostly open/unminimize set.
- More model + input state: flat first-match resolveTap gains a modal precedence branch + confirm sub-state + per-item cell rects.
- Goldens: an overlay-OPEN golden needs a fresh GraphemeWidth baseline; the closed path MUST stay off the canvas to avoid churning existing goldens.

## open questions (yours)

1. P0 fallback scope if the spike fails: body-region-only compositing straddles two render paths for strip-adjacent anchors -- clamp such anchors into the body, or accept the seam?
2. Firmware source dir: DECIDED default = ~/Downloads (Oryx/Wally/QMK land .bin there), CONFIGURABLE via the module (a `firmwareDir` param, not a hardcode) -- the ctime-sorted lister points there by default. Still open: which flasher (dfu-util vs wally-cli), how DFU entry triggers (magic key / reset), and whether to filter the Downloads listing to the board's known .bin name pattern so an unrelated download can't be flashed. Entirely greenfield otherwise (no firmware/dfu/wally/qmk in-tree today); nix provisions the flasher toolchain.
3. Is the Popover khudson-only (internal/dock or internal/overlay) or extracted to a standalone bubbletea v2 package? The component is portable; the delivery contract (module.Row.Menu + bus) is khudson-specific -- extract only if another app wants it.
4. Gesture keystone timing: if it hasn't landed before P1, is the `hitRegion` longPress-slot bridge acceptable as shipped, or does P1 block on the keystone?
5. Bundle id vs multi-instance apps: confirm bundle id disambiguates multiple windows/instances for force-quit (ASN is ephemeral so it can't live in poll-time st.Acts -- bundle-id + re-validate-at-exec is the chosen handle).
6. Firmware-list overflow on a single-touch surface: cap+page, vs wiring a drag-scroll gesture for the open overlay (swipe/wheel are dock no-ops today) -- given slide-to-select is impossible and lift-between-touches is mandatory.
7. Confirm UX: a second overlay vs an in-place confirm row; and whether non-destructive items (quit) skip confirm while force-quit/flash require it.
