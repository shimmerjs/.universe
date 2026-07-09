# Edge host defaults (hand-applied to hosts/aw-chainguard)

## which host

`aw-chainguard`. Verified 2026-07-02 on the machine physically driving the
Xeneon Edge: `scutil --get ComputerName` and `hostname` both return
`aw-chainguard`. It is also the only host with the PR/claude widget substrate
(gh config, clod statusline).

Scoping rule: these defaults go in
`hosts/aw-chainguard/default.nix`, NEVER in the shared
`homies/shimmerjs/darwin.nix` layer -- unscoped they would land on all four
darwin hosts, including machines with a lock policy that display-never-sleeps
would violate. If a second host ever gets an Edge, the block gets extracted
to a module then, not preemptively.

## defaults block for `hosts/aw-chainguard/default.nix` `systemConfig`

```nix
      # Xeneon Edge substrate (khudson). This host physically drives the Edge;
      # keep these out of the shared darwin layer.
      system.defaults = {
        dock = {
          # Stable Space ordering: auto-rearrange would shuffle the Space the
          # Edge panel and its neighbors live on.
          mru-spaces = false;
          # 1 = disabled. Edge-origin swipes and taps near the glass corners
          # must never trigger hot-corner actions on the mains.
          wvous-tl-corner = 1;
          wvous-tr-corner = 1;
          wvous-bl-corner = 1;
          wvous-br-corner = 1;
        };
      };

      # The Edge is a HUD: its content is only useful if the panel never
      # sleeps. Applies to all displays on this host; lock posture is
      # unchanged (screen lock and require-password settings are untouched).
      power.sleep.display = "never";
```

## screensaver idleTime (activation script, per-user ByHost)

`com.apple.screensaver idleTime` is ByHost, so nix-darwin's
CustomUserPreferences cannot set it; it needs `defaults -currentHost` at
activation. Goes in the `home` block of `hosts/aw-chainguard/default.nix`:

```nix
      # Screensaver off: it would blank the Edge HUD. ByHost domain ->
      # -currentHost activation write, not CustomUserPreferences.
      home.activation.edgeScreensaverOff = lib.hm.dag.entryAfter [ "writeBoundary" ] ''
        run /usr/bin/defaults -currentHost write com.apple.screensaver idleTime -int 0
      '';
```

## not included, on purpose

- `dock.orientation`: DESIGN-v2 conditions it on the Edge sitting below the
  mains; decide at wiring time, not here.
- `displayplacer` (arrangement persistence) is homebrew, host-scoped, and
  lands with the wiring change alongside `homebrew.brews`.
- The khudson home-manager module import itself: this doc is substrate only;
  module.nix is imported host-scoped by hosts/aw-chainguard/default.nix.
