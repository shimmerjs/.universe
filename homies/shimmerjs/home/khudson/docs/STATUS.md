# khudson status

Current state, backlog, and open items. Design rationale is in
`DESIGN.md`; execution history (home v2 devplan, dev milestones,
research notes) lives in git history -- docs/ carries living docs only.
Reconciled 2026-07-08 (supersedes the accreted 07-07/08 bullets).

## State (2026-07-09)

- IN TREE, UNCOMMITTED (2026-07-09 batch, awaiting user commit +
  switch): claude-panel ("clod" on the border now, hauz style) fleet
  trees -- detail zone renders one tree per workflow dir plus one for
  loose subagents (children indented, running-first), touch ANYWHERE
  in a tree folds it to a one-line summary root (counts + type
  breakdown) and back; fold acts are handled in-process
  (module.ActHandler + bus repoll poke, ~250ms to glass, never exec'd;
  vet gate unchanged). Input-requested emphasis: Row.Attention marks
  the exact rows awaiting input and the dock washes them with a STEADY
  mid-blend background (attnRowBGBlend 0.65; the first cut animated a
  comet across the row -- glass-verdict: unreadable, reverted to the
  wash), border ramp gained a 4-cell pure-warn plateau. Attention
  false-positive fixed (glass-reported: bell on a working session):
  mid-turn gates (permission_prompt / agent_needs_input) resolve with
  NO hook firing, so main-transcript activity or a Stop strictly after
  notification_ts now answers them (observed live: transcript +12s
  after the bell with attention still set); idle_prompt keeps
  prompt-only answering. Chrome: the base HUD's outer frame dropped
  (panelRegion is now the full body) and the per-view right-edge
  "home" column deleted -- the strip's persistent home icon is the one
  return affordance.
  Adversarially reviewed (38-agent lens/verify workflow + two codex
  cross-model passes); confirmed findings fixed in-tree: fleet-tree
  order now live-first + key-stable (per-poll mtime sort would re-key
  fold acts under a finger), wide-rune/combining-mark cells in the
  comet, plateau off-by-one, vet/dispatch config-swap TOCTOU
  (vetRowAct single snapshot), repoll clears failure backoff,
  same-run-id wf dirs across satellites merge into one tree; act
  publication (applyNative) and repoll scheduling now test-pinned.
  vet/test green (27 pkgs) + closure green. Known cosmetic gap
  (accepted): a just-launched workflow with no agents yet counts in
  the title rollup but draws no tree for its first seconds.
- ON GLASS (last switch ~2026-07-08 01:37):
  home v2 -- strip-hosted nav under the panel (tabs + cup), claude-panel
  center column w/ cwd-first grouped rows, kb-live right column (K=75),
  home-kb-strip collapse behind chevrons, layout.state persistence,
  org.khudson.* launchd agents, touchd running -daemon. The deployed
  strip icons are the glass-verified BLOB art and the rail is the
  borderless grid -- both corrected in-tree, below.
- COMMITTED, AWAITING NEXT SWITCH (the project now lives in ONE
  squashed commit, subject "khudson: Corsair Xeneon Edge touch HUD";
  every batch below was spec-locked + adversarially reviewed,
  vet/test/race + closure green before the squash): marching
  attention border on the claude panel, ansi.Cut row refactor,
  tap-down flashes (wall-clock-synced), kb fill textures v2
  (width-safe recipes dots/dot-grid/line-grid + 5 nerd-font speckles,
  :sparse/:dense density grammar, PUA double-measure fail-safe),
  NO-OVERFLOW filled frames settled (frame question CLOSED, revised
  on glass: the kb frame keeps the SAME square chrome glyphs as every
  widget and signals the layer by border COLOR alone -- identity hue
  off-base, dim on base; fill dropped to 0.06, texture canvas only;
  oryx link moved to the bottom-right border so it cannot read as a
  layer button), attention liveness horizon 1h (kills the 17h
  bell), quieter fill (chip blend 0.12), selector-row oryx button
  (configure.zsa.io at the synced revision + active layer),
  duplicate-strip-label vet, strip icons as single-cell nerd glyphs,
  bordered rail grid, kb layer chip tint, dockmirror omits khudson
  itself, deep-loop cleanup (below), claude hooks compiled (khudson
  hook <event>: ~12ms median/fire vs ~65-70ms bash+jq, semantics
  test-pinned in internal/hookspool), session short id dropped from
  panel rows (idless sessions render "-"). Also HUD window identity:
  rebranded khudson.app
  bundle copy of kitty.app + generated icns; the hud agent execs the
  copy's .kitty-wrapped. Caveat: the Dock may serve a stale icon until
  `rm /var/folders/*/*/*/com.apple.dock.iconcache; killall Dock`
  (manual flush, documented only, not automated). Eyes-on at switch:
  Dock tile + Cmd-Tab show khudson + icon; fullscreen + touch still
  work; kitten resolution inside HUD windows.
