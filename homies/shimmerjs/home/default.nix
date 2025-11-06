{ pkgs, ... }:
{
  imports = [
    ../../../modules/home-manager
    ./git.nix
    ./dev.nix
    ./zsh.nix
  ];
}
