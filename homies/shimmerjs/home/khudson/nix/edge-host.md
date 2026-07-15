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

## screensaver blank hazard: CLOSED 2026-07-15

Landed exactly as prescribed, in hosts/aw-chainguard/default.nix:

- `system.defaults.dock.wvous-br-corner = 1` disarms the corner that let
  an Edge-glass tap start the screensaver (caffeinate does not block
  user-triggered screensavers); `mru-spaces = false` durable-ized in the
  same block. The Dock reads both on its next restart.
- `com.apple.screensaver idleTime = 0` via a `-currentHost`
  home.activation write (ByHost domain; CustomUserPreferences cannot set
  it).

Takes effect at the next switch (+ one Dock restart for the dock keys).

## not included, on purpose

- `dock.orientation`: decide at wiring time, not here.
- `displayplacer` (arrangement persistence): homebrew, host-scoped, lands
  with the wiring change alongside `homebrew.brews`.
- The khudson module import itself: this doc is substrate only; module.nix
  is imported host-scoped by hosts/aw-chainguard/default.nix.
