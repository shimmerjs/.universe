- [ ] Make username and other me-details options so that chunks can be reusable
- [ ] Current profile (`/Users/shimmerjs/.nix-profile/bin` and 
      `/nix/var/nix/profiles/default/bin`) are duplicated in `PATH`
      (https://github.com/NixOS/nix/issues/5950?).
- [ ] Set up Rectangle launch agent (or whatever) via `nix-darwin`.
- [ ] Setup Flycut launch agent (or whatever) via `nix-darwin`.
- [ ] Sync settings for Flycut via `nix-darwin` or `home-manager`
- [ ] Use shell created from `flake.nix` for `hack/` scripts instead of `nix-shell`?
      - Confirm that using `nix-shell` uses unpinned channels that aren't
        controlled by `flake.nix`.
- [ ] Improve `$UNIVERSE_PATH`
- [ ] Make it easier to weave multiple "profile"-esque things (sets of configuration that for `home-manager`, `nix-darwin`, `nixos`) together via `lib/mksystem.nix`, instead of requiring manually wiring up imports, based on how it works for `homie` support in `lib/mksystem.nix`
- [ ] Factor `dev.nix` out of `homies/shimmerjs/home/default.nix` so that all my userlands don't get full dev tools.
- [ ] Figure out updating `brew` apps (check in lockfile?)

## Long-term

- [ ] [WIP] `modules/` should probably only be actually reusable modules that have
      options, etc.
      
