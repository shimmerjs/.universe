# Darwin-side companion to the clod home module: claude-code from a fresh
# nixpkgs (flake input nixpkgs-claude, tracks master). The main nixpkgs is
# the cache-warm-but-lagging nixos-unstable, which trails claude-code's
# release cadence -- and a new model needs the version floor even when
# inference works on a stale binary, because the context-window metadata
# does not come down the wire. Bump with `nix flake update nixpkgs-claude`.
#
# Lives beside the clod home module but imports at the DARWIN layer (the
# host's systemConfig): home-manager runs with useGlobalPkgs, so hm
# modules cannot contribute nixpkgs.overlays. Only hosts running clod
# import this -- other darwin machines keep stock packages.
#
# The codex half of the same input is ../cody/overlays.nix.
#
# `import` (not legacyPackages) to set allowUnfree: claude-code is unfree
# and the input's default package set is free-only, unlike our main
# nixpkgs (modules/nix.nix).
{ inputs, ... }:
{
  nixpkgs.overlays = [
    (final: prev: {
      claude-code =
        (import inputs.nixpkgs-claude {
          inherit (prev.stdenv.hostPlatform) system;
          config.allowUnfree = true;
        }).claude-code;
    })
  ];
}
