# The statusline as a nix-built script, so jq/git and friends resolve by store
# path. Claude Code spawns it with the app's own PATH, which on a GUI launch
# carries no nix profile -- the script used to compensate by hardcoding a PATH
# prefix, which also shadowed nix tools with /usr/bin ones.
#
# Consumed by ./default.nix (home.file) and by lib/mkchecks.nix, so the check
# exercises the same artifact that gets deployed.
{ pkgs }:
pkgs.writeShellApplication {
  name = "clod-statusline";
  runtimeInputs = with pkgs; [
    jq
    git
    coreutils
    findutils
    gnused
    gawk
  ];
  # The script leans on `test && assign` short-circuits throughout; errexit
  # would turn every false test into an early exit.
  bashOptions = [ "pipefail" ];
  text = builtins.readFile ./statusline.sh;
}
