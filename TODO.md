- [ ] Make username and other me-details options so that chunks can be reusable
- [ ] `niv` sources in home-manager modules? Figure out if this can be replaced
      with flake-isms.
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

## Long-term

- [ ] `modules/` should probably only be actually reusable modules that have
      options, etc.
- [ ] Pretty tacky to need to split what _feels_ like the same kind of config
      simply because `home-manager` supports different things than `nix-darwin`,
      e.g., `brew` installs of Flycut and other userland apps.