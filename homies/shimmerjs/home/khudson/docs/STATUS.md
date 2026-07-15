# khudson status

Reconciled 2026-07-10 (supersedes the accreted 07-08/09/10 bullets;
mapped doc-by-doc against the working tree by a 5-mapper pass).
Living state only: history lives in git, doctrine in DESIGN.md, RC
integration in nix/main-kitty-integration.md, host scoping in
nix/edge-host.md, panel visual reference in panel-anatomy.html,
component/permission map in architecture.html.

## State

COMMITTED: faceae3 (07-08, the Edge HUD landing), 3538430 (07-09,
fleet trees / accordion / wash / home-icon / tap-flash / substrate
identity), 30d5f70 (07-10 "clod: sideload effective html skills" --
also carried the first registry tests, a kb golden snapshot, and the
clod skills block; the codex 5.6-sol pin and clod overlays.nix ride
the same clod arc).

LANDED SINCE: the 07-10 batch below committed via the
clod-khudson-hardening merge (now on main); the 07-14 incident
hardening (fscache O(1) panel, launcher supervision, cadence caches)
and the 07-15 magicbus/logiretch arc live in git history.

The 07-10 batch:

- Keyboard full render 17 -> 15 body lines: thumb cluster is one
  4-key line pair, wide piano key at the inner end against the
  center gap (an intermediate row-4 seat was glass-rejected);
  kbFullLines 15, golden re-captured, compact fold shares the
  order (test discriminator is main-row pitch now, not the fold).
- Clod panel rebuilt on the Claude Code session registry
  (~/.claude/sessions/<pid>.json), replacing the spool heuristics
  as truth:
  - membership: only sessions whose registry pid is verifiably the
    recorded live process render (kill -0 with EPERM=dead, sysctl
    start-time vs startedAt within 1m, 7d updatedAt backstop for
    unverifiable records). The 6h window prune retired (param
    parses, vestigial; dropped from edge.cue).
  - needs-user (wash, glyph, detail pin, Data.Attention): status
    "waiting" exactly; "busy"/"idle" never wash (idle observed live
    -- distinct from waiting); unknown statuses and a bell newer
    than the busy flip fall back to the spool heuristic
    (attentionLive), which survives only as that fallback.
  - activity tone: session.active() = files fresh within 60s OR
    registry busy (a turn parked in one long tool call appends no
    files; verified live -- can-work busy, transcript 84s stale).
    Age text stays the honest last-output age.
  - names: derived registry handles ("can-9b") never display;
    explicit name > spool session_title > cwd basename.
  - hardening (aw-review, 8 confirmed findings, all fixed):
    registry read faults are Poll errors, never the empty state;
    one-poll grace absorbs torn registry reads (fold state and
    detail zone survive a flicker); waitingFor scrubbed before
    glass; stale-bell guards prefer the registry flip when it
    postdates the notification.
  - spool wiped 2026-07-10 (user-directed; hook-derived,
    regenerates).
- Tap-to-focus fixed end to end: every tap died at kitten auth --
  KITTY_RC_PASSWORD forces pubkey auth, and KITTY_PUBLIC_KEY exists
  only inside kitty's own children, never in the launchd bus. The
  fix became the posture change below; the focus resolution chain
  and claude-verbs.log stay the observables.
- Kitty RC password machinery REMOVED (user-directed): kitty never
  consults remote_control_password for socket-only peers, so the
  arc was inert -- auth is the user-write-only socket under the
  0700 state root. Gone: rc-password.conf (file deleted),
  ReadRCAuth/Allows/parse, ClaudeVerbs.PasswordFile, -password-file
  flag, the resume verb-allowlist gate (resume's consent is
  CLI-only reachability). posture-check legs (a)/(c) now REJECT any
  password line. Passwordless socket path live-verified (kitten ls
  round-trip + the kittysessions gated live test).
- Panel showcase: env-gated TestPanelShowcase dumps real renders
  for curated states; docs/panel-anatomy.html annotates them.
