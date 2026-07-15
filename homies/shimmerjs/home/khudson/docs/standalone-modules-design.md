# standalone khudson modules -- design (keyboard first)

status: proposed. from a 3-candidate design workflow (11 agents; 1 stress lens -- packaging/macOS -- died on a schema retry, so that area is UNVERIFIED by the adversarial pass and sits exactly under open-question 2). All anchors are the workflow's, verified against source.

## goal

run each major khudson unit end-to-end on its own -- kitty split, kitty panel, tmux pane, plain tty -- decoupled from the Edge and the full dock/bus stack, keyboard unit first. Unify with couch mode (one host, docked = Edge dock, undocked = panels, no nix reconfig). User steer: prefer composable contracts (a Go interface surface, or a git-subcommand-style binary with an agreed stdin/stdout contract) over growing the top-level khudson app to do both modes -- because the monolith is harder to share/recompose with other tools.

## recommended spine: a Go library CORE + thin forwarding clients

GOVERNING PRINCIPLE (user directive): for tools we own, the CORE is a Go library that owns ALL view state, rendering, and logic. The runnable surfaces -- a `khudson` client (Edge dock) and a `standalone` client (panel/split), plus a `stdio` client for foreign tools (amendment below) -- are VERY THIN forwarding layers. Thinness is not aesthetic: it is the behavior-consistency guarantee. Anything a client reimplements is a place the surfaces can diverge -- exactly the FORK the workflow's rejected candidates fell into (per-unit binaries rebuilt module rendering via `renderRows + renderTitledBox` inline and silently dropped the charm: the `Data.Title` override home.go:284, the "stale" badge home.go:291, the `renderAttentionBox` marching border home.go:303, the row-cap home.go:287). One library, rendered through the same `renderHomeWidget` composition (home.go:274-307), everywhere.

**Where the core/client line falls (draw it so clients stay thin):**
- **CORE (the library) owns:** all view state (cfg, layout, now, widgetData, palette + rowStyles, hits, kb state, dirty flag, openURL); ALL rendering -- module regions, strip chrome, layout composition, the keyboard, `renderHomeWidget` and every leaf; the hit table; the `invalidate()` seam. The host-cache-invalidation writes now inline (`homeCache.ok=false` at kb.go:242, kb.go:296, kb.go:142) become `v.invalidate()` calls the client wires. The same golden tests run against the CORE, so every client is provably identical output for identical inputs -- consistency enforced, not asserted.
- **A CLIENT owns ONLY host plumbing** (the irreducibly per-surface parts): where data comes from (a DataSource), where frames go (present target + AltScreen on/off + sizing), how input arrives (input translation), and lifecycle. Everything else forwards to the core.

Concretely the clients are thin shells: `khudson` (edge) = AltScreen on, khudson render-bus attached as DataSource/ThemeSource/ActSink, sendGrid + grid negotiation (render-bus-side scrape/recognizer sizing, NOT a render input), touch->gesture; `standalone` (panel) = AltScreen off, WindowSizeMsg self-size, no render-bus, no grid negotiation, mouse-driven. The khudson render-bus is demoted from mandatory substrate to one optional DataSource/ThemeSource/ActSink a client may attach -- it is client plumbing, not core.

### two backbones, do NOT conflate them

There are two "bus"-like things and only ONE of them is optional for a standalone client:

- **magicbus daemon** (touchd/magicbusd -- the HID backbone, see magicbus-design.md): the single shared daemon that owns HID devices, holds the Input Monitoring TCC grant, and serves keys.sock / touch.sock / logi.sock. It is NEVER embedded per app -- that is the whole "one daemon, never N HID daemons, one grant" principle. EVERY client rides it, docked or couch. The keyboard module core includes a `keysReader` (factored from bus/keys.go) that DIALS keys.sock -- a thin socket consumer, not a HID opener. Because keys.sock is the keyboard's only source in every client, the keysReader lives in the CORE, not a per-client DataSource seam: khudson, standalone, and stdio clients all get key events identically, by the core dialing the one shared daemon.
- **khudson render-bus** (internal/bus -- the render orchestrator): gesture recognition, module poll scheduling, fanning frames to docks. THIS is what a standalone client does not need (it self-renders through the core library and takes input natively). "no bus" above always means this one.

