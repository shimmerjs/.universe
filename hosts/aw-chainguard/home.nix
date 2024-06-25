{ inputs, ... }:
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/home-manager
    ../../modules/home-manager/darwin
    ../../modules/home-manager/kitty
    ../../modules/home-manager/vscode
  ];

  programs.git = {
    userEmail = "alex.weidner@chainguard.dev";
  };
}
