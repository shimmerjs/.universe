# Darwin-side companion to the cody home module: codex from a fresh
# nixpkgs (flake input nixpkgs-claude, tracks master). The main nixpkgs is
# the cache-warm-but-lagging nixos-unstable, which trails codex's release
# cadence -- and codex's server gates new models on CLI version
# ("requires a newer version of Codex"), so a stale codex cannot reach the
# current model at all regardless of what the config asks for. Bump with
# `nix flake update nixpkgs-claude`, then re-probe with
# `codex debug models` before changing a model pin.
#
# Lives beside the cody home module but imports at the DARWIN layer (the
# host's systemConfig): home-manager runs with useGlobalPkgs, so hm
# modules cannot contribute nixpkgs.overlays. Only hosts running cody
# import this -- other darwin machines keep stock packages.
#
# The claude-code half of the same input is ../clod/overlays.nix. They are
# separate files because cody is no longer a subdirectory of clod, and one
# input feeding two packages does not make them one concern.
{ inputs, ... }:
{
  nixpkgs.overlays = [
    (final: prev: {
      # `import` rather than legacyPackages so the free-only default of
      # the input's package set can be lifted if codex ever needs it.
      codex =
        (import inputs.nixpkgs-claude {
          inherit (prev.stdenv.hostPlatform) system;
          config.allowUnfree = true;
        }).codex;
    })
  ];
}
