# khudson dev shell, project-local on purpose (user 2026-07-07: no flake
# wiring for this -- "shell.nix is simple and fine"). <nixpkgs> is pinned by
# nix-darwin's NIX_PATH to the system flake's nixpkgs, so this tracks the
# same rev the deployed config builds from. Usage: nix-shell (from this dir).
{
  pkgs ? import <nixpkgs> { },
}:
import ./nix/devshell.nix { inherit pkgs; }
