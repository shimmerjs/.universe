# home-manager configuration applied to all macOS hosts.
{ pkgs, ... }:
{
  imports = [
    ./rectangle
  ];

  home.packages = with pkgs; [
    # Manage macOS CoreFoundation libraries with Nix
    darwin.CF
    telegram-desktop
  ];
}
