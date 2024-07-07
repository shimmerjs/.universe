# Personal Mac Mini
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/darwin
    ../../modules/darwin/play.nix
  ];

  networking = {
    hostName = "mother";
    computerName = "mother";
  };
}
