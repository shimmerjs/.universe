# khudson design decisions

Reconciled, load-bearing decisions for the Corsair Xeneon Edge touch HUD.
Durable reference; current state and backlog live in `STATUS.md`. Deep
gotchas and the verified-constraint history live in the author's memory
(`edge-hud-project`).

## Shape

- **Two-zone contract.** The glass partitions into CHROME (khudson-owned,
  khudson-rendered: rail, strip, native widgets; owns the screen edges;
  khudson's touch vocabulary) and CONTENT SLOTS (kitty-split regions whose
  occupant swaps under chrome's RC control -- khudson views, foreign TUIs, or
  arbitrary terminal programs with zero khudson wiring). khudson is a touch-native
  tiling shell + slot manager, not a renders-everything monolith. Chrome and
  content are separate kitty windows = separate processes.
- **Strip-hosted nav** (home v2, 2026-07-08): the nav-tray rail is dead. The
  2-row status strip under the body hosts the nav band -- chrome home glyph,
  config tab entries (top-level `strip` CUE block, not a widget), state
  toggles (cup, warn on bus-absent/skew) -- ahead of layout/bus/gesture
  status and the clock. The home-return affordance is the strip's
  persistent home glyph, layout-NAME based: homeLayout prefers the layout
  named `home` (kind drives the engine, name drives the affordance).
- **Widget taxonomy.** Bespoke native builtins (khudson-rendered, full khudson
  touch) vs foreign TUIs as config-driven kitty-native split panes (NO khudson
  touch forwarding -- kitty routes clicks/scroll to the pane natively under
  the current driver; scroll is momentum-wheel-free, or `kitten @
  scroll-window` for scrollback). OPEN DECISION, not yet executed: panes
  would retire the scrape/substrate/blit/injection machinery (keepAlive as
  a background tab; no invisible-paint requirement) -- the exec/scrape
  stack is still deployed and load-bearing (btop, spotatui).
  First-class seams: khudson styles split borders/colors/margins
  via kitty so foreign panes feel native; richer adornments draw in chrome
  cells adjacent to the slot (no per-split titlebars).
- **Dock-rail per-window unminimize = Dock-item AXPress, direct AX**
  (2026-07-08): minimized rows act through `khudson ax unminimize
  <title>`, which walks the Dock process's own AX tree and presses the
  exact-titled AXMinimizedWindowDockItem (in-app AXMinimized-write
  fallback for the tap-vs-sweep race). ONE TCC grant: Accessibility on
  the fixed-path khudson binary. The osascript/System Events sweep is
  retired and the Automation grant dropped.

## Rendering

- **Style deferral.** Chrome uses ANSI-16 / default fg-bg so the kitty theme
  IS the theme; a theme swap is `kitten @ set-colors` (+ m1ddc), which also
  remaps foreign panes. CARVE-OUT: identity is data-not-style -- per-app /
  per-session colors (stable FNV hash into ANSI-16 hues, config hex
  overrides) and gauge/series heat may exceed plain chrome styling, still
  theme-mapped.
- **Config-driven UI.** Layouts are CUE regions (edge-peel + fill-split);
  layout ALGORITHM in Go, layout CONTENT in config. New surfaces extend
  config, not code.
- **One row renderer + unified hit table.** Every row kind (text / kv / gauge
  / series / resource / spans) renders through one shared path, so a new kind
  works in every panel by construction; one `resolveTap` over one hit table
  serves both mouse and gesture input. Home bodies are cached between data
  changes.
- **The compositor owns the wire** (probed + test-pinned 2026-07-08).
  bubbletea v2 composites View() through ultraviolet, which forwards ONLY
  SGR and OSC 8 to the pty: OSC 66 text-sizing, cursor-motion escapes, and
  kitty graphics CANNOT be emitted from inside the dock's renderer -- eaten
  before kitty sees a byte. Consequences: icons bigger than one cell are
  drawn cells or nothing (and 4x2 quadrant art is ~8x4 px of resolution --
  read as blobs on glass; single-cell nerd glyphs won), and any future
  image/scaled rendering needs a compositor bypass (own project). Render
  assertions belong at the COMPOSITOR layer (round-trip through the
  ultraviolet cell buffer, TestStripSurvivesCompositor precedent), never on
  View() strings -- a green View() test proved nothing while the glass was
  blank. Supersedes the old "revisit OSC 66 with a measuring story" note.
  AMENDMENTS from the charm-ecosystem survey (2026-07-08, pinned-version
  sources): (1) images ARE possible SGR-only via x/mosaic half-block
  cells (low-res, but survives the compositor -- kitty graphics is no
  longer "the only route"); (2) the half-block "rounded fill" border was
  demoed and REJECTED (user 2026-07-08): the chamfer is a quarter-cell,
  invisible at 13px cells -- it only buys a borderless flat card, and
  square line frames stay the house style; true semicircle caps
  (powerline glyphs) exist only for 1-row pills; (3) lipgloss v2 Canvas/Layer/Compositor
  (Hit() at layer-bounds granularity) exists for z-ordered overlays;
  (4) per-region render caching has a sanctioned middle path: cache
  regions as uv.Buffers composed on a lipgloss Canvas per frame --
  bubbletea's renderer already no-ops on unchanged View strings and
  diffs cell-wise otherwise, so whole-frame homeCache already rides the
  fast path; (5) at the pinned versions lipgloss.Width == ansi.
  StringWidth per line and kitty reports mode 2027, so the deployed
  stack is GraphemeWidth end-to-end (fitCell stays as the WcWidth
  fallback belt).

## Fullscreen + launch

