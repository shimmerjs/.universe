{ pkgs, ... }:
{
  imports = [
    ../../../modules/home-manager
    ./git.nix
    ./dev.nix
    ./worktrunk.nix
    ./zsh.nix
  ];
}