- RUNTIME VERIFIED LIVE 2026-07-08: touchd daemon up, Moonlander
  vendor channel open shared + pairing init sent, keys.sock present.
  Input Monitoring for khudson-touchd is MISSING (0xE00002E2 on the
  mouse collection; the launchd prompt never fired) -- grant it
  manually + kickstart, then presses should light the board. The Edge
  digitizer/mouse legs retry quietly forever while Touchscreen
  Gestures holds the device (parked swap decision).
- Home v2 ledger: shipped in full (direction + devplan retired to git
  history). Every batch spec-locked (aw-implement) +
  adversarially reviewed, follow-ups fixed in-tree; vet/test + closure
  independently re-run green at each step. Durable constraint found
  along the way: bubbletea v2's compositor forwards ONLY SGR and
  OSC 8 -- see DESIGN.md Rendering.
- Dev lifecycle: project-local `shell.nix` -> `khudson-{test,race,
  build,vet,live,vendorhash}`; validation build-local; closure =
  `nix build .#darwinConfigurations.aw-chainguard.system` (never
  `nix flake check`: pre-existing kraken breakage).

## Backlog

Design questions (user's call, in play):

- **Collapse/expand UX**: CLOSED 2026-07-08 -- user picked INVERT.
  Collapse now hides the kb column: home-kb-strip deleted, home-no-kb
  added (no keyboard, claude-panel takes the freed width at 148), the
  kb-border chevron gone, the flip control lives on the status strip
  beside the nav tabs (strip.flip in edge.cue). Eyes-on at the next
  switch: the strip flip chevron hides/restores the kb column; state
  survives bus restart via layout.state (bus.go:187-190) as before --
  the flip is still just a layout switch, persistence needed NO
  change.
- **Resources box fit**: deliberately untuned (crops bottom); revisit
  dials on glass (top N, drop the disk volume, height-aware renderer).
- **kb column width**: K=75 shows 4-cell legends; narrowing via the
  compact render (frees ~35 cols for claude) is a post-eyes-on call.
- **SYS pill target**: stub ("soon" flash). Cheap path = native
  resources full-fill layout; btop-as-pane = own design pass under the
  two-zone contract.

Feature backlog (durable):

- **Event-driven data layer** (user direction 2026-07-08: "make
  everything event/listener based as possible for live updates with
  minimal resource overhead"). Design pass mapping each polling site
  to its macOS event source: claudesessions discover() -> FSEvents on
  ~/.claude/projects + the spool dir (poll only on change; kills the
  3s lstat storm outright); keymapp db -> watch the sqlite file;
  hudlaunch display poll -> CGDisplay reconfigure callback; dockmirror
  -> NSWorkspace launch/terminate notifications. procs/top stays
  sampled (CPU%% has no event source) -- its fix remains the cadence
  cache. FSEvents needs cgo (precedent: ax package); fsnotify/kqueue
  is fd-per-dir and does not scale to hundreds of project dirs.
  Subsumes most of BATCH-POLLCACHE -- fold those items into this
  design pass; the cadence caches are the cheap interim if this waits.
- **Claude panel v3** (four user asks 2026-07-08, one design pass):
  1. ATTENTION SEMANTICS: the panel equates idle_prompt ("Claude is
     waiting for your input" -- fires 60s after ANY turn ends) with
     "actively requesting input". Spool forensics: every attention
     entry present is idle_prompt; zero permission_prompt /
     agent_needs_input. Proposed: idle_prompt renders dim "at prompt"
     (no pin, no marching border); warn attention reserved for
     permission_prompt + agent_needs_input. Trade-off to decide: a
     turn ENDING with a question also only fires idle_prompt (the
     spool's last_assistant tail could heuristically upgrade).
  2. SESSION LOCATOR: cheaper than the lsof plan -- the hook spool
     ALREADY records kitty_window_id per session; render it (and
     optionally tap-to-focus via main-kitty RC focus-window, socket
     already spoken by kittysessions). Non-kitty/headless degrade to
     a badge.
  3. TREE VIEW: a top-level session and its subagents / workflows /
     fanouts render as a flat list of independent sessions; should be
     a tree, expandable/collapsible via touch (fleet() already
     discovers the children; this is a rows-layout + hit-table
     change).
  4. SPOOL HYGIENE: the hook spool is the one PERSISTENT surface and
     it accretes stale generations (two 21h-stuck attention entries
     from a pre-horizon hook shape observed 2026-07-08). Sweep on bus
     boot + periodically in the background: drop entries whose
     transcript is gone or ancient, version-stamp the shape so old
     generations are dropped rather than misread. (In-memory panel
     caches already reset on process restart -- the spool is what
     does not.)
- **Media as foreign PANE** (spotatui via the content-slot contract):
  first foreign view; own design pass before build.
- **Bigger-than-cell icons / images**: only remaining route is kitty
  graphics with a compositor bypass (own project); OSC 66 and drawn
  block art are both dead ends (DESIGN.md Rendering).
- **Resume verb unlock** (user gate): add `launch` to the M9 allowlist
  in rc-password.conf; `khudson claude resume` self-stages until then.
- **Multitouch**: parked, macOS-platform-blocked (research thread).

Debt (DEEP LOOP 2026-07-08 late: full-tree review -- correctness +
simplify + deslop, 165 adversarially-confirmed items applied in two
rounds; headline fixes: strip clock rendered a literal "mon" every
day, clock-crop off-by-one, custom-label keys lost their layer tint,
touchd board-loss now clears held keys, gesture 2->1->2 re-anchor,
send-key target vet, hist-flush socket-claim gate; keymapp gRPC cone
deleted as stranded dead code, grpc dropped from go.mod. Audit
wf_27e3b86e: 22 confirmed of 66 found -> five waves; the
42-finding tail JUDGED 2026-07-08, wf_560a3cf1: 26 already fixed by
the waves, 16 confirmed live by 3/3 adversarial votes, 0 refuted, NO
p0 -- nothing user-visible is broken on the deployed HUD. Judged fix
list, next-wave order:
- BATCH-POLLCACHE (cadence-gated cache seam, dockmirror.minimizedCached
  shape): procs.go:69 top -l 2 at ~20% duty cycle (the daemon's
  largest steady cost, the one meaningful p1 perf item);
  dockmirror.go:61 defaults-export every 5s; claudesessions.go:373
  discover() full-tree lstat every 3s; hudlaunch.go:155 healthy-tick
  JXA every 15s.
- BATCH-TESTS (harnesses exist): touch ingest e2e (bus.go:543, sole
  input path, keys twin is tested); hudlaunch Run supervision loop
  (the 07-06 junk-window class); sysmon seam + VMStatUsedGiB fixtures
  (live tests skip in nix builds); rc LS/Launch response shapes;
  scheduler scrape-vs-resize interleave pin; cmdCtl arg table; touchd
  flag guards.
- Standalone: kittykrib nix wiring (below); readDCS scan watermark +
  theme.go dedupe.
- ACCEPT (judged confirmed but not meaningful for a single-dock
  localhost HUD): broadcast-under-b.mu 2s stall (self-heals via
  eviction), kb-live whole-frame recompose (bench + invalidation test
  already pin it).):

- DONE: ALL FIVE WAVES + review follow-ups (2026-07-08). Wave 1 bus
  p1s (TypeReload propagation + greeting config, sockclaim
  probe-then-claim shared with hudlaunch, shutdown drain + Binding
  read), panel freshness fixes, AX unminimize, HUD identity, invert.
  Wave 2 scheduler p2s (scrape apply-guard, async adopt w/ in-flight
  materialize gate + Binding-read discipline, stale marks
  error-proofed, ctx-aware serve). Wave 3 exec/io (act allowlist +
  exit/start surfacing via TypeNotice, dock dial generations, media
  calm-state discrimination, poller leak, socket umask). Wave 4 nix
  (identity-pinned codesign verify each activation w/ -x repair +
  tamper check legs, RC posture flake checks, sub-11-col golden,
  recompose bench). Wave 5 kittykrib (one-line column contract on
  every column, slashless + control-whitespace-free name vet).
- NEW DEBT (wave-5 executor finding): pkgs/kittykrib has NO flake
  check -- nothing in the repo builds it; validation was a targeted
  buildGoModule expr. Wire a real check (mkchecks builder or packages
  output) when convenient.
- Accepted-with-notes: sockclaim TOCTOU window (in-code note),
  drainInFlight deadline invariant (in-code note), rail tap flash can
  migrate tiles if rows reorder inside its 250 ms window (cost of
  index-keying), marching attention border forfeits
  the home frame cache at 1 Hz while a bell is live (bounded to 1 h
  by the attention horizon).

Older debt:

- The 07-06 khudson-fix-list (17 deferred findings) is RESOLVED by
  the five waves above; the 07-06 review's 31 unverified findings got
  their judgement pass in audit wf_27e3b86e -- its 42-over-cap tail
  (tracked above) is the only judgement debt left. kittykrib was
  reviewed in wave 5 + the audit; its remaining debt is the missing
  flake check (tracked above).
- **Perf watch**: with kb-live visible every press/release recomposes
  the whole home frame (whole-frame homeCache); per-region caching is
  the named escape hatch if the glass ever feels it.
- **layout.state resurrection edge** (on record, in-code documented):
  a reload that drops the active layout leaves the state file behind;
  a much-later restart under a config that re-defines the name adopts
  it.

## Open items (user gates, in order)

1. `sudo darwin-rebuild switch` -- deploys the squashed project
   commit (glyph icons, rail grid, layer tint, khudson.app identity,
   invert, all debt waves, styling batches, oryx button, deep-loop
   cleanup).
2. Input Monitoring grant for khudson-touchd (System Settings; the
   prompt does not fire for launchd daemons) + `launchctl kickstart -k
   gui/$UID/org.khudson.touchd`; then Moonlander liveness eyes-on:
   presses light the board, OSL follows the layer, Keymapp
   coexistence (shared open) confirmed.
3. Accessibility grant for the khudson binary (System Settings; the
   prompt fires once from the bus on first AX use) + `launchctl
   kickstart -k gui/$UID/org.khudson.bus` -- unlocks dock-mirror
   per-window unminimize (Dock-item AXPress; backlog item closed).
4. Collapse/expand UX decision -- CLOSED 2026-07-08 (invert; shipped
   as the strip flip chevron, see backlog). Remaining gate is the
   eyes-on at switch.
5. Optional: `launch` allowlist edit to unlock resume.
6. Physical eyes-on debt: f2 cheatsheet + cmd+shift+e under
   socket-only; top-edge hover-reveal; first-tap swallow; night theme
   on the glass (now also: night theme x layer chip tint).
7. Deps bump commit (flake.lock + modules/darwin), still awaiting
   approval. The khudson history is squashed to one project commit
   (2026-07-08).

Notes: hooks and panel go live together at switch -- running claude
sessions keep their old hook set until restarted. kraken eval breakage
is pre-existing and unrelated (memory).
