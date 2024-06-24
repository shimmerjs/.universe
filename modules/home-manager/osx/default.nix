# home-manager configuration applied to all macOS hosts.
{ pkgs, ... }:
{
  imports = [
    ./rectangle
  ];

  programs.git = {
    extraConfig = {
      credential = {
        helper = "osxkeychain";
      };
    };
  };

  home.packages = with pkgs; [
    # Manage macOS CoreFoundation libraries with Nix
    darwin.CF
    telegram-desktop
  ];
}
