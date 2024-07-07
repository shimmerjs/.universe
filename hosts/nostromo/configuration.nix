# Personal macbook.
{ config, lib, pkgs, ... }:
{
  imports = [
    ../../modules/darwin
    ../../modules/darwin/play.nix
  ];

  networking = {
    hostName = "nostromo";
    computerName = "nostromo";
  };

  homebrew = {
    casks = [
      "docker" # Tool for creation of human suffering
    ];
  };
}