So on the couch: the magicbus daemon runs (moonlander module, keys.sock served, holds the grant), the standalone keyboard client dials keys.sock and renders through the core; the khudson render-bus and the Edge dock are NOT running. Daemon up, render-bus and dock optional -- the exact "daemon up, dock optional" split from the couch-mode decision. No app embeds the HID backbone; they are all clients of the one daemon.

The ONE legitimate per-client behavior difference is input-focus policy (see the Moonlander catch below): the standalone client must NOT grab keyboard focus. That is genuinely host policy, not core logic, so it lives in the client -- the documented exception to "clients forward everything."

## the decisive catch: the Moonlander IS the input device

All three candidates initially proposed `--focus-policy=on-demand` + native key nav for the keyboard panel. That is a fatal inversion: the Moonlander is the same physical keyboard whose presses macOS routes to the focused window, while touchd reads its raw HID to serve keys.sock regardless of focus. If the panel grabbed keyboard focus, every physical press would BOTH light a cell (via keys.sock) AND arrive as a `tea.KeyMsg`, and any layer emitting `q` or `ctrl+c` would quit the panel out from under the user (Update binds those to `tea.Quit`, dock.go:364).

Resolution: **the P1 keyboard panel is display + live-highlight + MOUSE-only, no keyboard grab, no native key nav.** Live highlights come from keys.sock (focus-independent HID); layer switch and oryx-open are mouse via the existing hit-table fallback (`resolveTap` already fires on kitty `MouseClickMsg`, dock.go:379-382); dismiss is mouse or the toggle-visibility hotkey, never `q`. Native key nav + wheel scroll defer to focused DATA panels where the collision doesn't exist.

## keyboard factoring (P1 concrete)

Extract a `kbView` from kb.go owning: `ensureBoard` (kb.go:65, fully offline -- keymappdb.DefaultPath + Active + keyboard.FromRevision, no net/bus), `kbRegionBody` (kb.go:195), `kbSelector` (kb.go:272), `kbLayerChip`/`kbLayerEdge` (kb.go:346,608), `kbOryxOverlay` (kb.go:404), `kbCycleHit` (kb.go:238), and a NEW pure `applyKey(ev *proto.KeyEvent) (changed bool)` folded from `handleKeyMsg` (kb.go:107-135, matrix->slot via keyboard.SlotAt). `*model` embeds `*kbView`; `renderKeyboard`/`renderKBLive` delegate and stay byte-identical (pinned by TestKeyboardFullscreenGolden, kb_test.go:220). Factor a shared `keysReader` out of bus/keys.go:27-59 that emits KeyEvents on a channel AND self-synthesizes KeyEventClear on socket disconnect (the bus does this at keys.go:41; a direct consumer inherits the duty or held highlights stick after touchd dies). `kbTexture` reads `cfg.Widgets` (kb.go:433), so the panel host must load the dock cfg and pass the same texture param or the board renders plain and diverges from the docked pixels. Add a compact ~80x17 golden -- the fullscreen golden pins only 196x24, and standalone runs the untested compact branch (kb.go:208, kbKeyW clamp kb.go:618).

## AMENDMENT (mine): the stdio contract the synthesis left out

The synthesis delivers a `khudson panel <unit>` subcommand -- one binary, shared packages, git-subcommand-SHAPED at the CLI, runs in any terminal. That satisfies "Go interface surface" and "runs in a split." It does NOT deliver the language-neutral stdin/stdout DATA contract you also named, and it is technically "more surface area on the top-level khudson app" -- the route you ranked least desirable, because lifting one module out for another tool means shipping the whole khudson binary.

These reconcile cleanly under the library-core principle: the `stdio` client is just a THIRD thin client over the same core, alongside `khudson` and `standalone`. It forwards the core's rendered frames to stdout as ndjson and reads input events from stdin -- the git-external-subcommand DATA contract. Same core (no fork), three thin clients: `khudson` (Edge dock, bus-attached), `standalone` (local panel), `stdio` (drivable by fzf, another WM's bar, a non-Go tool). First-party tools get the Go library directly (linking the core); foreign tools get the stdio contract. The contract is the versioned PUBLIC seam for non-Go consumers; the Go library is the seam for our own clients. Do NOT ship separate per-unit binaries with their own render code -- that is the fork.

