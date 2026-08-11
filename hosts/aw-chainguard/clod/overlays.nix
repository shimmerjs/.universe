# Darwin-side companion to the clod home module: claude-code AND codex from a
# fresh nixpkgs (flake input nixpkgs-claude, tracks master). The hm-side
# cody module (config.toml, AGENTS.md, consult skill, allowlist slice)
# lives in ./cody/; this file stays darwin-level because hm modules
# cannot contribute nixpkgs.overlays under useGlobalPkgs. The main nixpkgs
# is the cache-warm-but-lagging nixos-unstable, which trails both CLIs'
# release cadence -- and codex's server gates new models on CLI version
# ("requires a newer version of Codex"), so a stale codex can't reach the
# current model at all. Bump with `nix flake update nixpkgs-claude`.
#
# Lives beside the clod home module but imports at the DARWIN layer (the
# host's systemConfig): home-manager runs with useGlobalPkgs, so hm modules
# cannot contribute nixpkgs.overlays. Only hosts running clod import this --
# other darwin machines keep stock packages.
#
# `import` (not legacyPackages) to set allowUnfree: claude-code is unfree and
# the input's default package set is free-only, unlike our main nixpkgs
# (modules/nix.nix).
{ inputs, ... }:
{
  nixpkgs.overlays = [
    (final: prev: let
      fresh = import inputs.nixpkgs-claude {
        inherit (prev.stdenv.hostPlatform) system;
        config.allowUnfree = true;
      };
    in {
      claude-code = fresh.claude-code;
      codex = fresh.codex;
    })
  ];
}
