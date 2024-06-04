# Personal macbook.
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/darwin
  ];

  networking = {
    hostName = "nostromo";
    computerName = "nostromo";
  };
}
