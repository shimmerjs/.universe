{ inputs, ... }:
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/home-manager
    ../../modules/home-manager/darwin
    ../../modules/home-manager/play
    ../../modules/home-manager/kitty
    ../../modules/home-manager/vscode
  ];
}
