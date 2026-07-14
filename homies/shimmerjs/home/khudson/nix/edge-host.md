# Edge host defaults (hosts/aw-chainguard)

## which host

`aw-chainguard` (re-verified 2026-07-10: `scutil --get ComputerName` and
`hostname`). The only host importing clod (hosts/aw-chainguard/default.nix)
and the only one physically driving the Xeneon Edge.

Scoping rule: Edge-host settings go in `hosts/aw-chainguard/default.nix`,
NEVER in the shared `homies/shimmerjs/darwin.nix` layer -- unscoped they
land on every shimmerjs darwin host (aw-chainguard, nostromo, mother),
including machines whose lock policy a display-never-sleeps would violate.
If a second host ever gets an Edge, extract a module then, not
preemptively.

## display sleep: superseded by runtime caffeinate

The old plan (static `power.sleep.display = "never"`) is superseded by
decision: the bus owns a caffeinate supervisor (`/usr/bin/caffeinate -di`,
default ON, glass-toggleable via the strip cup / `khudson ctl
caffeinate`). A static never-sleep could not be toggled off from the
glass; runtime wins, the static setting stays unset. Anchors: edge.cue
caffeinate comment, internal/bus/caffeinate.go, schema CaffeinateOn. Do
not re-add the static setting.

## open hazard: screensaver can still blank the HUD

Live-probed 2026-07-10 on this host:

- `wvous-br-corner = 5` with modifier 0: an armed no-modifier
  start-screensaver hot corner. An Edge-glass tap near that corner blanks
  the HUD today; caffeinate does not block user-triggered screensavers.
- screensaver `idleTime = 600` (ByHost), not 0.

Neither the hot-corner disable (`wvous-* = 1`) nor the idleTime zeroing
ever landed in nix. If/when landing them: dock keys go in `systemConfig`
`system.defaults.dock`; `com.apple.screensaver idleTime` is a ByHost
domain nix-darwin CustomUserPreferences cannot set -- it needs a
`defaults -currentHost write` home.activation step. Also durable-ize
`mru-spaces = false` while there (live on the machine, hand-applied,
declared nowhere -- auto-rearrange would shuffle the Space the Edge panel
lives on).

## not included, on purpose

- `dock.orientation`: decide at wiring time, not here.
- `displayplacer` (arrangement persistence): homebrew, host-scoped, lands
  with the wiring change alongside `homebrew.brews`.
- The khudson module import itself: this doc is substrate only; module.nix
  is imported host-scoped by hosts/aw-chainguard/default.nix.
