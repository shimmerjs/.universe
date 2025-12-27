# Configuration that helps work with the system configuration defined by
# .universe.
{ pkgs, hostname, ... }:
let
  cmd = if pkgs.stdenv.isDarwin then "sudo darwin-rebuild" else "sudo nixos-rebuild";
in
{
  environment.variables = {
    UNIVERSE_PATH = "$HOME/.universe";
  };
  # TODO: create proper script which can determine if anything is cloned to
  # UNIVERSE_PATH and fall back to building flake from GitHub
  environment.shellAliases = {
    edit = "$EDITOR $UNIVERSE_PATH";
    switch = "${cmd} switch --flake $UNIVERSE_PATH#${hostname}";
    build = "${cmd} build --flake $UNIVERSE_PATH#${hostname}";
  };
}