- Detail zone reads as a card: every zone row (pads included) leads
  with an occupant-hued rail (one-eighth block, identity-as-data --
  the hue matches the session's list name), splitting the two
  subpanels visually. Workflow legs named on glass: aw-* prompts
  carry a [wf:leg] name-plate; the panel reads transcript heads
  (bounded, memoized -- heads are immutable) for leg descs and the
  run name on the tree root (fold key stays the run id).
- This ledger + DESIGN.md + main-kitty-integration.md +
  edge-host.md consolidated (5-mapper + 2-verifier pass).

## Owed manual steps (next switch or after)

- Quit + relaunch the daily kitty ONCE: RC posture binds at
  startup, so the RUNNING kitty keeps the retired password-era
  posture until relaunched (switch replaces the conf file itself).
  Then: socket exists + passwordless `kitten @ ls` round-trips.
- Running claude sessions keep their OLD hook set until the
  session restarts -- hooks + panel only live together after both
  the switch and a session restart.
- Input Monitoring grant for khudson-touchd (launchd daemons get
  no TCC prompt); Accessibility grant for the khudson binary.
- Dock stale-icon flush if the old icon lingers (rm the
  com.apple.dock.iconcache under /var/folders + killall Dock).

## Registered follow-ups (awaiting user go)

- Event-driven data layer: CLOSED 07-15 via the hook-poke leg --
  khudson hook nudges `repoll claude-sessions` over khudson.sock
  after every spool write (fire-and-forget, 150ms hard deadline,
  silent on a bus-less machine); the bus's repoll ctl verb resolves
  widgets by module or id onto the scheduler's repoll channel. The
  panel now updates on session events; the 3s poll is the backstop.
  Per-site FSEvents/kqueue variants stay deliberately unbuilt (the
  07-14 cadence caches cover those sites).
- Strip error surfacing: CLOSED 07-15 (TypeActFail one-slot record
  + greeting replay; strip warn cell decays after 60s; act path
  untouched on the failure branch).
- Spool hygiene: CLOSED 07-15 (spool_version stamp on every hook
  write, legacy unstamped files tolerated; Sweep prunes age +
  foreign-version at session end and bus boot, never per-tick).
- Screensaver blank hazard: CLOSED 07-15 per the edge-host.md
  plan (wvous-br-corner disarmed + mru-spaces pinned in
  system.defaults.dock; idleTime zeroed via -currentHost
  activation). Effective at the next switch + Dock restart.

## Backlog (dials and batches)

- BATCH-POLLCACHE, BATCH-TESTS, readDCS watermark: CLOSED 07-15.
  dockmirror + claudesessions legs were already covered by the
  07-14 cadence/fscache work; procs top now samples behind a 15s
  sampleEvery cache, the hud-launcher healthy tick rides
  HealthyPoll (4x -poll default, -healthy-poll flag), readDCS got
  an 8MB cap + linear scan, and every BATCH-TESTS item has a pin
  (details in the test doc comments; commits carry the rest).
- magicbusd flag/mode residue: FULLY RESOLVED 07-15 third pass. A
  mode is now REQUIRED (bare argv errors -- the flagless-launchd
  trap is dead; spike needs -spike, -mouse implies it); trailing
  args after logiretch-probe rejected; probe/list reject every
  spike/daemon knob; full combo matrix pinned in TestRunComboGuards
  + TestMainArgv.
- Long-press on glass: root-caused 07-15 (gestures driver still
  seizes the digitizer, so the recognizer LongPress path never
  fires; driver delivers hold as RIGHT-CLICK). Dock now routes
  right-click to the same menu opener -- menus reachable under the
  driver today, unchanged post-swap. Lands on glass at the next
  switch + HUD restart.
- magicbus disable cleanup: CLOSED 07-15 (a !enable activation leg
  boots out + removes the agent when the plist exists; the signed
  binary and stamps stay so re-enable keeps the TCC grant).
- Design dials: resources box fit, kb column width K=75, SYS pill.
- Collapse/expand UX: shipped (strip.flip home/home-no-kb);
  eyes-on at switch is the remaining gate.

## Accepted limitations

- A just-launched workflow counts in the title rollup before it
  draws a tree (cosmetic; discovery vs fleetNodes timing).
- Shell-spawned kitty binds no RC socket (the daily kitty is
  LS-launched; macos-launch-services-cmdline is LS-only).
- Same-user processes can drive kitty via the socket; accepted
  threat model (a password file would be same-user-readable too).
- Rail tap flash can migrate tiles within its 250ms window --
  accepted cost of index-keying (same-named tiles must not
  co-flash; home.go records the keying decision).

## Dev lifecycle

- nix/devshell.nix targets: khudson-{test,race,build,vet,live,
  vendorhash,dock-dev,glass-dev,glass-restore}; shell.nix
  deliberately non-flake (user 2026-07-07). UX iteration loop
  (07-15): dock-dev runs a working-tree dock in the current kitty
  window against the live bus (real data/theme/gestures, no
  switch); glass-dev swaps the ON-GLASS dock for a working-tree
  build via the hud-launcher dev-override marker (loud log, 6h
  auto-expiry, no TCC surface); glass-restore reverts.
- House accent is color5/magenta as of 07-15 (the icon's mauve,
  ~= everforest color5): chromeAccent, tapStyle chip, overlay
  bloom, brand, row emphasis. Heat/gauge ramps stay semantic
  green-yellow-red (color-is-data carve-out).
- Module builds run doCheck=true (darwin builds sandbox-off);
  sqlite3-gated kb tests skip hermetically, run locally.
- Full gate: build the darwin closure
  (nix build .#darwinConfigurations.aw-chainguard.system).
  NEVER `nix flake check`: kraken's darwinConfiguration fails eval
  (pre-existing, scott's rectangle plist option) and takes the
  whole check run with it.