- Plain stock-kitty window + native fullscreen (`--position` onto the Edge +
  `--start-as fullscreen`). NOT panels -- they refuse fullscreen; the
  menubar-cover patch is shelved. Display-gated launcher: never opens with the
  display absent (clamps a junk window otherwise), computes `--position` from
  NSScreen at launch, tears down / relaunches on display or child change, no
  `macos_hide_from_tasks` (breaks fullscreen).
- **HUD identity = rebranded bundle copy** (2026-07-08): kitty.app is copied
  for real to a khudson.app bundle (patched Info.plist + generated icns), and
  the launcher execs the copy's `Contents/MacOS/.kitty-wrapped` -- bundle
  discovery walks up from the running image's path, so a direct exec needs no
  LaunchServices registration. Dock/Cmd-Tab EXCLUSION is CLOSED as
  unsupported: it requires accessory activation policy, and accessory breaks
  native fullscreen.

## claude widget

- **Control-center identity, NOT per-session metrics.** Cost / ctx
  removed (ctx-from-cache lied at 100%; token/burn unwanted here); model +
  effort deliberately returned as dim context on the panel detail header
  only. One line per session, static-width columns leading: activity
  clock first (tone carries active, text stays honest last-output age),
  typed state glyph (needs-you / error / done / in-flight blank), glyph
  agent/wf counts, fixed-width cwd (repo-relative primary, fish-style
  abbreviation as the overflow fallback), then the identity-colored
  session name (hue keyed to the sid, stable across name appearance) and
  last-prompt tail. Rows GROUP by cwd; groups order by
  NEWEST member start (user pick 2026-07-08: a session birth floats its
  group; zero starts excluded from the fold), rows within
  by first-transcript timestamp -- every ordering key immutable per
  session, so geometry cannot flap under a finger (tap-target safety).
  Tally on the region border, not a header.
- **Discovery.** `~/.claude/projects` fs layout (general case, no deploy dep)
  + cross-project uuid SATELLITE join (28/52 dirs are satellites -- fleet
  files land under the workflow's cwd dir) + fleet-driven liveness (busiest
  sessions read dead by parent mtime alone) + `~/.claude/sessions`
  pid-registry as the membership gate and needs-user/activity truth
  (waiting/busy/idle; identity-checked pid; no age prune -- the 6h window
  param is vestigial) + module-owned claude hooks for prompt/cwd.
- **Hook economics (measured).** Hooks run `khudson hook <event>` -- one
  static-binary fork, ~12ms median per fire (50-run measure; the bash+jq
  scripts it replaced forked 4-9 children at ~65-70ms). Per-batch events
  still cost the same class as per-tool (1.23-1.36x measured on the shell
  era), so the hook surface stays per-turn-class regardless of the
  cheaper handler. Hooks have NO controlling tty -- identity plants ride
  env (KITTY_WINDOW_ID) or launch --var, never OSC to /dev/tty. The
  UserPromptSubmit payload carries no effort field; steering a running
  turn fires no UserPromptSubmit at all (the transcript tail is the
  corrective source).

## Moonlander keyboard

- **Live source = HID-direct, event-driven** (chosen over keymapp gRPC
  polling: more control over perf/overhead/surface). Protocol decoded: QMK
  raw HID 0xFF60/0x61, host writes a 0x01 PAIRING_INIT report, firmware then
  streams `[0x06,col,row]` keydown / `[0x07,col,row]` keyup / `[0x05,layer]`;
  open SHARED (`hid_darwin_set_open_exclusive(0)`), single-owner vs Keymapp.
  Reader folds into touchd (holds Input Monitoring TCC, vendors go-hid,
  broadcasts on a socket).
- **Static layer view** (one layer at a time, tap-to-cycle selector --
  4-8 layers of tap+hold legends cannot legibly coexist at 196x24) reads
  the user's real layout from the local
  `keymapp.sqlite3` (offline, no network/hashId gate; requires Keymapp to have
  synced; oryx GraphQL fetch is the fallback; "open Keymapp to sync" empty
  state). Static layout + LiveSource JOIN at a renderer: static view = static
  only (works unplugged); live view = static + HID overlay. Static is
  foundational for BOTH (HID gives coords+layer; legends come from the layout).

## Packaging + lifecycle

- **Module owns upstream-app config.** Enabling `universe.home.khudson`
  brings up its own claude integration (the six module-owned spool hooks:
  UserPromptSubmit, SessionStart, SessionEnd, Notification, Stop,
  StopFailure, merged into `programs.claude-code.settings.hooks`) and
  kitty integration (hud-kitty.conf, `mainKittyIntegration` option). The
  module touches the user's claude settings only when enabled.
- **Deps via vendorHash, not committed vendor.** khudson + touchd fetch from
  go.mod/go.sum (nix builds the go-modules FOD). Recompute after a dep bump
  with `khudson-vendorhash` inside the devShell. (Repo-wide, other Go builds
  use committed vendor; khudson is the exception, chosen to keep the tree
  lean.)
- **Dev lifecycle in a devShell.** `nix-shell` from the khudson dir (the
  project-local shell.nix; deliberately NOT wired into the root flake,
  user 2026-07-07) provides go + the
  host tools the tests/modules exec (m1ddc, gh, kitten, btop, sqlite) and the
  `khudson-{test,race,build,vet,live,vendorhash}` tasks. `doCheck = true` on
  the module builds (darwin builds run sandbox-off; host-tool-gated tests
  skip-on-missing and run in the devShell instead).

## Working discipline (hard-won)

- sha-verify every binary swap (a stale-binary deploy burned two screenshot
  rounds).
- Live-debug over fixtures: scrape the glass + dump the real widgetData +
  replay through the real render path. Fixtures modeled runtime wrongly twice.
- `grep -c` and `| tail` eat exit codes -- check exits unpiped.
