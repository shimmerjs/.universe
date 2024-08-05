- [ ] [WIP] Make username and other me-details options so that chunks can be 
      reusable
- [ ] Current profile (`/Users/shimmerjs/.nix-profile/bin` and 
      `/nix/var/nix/profiles/default/bin`) are duplicated in `PATH`
      (https://github.com/NixOS/nix/issues/5950?).
- [ ] Set up Rectangle/Flycut/etc launch agent (or whatever) via `nix-darwin`.
- [ ] Sync settings for Flycut via `nix-darwin` or `home-manager`
- [ ] Use shell created from `flake.nix` for `hack/` scripts instead of 
      `nix-shell`?
      - Confirm that using `nix-shell` uses unpinned channels that aren't
        managed by `flake.nix`.
- [ ] Make it easier to weave multiple "profile"-esque things (sets of 
      configuration that for `home-manager`, `nix-darwin`, `nixos`) together via
      `lib/mksystem.nix`, instead of requiring manually wiring up imports, based
      on how it works for `homie` support in `lib/mksystem.nix`
- [ ] Factor `dev.nix` out of `homies/shimmerjs/home/default.nix` so that all my
      userlands don't get full dev tools.
- [ ] Figure out updating `brew` apps (check in lockfile?)
- [ ] [WIP] `modules/` should probably only be actually reusable modules 
      that have options, etc.
- [ ] Make tailscale configs into OS-agnostic module
- [ ] Figure out remotely pulling updates for hosts
- [ ] Figure out how to script updates to `.nix` files, e.g. updating IPs, disk
      names, `secrets/secrets.nix`, etc.
- [ ] System activation script which checks for ~/.universe, clones it if not
      present, and attempts to sync if it is present
- [ ] Propagate additional information about the set of managed hosts into
      configurations, such as the machine IP.