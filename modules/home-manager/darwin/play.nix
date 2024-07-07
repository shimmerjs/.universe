# Darwin home-manager config for non-work machines
{ pkgs, ... }:
{
  home.packages = with pkgs; [
    # TODO: uncomment when https://github.com/NixOS/nixpkgs/issues/315667
    # is resolved
    # bitwarden-cli
  ];
}
