{ inputs, ... }:
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/home-manager
    ../../modules/home-manager/osx
    ../../modules/home-manager/tools
    ../../modules/home-manager/tools/dev.nix
    ../../modules/home-manager/git
    ../../modules/home-manager/kitty
    ../../modules/home-manager/vscode
    ../../modules/home-manager/zsh
  ];
}
