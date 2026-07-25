{ pkgs, ... }:
{
  imports = [
    ../../../modules/home-manager
    ./bat
    ./git.nix
    ./dev.nix
    ./worktrunk.nix
    ./zsh.nix
  ];
}
