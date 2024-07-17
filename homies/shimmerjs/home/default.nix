{ pkgs, ... }:
{
  imports = [
    ../../../modules/home-manager
    ./git.nix
    ./dev.nix
    ./zsh.nix
  ];

  home.packages = with pkgs; [
    bitwarden-cli
  ];
}
