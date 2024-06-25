# Personal Mac Mini
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/darwin
  ];

  networking = {
    hostName = "mother";
    computerName = "mother";
  };
}