Open sub-decision: whether `stdioSurface` ships in P1 (keyboard over the contract from day one) or lands after the panel host proves the view core. Recommendation: P1 ships panelSurface (fastest to "keyboard in a split"); stdioSurface is P1.5 once the view core is stable, since it is a pure additional Surface with no view-core change.

## phases (each independently deployable)

1. **`khudson panel keyboard`** end-to-end in a split/panel/tmux/tty: offline static board, live highlight FROM the shared magicbus keys.sock when the daemon is up, mouse-driven selector + oryx link, degrades to the dim "open Keymapp" hint with no DB, no altscreen, self-sized, zero khudson RENDER-bus (still rides the magicbus daemon's keys.sock), zero Edge, no keyboard grab. Extract kbView + invalidate callback (dock stays byte-identical; add the compact golden), factor keysReader into the core, add the standalone client, add the subcommand, load cfg for texture parity.
1.5 (amendment) **`stdioSurface`**: the same keyboard unit drivable over the ndjson stdin/stdout contract by a non-khudson host.
2. **`khudson panel <data-widget>`** for pure-Poll modules, claude-sessions first: one titled region through the SAME renderHomeWidget path (title/badge/border/cap preserved), read-only then in-process row-acts. A local poll loop must re-derive single-flight + cadence outside the bus scheduler (module.go:22) or a slow Poll stacks goroutines and breaks constant-cost.
3. **Edge dock re-expressed as edgeSurface** over the same view core -- kills the docked-vs-undocked divergence; bus becomes a plug-in DataSource/ThemeSource/ActSink. (Until here, two tea.Models coexist -- accepted interim duplication.)
4. **Quick-access packaging + parity polish**: resolved global-hotkey bind, recovered layer tint (an OSC palette query, since kbLayerChip/Edge return nil without the bus TypeTheme palette), tmux/tty parity, couch power-posture decision.

## what gets worse

- Two tea.Models until P3 (dock host + panel host); host scaffolding duplicated until the dock rides the shared view core.
- The kbView extraction touches the shipping dock keyboard; the invalidate-callback re-threading is the sharp edge (wrong -> dock stops invalidating, or view leaks host state). Guard with the existing + new goldens.
- Standalone loses the layer-tint cue until the P4 OSC query (every layer renders identical chrome without the bus palette).
- Scroll for data-panel lists needs new render-side offset plumbing through renderRows/renderChromeRows + hit/act tables (renderChromeRows truncates from row 0, home.go:701) -- the Edge never needed it.
- **Couch runs the FULL headless stack with no reconfig**: bus/substrate/touchd are RunAtLoad+KeepAlive (module.nix:187) and the bus's `caffeinate -di` (edge.cue:175) keeps the couch machine + display awake. Zero-reconfig-on-undock (the earlier decision) has this cost on battery -- see open question.
- nav-tray and dock-mirror(rail) do NOT decompose: no backing module, bespoke bus/touch-coupled renderers (home.go:246), taps that only mean something inside the composed Edge dock. They stay dock-only.

## open questions (yours)

1. **stdio contract**: adopt the `stdioSurface` amendment (cross-tool recomposition, your stated goal), and does it ship P1.5 or later?
2. **quick-access bind**: macOS kitty cannot register a system-wide hotkey (grab-keyboard unsupported), and the in-repo quick-access-terminal kitten is a different default-off component. A new registrar (skhd / Karabiner / Hammerspoon / macOS Shortcuts) is needed. Which? (This is also the area the failed stress lens left unverified.)
3. **next unit after keyboard**: claude-sessions has the highest couch value (read-heavy, Attention-driven, acts already exist as `khudson claude focus/resume`), vs resources/sysmon (pure display, no act path).
4. **keyboard unit input scope**: keep it display + live-highlight + mouse-only (the collision-safe P1), or later add a mode?
5. **couch power posture**: on battery, undocked, should the runtime gate the bus's caffeinate and/or poll cadence when the Edge is absent, or is the headless bus + display assertion the accepted price of zero-reconfig unification? (Directly trades against the earlier "one config, runtime adapts, no reconfig" decision.)
