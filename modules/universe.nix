# Configuration that helps work with the system configuration defined by
# .universe.
{
  inputs,
  pkgs,
  lib,
  hostname,
  ...
}:
let
  cmd =
    if pkgs.stdenv.isDarwin then
      "sudo ${lib.getExe inputs.darwin.packages.${pkgs.stdenv.hostPlatform.system}.darwin-rebuild}"
    else
      "sudo nixos-rebuild";
in
{
  environment.variables = {
    UNIVERSE_PATH = "$HOME/.universe";
  };
  # TODO: create proper script which can determine if anything is cloned to
  # UNIVERSE_PATH and fall back to building flake from GitHub
  # TODO: dont install globally
  environment.systemPackages = with pkgs; [
    (writeShellScriptBin "ubuild" "${cmd} build --flake $UNIVERSE_PATH#${hostname} \"$@\"")
    (writeShellScriptBin "uswitch" "${cmd} switch --flake $UNIVERSE_PATH#${hostname} \"$@\"")
  ];
}
